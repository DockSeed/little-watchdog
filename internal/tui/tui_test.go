// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/state"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestDecode(t *testing.T) {
	cases := []struct {
		in   string
		want []Key
	}{
		{"\x1b[A\x1b[B\x1b[C\x1b[D", []Key{{Code: Up}, {Code: Down}, {Code: Right}, {Code: Left}}},
		{"\x1bOA", []Key{{Code: Up}}},
		{"\x1b", []Key{{Code: Esc}}},
		{"\r\n", []Key{{Code: Enter}, {Code: Enter}}},
		{"\x7f\x08", []Key{{Code: Backspace}, {Code: Backspace}}},
		{"\x03", []Key{{Code: CtrlC}}},
		{"\t", []Key{{Code: Tab}}},
		{"aä€", []Key{{Rune, 'a'}, {Rune, 'ä'}, {Rune, '€'}}},
		{"\x1b[3~x", []Key{{Rune, 'x'}}},
		{"\x1b[1;5Ax", []Key{{Code: Up}, {Rune, 'x'}}},
		{"\x01\x00", nil},
	}
	for _, c := range cases {
		if got := Decode([]byte(c.in)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Decode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rig runs the TUI on a pipe; keys go in as bytes, the last drawn frame is inspected.
type rig struct {
	t        *testing.T
	in       *io.PipeWriter
	out      *syncBuf
	done     chan error
	restored atomic.Bool
}

func start(t *testing.T, a *App) *rig {
	setGroup(t, "", "alice", nil) // a user who is not in the group, whoever runs the tests
	if a.Getenv == nil {
		a.Getenv = env("TERM", "xterm-256color", "LANG", "C.UTF-8")
	}
	pr, pw := io.Pipe()
	r := &rig{t: t, in: pw, out: &syncBuf{}, done: make(chan error, 1)}
	a.Setup = func() (func(), error) { return func() { r.restored.Store(true) }, nil }
	a.Tick = 50 * time.Millisecond
	go func() { r.done <- a.Loop(context.Background(), pr, r.out) }()
	t.Cleanup(func() { pw.Close() })
	return r
}

var sgrRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// raw is the last drawn frame with its escape sequences.
func (r *rig) raw() string {
	s := r.out.String()
	i := strings.LastIndex(s, "\x1b[H")
	if i < 0 {
		return ""
	}
	return s[i+3:]
}

// frame is the last drawn frame as plain rows.
func (r *rig) frame() string { return sgrRe.ReplaceAllString(r.raw(), "") }

func (r *rig) send(s string) { r.t.Helper(); r.in.Write([]byte(s)) }

func (r *rig) wait(sub string) {
	r.t.Helper()
	for i := 0; i < 400; i++ {
		if strings.Contains(r.frame(), sub) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("never saw %q in:\n%s", sub, r.frame())
}

func (r *rig) waitNot(sub string) {
	r.t.Helper()
	for i := 0; i < 400; i++ {
		if !strings.Contains(r.frame(), sub) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("still seeing %q in:\n%s", sub, r.frame())
}

func (r *rig) finish() {
	r.t.Helper()
	select {
	case err := <-r.done:
		if err != nil {
			r.t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		r.t.Fatal("loop did not end")
	}
	if !r.restored.Load() || !strings.HasSuffix(r.out.String(), "\x1b[?25h\x1b[?1049l") {
		r.t.Fatal("terminal not restored")
	}
}

// aliveCfg is a config whose daemon has just beaten, so "Saved" may promise a reload.
func aliveCfg(t *testing.T, body string) string {
	t.Helper()
	sf := filepath.Join(t.TempDir(), "state.json")
	if err := state.Beat(sf, time.Now()); err != nil {
		t.Fatal(err)
	}
	return newCfg(t, "STATE_FILE="+sf+"\n"+body)
}

// etcPath is a not yet existing config file in an existing directory (the tmpfiles.d one).
func etcPath(t *testing.T) string { return filepath.Join(t.TempDir(), "lw.env") }

func newCfg(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "lw.env")
	os.WriteFile(p, []byte(body), 0o600)
	return p
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// setGroup points the group lookup at a fake group file (members "" = none) and user.
func setGroup(t *testing.T, members, user string, groups []int) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "group")
	os.WriteFile(f, []byte("root:x:0:\nlittle-wd:x:55555:"+members+"\n"), 0o600)
	og, ou, op := config.GroupFile, config.CurrentUser, config.ProcessGroups
	config.GroupFile, config.CurrentUser, config.ProcessGroups = f, func() string { return user }, func() ([]int, error) { return groups, nil }
	t.Cleanup(func() { config.GroupFile, config.CurrentUser, config.ProcessGroups = og, ou, op })
}

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

// rows splits a plain frame into its terminal rows.
func rows(f string) []string { return strings.Split(f, "\r\n") }

// check80 asserts the frame is exactly 24 rows of 80 cells.
func check80(t *testing.T, f string) []string {
	t.Helper()
	rs := rows(f)
	if len(rs) != 24 {
		t.Fatalf("rows: %d\n%s", len(rs), f)
	}
	for i, r := range rs {
		if n := len([]rune(r)); n != 80 {
			t.Fatalf("row %d has %d cells: %q", i, n, r)
		}
	}
	return rs
}

func has(t *testing.T, f string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(f, s) {
			t.Fatalf("missing %q in:\n%s", s, strings.ReplaceAll(f, "\r\n", "\n"))
		}
	}
}

func lacks(t *testing.T, f string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(f, s) {
			t.Fatalf("unexpected %q in:\n%s", s, strings.ReplaceAll(f, "\r\n", "\n"))
		}
	}
}

type ent struct {
	typ, name, rep, value string
	fails                 int
	down, msg             time.Time
}

// writeState writes a state file with entries and returns its path.
func writeState(t *testing.T, es ...ent) string {
	t.Helper()
	var parts []string
	now := time.Now()
	for _, e := range es {
		j := fmt.Sprintf(`"%s:%s":{"name":%q,"type":%q,"reported":%q,"since":%q,"fails":%d,"value":%q`,
			e.typ, e.name, e.name, e.typ, e.rep, now.Add(-29*time.Second).Format(time.RFC3339), e.fails, e.value)
		if !e.down.IsZero() {
			j += `,"down_since":"` + e.down.Format(time.RFC3339) + `"`
		}
		if !e.msg.IsZero() {
			j += `,"last_msg_at":"` + e.msg.Format(time.RFC3339) + `"`
		}
		parts = append(parts, j+"}")
	}
	p := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(p, []byte(`{"version":1,"checks":{`+strings.Join(parts, ",")+`}}`), 0o600)
	return p
}

var sized = func() (int, int) { return 80, 24 }

// fixture: a check set with every kind of check (shortened).
func fixture(t *testing.T) (cfg string) {
	down := time.Now().Add(-10 * time.Minute)
	sf := writeState(t,
		ent{"http", "nextcloud", "OK", "HTTP 200 in 2ms", 0, time.Time{}, time.Time{}},
		ent{"http", "photos", "FAIL", "dial tcp 192.0.2.12:2283: connect: connection refused", 3, down, down.Add(time.Minute)},
		ent{"tcp", "dns", "OK", "open in 0ms", 0, time.Time{}, time.Time{}},
		ent{"host", "router", "OK", "open in 2ms", 0, time.Time{}, time.Time{}},
		ent{"cpu", "cpu", "OK", "5% (20s avg)", 0, time.Time{}, time.Time{}},
		ent{"disk", "disk /", "OK", "77% free", 0, time.Time{}, time.Time{}},
		ent{"temp", "temperature", "OK", "33.6 C", 0, time.Time{}, time.Time{}},
	)
	state.Beat(sf, time.Now())
	return newCfg(t, "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\nDEADMAN_URL=https://x.example/p\nSTATE_FILE="+sf+
		"\nHTTP_NEXTCLOUD=http://192.0.2.12:80/\nHTTP_PHOTOS=http://192.0.2.12:2283/\nTCP_DNS=h:53\nHOST_ROUTER=198.51.100.20:443\n")
}

func TestMenu80(t *testing.T) { // main menu
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.wait("Dead-man switch")
	rs := check80(t, r.frame())
	has(t, rs[0], "little-wd", "6 OK  1 FAIL  sent")
	lacks(t, rs[0], "›")
	has(t, rs[1], "╭", "Menu", "╮")
	has(t, rs[2], "Status", "6 OK, 1 FAIL (photos)")
	has(t, rs[3], "Add check", "Website, port or host")
	has(t, rs[4], "Remove check", "8 checks")
	has(t, rs[5], "Telegram", "chat set")
	has(t, rs[6], "Dead-man switch", "Every 2m")
	has(t, rs[19], "╰", "╯")
	has(t, rs[20], "What every check said at the daemon's last cycle (every 1m)")
	has(t, rs[22], "↑↓ move", "Enter open", "Q quit", "? help")
	raw := r.raw()
	has(t, raw, "\x1b[0;38;5;234;48;5;80m", "48;5;236") // header bar, hint bar
	r.send("q")
	r.finish()
}

func TestStatus80(t *testing.T) { // status
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.send("\r")
	r.wait("Services")
	f := r.frame()
	rs := check80(t, f)
	has(t, rs[0], "little-wd  › Status")
	has(t, rs[1], "Status")
	has(t, rs[2], "Services")
	has(t, rs[3], "[OK]", "nextcloud", "http", "HTTP 200 in 2ms", "29 s")
	has(t, f, "[FAIL] photos", "This host", "disk /", "temperature")
	if i := strings.Index(f, "This host"); i < strings.Index(f, "router") {
		t.Fatal("sections out of order")
	}
	has(t, rs[20], "nextcloud: OK since")
	r.send("j")
	r.wait("photos: failing since")
	f = r.frame()
	has(t, f, "3 of 3 tries", "Telegram sent at")
	// cursor on photos: the FAIL row is the bar; move away and the mark turns red
	r.send("j")
	r.wait("dns: OK")
	has(t, r.raw(), "1;38;5;203;48;5;234m[FAIL]")
	r.send("\x1b")
	r.wait("Menu")
	r.send("q")
	r.finish()
}

func TestDetails80(t *testing.T) { // details dialog
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.send("\rj\r")
	r.wait("[ Close ]")
	f := r.frame()
	rs := check80(t, f)
	has(t, f, "photos", "Type", "Target", "http://192.0.2.12:2283/", "FAIL since", "(3 of 3 tries)", "connection refused", "Telegram", "sent at")
	has(t, rs[3], "╭")
	has(t, r.raw(), "1;38;5;203;48;5;236m")
	r.send("\r")
	r.wait("↑↓ move  Enter details")
	r.send("\x03")
	r.finish()
}

func TestAdd80(t *testing.T) { // add check dialog
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.send("j\r")
	r.wait("Check and save")
	f := r.frame()
	rs := check80(t, f)
	has(t, rs[0], "little-wd  › Add check")
	has(t, f, "Kind", "(•) Website", "( ) Port", "( ) Host", "Name", "URL", "Text", "Checked once before saving.", "[ Check and save ]   [ Cancel ]")
	has(t, rs[20], "Name: letters, digits and _ only")
	has(t, rs[22], "Enter next", "Esc cancel")
	r.send("\x1b[A\x1b[C") // kind: Port
	r.wait("(•) Port")
	lacks(t, r.frame(), "Text")
	r.send("\t\t")
	r.send("\x1b[Z\x1b[Z") // back to kind (Tab cycles, BackTab too)
	r.send("\x1b")
	r.wait("Menu")
	r.send("q")
	r.finish()
}

func TestRemoveList80(t *testing.T) { // remove list, both sections
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.send("jj\r")
	r.wait("This host")
	f := r.frame()
	rs := check80(t, f)
	has(t, rs[2], "Services")
	has(t, f, "[OK]", "nextcloud", "http://192.0.2.12:80/", "router", "198.51.100.20:443",
		"cpu", "alarm above 90% (15m average)", "memory", "alarm below 5% available", "disk /", "alarm below 10% free", "temperature", "alarm above 85 C")
	if strings.Index(f, "This host") < strings.Index(f, "router") {
		t.Fatal("sections out of order")
	}
	r.send("\x03")
	r.finish()
}

func TestRemoveConfirm80(t *testing.T) { // remove confirmation
	p := fixture(t)
	r := start(t, &App{Path: p, Size: sized})
	r.send("jj\r")
	r.wait("This host")
	r.send("j\r")
	r.wait("Remove photos?")
	f := r.frame()
	rs := check80(t, f)
	has(t, f, "It is no longer checked and no longer reported.", "[ Remove ]   [ Cancel ]")
	lacks(t, f, "turn it on again")
	has(t, rs[22], "Tab next button", "Enter choose", "Esc cancel")
	r.send("\t\r") // Cancel
	r.waitNot("Remove photos?")
	r.send("\r")
	r.wait("Remove photos?")
	r.send("\r")
	r.wait("Removed photos.")
	if strings.Contains(read(t, p), "HTTP_PHOTOS") {
		t.Fatal(read(t, p))
	}
	r.send("\x03")
	r.finish()
}

// removeHost opens Remove check, moves down n rows (section headers are skipped) and confirms.
func removeHost(t *testing.T, cfg string, n int, want string) (string, *rig) {
	t.Helper()
	p := newCfg(t, cfg)
	r := start(t, &App{Path: p, Size: sized})
	r.send("jj\r")
	r.wait("This host")
	r.send(strings.Repeat("j", n) + "\r")
	r.wait("Remove " + want + "?")
	return p, r
}

func TestRemoveHostChecks(t *testing.T) {
	base := "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\nTCP_B=h:1\n"
	for _, c := range []struct {
		n                int
		name, back, line string
	}{
		{1, "cpu", "CPU_MAX_PCT=90", "CPU_MAX_PCT=0\n"},
		{2, "memory", "MEM_MIN_AVAIL_PCT=5", "MEM_MIN_AVAIL_PCT=0\n"},
		{3, "disk /", "DISK_PATHS=/", "DISK_PATHS=\n"},
		{4, "temperature", "TEMP_MAX_C=85", "TEMP_MAX_C=0\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, r := removeHost(t, base, c.n, c.name)
			has(t, r.frame(), "To turn it on again", c.back)
			r.send("\r")
			r.wait("Saved")
			if got := read(t, p); got != base+c.line {
				t.Fatalf("%q", got)
			}
			r.send("\x03")
			r.finish()
		})
	}
}

