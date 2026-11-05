// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/state"
	"github.com/DockSeed/little-watchdog/internal/telegram"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

const tok2 = "654321:OTHERSECRET"

type rl struct {
	t     *testing.T
	a     *App
	path  string
	api   string
	state string
	buf   *bytes.Buffer
	f     *testfake.Telegram
}

func (r *rl) body(token string, extra ...string) string {
	return "TELEGRAM_TOKEN=" + token + "\nTELEGRAM_CHAT_ID=42\nTELEGRAM_API_URL=" + r.api + "\nSTATE_FILE=" + r.state +
		"\nFAIL_AFTER=1\nRECOVER_AFTER=1\nINTERVAL=10s\nTIMEOUT=2s\nDISK_MIN_FREE_PCT=0\nMEM_MIN_AVAIL_PCT=0\nTEMP_MAX_C=0\nCPU_WINDOW=1h\n" +
		strings.Join(extra, "\n") + "\n"
}

// write replaces the config the way the TUI does: temp file, rename.
func (r *rl) write(s string) {
	r.t.Helper()
	tmp := r.path + ".t"
	if err := os.WriteFile(tmp, []byte(s), 0o600); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		r.t.Fatal(err)
	}
}

func newRL(t *testing.T, accept string, extra ...string) *rl {
	f, srv := testfake.Start(accept)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	r := &rl{t: t, api: srv.URL, state: filepath.Join(dir, "state.json"), path: filepath.Join(dir, "c.env"), buf: &bytes.Buffer{}, f: f}
	r.write(r.body(tok, extra...))
	sig := StatConfig(r.path)
	cfg, err := config.Load(r.path, true)
	if err != nil {
		t.Fatal(err)
	}
	r.a = New(cfg, r.buf)
	r.a.Host, r.a.Eng.Host = "testhost", "testhost"
	r.a.Watch(r.path, sig)
	return r
}

func (r *rl) names() []string {
	var n []string
	for _, e := range r.a.Eng.Snapshot() {
		n = append(n, e.Name)
	}
	return n
}

func (r *rl) invalidMsgs() int {
	n := 0
	for _, s := range r.f.Texts() {
		n += strings.Count(s, "the config file has a problem")
	}
	return n
}

func TestReloadAppliesChangedChecksKeepsState(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	addr := l.Addr().String()
	r := newRL(t, tok, "TCP_A="+addr, "TCP_B="+addr)
	ctx := context.Background()
	r.a.Cycle(ctx)
	a1 := r.a.Checks[0]
	since := r.a.Eng.Snapshot()[0].Since
	r.write(r.body(tok, "TCP_A="+addr, "TCP_C="+addr, "INTERVAL=20s"))
	r.a.Cycle(ctx)
	has := map[string]bool{}
	for _, n := range r.names() {
		has[n] = true
	}
	if has["b"] || !has["c"] || !has["a"] {
		t.Fatalf("names %v", r.names())
	}
	if r.a.Checks[0] != a1 || r.a.Eng.Snapshot()[0].Since != since {
		t.Fatal("unchanged check was rebuilt or lost its state")
	}
	if r.a.Cfg.Interval.Seconds() != 20 || !strings.Contains(r.buf.String(), "config reloaded") {
		t.Fatalf("interval %v log %q", r.a.Cfg.Interval, r.buf.String())
	}
}

