// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package deadman pings a Healthchecks-style dead-man's-switch URL.
package deadman

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/DockSeed/little-watchdog/internal/clock"
)

const (
	Threshold = 3
	// MinEvery is the hard floor between pings (healthchecks.io ignores > 5 per minute).
	MinEvery = 60 * time.Second
	// slack absorbs scheduler jitter so a cycle landing 1 ms early is not skipped.
	slack = 2 * time.Second
)

// retryWaits are the pauses before the 2nd and 3rd try after a network error.
var retryWaits = []time.Duration{5 * time.Second, 10 * time.Second}

type Switch struct {
	URL    string
	Every  time.Duration // time between two pings, never below MinEvery
	Clk    clock.Clock
	hc     *http.Client
	logf   func(string, ...any)
	OnBad  func(error) // optional: called once when the URL is rejected (wrong UUID)
	OnPing func(error) // optional: the outcome of every normal ping (not of /fail)

	mu     sync.Mutex
	fails  int
	last   time.Time // start of the last ping round
	badLog bool      // a wrong answer was already logged since the last good one
}

func New(rawURL string, every time.Duration, logf func(string, ...any)) *Switch {
	every = max(every, MinEvery)
	return &Switch{URL: rawURL, Every: every, logf: logf, Clk: clock.Real(),
		hc: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{
			Proxy: nil, MaxResponseHeaderBytes: 64 << 10, IdleConnTimeout: every + 30*time.Second,
			DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext}}}
}

// Close drops idle connections.
func (s *Switch) Close() { s.hc.CloseIdleConnections() }

// NoteSend records the outcome of a Telegram send.
func (s *Switch) NoteSend(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.fails = 0
	} else {
		s.fails++
	}
}

func (s *Switch) Fails() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fails
}

// due reports whether a round may start now, and stamps it.
func (s *Switch) due() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Clk.Now()
	if !s.last.IsZero() && now.Sub(s.last) < s.Every-slack {
		return false
	}
	s.last = now
	return true
}

// Cycle is called after each check cycle but pings at most once per Every. After
// Threshold failed sends it first probes Telegram; if that works the counter is
// reset and the normal ping is sent, otherwise <URL>/fail (instead of, never in
// addition to, the normal ping). Returns true when /fail was sent.
func (s *Switch) Cycle(ctx context.Context, probe func(context.Context) error) bool {
	if s.URL == "" || !s.due() {
		return false
	}
	failMode := false
	if s.Fails() >= Threshold {
		if err := probe(ctx); err == nil {
			s.NoteSend(true)
		} else {
			failMode = true
		}
	}
	target := s.URL
	if failMode {
		target = failURL(s.URL)
	}
	err := s.Ping(ctx, target)
	if !failMode && s.OnPing != nil {
		s.OnPing(err)
	}
	var be *badAnswer
	switch {
	case err == nil:
		s.mu.Lock()
		s.badLog = false
		s.mu.Unlock()
	case errors.As(err, &be):
		s.mu.Lock()
		again := s.badLog
		s.badLog = true
		s.mu.Unlock()
		if !again {
			if s.OnBad != nil {
				s.OnBad(err)
			}
			s.logf("dead-man ping rejected: %v (wrong UUID in DEADMAN_URL?), not logged again until it works", err)
		}
	default:
		s.logf("dead-man ping failed: %v", err)
	}
	return failMode
}

// failURL appends /fail to the path of u and keeps a query string.
func failURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return strings.TrimRight(raw, "/") + "/fail"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/fail"
	u.RawPath = ""
	return u.String()
}

type badAnswer struct{ msg string }

func (e *badAnswer) Error() string { return e.msg }

// Ping does one GET, retried (5 s, 10 s) only after network errors. Success is
// HTTP 200 with the body exactly "OK". Errors never contain the URL.
func (s *Switch) Ping(ctx context.Context, target string) error {
	var err error
	for i := 0; ; i++ {
		var netErr bool
		if netErr, err = s.try(ctx, target); err == nil || !netErr || i >= len(retryWaits) {
			return err
		}
		if s.Clk.Sleep(ctx, retryWaits[i]) != nil {
			return err
		}
	}
}

// try makes one request; netErr is true when no HTTP answer came back.
func (s *Switch) try(ctx context.Context, target string) (netErr bool, _ error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, errors.New("bad dead-man URL")
	}
	req.Header.Set("User-Agent", "little-watchdog")
	resp, err := s.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return true, fmt.Errorf("%s: %w", ue.Op, ue.Err)
		}
		return true, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if got := strings.TrimSpace(string(b)); got != "OK" {
		return false, &badAnswer{fmt.Sprintf("HTTP 200 but body %q instead of \"OK\"", got)}
	}
	return false, nil
}