func TestRemoveDiskPathFromList(t *testing.T) {
	base := "TCP_B=h:1\nDISK_PATHS=/,/data,/home\n"
	p, r := removeHost(t, base, 4, "disk /data") // cpu, memory, disk /, disk /data
	has(t, r.frame(), "DISK_PATHS=/,/data,/home")
	r.send("\r")
	r.wait("Saved")
	if got := read(t, p); got != "TCP_B=h:1\nDISK_PATHS=/,/home\n" {
		t.Fatalf("%q", got)
	}
	r.send("x")
	r.wait("Menu")
	r.send("\r") // the menu cursor is still on Remove check
	r.wait("disk /home")
	r.send("jjjj\r")
	r.wait("Remove disk /home?")
	r.send("\r")
	r.wait("Saved")
	r.send("x")
	r.wait("Menu")
	r.send("\r")
	r.wait("This host")
	r.send("jjj\r")
	r.wait("Remove disk /?")
	r.send("\r")
	r.wait("Saved")
	if got := read(t, p); got != "TCP_B=h:1\nDISK_PATHS=\n" {
		t.Fatalf("%q", got)
	}
	r.send("x")
	r.send("\x03")
	r.finish()
}

func TestTelegram80(t *testing.T) { // Telegram setup
	const tok = "123456:SECRETTOKEN"
	f, srv := testfake.Start(tok)
	defer srv.Close()
	p := etcPath(t)
	r := start(t, &App{Path: p, APIURL: srv.URL, PollWindow: 5 * time.Minute, Size: sized})
	r.wait("Bot token")
	check80(t, r.frame())
	r.send(tok + "\r")
	r.wait("/start ")
	fr := r.frame()
	rs := check80(t, fr)
	has(t, rs[0], "little-wd  › Telegram setup")
	has(t, fr, "[✓] 1  Bot token", "accepted, bot @", "[✓] 2  Webhook", "none set", "[ ] 3  Link your chat", "[ ] 4  Test message", "left", "[ Cancel ]")
	has(t, rs[22], "Esc cancel")
	lacks(t, r.out.String(), tok)
	_, rest, _ := strings.Cut(fr, "/start ")
	f.AddMessage(777, "/start "+rest[:6])
	r.wait("Saved")
	r.send("x")
	r.wait("Menu")
	r.send("q")
	r.finish()
}

