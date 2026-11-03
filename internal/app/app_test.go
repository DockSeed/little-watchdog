// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/clock"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/telegram"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

const tok = "123456:SECRETTOKEN"

// fk is the virtual clock of the app made by the latest newApp.
var fk *clock.Fake

type scripted struct {
	id  string
	res atomic.Value // checks.Result
}

func (s *scripted) ID() string { return s.id }

func (s *scripted) Name() string { return s.id }

func (s *scripted) Type() string { return "http" }

func (s *scripted) Run(context.Context) checks.Result { return s.res.Load().(checks.Result) }

func (s *scripted) set(r checks.Result) { s.res.Store(r) }

func newApp(t *testing.T, apiURL, dmURL string, buf *bytes.Buffer) (*App, *scripted) {
	cfg := config.Defaults()
	cfg.Token, cfg.ChatID, cfg.APIURL, cfg.DeadmanURL = tok, 42, apiURL, dmURL
	cfg.FailAfter, cfg.RecoverAfter = 2, 1
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	cfg.Timeout = time.Second
	a := New(cfg, buf)
	a.Host, a.Eng.Host = "testhost", "testhost"
	a.TG.Backoff = time.Millisecond
	fk = clock.NewFake()
	a.SetClock(fk.Clock())
	s := &scripted{id: "http:web"}
	s.set(checks.Result{Status: checks.OK})
	a.Checks = []checks.Check{s}
	return a, s
}

func TestCycleSendsOnlyOnChange(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	ctx := context.Background()
	a.Cycle(ctx)
	bad := checks.Result{Status: checks.FAIL, Detail: "HTTP 502"}
	s.set(bad)
	for i := 0; i < 5; i++ {
		a.Cycle(ctx)
	}
	s.set(checks.Result{Status: checks.OK})
	a.Cycle(ctx)
	a.Cycle(ctx)
	got := f.Texts()
	if len(got) != 2 || !strings.HasPrefix(got[0], "❌ <b>http:web</b> on <b>testhost</b> is down since ") || !strings.HasSuffix(got[0], ": HTTP 502.") || !strings.HasPrefix(got[1], "✅ <b>http:web</b> on <b>testhost</b> is back after ") {
		t.Fatalf("%q", got)
	}
}

func TestUndeliveredMessageIsRetried(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	ctx := context.Background()
	a.Cycle(ctx)
	f.Set(func(f *testfake.Telegram) { f.Fail = true })
	s.set(checks.Result{Status: checks.FAIL, Detail: "HTTP 502"})
	a.Cycle(ctx)
	a.Cycle(ctx) // alarm raised, send fails
	if len(f.Texts()) != 0 {
		t.Fatal("sent despite failure")
	}
	f.Set(func(f *testfake.Telegram) { f.Fail = false })
	a.Cycle(ctx)
	a.Cycle(ctx)
	if got := f.Texts(); len(got) != 1 {
		t.Fatalf("want exactly one alarm, got %q", got)
	}
}

func TestDeadmanFailAndRecovery(t *testing.T) {
	f, tsrv := testfake.Start(tok)
	defer tsrv.Close()
	var paths []string
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { paths = append(paths, r.URL.Path); w.Write([]byte("OK")) }))
	defer dm.Close()
	var buf bytes.Buffer
	a, s := newApp(t, tsrv.URL, dm.URL+"/u", &buf)
	ctx := context.Background()
	fk.Advance(2 * time.Minute)
	a.Cycle(ctx)
	a.Deliver(ctx) // the first-ping notice goes out while Telegram works
	f.Set(func(f *testfake.Telegram) { f.Fail = true })
	s.set(checks.Result{Status: checks.FAIL, Detail: "x"})
	fk.Advance(2 * time.Minute)
	a.Cycle(ctx)
	fk.Advance(2 * time.Minute)
	a.Cycle(ctx) // alarm + failed send #1
	fk.Advance(2 * time.Minute)
	a.Cycle(ctx) // #2
	if got := strings.Join(paths, " "); strings.Contains(got, "fail") {
		t.Fatalf("below threshold, must not ping /fail: %s\nlog:\n%s", got, buf.String())
	}
	fk.Advance(2 * time.Minute)
	a.Cycle(ctx) // #3: probe sendMessage fails too
	if got := strings.Join(paths, " "); !strings.Contains(got, "/u/fail") {
		t.Fatalf("want /fail: %s", got)
	}
	// Telegram back: the pending alarm goes out, that send resets the counter
	f.Set(func(f *testfake.Telegram) { f.Fail = false })
	paths = nil
	fk.Advance(2 * time.Minute)
	a.Cycle(ctx)
	if a.DM.Fails() != 0 || len(paths) == 0 || paths[len(paths)-1] != "/u" {
		t.Fatalf("fails=%d paths %v", a.DM.Fails(), paths)
	}
}

