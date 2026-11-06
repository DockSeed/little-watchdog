// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/telegram"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

const hostile = "<b>x</b> &amp; </code> <\x1b[2J>"

// validAll fails unless every text is valid Telegram HTML within the length limit,
// and the fake never had to reject one.
func validAll(t *testing.T, f *testfake.Telegram, texts []string) {
	t.Helper()
	for i, s := range texts {
		if err := testfake.CheckHTML(s); err != nil {
			t.Errorf("text %d: %v: %q", i, err, s)
		}
		if n := telegram.Len16(s); n > telegram.MaxText {
			t.Errorf("text %d: %d units", i, n)
		}
	}
	var bad []string
	f.Set(func(f *testfake.Telegram) { bad = append(bad, f.BadHTML...) })
	if len(bad) > 0 {
		t.Errorf("Telegram would have rejected: %q", bad)
	}
}

func TestEveryNoticeIsValidHTMLEvenWithHostileText(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	a.Host, a.Eng.Host = checks.Clean(hostile), checks.Clean(hostile)
	a.Path = "/etc/" + hostile + "/c.env"
	a.migrate(-100777)
	a.DM.OnBad(errors.New("HTTP 400 " + hostile))
	a.notify(a.invalidText(errors.New("config line 3: TCP_X: invalid value " + hostile)))
	a.notify(conflictText(a.Host, "webhook"))
	a.notify(conflictText(a.Host, "poller"))
	a.notify(FixHint(a.Path, true))
	a.notify(StartText(a.Host, []checks.Check{&scripted{id: hostile}, &scripted{id: "photos"}}))
	a.notify(telegram.Msg(a.Host, "Telegram is reachable again after 3 failed sends."))
	a.Deliver(context.Background())
	got := f.Texts()
	if len(got) != 8 {
		t.Fatalf("%d texts: %q", len(got), got)
	}
	validAll(t, f, got)
	all := strings.Join(got, "\n")
	for _, bad := range []string{"<b>x</b>", "</code> <", "\x1b"} {
		if strings.Contains(all, bad) {
			t.Errorf("hostile %q reached Telegram unescaped", bad)
		}
	}
	if !strings.Contains(all, "&lt;b&gt;x&lt;/b&gt; &amp;amp; &lt;/code&gt;") {
		t.Errorf("escaped form missing: %q", all)
	}
	// the late suffix is part of the rendered text and keeps it valid
	a.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if s := a.late(a.invalidText(errors.New(hostile)), time.Now()); testfake.CheckHTML(s) != nil || !strings.Contains(s, "(sent late, event at ") {
		t.Errorf("%q", s)
	}
}

func TestAlarmsWithHostileNamesAndDetailsAreValidAndBold(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	h := addCheck(a, "http:<b>photos</b>&")
	s.fail("TLS certificate problem: x509: certificate is valid for <nas>, not homelab & co")
	h.fail(hostile)
	for i := 0; i < 3; i++ {
		a.Cycle(context.Background())
	}
	s.set(checks.Result{Status: checks.OK})
	h.set(checks.Result{Status: checks.OK})
	a.Cycle(context.Background())
	got := f.Texts()
	if len(got) == 0 {
		t.Fatal("nothing sent")
	}
	validAll(t, f, got)
	all := strings.Join(got, "\n")
	if !strings.Contains(all, "❌ <b>http:web</b> on <b>testhost</b> has been down since ") || !strings.Contains(all, "valid for &lt;nas&gt;, not homelab &amp; co.") ||
		!strings.Contains(all, "✅ <b>http:&lt;b&gt;photos&lt;/b&gt;&amp;</b> on <b>testhost</b> is back after ") {
		t.Fatalf("%q", all)
	}
	if len(a.Eng.PendingMessages()) != 0 {
		t.Fatal("not delivered: matching on the stored text broke")
	}
}

func TestChunksCountTheRenderedLengthWithLateSuffix(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	a.Checks = nil
	// 190 characters of "<&>" escape to ~4x their size: the rendered text is what counts
	detail := strings.Repeat("<&>", 63)
	for i := 0; i < 30; i++ {
		addCheck(a, fmt.Sprintf("http:c%02d", i)).fail(detail)
	}
	ctx := context.Background()
	a.Check(ctx)
	a.Check(ctx)
	base := time.Now()
	a.Now = func() time.Time { return base.Add(time.Hour) } // every message is now late
	a.Deliver(ctx)
	got := f.Texts()
	if len(got) < 3 {
		t.Fatalf("only %d messages for ~30 x 800 characters", len(got))
	}
	validAll(t, f, got)
	n := 0
	for _, m := range got {
		n += strings.Count(m, "(sent late, event at ")
		for _, l := range strings.Split(m, "\n") {
			if !strings.HasSuffix(l, ")") || !strings.Contains(l, "&lt;&amp;&gt;") {
				t.Fatalf("line cut or without the escaped detail: %q", l)
			}
		}
	}
	if n != 30 || len(a.Eng.PendingMessages()) != 0 {
		t.Fatalf("%d of 30 delivered, pending %v", n, a.Eng.PendingMessages())
	}
}

func TestAlarmIsNotLostWhenTelegramRejectsTheFormatting(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.RejectHTML = true })
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	s.fail("a < b & c")
	for i := 0; i < 3; i++ {
		a.Cycle(context.Background())
	}
	got := f.Texts()
	if len(got) != 1 || !strings.HasPrefix(got[0], "❌ http:web on testhost has been down since ") || !strings.HasSuffix(got[0], ": a < b & c.") || strings.Contains(got[0], "<b>") {
		t.Fatalf("%q", got)
	}
	if len(a.Eng.PendingMessages()) != 0 {
		t.Fatal("still queued")
	}
	if !strings.Contains(buf.String(), "sending the message as plain text") || strings.Contains(buf.String(), "dropped") {
		t.Fatalf("log: %s", buf.String())
	}
}

func TestLateSuffixIsRenderedOnceForSizingAndSending(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	a.Checks = nil
	addCheck(a, "http:c").fail("HTTP 502")
	ctx := context.Background()
	a.Check(ctx)
	a.Check(ctx)
	f.Set(func(f *testfake.Telegram) { f.Fail = true })
	a.Deliver(ctx) // stays queued
	q := a.Eng.Queue()
	if len(q) != 1 || f.Texts() != nil {
		t.Fatalf("queue %v, sent %q", q, f.Texts())
	}
	f.Set(func(f *testfake.Telegram) { f.Fail = false })
	// the first reading is inside the two minutes, every later one outside
	var calls atomic.Int32
	a.Now = func() time.Time {
		if calls.Add(1) == 1 {
			return q[0].At.Add(lateAfter - time.Second)
		}
		return q[0].At.Add(lateAfter + time.Second)
	}
	a.Deliver(ctx)
	got := f.Texts()
	if len(got) != 1 || strings.Contains(got[0], "sent late") {
		t.Fatalf("the text sent must be the text measured: %q", got)
	}
}
