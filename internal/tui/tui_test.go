// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
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
		{"\x1b[3~x", []Key{{Code: Del}, {Rune, 'x'}}},
		{"\x1b[H\x1b[F\x1b[1~\x1b[4~\x12", []Key{{Code: Home}, {Code: End}, {Code: Home}, {Code: End}, {Code: CtrlR}}},
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
	return startAs(t, a, "", "alice", nil) // a user who is not in the group, whoever runs the tests
}

// startAs is start with a fake group set before the first frame (see setGroup).
func startAs(t *testing.T, a *App, members, user string, groups []int) *rig {
	setGroup(t, members, user, groups)
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

// skipWelcome leaves the first-start greeting by Esc; Telegram setup follows.
func (r *rig) skipWelcome() {
	r.t.Helper()
	r.wait("Welcome to")
	r.send("\x1b")
}

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
		j += `,"last_run":"` + now.Add(-3*time.Minute).Format(time.RFC3339) + `"`
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
		ent{"tcp", "dns", "OK", "port open, 0 ms", 0, time.Time{}, time.Time{}},
		ent{"host", "router", "OK", "port open, 2 ms", 0, time.Time{}, time.Time{}},
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
	has(t, rs[0], "little-wd", "1 FAIL")
	lacks(t, rs[0], "›")
	has(t, rs[1], "╭", "Menu", "╮")
	has(t, rs[2], "Status", "1 FAIL: photos")
	has(t, rs[3], "Add check", "Watch an address")
	has(t, rs[4], "Remove check", "8 checks")
	has(t, rs[5], "System checks", "4 on")
	has(t, rs[7], "Telegram", "linked, nothing sent yet")
	has(t, rs[8], "Dead-man switch", "every 2m")
	has(t, rs[19], "╰", "╯")
	has(t, rs[20], "Latest result of every check. Checks run every 1m.")
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
	has(t, rs[2], "State", "Check", "Type", "Last result", "Checked", "Since")
	has(t, rs[3], "This machine")
	has(t, rs[4], "[OK]", "cpu", "5% (20s avg)")
	has(t, rs[7], "Services")
	// alone on their host: in the usual order; two on 192.0.2.12: under the host
	has(t, rs[8], "[OK]    dns           tcp    port open, 0 ms")
	has(t, rs[9], "[OK]    router        host   port open, 2 ms")
	if got := rs[10]; got != "│    192.0.2.12"+strings.Repeat(" ", 64)+"│" {
		t.Fatalf("host line: %q", got)
	}
	has(t, rs[11], "[OK]    nextcloud     http   HTTP 200 in 2ms")
	hasAt(t, rs[11], -3*time.Minute)
	hasAt(t, rs[11], -29*time.Second)
	has(t, rs[12], "[FAIL]  photos")
	has(t, f, "disk /", "temperature")
	hasAt(t, rs[12], -10*time.Minute) // FAIL: since the first failed check
	if i := strings.Index(f, "Services"); i < strings.Index(f, "temperature") || i > strings.Index(f, "router") {
		t.Fatal("sections out of order")
	}
	has(t, rs[20], "cpu: OK since")
	has(t, rs[22], "Enter details", "A add", "Esc menu")
	r.send("jjjjjj")
	r.wait("photos: failing since")
	f = r.frame()
	has(t, f, "3 of 3 tries", "Telegram sent at")
	// cursor on photos: the FAIL row is the bar; move away and the mark turns red
	r.send("k")
	r.wait("nextcloud: OK")
	has(t, r.raw(), "1;38;5;203;48;5;234m[FAIL]")
	r.send("\x1b")
	r.wait("Menu")
	r.send("q")
	r.finish()
}