func TestTelegramWebhookDialog(t *testing.T) {
	f, srv := testfake.Start("1:a")
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.Webhook = "https://example.org/hook" })
	r := start(t, &App{Path: filepath.Join(t.TempDir(), "lw.env"), APIURL: srv.URL, PollWindow: time.Minute, Size: sized})
	r.send("1:a\r")
	r.wait("[ Delete webhook ]")
	has(t, r.frame(), "Delete the webhook now?", "[ Cancel ]")
	r.send("\x1b")
	r.wait("aborted: webhook still set")
	r.send("x")
	r.send("\x03")
	r.finish()
}

func TestExplore80(t *testing.T) { // explore mode
	skipRoot(t)
	p := fixture(t)
	os.Chmod(p, 0)
	t.Cleanup(func() { os.Chmod(p, 0o600) })
	r := start(t, &App{Path: p, Size: sized})
	r.wait("Dead-man switch")
	rs := check80(t, r.frame())
	has(t, rs[0], "explore mode, no changes")
	has(t, rs[3], "Add check", "unknown")
	r.send("j")
	r.wait("Needs group little-wd: sudo usermod -aG little-wd alice,")
	// locked items are grey, Status is not
	has(t, r.raw(), "48;5;240", "1;38;5;231;48;5;234m")
	r.send("\x03")
	r.finish()
}

