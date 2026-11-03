// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/state"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestLateMessageNamesTheEventTime(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	cur := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	a.Now = func() time.Time { return cur }
	ctx := context.Background()
	a.Cycle(ctx)
	f.Set(func(f *testfake.Telegram) { f.Fail = true })
	s.fail("HTTP 502")
	a.Cycle(ctx)
	cur = cur.Add(time.Minute)
	a.Cycle(ctx) // second failure: the alarm is queued, Telegram is down
	if len(f.Texts()) != 0 || len(a.Eng.PendingMessages()) != 1 {
		t.Fatalf("%q", f.Texts())
	}
	cur = cur.Add(5 * time.Minute)
	f.Set(func(f *testfake.Telegram) { f.Fail = false })
	a.Cycle(ctx)
	got := f.Texts()
	if len(got) != 1 || !strings.HasSuffix(got[0], "HTTP 502. (sent late, event at 10:01)") {
		t.Fatalf("%q", got)
	}
}

func TestMessageWithinTwoMinutesIsNotLate(t *testing.T) {
	a := &App{Now: func() time.Time { return time.Date(2026, 11, 3, 10, 2, 0, 0, time.Local) }}
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	if got := a.late("x", at); got != "x" {
		t.Fatalf("%q", got)
	}
	if got := a.late("x", time.Time{}); got != "x" {
		t.Fatalf("unknown time: %q", got)
	}
	a.Now = func() time.Time { return at.Add(2*time.Minute + time.Second) }
	if got := a.late("x", at); got != "x (sent late, event at 10:00)" {
		t.Fatalf("%q", got)
	}
	a.Now = func() time.Time { return at.Add(30 * time.Hour) }
	if got := a.late("x", at); got != "x (sent late, event at Nov 3 10:00)" {
		t.Fatalf("other day: %q", got)
	}
}

func TestNoticesCarryNoClockPrefix(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	a.notify("testhost: hello")
	a.Deliver(context.Background())
	if got := f.Texts(); len(got) != 1 || got[0] != "testhost: hello" {
		t.Fatalf("%q", got)
	}
}

func TestStartMessageIsRetriedUntilItGoesOut(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.Fail = true })
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	a.Cfg.Interval = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- a.Run(ctx) }()
	waitFor(t, func() bool {
		var n int
		f.Set(func(f *testfake.Telegram) { n = f.SendCount })
		return n >= 3
	})
	f.Set(func(f *testfake.Telegram) { f.Fail = false })
	waitFor(t, func() bool { return len(f.Texts()) > 0 })
	cancel()
	<-done
	if got := f.Texts(); !strings.Contains(got[0], "watching 1 check (http:web)") {
		t.Fatalf("%q", got)
	}
}

func TestCancelledSendIsNoTelegramProblem(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.SendDelay = 5 * time.Second })
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if err := a.send(ctx, "x"); err == nil {
		t.Fatal("expected an error")
	}
	path := filepath.Join(t.TempDir(), "s.json")
	a.Eng.Save(path)
	st, _ := state.ReadAll(path)
	if st.Telegram == nil || st.Telegram.Err != "" || strings.Contains(buf.String(), "rejects") {
		t.Fatalf("a send cut short by shutdown was counted: %+v %s", st.Telegram, buf.String())
	}
}

func TestConflictKind(t *testing.T) {
	for desc, want := range map[string]string{
		"Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first": state.ConflictWebhook,
		"Conflict: terminated by other getUpdates request; make sure that only one bot instance is running":            state.ConflictPoller,
		"Conflict": state.ConflictUnknown,
	} {
		if got := conflictKind(desc); got != want {
			t.Errorf("%q: %s, want %s", desc, got, want)
		}
	}
}

func pollUntilRetries(t *testing.T, a *App, f *testfake.Telegram, n int) {
	t.Helper()
	pollUntil(t, a, func() bool {
		var c int
		f.Set(func(f *testfake.Telegram) { c = f.Calls["getUpdates"] })
		return c >= n
	})
}

// pollUntil runs Poll until cond holds (generous deadline), then stops it.
func pollUntil(t *testing.T, a *App, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Poll(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, cond)
}

func TestConflictIsAnnouncedOncePerStreakAndRecorded(t *testing.T) {
	for _, tc := range []struct{ desc, kind, text string }{
		{"", state.ConflictPoller, "this bot is also used elsewhere, so /status does not work here. Use one bot per machine."},
		{"Conflict: can't use getUpdates method while webhook is active", state.ConflictWebhook, "a webhook is set for this bot"},
	} {
		f, srv := testfake.Start(tok)
		f.Set(func(f *testfake.Telegram) { f.Fail409, f.Desc409 = true, tc.desc })
		var buf bytes.Buffer
		a, _ := newApp(t, srv.URL, "", &buf)
		pollUntilRetries(t, a, f, 5)
		a.Deliver(context.Background())
		got := f.Texts()
		if len(got) != 1 || !strings.Contains(got[0], tc.text) || !strings.HasPrefix(got[0], "testhost: ") {
			t.Fatalf("%s: one notice per streak expected: %q", tc.kind, got)
		}
		if a.Eng.Conflict() != tc.kind {
			t.Fatalf("meta %q, want %q", a.Eng.Conflict(), tc.kind)
		}
		// more retries of the same streak say nothing; a fixed bot clears the record
		pollUntilRetries(t, a, f, 12)
		a.Deliver(context.Background())
		if n := len(f.Texts()); n != 1 {
			t.Fatalf("%d notices", n)
		}
		f.Set(func(f *testfake.Telegram) { f.Fail409 = false })
		// the call count says nothing about a successful answer being processed
		// (the count includes the in-flight call): wait for the record itself
		pollUntil(t, a, func() bool { return a.Eng.Conflict() == "" })
		srv.Close()
	}
}

func TestRestartDuringConflictDoesNotAnnounceAgain(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.Fail409 = true })
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	pollUntilRetries(t, a, f, 3)
	a.saveState(true)
	b, _ := newApp(t, srv.URL, "", &buf)
	b.Cfg.StateFile = a.Cfg.StateFile
	b.LoadState()
	pollUntilRetries(t, b, f, 8)
	b.Deliver(context.Background())
	a.Deliver(context.Background())
	if n := len(f.Texts()); n != 1 {
		t.Fatalf("%d notices: %q", n, f.Texts())
	}
}

func TestInvalidConfigNoticeHasTheFixHint(t *testing.T) {
	r := newRL(t, tok)
	ctx := context.Background()
	r.a.Cycle(ctx)
	r.write(r.body(tok, "TCP_X=nonsense"))
	r.a.Cycle(ctx)
	r.a.Cycle(ctx)
	r.a.Cycle(ctx)
	got := strings.Join(r.f.Texts(), "\n")
	want := "❌ testhost: the config file has a problem, so the previous one stays in use: config line"
	hint := "\nFix it in little-wd or in " + r.path + `. "little-wd check" tests the file.`
	if !strings.Contains(got, want) || !strings.Contains(got, hint) || strings.Contains(got, "starts by itself") {
		t.Fatalf("%q", got)
	}
}