func TestDetails80(t *testing.T) { // details dialog
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.send("\rjjjjjj\r")
	r.wait("[ Close ]")
	f := r.frame()
	rs := check80(t, f)
	has(t, f, "photos", "Type", "Target", "http://192.0.2.12:2283/", "FAIL since", "(3 of 3 tries)", "connection refused", "Telegram", "sent at")
	has(t, rs[4], "╭") // optical centre: 3 free rows above, 5 below
	has(t, rs[13], "╰")
	has(t, r.raw(), "1;38;5;203;48;5;236m")
	r.send("\r")
	r.wait("↑↓ move  Enter details")
	r.send("\x03")
	r.finish()
}

func TestAdd80(t *testing.T) { // add check dialog: one form, address first
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.send("j\r")
	r.wait("Check and save")
	f := r.frame()
	rs := check80(t, f)
	has(t, rs[0], "little-wd  › Add check")
	has(t, f, "Address", "Name", "Checked once before saving.", "[ Check and save ]   [ Cancel ]")
	lacks(t, f, "Kind", "Website", "( ) Port", "URL", "Text")
	if strings.Index(f, "Address") > strings.Index(f, "Name ") {
		t.Fatal("Name above Address")
	}
	has(t, f, "An IP or name is pinged") // focus starts on Address
	r.send("\t")
	r.wait("Optional, any text")
	r.send("\x1b")
	r.wait("Menu")
	r.send("q")
	r.finish()
}

func TestRemoveList80(t *testing.T) { // remove list, both sections
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.send("jj\r")
	r.wait("Services")
	f := r.frame()
	rs := check80(t, f)
	has(t, rs[2], "State", "Check", "Type", "Watches")
	has(t, rs[3], "This machine")
	has(t, f, "[OK]", "nextcloud", "http://192.0.2.12:80/", "router", "198.51.100.20:443",
		"cpu", "alarm above 90% (15m average)", "memory", "alarm below 5% available", "disk /", "alarm below 10% free", "temperature", "alarm above 85 C")
	if strings.Index(f, "Services") < strings.Index(f, "temperature") || strings.Index(f, "Services") > strings.Index(f, "router") {
		t.Fatal("sections out of order")
	}
	r.send("\x03")
	r.finish()
}