func TestReadOnlyHeader(t *testing.T) {
	skipRoot(t)
	p := newCfg(t, "TCP_B=h:1\n")
	dir := filepath.Dir(p)
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	r := start(t, &App{Path: p, Size: sized})
	r.wait("read-only")
	r.send("\x03")
	r.finish()
}

func TestHelp80(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.send("?")
	r.wait("[ Close ]")
	f := r.frame()
	check80(t, f)
	has(t, f, "Keys", "move", "this help", "Esc")
	r.send("\x1b")
	r.wait("↑↓ move  Enter open")
	lacks(t, r.frame(), "[ Close ]")
	r.send("q")
	r.finish()
}

func TestASCIIFallback(t *testing.T) {
	for name, e := range map[string]func(string) string{
		"vt100":  env("TERM", "vt100", "LANG", "C.UTF-8"),
		"nolang": env("TERM", "xterm-256color", "LANG", "C"),
		"lcall":  env("TERM", "xterm", "LANG", "de_DE.UTF-8", "LC_ALL", "C"),
	} {
		t.Run(name, func(t *testing.T) {
			r := start(t, &App{Path: fixture(t), Size: sized, Getenv: e})
			r.wait("Dead-man switch")
			f := r.frame()
			rs := check80(t, f)
			has(t, f, "+-", "^v move", "|")
			lacks(t, f, "╭", "│", "↑", "›", "─")
			if !strings.HasPrefix(rs[1], "+") || !strings.HasSuffix(rs[1], "+") || !strings.HasPrefix(rs[2], "|") {
				t.Fatal(f)
			}
			r.send("\r")
			r.wait("Services")
			has(t, r.frame(), "little-wd  > Status", "[OK]")
			r.send("\x1b")
			r.send("jjj\r")
			r.wait("Bot token")
			r.send("\x1b")
			r.send("q")
			r.finish()
		})
	}
}

