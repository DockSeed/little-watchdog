// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/state"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestPermanentlyRejectedMessageDoesNotBlockOthers(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	b := addCheck(a, "http:zz")
	ctx := context.Background()
	f.Set(func(f *testfake.Telegram) { f.RejectContains = "BADTEXT" })
	s.fail("BADTEXT")
	b.fail("fine")
	for i := 0; i < 4; i++ {
		a.Cycle(ctx)
	}
	got := strings.Join(f.Texts(), "\n")
	if !strings.Contains(got, "<b>http:zz</b> on <b>testhost</b> has been down since ") || !strings.Contains(got, ": fine.") || strings.Contains(got, "BADTEXT") {
		t.Fatalf("%q", got)
	}
	if n := strings.Count(buf.String(), "dropped"); n != 1 {
		t.Fatalf("logged %d times: %s", n, buf.String())
	}
	if len(a.Eng.PendingMessages()) != 0 {
		t.Fatal("rejected message still queued")
	}
}

func TestMessagesOfOneCycleAreBundledWithoutClockPrefix(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	b := addCheck(a, "http:b")
	s.fail("one")
	b.fail("two")
	a.Cycle(context.Background())
	a.Cycle(context.Background())
	got := f.Texts()
	if len(got) != 1 {
		t.Fatalf("%q", got)
	}
	lines := strings.Split(got[0], "\n")
	if len(lines) != 2 {
		t.Fatalf("%q", got[0])
	}
	for _, l := range lines {
		if !regexp.MustCompile(`^❌ <b>\S+</b> on <b>testhost</b> has been down since \d\d:\d\d: \w+\.$`).MatchString(l) {
			t.Fatalf("unexpected line: %q", l)
		}
	}
}

func TestBundleSplitsAt4096(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	a.Checks = nil
	for i := 0; i < 30; i++ {
		c := addCheck(a, "http:c"+strings.Repeat("x", 3)+string(rune('a'+i%26))+string(rune('a'+i/26)))
		c.fail(strings.Repeat("d", 190))
	}
	_ = s
	a.Cycle(context.Background())
	a.Cycle(context.Background())
	got := f.Texts()
	if len(got) < 2 {
		t.Fatalf("not split: %d messages", len(got))
	}
	total := 0
	for _, m := range got {
		if len([]rune(m)) > 4096 {
			t.Fatalf("message of %d runes", len([]rune(m)))
		}
		total += strings.Count(m, "has been down since")
	}
	if total != 30 {
		t.Fatalf("%d of 30 alarms delivered", total)
	}
}

func TestSlowSendDoesNotStallChecks(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.SendDelay = 5 * time.Second })
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	a.Cfg.Interval = 50 * time.Millisecond
	cnt := &counting{scripted: s}
	a.Checks = []checks.Check{cnt}
	s.fail("down")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(700 * time.Millisecond)
	n := cnt.n.Load()
	cancel()
	<-done
	if n < 8 {
		t.Fatalf("only %d check runs in 700ms while a send hangs", n)
	}
}

func TestRejectedChatKeepsAlarmAndLogsLoudly(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.FailCode = 403 })
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	ctx := context.Background()
	a.Cycle(ctx)
	s.set(checks.Result{Status: checks.FAIL, Detail: "HTTP 502"})
	for i := 0; i < 5; i++ {
		a.Cycle(ctx)
	}
	if len(a.Eng.Queue()) != 1 {
		t.Fatalf("alarm was dropped: %v", a.Eng.Queue())
	}
	if n := strings.Count(buf.String(), "ERROR: Telegram rejects the token or chat"); n != 1 {
		t.Fatalf("%d loud lines:\n%s", n, buf.String())
	}
	f.Set(func(f *testfake.Telegram) { f.FailCode = 0 })
	a.Cycle(ctx)
	if got := f.Texts(); len(got) != 1 || !strings.Contains(got[0], "HTTP 502") {
		t.Fatalf("not delivered after the fix: %q", got)
	}
	if !strings.Contains(buf.String(), "Telegram accepts messages again") {
		t.Fatal(buf.String())
	}
}

func TestMessageSpecificRejectionIsStillDropped(t *testing.T) { // only token/chat errors keep the queue
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.RejectContains = "HTTP 502" })
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	a.Cycle(context.Background())
	s.set(checks.Result{Status: checks.FAIL, Detail: "HTTP 502"})
	a.Cycle(context.Background())
	a.Cycle(context.Background())
	if len(a.Eng.Queue()) != 0 || !strings.Contains(buf.String(), "dropped") {
		t.Fatalf("%v\n%s", a.Eng.Queue(), buf.String())
	}
}

func TestDroppedNoticeIsLogged(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.RejectContains = "hello" })
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	a.notify("hello")
	a.Cycle(context.Background())
	if !strings.Contains(buf.String(), "notice dropped") {
		t.Fatal(buf.String())
	}
}

func TestChatWide400KeepsTheAlarm(t *testing.T) { // bot muted, group disabled: not a bad message
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.FailCode = 400 }) // description "rejected": not about one message
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	ctx := context.Background()
	a.Cycle(ctx)
	s.set(checks.Result{Status: checks.FAIL, Detail: "HTTP 502"})
	for i := 0; i < 4; i++ {
		a.Cycle(ctx)
	}
	if len(a.Eng.Queue()) != 1 {
		t.Fatalf("alarm dropped: %v\n%s", a.Eng.Queue(), buf.String())
	}
	f.Set(func(f *testfake.Telegram) { f.FailCode = 0 })
	a.Cycle(ctx)
	if got := f.Texts(); len(got) != 1 || !strings.Contains(got[0], "HTTP 502") {
		t.Fatalf("%q", got)
	}
}

func TestStateFileCarriesTelegramOutcome(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	ctx := context.Background()
	f.Set(func(f *testfake.Telegram) { f.FailCode = 403 })
	s.set(checks.Result{Status: checks.FAIL, Detail: "HTTP 502"})
	for i := 0; i < 3; i++ {
		a.Cycle(ctx)
	}
	st, err := state.ReadAll(a.Cfg.StateFile)
	tg := st.Telegram
	if err != nil || tg == nil || tg.Err == "" || tg.ErrSince.IsZero() || !tg.LastOKAt.IsZero() {
		t.Fatalf("%+v %v", tg, err)
	}
	f.Set(func(f *testfake.Telegram) { f.FailCode = 0 })
	a.Cycle(ctx)
	st, _ = state.ReadAll(a.Cfg.StateFile)
	if tg = st.Telegram; tg == nil || tg.Err != "" || tg.LastOKAt.IsZero() {
		t.Fatalf("%+v", tg)
	}
}