func TestReloadInvalidKeepsOldAndNotifiesOnce(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	addr := l.Addr().String()
	r := newRL(t, tok, "TCP_A="+addr)
	ctx := context.Background()
	r.a.Cycle(ctx)
	r.write(r.body(tok, "TCP_A="+addr, "TCP_X=nonsense"))
	for i := 0; i < 3; i++ {
		r.a.Cycle(ctx)
	}
	r.write(r.body(tok, "TCP_A="+addr, "TCP_X=still:nonsense:0"))
	r.a.Cycle(ctx)
	r.a.Cycle(ctx)
	texts := r.f.Texts()
	if r.invalidMsgs() != 1 || !strings.Contains(strings.Join(texts, "\n"), "❌ <b>testhost</b>: the config file has a problem, so the previous one stays in use: config line 14: TCP_X") {
		t.Fatalf("texts %q", texts)
	}
	if strings.Contains(strings.Join(texts, "\n"), "nonsense") || len(r.a.Checks) == 0 || r.a.Checks[0].Name() != "a" {
		t.Fatal("old config not kept or content leaked", texts)
	}
	r.write(r.body(tok, "TCP_A="+addr))
	r.a.Cycle(ctx)
	r.write(r.body(tok, "TCP_A="+addr, "TCP_Y=bad"))
	r.a.Cycle(ctx)
	r.a.Cycle(ctx) // the second failed read announces it
	if r.invalidMsgs() != 2 {
		t.Fatalf("a new invalid episode must report again: %q", r.f.Texts())
	}
}

func TestReloadTokenChangeNewClient(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	r := newRL(t, tok2, "TCP_A="+addr) // fake accepts only tok2
	ctx := context.Background()
	r.a.Cycle(ctx)
	old := r.a.TG
	l.Close() // now FAIL, reported at once (FAIL_AFTER=1)
	r.write(r.body(tok2, "TCP_A="+addr))
	r.a.Cycle(ctx)
	if r.a.TG == old || r.a.Cfg.Token != tok2 {
		t.Fatal("no new client")
	}
	if got := r.f.Texts(); len(got) != 1 || !strings.Contains(got[0], "is down since") {
		t.Fatalf("alarm must go out with the new token: %q", got)
	}
	r.a.Log.Printf("leak %s %s", tok, tok2)
	if strings.Contains(r.buf.String(), "SECRETTOKEN") || strings.Contains(r.buf.String(), "OTHERSECRET") {
		t.Fatal("token in log:", r.buf.String())
	}
}

func TestReloadUnchangedFileIsIgnored(t *testing.T) {
	r := newRL(t, tok)
	r.a.Cycle(context.Background())
	r.a.Cycle(context.Background())
	if strings.Contains(r.buf.String(), "reloaded") {
		t.Fatal(r.buf.String())
	}
}