func TestASCIIMarks(t *testing.T) {
	f, srv := testfake.Start("1:a")
	defer srv.Close()
	_ = f
	r := start(t, &App{Path: filepath.Join(t.TempDir(), "lw.env"), APIURL: srv.URL, PollWindow: time.Minute, Size: sized,
		Getenv: env("TERM", "vt100")})
	r.send("1:a\r")
	r.wait("/start ")
	has(t, r.frame(), "[x] 1  Bot token", "[ ] 3  Link your chat")
	lacks(t, r.frame(), "✓")
	r.send("\x1b")
	r.wait("Cancelled")
	r.send("x")
	r.send("\x03")
	r.finish()
}

func TestNoColor80(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: sized, Getenv: env("NO_COLOR", "1", "TERM", "xterm-256color", "LANG", "C.UTF-8")})
	r.wait("Dead-man switch")
	check80(t, r.frame())
	raw := r.raw()
	lacks(t, raw, "38;5;", "48;5;", ";40m", ";46m")
	has(t, raw, "\x1b[0;1;7m") // header: bold + reverse
	r.send("\r")
	r.wait("Services")
	lacks(t, r.raw(), "38;5;", "203")
	has(t, r.raw(), "\x1b[0;7m") // cursor bar is reverse video
	r.send("\x03")
	r.finish()
}

func TestSixteenColours(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: sized, Getenv: env("TERM", "linux", "LANG", "C.UTF-8")})
	r.wait("Dead-man switch")
	lacks(t, r.raw(), "38;5;")
	has(t, r.raw(), "30;46")
	r.send("\x03")
	r.finish()
}

func TestLongInputField(t *testing.T) { // long URL typed into the dialog
	long := "https://example.org/" + strings.Repeat("abcdefghij/", 12) + "end"
	if len(long) <= 100 {
		t.Fatal("test URL too short")
	}
	r := start(t, &App{Path: newCfg(t, "TELEGRAM_TOKEN=1:a\n"), Size: sized})
	r.send("j\r")
	r.wait("Check and save")
	r.send("n\r" + long)
	r.wait("end_")
	f := r.frame()
	rs := check80(t, f)
	for _, row := range rs {
		if strings.Contains(row, "end_") {
			if !strings.Contains(row, "URL       <") || !strings.Contains(row, "end_") {
				t.Fatalf("no start marker: %q", row)
			}
			if !strings.HasSuffix(strings.TrimRight(row, " │"), "_") {
				t.Fatalf("cursor not at the end: %q", row)
			}
		}
	}
	// all fields of a dialog are equally wide: the Name row's field fills the dialog too
	var nameRow string
	for _, row := range rs {
		if strings.Contains(row, "Name") && strings.Contains(row, "│") && strings.Contains(row, "Name  ") {
			nameRow = row
		}
	}
	r.send("\x1b[A") // Name field: unfocused URL shows its beginning with '>'
	r.wait("Name: letters")
	has(t, r.frame(), "https://example.org/abcdefghij", ">")
	if nameRow == "" {
		t.Fatal("no name row")
	}
	// the same in ASCII
	r2 := start(t, &App{Path: newCfg(t, "TELEGRAM_TOKEN=1:a\n"), Size: sized, Getenv: env("TERM", "vt100")})
	r2.send("j\r")
	r2.wait("Check and save")
	r2.send("n\r" + long)
	r2.wait("end_")
	has(t, r2.frame(), "URL       <")
	r2.send("\x03")
	r2.finish()
	r.send("\x03")
	r.finish()
}