func TestTokenMaskedInLog(t *testing.T) {
	var buf bytes.Buffer
	a, _ := newApp(t, "http://127.0.0.1:1", "", &buf)
	a.Log.Printf("oops %s", tok)
	a.Cycle(context.Background())
	a.TG.SendMessage(context.Background(), 1, "x")
	if strings.Contains(buf.String(), "SECRETTOKEN") || !strings.Contains(buf.String(), "<token>") {
		t.Fatal(buf.String())
	}
}

func TestPollAnswersStatusAfterStart(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	ctx, cancel := context.WithCancel(context.Background())
	a.Cycle(ctx)
	done := make(chan struct{})
	go func() { a.Poll(ctx); close(done) }()
	time.Sleep(300 * time.Millisecond)
	f.AddMessage(999, "/status")
	f.AddMessage(42, "/status@fakebot")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(f.Texts()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	got := f.Texts()
	if len(got) != 1 || !strings.Contains(got[0], "http:web") {
		t.Fatalf("%q", got)
	}
}

func TestPoll409LoggedOnce(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.Fail409 = true })
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	a.Poll(ctx)
	if n := strings.Count(buf.String(), "409"); n != 1 {
		t.Fatalf("409 logged %d times: %s", n, buf.String())
	}
	f.Set(func(f *testfake.Telegram) { f.Fail409 = false })
}

func TestRunStartMessage(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(f.Texts()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := f.Texts(); len(got) == 0 || got[0] != "<b>testhost</b>: watching 1 check (<b>http:web</b>). /status shows the details." {
		t.Fatalf("%q", got)
	}
}

func (s *scripted) fail(detail string) { s.set(checks.Result{Status: checks.FAIL, Detail: detail}) }

func addCheck(a *App, id string) *scripted {
	s := &scripted{id: id}
	s.set(checks.Result{Status: checks.OK})
	a.Checks = append(a.Checks, s)
	return s
}

func TestStartText(t *testing.T) {
	mk := func(names ...string) []checks.Check {
		var cs []checks.Check
		for _, n := range names {
			cs = append(cs, &scripted{id: n})
		}
		return cs
	}
	got := StartText("box", mk("disk /", "memory", "temperature", "cpu"))
	if want := "<b>box</b>: watching 4 checks (<b>disk /</b>, <b>memory</b>, <b>temperature</b>, <b>cpu</b>). /status shows the details."; got != want {
		t.Fatalf("%q", got)
	}
	if got := StartText("", mk("disk")); got != "Watching 1 check (<b>disk</b>). /status shows the details." {
		t.Fatalf("%q", got)
	}
	got = StartText("box", mk("a", "b", "c", "d", "e", "f", "g", "h"))
	if want := "<b>box</b>: watching 8 checks (<b>a</b>, <b>b</b>, <b>c</b>, <b>d</b>, <b>e</b> and 3 more). /status shows the details."; got != want {
		t.Fatalf("%q", got)
	}
	long := strings.Repeat("n", 60)
	got = StartText("box", mk(long+"1", long+"2", long+"3", long+"4"))
	if len([]rune(telegram.Plain(got))) > 200 || !strings.Contains(got, " more). /status") {
		t.Fatalf("%d: %q", len([]rune(got)), got)
	}
}

func TestMessagesWithoutHost(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	a.Host, a.Eng.Host = "", ""
	ctx := context.Background()
	a.Cycle(ctx)
	s.fail("down")
	for i := 0; i < 3; i++ {
		a.Cycle(ctx)
	}
	got := f.Texts()
	if len(got) != 1 || !strings.HasPrefix(got[0], "❌ <b>http:web</b> is down since ") || !strings.HasSuffix(got[0], ": down.") {
		t.Fatalf("%q", got)
	}
}

func TestRunSavesStateOnShutdown(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- a.Run(ctx) }()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && len(f.Texts()) == 0; {
		time.Sleep(20 * time.Millisecond)
	}
	cfg, _, _, _ := a.cur()
	os.Remove(cfg.StateFile)
	cancel()
	<-done
	if _, err := os.Stat(cfg.StateFile); err != nil {
		t.Fatalf("no state saved on shutdown: %v", err)
	}
}