func TestReloadSignalReadsConfigAtOnce(t *testing.T) {
	r := newRL(t, tok, "TCP_A=127.0.0.1:1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool { return len(r.names()) > 0 })
	// same size and mtime as far as the stat goes: only the signal can make the daemon look again
	body := r.body(tok, "TCP_A=127.0.0.1:1", "TCP_B=127.0.0.1:1")
	r.write(body)
	if err := os.Chtimes(r.path, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	r.a.Reload()
	waitFor(t, func() bool { return len(r.names()) == 2 })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 2000; i++ { // 20 s: only a failure waits this long
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestWaitConfigWaitsForAFixAndTellsOnce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.env")
	os.WriteFile(p, []byte("TELEGRAM_TOKEN=1:abc\nTELEGRAM_CHAT_ID=7\nTCP_X=nonsense\n"), 0o600)
	var told []string
	var buf bytes.Buffer
	go func() {
		time.Sleep(150 * time.Millisecond)
		os.WriteFile(p, []byte("TELEGRAM_TOKEN=1:abc\nTELEGRAM_CHAT_ID=7\nTCP_X=h:1\n"), 0o600)
	}()
	cfg, _, err := WaitConfig(context.Background(), p, filepath.Join(dir, "state.json"), logTo(&buf), 20*time.Millisecond, func(tok string, chat int64, _, text string) {
		told = append(told, tok+"|"+text)
	})
	if err != nil || len(cfg.Checks) != 1 {
		t.Fatal(cfg, err)
	}
	want := "1:abc|❌ " + telegram.Msg(checks.Host(), "little-watchdog cannot start: config line 3: TCP_X: invalid value (expected host:port).") +
		"\nFix it in little-wd or in <code>" + p + "</code>. <code>little-wd check</code> tests the file. It starts by itself once the config is fixed."
	if len(told) != 1 || told[0] != want {
		t.Fatalf("%q\nwant %q", told, want)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	os.WriteFile(p, []byte("nonsense\n"), 0o600)
	if _, _, err := WaitConfig(ctx, p, filepath.Join(dir, "state.json"), logTo(&buf), time.Hour, nil); err == nil {
		t.Fatal("cancel not honoured")
	}
}

func TestHalfWrittenConfigIsNotAnnounced(t *testing.T) {
	r := newRL(t, tok, "TCP_A=127.0.0.1:1")
	ctx := context.Background()
	r.a.Cycle(ctx)
	r.write("TELEGRAM_TOKEN=") // an editor in the middle of writing in place
	r.a.Cycle(ctx)
	r.write(r.body(tok, "TCP_A=127.0.0.1:1", "TCP_B=127.0.0.1:1"))
	r.a.Cycle(ctx)
	r.a.Cycle(ctx)
	if !strings.Contains(strings.Join(r.names(), ","), "b") {
		t.Fatalf("complete file not applied: %v", r.names())
	}
	for _, s := range r.f.Texts() {
		if strings.Contains(s, "config") {
			t.Fatalf("false alarm: %q", r.f.Texts())
		}
	}
}

func TestReloadClosesTheOldConnections(t *testing.T) {
	var open, closed atomic.Int32
	f := &testfake.Telegram{Token: tok}
	srv := httptest.NewUnstartedServer(f.Handler())
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		switch st {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed:
			closed.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	a.Path = filepath.Join(t.TempDir(), "c.env")
	if err := a.TG.SendOnce(context.Background(), 42, "x"); err != nil { // leaves one idle connection
		t.Fatal(err)
	}
	nc := *a.Cfg
	nc.Token = tok + "x" // a new token means a new client
	a.apply(&nc)
	waitFor(t, func() bool { return open.Load() > 0 && closed.Load() == open.Load() })
}

func logTo(w *bytes.Buffer) *log.Logger { return log.New(w, "", 0) }

func TestWaitConfigIsQuietAndKeepsAHeartbeat(t *testing.T) { // fresh install: no config yet
	dir := t.TempDir()
	p := filepath.Join(dir, "c.env")
	sf := filepath.Join(dir, "state.json")
	var buf bytes.Buffer
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.WriteFile(p, []byte("TELEGRAM_TOKEN=1:abc\nTELEGRAM_CHAT_ID=7\n"), 0o600)
	}()
	if _, _, err := WaitConfig(context.Background(), p, sf, logTo(&buf), 20*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "\n"); n != 1 || !strings.Contains(buf.String(), "no config yet") {
		t.Fatalf("want one log line, got:\n%s", buf.String())
	}
	if _, ok := state.LastBeat(sf); !ok {
		t.Fatal("no heartbeat while waiting: the UI would call the running daemon dead")
	}
}

func TestWaitConfigBeatsAtTheConfiguredStateFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.env")
	custom := filepath.Join(dir, "custom", "state.json")
	os.MkdirAll(filepath.Dir(custom), 0o700)
	os.WriteFile(p, []byte("STATE_FILE="+custom+"\nTCP_X=nonsense\n"), 0o600)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var buf bytes.Buffer
	WaitConfig(ctx, p, filepath.Join(dir, "default.json"), logTo(&buf), 20*time.Millisecond, nil)
	if _, ok := state.LastBeat(custom); !ok {
		t.Fatal("no heartbeat at the STATE_FILE of the config")
	}
	if _, ok := state.LastBeat(filepath.Join(dir, "default.json")); ok {
		t.Fatal("heartbeat at the default path although the config names another")
	}
}

// Switching between disk and RAM while running: the state moves with the next reload and
// the old copy is gone; the history and "since" stay.
func TestReloadMovesStateBetweenDiskAndRAM(t *testing.T) {
	ram := t.TempDir()
	t.Setenv("RUNTIME_DIRECTORY", ram)
	r := newRL(t, tok, "TCP_A=127.0.0.1:1")
	ctx := context.Background()
	r.a.Cycle(ctx)
	since := r.a.Eng.Snapshot()[1].Since // tcp:a, after the system check
	if _, err := os.Stat(r.state); err != nil {
		t.Fatal("no state on disk before the switch:", err)
	}
	r.write(r.body(tok, "TCP_A=127.0.0.1:1", "STATE_IN_RAM=yes"))
	r.a.Cycle(ctx)
	if _, err := os.Stat(r.state); !os.IsNotExist(err) {
		t.Fatalf("disk copy stays: %v", err)
	}
	if _, err := os.Stat(r.state + state.BeatSuffix); !os.IsNotExist(err) {
		t.Fatalf("disk heartbeat stays: %v", err)
	}
	st, err := state.ReadAll(filepath.Join(ram, "state.json"))
	if err != nil || len(st.Entries) < 2 || st.Entries[1].ID != "tcp:a" || !st.Entries[1].Since.Equal(since) {
		t.Fatalf("%+v %v", st, err)
	}
	if _, ok := state.LastBeat(filepath.Join(ram, "state.json")); !ok {
		t.Fatal("no heartbeat in RAM")
	}
	r.write(r.body(tok, "TCP_A=127.0.0.1:1", "STATE_IN_RAM=no"))
	r.a.Cycle(ctx)
	if _, err := os.Stat(r.state); err != nil {
		t.Fatal("state is not back on disk:", err)
	}
	if _, err := os.Stat(filepath.Join(ram, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("RAM copy stays: %v", err)
	}
}

// RAM mode switched on while the RAM directory cannot be written (unit without
// RuntimeDirectory): the daemon keeps the old place, still beats there and says why.
func TestReloadKeepsOldStateFileWhenTheNewPlaceIsNotWritable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(blocker, "run")) // cannot be created below a file
	r := newRL(t, tok, "TCP_A=127.0.0.1:1")
	ctx := context.Background()
	r.a.Cycle(ctx)
	r.write(r.body(tok, "TCP_A=127.0.0.1:1", "STATE_IN_RAM=yes"))
	r.a.Cycle(ctx)
	if got := r.a.Cfg.StateFile; got != r.state {
		t.Fatalf("daemon writes to %s, want the old %s", got, r.state)
	}
	if !strings.Contains(r.buf.String(), "RuntimeDirectory") {
		t.Fatalf("no hint in log: %s", r.buf.String())
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(r.state+state.BeatSuffix, old, old)
	r.a.Cycle(ctx)
	if _, stale := state.Stale(r.state, time.Minute, time.Now()); stale {
		t.Fatal("heartbeat froze")
	}
}

// Start-up with STATE_IN_RAM=yes and an unwritable RAM directory uses the disk path.
func TestStartFallsBackToDiskWhenRAMIsNotWritable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o600)
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(blocker, "run"))
	r := newRL(t, tok, "TCP_A=127.0.0.1:1", "STATE_IN_RAM=yes")
	r.a.Cfg.DiskState = r.state
	r.a.fallbackToDisk()
	if r.a.Cfg.StateFile != r.state || !strings.Contains(r.buf.String(), "RuntimeDirectory") {
		t.Fatalf("%s: %s", r.a.Cfg.StateFile, r.buf.String())
	}
}

// A save from the sender goroutine must not bring the old state file back after a move.
func TestMoveNeverRecreatesTheOldStateFile(t *testing.T) {
	r := newRL(t, tok, "TCP_A=127.0.0.1:1")
	a := r.a
	a.Cycle(context.Background())
	home, other := r.state, filepath.Join(t.TempDir(), "state.json")
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				a.saveState(true)
				a.endCycle()
			}
		}
	}()
	for i := 0; i < 301; i++ {
		nc := *a.Cfg
		nc.StateFile = other
		if i%2 == 1 {
			nc.StateFile = home
		}
		a.apply(&nc)
	}
	// 301 moves end at "other" for an even count: the daemon now writes there, home must stay gone.
	close(stop)
	<-done
	for _, p := range []string{home, home + state.BeatSuffix} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("old place came back: %s (%v)", p, err)
		}
	}
}