func TestLongValuesDisplay(t *testing.T) {
	url := "http://192.0.2.12:2283/" + strings.Repeat("segment/", 15) + "end"
	val := "dial tcp: " + strings.Repeat("x", 120) + "ENDVALUE"
	sf := writeState(t, ent{"http", "photos", "FAIL", val, 3, time.Now().Add(-time.Hour), time.Time{}})
	p := newCfg(t, "STATE_FILE="+sf+"\nHTTP_PHOTOS="+url+"\n")
	r := start(t, &App{Path: p, Size: sized})
	r.send("\r")
	r.wait("[FAIL] photos")
	rs := check80(t, r.frame())
	has(t, rs[3], "[FAIL] photos", "~") // status row cut at the edge
	r.send("\r")
	r.wait("[ Close ]")
	f := r.frame()
	check80(t, f)
	// the whole URL is on screen, wrapped over rows
	var sb strings.Builder
	for _, row := range rows(f) {
		if i := strings.Index(row, "│"); i >= 0 {
			if cells := []rune(row); len(cells) > 12 {
				sb.WriteString(strings.TrimRight(string(cells[strings.Count(row[:i], "")-1:]), " "))
			}
		}
	}
	flat := strings.NewReplacer(" ", "", "│", "", "Target", "", "Last", "").Replace(f)
	flat = strings.ReplaceAll(flat, "\r\n", "")
	if !strings.Contains(flat, url) || !strings.Contains(flat, "ENDVALUE") {
		t.Fatalf("value cut instead of wrapped:\n%s", f)
	}
	r.send("\x03")
	r.finish()
}

func TestAddWebsiteAndQuit(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "hello world") }))
	defer web.Close()
	p := aliveCfg(t, "# my config\nTELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n")
	pre, _, _ := strings.Cut(read(t, p), "# my config")
	r := start(t, &App{Path: p})
	r.wait("Add check")
	r.send("j\r")
	r.wait("Check and save")
	r.send("my_web\r" + web.URL + "\rhello\r")
	r.wait("Saved")
	if f := r.frame(); !strings.Contains(f, "OK") || !strings.Contains(f, "applies it within one interval") {
		t.Fatal(f)
	}
	want := pre + "# my config\nTELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\nHTTP_MY_WEB=" + web.URL + " contains=hello\n"
	if got := read(t, p); got != want {
		t.Fatalf("%q", got)
	}
	if got := read(t, p+".bak"); !strings.Contains(got, "# my config") || strings.Contains(got, "HTTP_") {
		t.Fatalf("bak %q", got)
	}
	r.send("x") // back to menu
	r.wait("Menu")
	r.send("q")
	r.finish()
}

func TestAddPortKindAndButtons(t *testing.T) {
	p := newCfg(t, "TCP_A=h:1\n")
	r := start(t, &App{Path: p})
	r.send("j\r")
	r.wait("Check and save")
	r.send("\x1b[A \x1b[B") // kind: space picks Port; back to Name
	r.wait("( ) Website  (•) Port")
	r.send("a\rh:2\r") // name A exists
	r.wait("exists, remove it first")
	r.send("\t\t\r") // Tab to [ Cancel ]... fields: name, target, then buttons
	r.wait("Menu")
	if read(t, p) != "TCP_A=h:1\n" {
		t.Fatal("config changed")
	}
	r.send("\x03")
	r.finish()
}

func TestAddInvalidStaysInForm(t *testing.T) {
	p := newCfg(t, "TCP_A=h:1\n")
	r := start(t, &App{Path: p})
	r.send("j\r")
	r.send("\x1b[A\x1b[C\x1b[C\x1b[B") // up to the kind, Host, down to Name
	r.wait("(•) Host")
	r.send("a\rh:2\r")
	r.wait("Error: ")
	r.send("\x1b[A\x7fb\r") // name back to "b", target stays
	r.send("\x7f\x7fnonsense\r")
	r.wait("Error: config line 2: HOST_B")
	if read(t, p) != "TCP_A=h:1\n" {
		t.Fatal("config changed")
	}
	r.send("\x03")
	r.finish()
}

