// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package telegram is a minimal Bot API client. The bot token is part of every
// request URL, so all errors are scrubbed before they leave this package.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/DockSeed/little-watchdog/internal/clock"
)

const maxResp = 1 << 20

// ErrBadAnswer marks an answer that could not be read (too large, not JSON, e.g. a proxy page).
var ErrBadAnswer = errors.New("telegram: unreadable answer")

// ErrHeld means a flood wait is running and the caller asked not to wait for it.
var ErrHeld = errors.New("telegram: sends held back (flood wait)")

// maxPaceWait is the longest a single-attempt send waits for its slot.
const maxPaceWait = 5 * time.Second

// updateLimit bounds one getUpdates answer, so a flood cannot exceed maxResp.
const updateLimit = 10

// MaxText is Telegram's limit for one message, in characters.
const MaxText = 4096

// Limits from the Bots FAQ (1 msg/s per chat, 20/min per group) and getUpdates advice.
const (
	GapChat      = time.Second
	GapGroup     = 3 * time.Second
	PollMin      = 25 // long-poll seconds; shorter is never sent
	PollDefault  = 50
	maxFloodWait = 24 * time.Hour
)

type Client struct {
	base, token string
	hc          *http.Client
	timeout     time.Duration
	Attempts    int           // sendMessage attempts, default 3
	Backoff     time.Duration // base delay, multiplied by attempt number
	Clk         clock.Clock

	mu    sync.Mutex // serializes sendMessage pacing
	last  time.Time  // start of the last sendMessage request
	until time.Time  // flood wait from the last retry_after
}

func New(base, token string, timeout time.Duration) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"), token: token, timeout: timeout,
		hc: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, MaxResponseHeaderBytes: 64 << 10,
			IdleConnTimeout: 90 * time.Second, DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext}},
		Attempts: 3, Backoff: 2 * time.Second, Clk: clock.Real(),
	}
}

// APIError is an error answer from the Bot API.
type APIError struct {
	Code       int
	Desc       string
	RetryAfter int
	MigrateTo  int64 // group became a supergroup: new chat id
}

func (e *APIError) Error() string { return fmt.Sprintf("telegram API error %d: %s", e.Code, e.Desc) }

type User struct {
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type Update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

// scrub removes the token (and URL details) from an error.
func (c *Client) scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "<token>"))
}

