// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/clock"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestEscAndPlainRoundTrip(t *testing.T) {
	for _, s := range []string{"<b>x</b>", "&amp;", "</code>", "a & b < c > d", "tab\tnl\nctl ü ✅", ""} {
		e := Esc(s)
		if strings.ContainsAny(e, "<>") || testfake.CheckHTML(e) != nil {
			t.Errorf("%q escaped to %q", s, e)
		}
		if got := Plain("<b>" + e + "</b>"); got != s {
			t.Errorf("%q came back as %q", s, got)
		}
	}
	if got := Esc("<b>&amp;</b>"); got != "&lt;b&gt;&amp;amp;&lt;/b&gt;" {
		t.Fatal(got)
	}
}

func TestCutHTMLNeverSplitsATagOrEntity(t *testing.T) {
	msg := "<b>" + strings.Repeat("a&amp;b&lt;", 40) + "</b> <code>" + strings.Repeat("é<&>", 20) + "</code>"
	msg = strings.ReplaceAll(msg, "é<&>", "é"+Esc("<&>"))
	if err := testfake.CheckHTML(msg); err != nil {
		t.Fatal(err)
	}
	for n := 0; n <= Len16(msg)+2; n++ {
		got := CutHTML(msg, n)
		if Len16(got) > n && Len16(msg) > n {
			t.Fatalf("n=%d: %d units", n, Len16(got))
		}
		if err := testfake.CheckHTML(got); err != nil {
			t.Fatalf("n=%d: %v: %q", n, err, got)
		}
		if !strings.HasPrefix(Plain(msg), Plain(got)) {
			t.Fatalf("n=%d: not a prefix: %q", n, got)
		}
	}
	if CutHTML(msg, Len16(msg)) != msg {
		t.Fatal("a message that fits was changed")
	}
}

func TestSendUsesHTMLAndFallsBackToPlainOnParseError(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	c := New(srv.URL, tok, time.Second)
	var mu sync.Mutex
	var logs []string
	c.Log = func(format string, args ...any) { mu.Lock(); logs = append(logs, format); mu.Unlock() }
	ctx := context.Background()
	if err := c.SendMessage(ctx, 1, "<b>a</b> ok"); err != nil {
		t.Fatal(err)
	}
	f.Set(func(f *testfake.Telegram) { f.RejectHTML = true })
	if err := c.SendMessage(ctx, 1, "❌ <b>a&lt;b&amp;</b> is down: <code>x</code> &gt; y."); err != nil {
		t.Fatalf("the alarm must still arrive: %v", err)
	}
	got := f.Texts()
	if len(got) != 2 || got[0] != "<b>a</b> ok" || got[1] != "❌ a<b& is down: x > y." {
		t.Fatalf("%q", got)
	}
	var modes []string
	f.Set(func(f *testfake.Telegram) { modes = append(modes, f.Modes...) })
	if strings.Join(modes, ",") != "HTML,HTML," {
		t.Fatalf("modes %q", modes)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "plain text") {
		t.Fatalf("the fallback must be logged once: %q", logs)
	}
}

func TestOther400IsNotRetriedAsPlain(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.RejectContains = "x" })
	c := New(srv.URL, tok, time.Second)
	if err := c.SendMessage(context.Background(), 1, "x"); err == nil {
		t.Fatal("must fail")
	}
	n := 0
	f.Set(func(f *testfake.Telegram) { n = f.SendCount })
	if n != 1 {
		t.Fatalf("%d requests for a message Telegram rejects as a whole", n)
	}
}

func TestParseErrorFallbackIsStickyAcrossRetries(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.RejectHTML, f.Fail = true, true }) // plain gets a 5xx
	clk := clock.NewFake()
	c := New(srv.URL, tok, time.Second)
	clk.OnWait = func(d time.Duration) { // only the retry backoff (not the pacing gap) heals the fake
		if d == c.Backoff {
			f.Set(func(f *testfake.Telegram) { f.Fail = false })
		}
	}
	c.Clk = clk.Clock()
	var mu sync.Mutex
	var logs []string
	c.Log = func(format string, args ...any) { mu.Lock(); logs = append(logs, format); mu.Unlock() }
	if err := c.SendMessage(context.Background(), 1, "<b>a&lt;b</b> is down"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 {
		t.Fatalf("want one warning per message: %q", logs)
	}
	var modes []string
	f.Set(func(f *testfake.Telegram) { modes = append(modes, f.Modes...) })
	if strings.Join(modes, ",") != "HTML,," { // HTML rejected, plain 5xx, plain ok
		t.Fatalf("modes %q", modes)
	}
	if got := f.Texts(); len(got) != 1 || got[0] != "a<b is down" {
		t.Fatalf("%q", got)
	}
}