func TestRemoveCheck(t *testing.T) {
	p := aliveCfg(t, "# keep\nHTTP_A=http://a.example/\nTCP_B=h:1\n")
	pre, _, _ := strings.Cut(read(t, p), "# keep")
	r := start(t, &App{Path: p})
	r.send("jj\r") // Remove check
	r.wait("http://a.example/")
	r.send("j\r")
	r.wait("Remove b?")
	r.send("n")
	r.send("\r")
	r.wait("Remove b?")
	r.send("y")
	r.wait("applies it within one interval")
	if got := read(t, p); got != pre+"# keep\nHTTP_A=http://a.example/\n" {
		t.Fatalf("%q", got)
	}
	r.send("x")
	r.send("q")
	r.finish()
}

func TestDeadman(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.Write([]byte("OK")) }))
	defer srv.Close()
	p := newCfg(t, "TELEGRAM_TOKEN=1:a\n")
	r := start(t, &App{Path: p, Size: sized})
	r.send("jjjj\r")
	r.wait("[ Save ]")
	check80(t, r.frame())
	r.send(srv.URL + "/ping\r")
	r.wait("Saved")
	if hits.Load() != 1 || !strings.Contains(r.frame(), "Ping OK") || !strings.Contains(read(t, p), "DEADMAN_URL="+srv.URL+"/ping\n") {
		t.Fatal(hits.Load(), r.frame())
	}
	r.send("x") // back to the menu (cursor stays on Dead-man switch), open it again: empty = off
	r.wait("Every 2m")
	r.send("\r")
	r.wait("[ Save ]")
	r.send("\r")
	r.wait("Dead-man switch off")
	if strings.Contains(read(t, p), "DEADMAN") {
		t.Fatal(read(t, p))
	}
	r.send("\x03")
	r.finish()
}

func TestTelegramFirstRun(t *testing.T) {
	const tok = "123456:SECRETTOKEN"
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.Webhook = "https://example.org/hook" })
	p := etcPath(t)
	r := start(t, &App{Path: p, APIURL: srv.URL, PollWindow: 5 * time.Second})
	r.wait("Bot token")
	r.send(tok + "\r")
	if strings.Contains(r.frame(), tok) {
		t.Fatal("token on screen")
	}
	r.wait("Delete the webhook now?")
	r.send("y")
	r.wait("waiting ...")
	_, rest, _ := strings.Cut(r.frame(), "/start ")
	f.AddMessage(777, "/start "+rest[:6])
	r.wait("Saved")
	if strings.Contains(r.out.String(), tok) {
		t.Fatal("token printed")
	}
	cfg := read(t, p)
	if !strings.Contains(cfg, "TELEGRAM_TOKEN="+tok+"\n") || !strings.Contains(cfg, "TELEGRAM_CHAT_ID=777\n") {
		t.Fatal(cfg)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o660 {
		t.Fatal(st.Mode())
	}
	r.send("x")
	r.wait("Menu")
	r.send("q")
	r.finish()
}

func TestTelegramEscCancels(t *testing.T) {
	f, srv := testfake.Start("1:a")
	_ = f
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "lw.env")
	r := start(t, &App{Path: p, APIURL: srv.URL, PollWindow: time.Minute})
	r.send("1:a\r")
	r.wait("Esc cancel")
	r.send("\x1b")
	r.wait("Cancelled")
	if _, err := os.Stat(p); err == nil {
		t.Fatal("wrote config")
	}
	r.send("x")
	r.send("\x03")
	r.finish()
}

func TestRestoreOnPanicAndCancel(t *testing.T) {
	var out syncBuf
	var restored atomic.Bool
	a := &App{Path: newCfg(t, ""), Size: func() (int, int) { panic("boom") },
		Setup: func() (func(), error) { return func() { restored.Store(true) }, nil }}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("no panic")
			}
		}()
		a.Loop(context.Background(), strings.NewReader(""), &out)
	}()
	if !restored.Load() || !strings.HasSuffix(out.String(), "\x1b[?1049l") {
		t.Fatal("not restored after panic")
	}
	// context cancel (SIGTERM path)
	pr, pw := io.Pipe()
	defer pw.Close()
	restored.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	a.Size = nil
	errc := make(chan error, 1)
	go func() { errc <- a.Loop(ctx, pr, &out) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-errc; err != nil || !restored.Load() {
		t.Fatal(err, restored.Load())
	}
}