func (c *Client) call(ctx context.Context, method string, params map[string]any, wait time.Duration, out any) error {
	body, _ := json.Marshal(params)
	ctx, cancel := context.WithTimeout(ctx, c.timeout+wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.scrub(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return c.scrub(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResp))
	if err != nil {
		return c.scrub(err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxResp))
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Desc   string          `json:"description"`
		Code   int             `json:"error_code"`
		Params struct {
			RetryAfter int   `json:"retry_after"`
			MigrateTo  int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("%w (HTTP %d)", ErrBadAnswer, resp.StatusCode)
	}
	if !env.OK {
		if env.Code == 0 {
			env.Code = resp.StatusCode
		}
		return &APIError{Code: env.Code, Desc: c.scrub(errors.New(env.Desc)).Error(), RetryAfter: env.Params.RetryAfter, MigrateTo: env.Params.MigrateTo}
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// Permanent reports an answer that retrying cannot fix (4xx except 429).
func Permanent(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code != 429 && ae.Code >= 400 && ae.Code < 500
}

// BadSetup reports a rejection that is about token or chat, not about one message:
// the queue must be kept until the operator fixes the config.
func BadSetup(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.Code {
	case 401, 403, 404:
		return true
	case 400: // only these 400s are about one message; any other is about the chat or the bot
		return !messageSpecific(ae.Desc)
	}
	return false
}

func messageSpecific(desc string) bool {
	d := strings.ToLower(desc)
	for _, s := range []string{"message is too long", "message text is empty", "can't parse", "can't find end of"} {
		if strings.Contains(d, s) {
			return true
		}
	}
	return false
}

// Len16 counts s in UTF-16 code units, the way Telegram counts a message's length.
func Len16(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// Cut16 cuts s to at most n UTF-16 code units, at a character boundary.
func Cut16(s string, n int) string {
	used := 0
	for i, r := range s {
		used += utf16.RuneLen(r)
		if used > n {
			return s[:i]
		}
	}
	return s
}

// Close drops idle connections (after the client was replaced by a reload).
func (c *Client) Close() { c.hc.CloseIdleConnections() }

// SendOnce makes a single attempt.
func (c *Client) SendOnce(ctx context.Context, chat int64, text string) error {
	return c.send(ctx, chat, text, 1)
}

// SendMessage sends plain text (no parse mode) with retries.
func (c *Client) SendMessage(ctx context.Context, chat int64, text string) error {
	return c.send(ctx, chat, text, c.Attempts)
}

func (c *Client) send(ctx context.Context, chat int64, text string, attempts int) error {
	text = Cut16(text, MaxText)
	var err error
	for i := 1; i <= attempts; i++ {
		maxWait := time.Duration(1 << 62)
		if attempts == 1 {
			maxWait = maxPaceWait
		}
		if err = c.pace(ctx, chat, maxWait); err != nil {
			return err
		}
		err = c.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text}, 0, nil)
		if err == nil {
			return nil
		}
		var ae *APIError
		wait := c.Backoff * time.Duration(i)
		if errors.As(err, &ae) {
			if ae.Code != 429 && ae.Code < 500 {
				return err // 4xx: retrying will not help
			}
			if ae.RetryAfter > 0 {
				wait = min(time.Duration(ae.RetryAfter)*time.Second, maxFloodWait)
				c.holdFor(wait)
			}
		}
		if i == attempts {
			break
		}
		if e := c.Clk.Sleep(ctx, wait); e != nil {
			return e
		}
	}
	return err
}

// holdFor keeps every sender back for d (Telegram's retry_after).
func (c *Client) holdFor(d time.Duration) {
	c.mu.Lock()
	c.until = c.Clk.Now().Add(d)
	c.mu.Unlock()
}

// pace reserves the next sendMessage slot and sleeps for it outside the lock; a wait
// longer than maxWait gives up with ErrHeld.
func (c *Client) pace(ctx context.Context, chat int64, maxWait time.Duration) error {
	gap := GapChat
	if chat < 0 {
		gap = GapGroup
	}
	c.mu.Lock()
	now := c.Clk.Now()
	slot := c.until
	if t := c.last.Add(gap); !c.last.IsZero() && t.After(slot) {
		slot = t
	}
	if slot.Before(now) {
		slot = now
	}
	if slot.Sub(now) > maxWait {
		c.mu.Unlock()
		return ErrHeld
	}
	c.last = slot
	c.mu.Unlock()
	if d := slot.Sub(now); d > 0 {
		return c.Clk.Sleep(ctx, d)
	}
	return nil
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", map[string]any{}, 0, &u)
	return u, err
}

func (c *Client) WebhookURL(ctx context.Context) (string, error) {
	var w struct {
		URL string `json:"url"`
	}
	err := c.call(ctx, "getWebhookInfo", map[string]any{}, 0, &w)
	return w.URL, err
}

func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]any{}, 0, nil)
}

// GetUpdates long-polls (never shorter than PollMin s). offset<0 is the one-shot
// lookup of the latest update and may use timeout 0.
func (c *Client) GetUpdates(ctx context.Context, offset int64, waitSec int) ([]Update, error) {
	if offset >= 0 || waitSec > 0 {
		waitSec = max(waitSec, PollMin)
	}
	var us []Update
	limit := updateLimit
	if offset < 0 {
		limit = 1
	}
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": waitSec, "limit": limit, "allowed_updates": []string{"message"},
	}, time.Duration(waitSec)*time.Second, &us)
	return us, err
}