func TestRemoveConfirm80(t *testing.T) { // remove confirmation
	p := fixture(t)
	r := start(t, &App{Path: p, Size: sized})
	r.send("jj\r")
	r.wait("Services")
	r.send("jjjjj\r") // cpu, memory, disk, temperature, nextcloud, photos
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
// The machine's own checks come first: cpu 0, memory 1, disk 2, temperature 3.
func removeHost(t *testing.T, cfg string, n int, want string) (string, *rig) {
	t.Helper()
	p := newCfg(t, cfg)
	r := start(t, &App{Path: p, Size: sized})
	r.send("jj\r")
	r.wait("This machine")
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
		{0, "cpu", "CPU_MAX_PCT=90", "CPU_MAX_PCT=0\n"},
		{1, "memory", "MEM_MIN_AVAIL_PCT=5", "MEM_MIN_AVAIL_PCT=0\n"},
		{2, "disk /", "DISK_PATHS=/", "DISK_PATHS=\n"},
		{3, "temperature", "TEMP_MAX_C=85", "TEMP_MAX_C=0\n"},
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
	p, r := removeHost(t, base, 3, "disk /data") // cpu, memory, disk /, disk /data
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
	r.send("jjj\r")
	r.wait("Remove disk /home?")
	r.send("\r")
	r.wait("Saved")
	r.send("x")
	r.wait("Menu")
	r.send("\r")
	r.wait("This machine")
	r.send("jj\r")
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
	r.skipWelcome()
	r.wait("Bot token")
	check80(t, r.frame())
	r.send(tok + "\r")
	r.wait("/start ")
	fr := r.frame()
	rs := check80(t, fr)
	has(t, rs[0], "little-wd  › Telegram setup")
	has(t, fr, "[✓] 1  Bot token", "accepted, bot @", "[✓] 2  Webhook", "none set", "[ ] 3  Link your chat", "[ ] 4  Test message", "waiting until ", "[ Cancel ]")
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
	r.skipWelcome()
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
	has(t, rs[3], "Add check", "locked, see the hint below")
	has(t, rs[6], "Settings", "locked, see the hint below")
	// the way out shows at once, with the cursor still on Status
	r.wait("Needs group little-wd: sudo usermod -aG little-wd alice,")
	r.send("j")
	r.wait("Needs group little-wd: sudo usermod -aG little-wd alice,")
	// locked items are grey, Status is not
	for i := 0; i < 200 && !strings.Contains(r.raw(), "48;5;240"); i++ { // the redraw after j
		time.Sleep(10 * time.Millisecond)
	}
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
	has(t, f, "Keys", "move", "Esc", "Status: add a check on this host", "Since: when that state", "at most since the daemon start")
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
			r.send("jjjjj\r")
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
	r.skipWelcome()
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
	r.send(long)
	r.wait("end ")
	f := r.frame()
	rs := check80(t, f)
	for _, row := range rs {
		if strings.Contains(row, "end ") {
			if !strings.Contains(row, "Address   <") {
				t.Fatalf("no start marker: %q", row)
			}
			if !strings.HasSuffix(strings.TrimRight(row, " │"), "end") && !strings.HasSuffix(strings.TrimRight(row, " │"), "_") {
				t.Fatalf("caret not at the end: %q", row)
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
	r.send("\x1b[B") // Name field: unfocused Address shows its beginning with '>'
	r.wait("Optional, any text")
	has(t, r.frame(), "https://example.org/abcdefghij", ">")
	if nameRow == "" {
		t.Fatal("no name row")
	}
	// the same in ASCII
	r2 := start(t, &App{Path: newCfg(t, "TELEGRAM_TOKEN=1:a\n"), Size: sized, Getenv: env("TERM", "vt100")})
	r2.send("j\r")
	r2.wait("Check and save")
	r2.send(long)
	r2.wait("end ")
	has(t, r2.frame(), "Address   <")
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
	r.wait("[FAIL]  photos")
	rs := check80(t, r.frame())
	has(t, rs[4], "[FAIL]  photos", "~") // status row cut at the edge
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
	addr := strings.TrimPrefix(web.URL, "http://")
	p := aliveCfg(t, "# my config\nTELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n")
	pre, _, _ := strings.Cut(read(t, p), "# my config")
	r := start(t, &App{Path: p})
	r.wait("Add check")
	r.send("j\r")
	r.wait("Check and save")
	r.send(addr + "\rImmich Fotos ä\r")
	r.wait("Saved as Immich Fotos ä.")
	f := r.frame()
	has(t, f, "✓ web 200, ", "Next address, or Esc to leave.")
	lacks(t, f, addr) // the form is empty for the next address
	want := pre + "# my config\nTELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\nHTTP_IMMICH_FOTOS_AE=" + web.URL + ` name="Immich Fotos ä" mode=lenient` + "\n"
	if got := read(t, p); got != want {
		t.Fatalf("%q", got)
	}
	if got := read(t, p+".bak"); !strings.Contains(got, "# my config") || strings.Contains(got, "HTTP_") {
		t.Fatalf("bak %q", got)
	}
	r.send(web.URL + "/x\r\r") // a path is strict; the name comes from the address
	r.wait("Saved as " + addr + "/x.")
	key := "HTTP_" + strings.NewReplacer(".", "_", ":", "_").Replace(addr) + "_X="
	if got := read(t, p); !strings.HasSuffix(got, key+web.URL+`/x name="`+addr+`/x" mode=strict`+"\n") {
		t.Fatalf("%q", got)
	}
	r.send("\x1b") // Esc leaves
	r.wait("Menu")
	r.send("q")
	r.finish()
}

// A failed check is saved at once; the form says why it fails and is cleared.
func TestAddFailedIsSavedAtOnce(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	p := aliveCfg(t, "TCP_A=h:1\n")
	r := start(t, &App{Path: p})
	r.send("j\r")
	r.wait("Check and save")
	r.send(addr + "\r\r")
	r.wait("it shows as failing until it answers.")
	has(t, r.frame(), "✗ connection refused", "Saved as", "Next address")
	if got := read(t, p); !strings.Contains(got, "HTTP_127_0_0_1_") || !strings.Contains(got, "mode=lenient") {
		t.Fatalf("%q", got)
	}
	r.send("\x03")
	r.finish()
}

// Invalid addresses never reach the config; a name in use gets _2.
func TestAddInvalidStaysInForm(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer web.Close()
	p := newCfg(t, "TCP_A=h:1\n")
	r := start(t, &App{Path: p})
	r.send("j\r")
	r.wait("Check and save")
	r.send("host:99999\r\r")
	r.wait("Error: Address: port")
	r.send("\x1b[A" + strings.Repeat("\x7f", 10) + "ftp://x\r\r")
	r.wait("Error: Address: a web address starts with http://")
	if read(t, p) != "TCP_A=h:1\n" {
		t.Fatal("config changed")
	}
	r.send("\x1b[A" + strings.Repeat("\x7f", 7) + web.URL + "\ra\r") // name a is taken by TCP_A
	r.wait("Saved as a.")
	if got := read(t, p); got != "TCP_A=h:1\nHTTP_A_2="+web.URL+` name="a" mode=lenient`+"\n" {
		t.Fatalf("%q", got)
	}
	r.send("\t\t\t\r") // Address, Name, buttons: Tab to [ Cancel ]
	r.wait("Menu")
	r.send("\x03")
	r.finish()
}

func TestRemoveCheck(t *testing.T) {
	p := aliveCfg(t, "# keep\nHTTP_A=http://a.example/\nTCP_B=h:1\n")
	pre, _, _ := strings.Cut(read(t, p), "# keep")
	r := start(t, &App{Path: p})
	r.send("jj\r") // Remove check
	r.wait("http://a.example/")
	r.send("jjjjj\r") // cpu, memory, disk, temperature, a, b
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
	p := aliveCfg(t, "TELEGRAM_TOKEN=1:a\n")
	r := start(t, &App{Path: p, Size: sized})
	r.send("jjjjjj\r")
	r.wait("[ Save ]")
	check80(t, r.frame())
	r.send(srv.URL + "/ping\r\r")
	r.wait("Saved")
	if hits.Load() != 1 || !strings.Contains(r.frame(), "Ping OK") || !strings.Contains(read(t, p), "DEADMAN_URL="+srv.URL+"/ping\n") {
		t.Fatal(hits.Load(), r.frame())
	}
	r.send("x") // back to the menu (cursor stays on Dead-man switch), open it again: "off" switches it off
	r.wait("every 2m")
	r.send("\r")
	r.wait("[ Save ]")
	r.send("off\r\r")
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
	r.skipWelcome()
	r.wait("Bot token")
	r.send(tok + "\r")
	if strings.Contains(r.frame(), tok) {
		t.Fatal("token on screen")
	}
	r.wait("Delete the webhook now?")
	r.send("y")
	r.wait("waiting until ")
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
	r.skipWelcome()
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

func TestEscDuringCheckKeepsTheSavedCheck(t *testing.T) {
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
	r.send(web.URL + "\r\r")
	<-started
	r.wait("Checking " + web.URL)
	r.send("x\r") // ignored while the check runs
	r.send("\x1b")
	r.wait("Menu")
	time.Sleep(100 * time.Millisecond)
	// saved before the check ran: Esc leaves, the address is not lost
	if got := read(t, p); !strings.HasPrefix(got, orig) || !strings.Contains(got, web.URL+" ") {
		t.Fatalf("config %q", got)
	}
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

// Existing lines keep their options; names with blanks show in the remove list.
func TestAddKeepsOtherLines(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"s":"hello"}`) }))
	defer web.Close()
	older := `HTTP_IMMICH=http://192.0.2.9:2283 name="Immich Fotos" mode=lenient future=1`
	p := aliveCfg(t, "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n"+older+"\nHTTP_OLD="+web.URL+" contains=hello\n")
	r := start(t, &App{Path: p})
	r.wait("Add check")
	r.send("j\r")
	r.wait("Check and save")
	r.send(web.URL + "\rmy web\r")
	r.wait("Saved as my web.")
	got := read(t, p)
	if !strings.Contains(got, "\n"+older+"\n") || !strings.Contains(got, "\nHTTP_OLD="+web.URL+" contains=hello\n") || !strings.HasSuffix(got, "HTTP_MY_WEB="+web.URL+` name="my web" mode=lenient`+"\n") {
		t.Fatalf("%q", got)
	}
	r.send("\x1b")
	r.wait("Menu")
	r.send("j\r") // Remove check
	r.wait("Immich Fotos")
	has(t, r.frame(), "my web", "old")
	r.send("\x03")
	r.finish()
}

// A check the daemon has no result for yet shows [?], but only when there is any state.
func TestRemoveListUnknownMark(t *testing.T) {
	for _, withState := range []bool{true, false} {
		sf := filepath.Join(t.TempDir(), "none.json")
		if withState {
			sf = writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
		}
		p := newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\nTCP_NEW=h:2\n")
		r := start(t, &App{Path: p})
		r.wait("Add check")
		r.send("jj\r") // Remove check
		r.wait("Watches")
		var b, n string
		for _, row := range rows(r.frame()) {
			if strings.Contains(row, " b ") {
				b = row
			}
			if strings.Contains(row, " new ") {
				n = row
			}
		}
		if withState && (!strings.Contains(b, "[OK]") || !strings.Contains(n, "[?]")) {
			t.Fatalf("%q\n%q", b, n)
		}
		if !withState && (strings.Contains(b, "[") || strings.Contains(n, "[")) {
			t.Fatalf("%q\n%q", b, n)
		}
		r.send("\x03")
		r.finish()
	}
}

func TestDetailsHelp80(t *testing.T) { // details dialog of a failing check with its "what to do"
	cfg := fixture(t)
	sf := cfgValue(t, cfg, "STATE_FILE")
	b, err := os.ReadFile(sf)
	if err != nil {
		t.Fatal(err)
	}
	help := checks.Help(checks.KindRefused)
	b = []byte(strings.Replace(string(b), `"name":"photos",`, `"name":"photos","help":`+strconv.Quote(help)+`,`, 1))
	if err := os.WriteFile(sf, b, 0o600); err != nil {
		t.Fatal(err)
	}
	r := start(t, &App{Path: cfg, Size: sized})
	r.send("\rjjjjjj\r")
	r.wait("[ Close ]")
	f := r.frame()
	rs := check80(t, f)
	has(t, f, "connection refused", "What to do:", "Maybe the service is stopped, or it listens", "sent at")
	top, bottom := -1, -1
	for i, l := range rs {
		if strings.Contains(l, "╭") && top < 0 {
			top = i
		}
		if strings.Contains(l, "╰") {
			bottom = i
		}
	}
	if top < 1 || bottom < 0 || bottom > 21 { // the dialog fits between the header and the key line
		t.Fatalf("dialog rows %d..%d:\n%s", top, bottom, f)
	}
	has(t, r.raw(), "38;5;245;48;5;236mWhat to do:")
	r.send("\r")
	r.wait("↑↓ move  Enter details")
	r.send("\x03")
	r.finish()
}

// cfgValue is the value of key in the config file at p.
func cfgValue(t *testing.T, p, key string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return v
		}
	}
	t.Fatalf("%s not in %s", key, p)
	return ""
}

// TestMain points RUNTIME_DIRECTORY at a directory that does not exist, so no test depends on
// (or writes to) the /run/little-watchdog of the host it runs on.
func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "lw-run-")
	if err != nil {
		panic(err)
	}
	os.Setenv("RUNTIME_DIRECTORY", filepath.Join(d, "run"))
	code := m.Run()
	os.RemoveAll(d)
	os.Exit(code)
}
