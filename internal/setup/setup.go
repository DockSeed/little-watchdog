// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package setup pairs the bot with a Telegram chat (token, webhook, one-time code).
package setup

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/telegram"
)

type Options struct {
	Out        io.Writer
	ReadSecret func(prompt string) (string, error)
	Confirm    func(prompt string) (bool, error)
	APIURL     string
	PollWindow time.Duration // default 5 min
	Step       func(Step)    // optional progress for a UI; Out stays the CLI text
}

// Step is one progress report: step N (1 token, 2 webhook, 3 chat link, 4 test message).
type Step struct {
	N     int
	Done  bool
	Note  string    // short result, e.g. "accepted, bot @name"
	Code  string    // step 3 started: the one-time code
	Until time.Time // step 3 started: deadline for the code
}

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewCode returns a random 6 character one-time code.
func NewCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b), nil
}

// Pair verifies the token, handles a webhook, waits for "/start CODE" and sends a test message.
func Pair(ctx context.Context, o Options) (token string, chat int64, err error) {
	if o.PollWindow == 0 {
		o.PollWindow = 5 * time.Minute
	}
	if o.APIURL == "" {
		o.APIURL = "https://api.telegram.org"
	}
	out := o.Out
	step := func(s Step) {
		if o.Step != nil {
			o.Step(s)
		}
	}

	// 1. token
	tok, err := o.ReadSecret("Bot token (input hidden): ")
	if err != nil {
		return "", 0, err
	}
	token = strings.TrimSpace(tok)
	fmt.Fprintln(out)
	cl := telegram.New(o.APIURL, token, 15*time.Second)
	me, err := cl.GetMe(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("getMe failed, token wrong or Telegram unreachable: %w", err)
	}
	fmt.Fprintf(out, "Bot: @%s (%s)\n", me.Username, me.FirstName)
	step(Step{N: 1, Done: true, Note: "accepted, bot @" + me.Username})

	// 2. webhook
	wh, err := cl.WebhookURL(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("getWebhookInfo: %w", err)
	}
	if wh != "" {
		fmt.Fprintln(out, "This bot has a webhook. While it is set, the /start code cannot arrive.")
		fmt.Fprintln(out, "Deleting it stops whatever uses the webhook today.")
		ok, err := o.Confirm("Delete the webhook now?")
		if err != nil {
			return "", 0, err
		}
		if !ok {
			return "", 0, errors.New("aborted: webhook still set (use a different bot or delete the webhook yourself)")
		}
		if err := cl.DeleteWebhook(ctx); err != nil {
			return "", 0, fmt.Errorf("deleteWebhook: %w", err)
		}
		fmt.Fprintln(out, "Webhook deleted.")
		step(Step{N: 2, Done: true, Note: "deleted"})
	} else {
		step(Step{N: 2, Done: true, Note: "none set"})
	}

	// 3. one-time code
	code, err := NewCode()
	if err != nil {
		return "", 0, err
	}
	var offset int64
	if us, err := cl.GetUpdates(ctx, -1, 0); err != nil {
		return "", 0, explain409(err)
	} else if len(us) > 0 {
		offset = us[len(us)-1].UpdateID + 1
	}
	fmt.Fprintf(out, "\nSend `/start %s` to @%s (waiting up to %s)...\n", code, me.Username, o.PollWindow)
	deadline := time.Now().Add(o.PollWindow)
	step(Step{N: 3, Note: "@" + me.Username, Code: code, Until: deadline})
	for chat == 0 {
		left := time.Until(deadline)
		if left <= 0 {
			return "", 0, errors.New("timed out waiting for the /start message")
		}
		wait := int(min(left, 20*time.Second).Seconds())
		us, err := cl.GetUpdates(ctx, offset, wait)
		if err != nil {
			if ctx.Err() != nil {
				return "", 0, ctx.Err()
			}
			if errors.Is(err, telegram.ErrBadAnswer) { // do not hang on an unreadable update: look at the newest one again
				if l, e := cl.GetUpdates(ctx, -1, 0); e == nil && len(l) > 0 {
					offset = l[len(l)-1].UpdateID
				}
				select {
				case <-ctx.Done():
					return "", 0, ctx.Err()
				case <-time.After(time.Second):
				}
				continue
			}
			return "", 0, explain409(err)
		}
		for _, u := range us {
			offset = u.UpdateID + 1
			if u.Message != nil && isStart(u.Message.Text, me.Username, code) {
				chat = u.Message.Chat.ID
				break
			}
		}
	}
	fmt.Fprintf(out, "Got it, chat id %d.\n", chat)
	step(Step{N: 3, Done: true, Note: "linked"})

	// 4. test message
	if err := cl.SendMessage(ctx, chat, TestText(checks.Host())); err != nil {
		fmt.Fprintf(out, "Test message FAILED: %v\n", err)
		return "", 0, errors.New("could not send a test message")
	}
	fmt.Fprintln(out, "Test message sent.")
	step(Step{N: 4, Done: true, Note: "sent"})
	return token, chat, nil
}

// TestText is the message that confirms the link.
func TestText(host string) string {
	return "little-watchdog" + telegram.On(host) + ": this chat is linked. You get a message here when the status of a check changes. /status lists all checks."
}

func explain409(err error) error {
	var ae *telegram.APIError
	if errors.As(err, &ae) && ae.Code == 409 {
		return errors.New("Telegram answered 409 (conflict): a webhook or another program (a second little-watchdog, a bot framework) uses this bot. The little-watchdog service here pauses during setup; stop the other program or use a separate bot, then try again")
	}
	return err
}

// isStart accepts "/start CODE" and, in groups, "/start@bot CODE".
func isStart(text, bot, code string) bool {
	f := strings.Fields(text)
	if len(f) != 2 || f[1] != code {
		return false
	}
	return f[0] == "/start" || strings.EqualFold(f[0], "/start@"+bot)
}