func TestSmallTerminal(t *testing.T) {
	for _, sz := range [][2]int{{20, 5}, {1, 1}, {10, 3}, {200, 60}, {80, 8}} {
		r := start(t, &App{Path: fixture(t), Size: func() (int, int) { return sz[0], sz[1] }})
		for i := 0; i < 400 && r.raw() == ""; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		rs := rows(r.frame())
		if len(rs) != sz[1] {
			t.Fatalf("%v: %d rows", sz, len(rs))
		}
		for _, row := range rs {
			if len([]rune(row)) != sz[0] {
				t.Fatalf("%v: row %q", sz, row)
			}
		}
		r.send("\r")
		r.send("\r")
		r.send("\x1b")
		r.send("jj\r")
		r.send("?")
		r.send("\x03")
		r.finish()
	}
}

func TestLargeTerminalFillsWindow(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: func() (int, int) { return 120, 40 }})
	r.wait("Dead-man switch")
	rs := rows(r.frame())
	if len(rs) != 40 || len([]rune(rs[1])) != 120 || !strings.HasSuffix(rs[1], "╮") || !strings.HasPrefix(rs[35], "╰") {
		t.Fatal(r.frame())
	}
	r.send("q")
	r.finish()
}

func TestEscDuringCheckSavesAndRestartsNothing(t *testing.T) {
	started := make(chan struct{}, 1)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer web.Close()
	orig := "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n"
	p := newCfg(t, orig)
	r := start(t, &App{Path: p})
	r.send("j\r")
	r.wait("Check and save")
	r.send("w\r" + web.URL + "\r\r")
	<-started
	r.wait("Esc cancels")
	r.send("\x1b")
	r.wait("Cancelled")
	time.Sleep(100 * time.Millisecond)
	if read(t, p) != orig {
		t.Fatalf("config %q", read(t, p))
	}
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Fatal("backup written")
	}
	r.send("x")
	r.send("\x03")
	r.finish()
}

func TestStatusScreenStripsControlCharacters(t *testing.T) {
	dir := t.TempDir()
	sf := filepath.Join(dir, "state.json")
	os.WriteFile(sf, []byte(`{"version":1,"checks":{"http:a":{"name":"a\u001b[2J","type":"http","reported":"OK","since":"2026-11-01T10:00:00Z","value":"v\u001b[2J\u009b31m"}}}`), 0o600)
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\n")})
	r.send("\r")
	r.wait("Services")
	r.send("\r")
	r.wait("[ Close ]")
	if f := r.raw(); strings.Contains(f, "\x1b[2J") || strings.Contains(f, "\u009b") {
		t.Fatalf("%q", f)
	}
	r.send("\x03")
	r.finish()
}

func TestStatusLines80(t *testing.T) {
	sf := writeState(t, ent{"http", "a", "FAIL", strings.Repeat("v", 120), 1, time.Time{}, time.Time{}})
	es, err := state.ReadFile(sf)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range StatusLines(es, time.Now(), 80) {
		if len([]rune(l)) > 80 {
			t.Fatalf("line too wide: %q", l)
		}
	}
}

func skipRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
}

const wantGroup = "needs group little-wd: sudo usermod -aG little-wd alice,"

func TestExploreModeWhenConfigUnreadable(t *testing.T) {
	skipRoot(t)
	p := newCfg(t, "TELEGRAM_TOKEN=1:SECRET\nTELEGRAM_CHAT_ID=5\n")
	os.Chmod(p, 0)
	t.Cleanup(func() { os.Chmod(p, 0o600) })
	r := start(t, &App{Path: p})
	r.wait("Add check")
	r.send("j")
	r.wait("Needs group little-wd")
	for i := 1; i <= 4; i++ { // every changing action says the same
		r.send("\r")
		r.wait(wantGroup)
		r.send("x")
		r.wait("Menu")
		if i < 4 {
			r.send("j")
		}
	}
	r.send("kkkk")
	r.send("\r") // Status still opens
	r.wait("Status")
	if strings.Contains(r.out.String(), "SECRET") {
		t.Fatal("config content shown")
	}
	r.send("\x03")
	r.finish()
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Fatal("wrote")
	}
}

func TestReadOnlyModeWhenDirNotWritable(t *testing.T) {
	skipRoot(t)
	sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
	p := newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\n")
	dir := filepath.Dir(p)
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	r := start(t, &App{Path: p})
	r.wait("Add check")
	r.send("\r") // Status works
	r.wait("Services")
	r.send("q")
	r.wait("Menu")
	r.send("jj\r") // Remove check
	r.wait(wantGroup)
	r.send("x")
	r.send("\x03")
	r.finish()
	if read(t, p) != "STATE_FILE="+sf+"\nTCP_B=h:1\n" {
		t.Fatal("config changed")
	}
}
