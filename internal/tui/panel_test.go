// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeProc is a /proc tree with load, uptime and (if psi) pressure files.
func fakeProc(t *testing.T, psi bool) string {
	t.Helper()
	d := t.TempDir()
	put := func(name, body string) {
		p := filepath.Join(d, name)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("loadavg", "0.52 0.40 0.31 1/234 5678\n")
	put("uptime", "273600.50 1000.00\n") // 3 d 4 h
	if psi {
		put("pressure/cpu", "some avg10=1.25 avg60=0.50 avg300=0.10 total=123\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
		put("pressure/memory", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
		put("pressure/io", "some avg10=4.00 avg60=2.00 avg300=1.00 total=9\nfull avg10=3.00 avg60=1.00 avg300=0.50 total=7\n")
	}
	return d
}

// footFixture is fixture with a footprint: 7 MB RAM, 24 writes in 4 h.
func footFixture(t *testing.T) string {
	t.Helper()
	cfg := fixture(t)
	var sf string
	for _, l := range strings.Split(read(t, cfg), "\n") {
		if v, ok := strings.CutPrefix(l, "STATE_FILE="); ok {
			sf = v
		}
	}
	now := time.Now()
	foot := fmt.Sprintf(`{"version":1,"footprint":{"started":%q,"at":%q,"writes":24,"rss_kb":7168,"cpu_s":1.5},`,
		now.Add(-4*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
	os.WriteFile(sf, []byte(strings.Replace(read(t, sf), `{"version":1,`, foot, 1)), 0o600)
	return cfg
}

// checkSize asserts the frame is h rows of w cells.
func checkSize(t *testing.T, f string, w, h int) []string {
	t.Helper()
	rs := rows(f)
	if len(rs) != h {
		t.Fatalf("rows: %d\n%s", len(rs), f)
	}
	for i, r := range rs {
		if n := len([]rune(r)); n != w {
			t.Fatalf("row %d has %d cells: %q", i, n, r)
		}
	}
	return rs
}

// col is the rune at cell x of a row.
func col(row string, x int) string { return string([]rune(row)[x]) }

func TestStatusPanel130(t *testing.T) {
	r := start(t, &App{Path: footFixture(t), Size: func() (int, int) { return 130, 30 }, Proc: fakeProc(t, true)})
	r.send("\r")
	r.wait("Last result")
	r.wait("load 1/5/15 min")
	f := r.frame()
	rs := checkSize(t, f, 130, 30)
	tw, pw := panelSplit(130, 128)
	if tw+pw+1 != 128 || tw < 70 {
		t.Fatal(tw, pw)
	}
	for y := 2; y < 25; y++ { // the separator runs down the inner area
		if col(rs[y], 1+tw) != "│" {
			t.Fatalf("row %d: no separator at %d: %q", y, 1+tw, rs[y])
		}
	}
	has(t, rs[2], "State", "Check", "Last result", "Since")
	has(t, rs[4], "[OK]", "cpu", "5% (20s avg)")
	// block 1: the check under the cursor
	has(t, f, "Type      cpu", "State     OK since", "Result    5% (20s avg)", "Checked", "Telegram  no message yet")
	// block 2: this machine
	host, _ := os.Hostname()
	has(t, f, host+" right now", "load 1/5/15 min 0.52 0.40 0.31 on "+plural(runtime.NumCPU(), "CPU", "CPUs"),
		"waited          10s    60s    5m", "  CPU           1.25%  0.50%  0.10%", "  RAM           0.00%  0.00%  0.00%",
		"  disk          4.00%  2.00%  1.00%", "up since boot   3 d 4 h")
	if !strings.Contains(rs[24], "up since boot") { // anchored at the bottom of the panel
		t.Fatalf("machine block not at the bottom:\n%s", f)
	}
	// cursor moves change block 1, the table keeps its cursor bar
	r.send("jjjjjj") // cpu, disk, temperature, dns, router, then 192.0.2.12: nextcloud, photos
	r.wait("Target    http://192.0.2.12:2283/")
	f = r.frame()
	has(t, f, "photos: failing since", "State     FAIL since", "(3 of 3 tries)", "Result    dial tcp 192.0.2.12:2283:", "Telegram  last message")
	lacks(t, f, "Type      cpu")
	has(t, r.raw(), "1;38;5;203;48;5;234mFAIL") // FAIL in the panel is red
	r.send("kkk")
	r.wait("Type      tcp")
	has(t, r.frame(), "Target    h:53", "Telegram  no message yet")
	r.send("?")
	r.wait("[ Close ]")
	has(t, r.frame(), "From 120 columns a panel on the right shows this machine, and", "what needs a look (menu) or the selected check (Status).")
	r.send("\x03")
	r.finish()
}

func TestStatusPanel160(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: func() (int, int) { return 160, 40 }, Proc: fakeProc(t, true)})
	r.send("\r")
	r.wait("Last result")
	r.wait("load 1/5/15 min")
	f := r.frame()
	rs := checkSize(t, f, 160, 40)
	tw, _ := panelSplit(160, 158)
	if col(rs[10], 1+tw) != "│" || !strings.HasSuffix(rs[10], "│") {
		t.Fatalf("%q", rs[10])
	}
	has(t, rs[2], "Type", "Checked")
	has(t, f, "1.25%", "Type      cpu")
	lacks(t, f, "little-wd       ", "no footprint")
	r.send("\x03")
	r.finish()
}

func TestStatusPanelNoPSIAndErrors(t *testing.T) {
	proc := fakeProc(t, false)
	os.Remove(filepath.Join(proc, "loadavg"))
	os.WriteFile(filepath.Join(proc, "uptime"), []byte("garbage\n"), 0o600)
	r := start(t, &App{Path: fixture(t), Size: func() (int, int) { return 160, 40 }, Proc: proc})
	r.send("\r")
	r.wait("Last result")
	r.wait("waited")
	has(t, r.frame(), "waited          /proc limited", "load 1/5/15 min not found", "up since boot   unexpected format")
	lacks(t, r.frame(), "10s    60s")
	r.send("\x03")
	r.finish()
	os.WriteFile(filepath.Join(proc, "loadavg"), []byte("0.52 0.40 0.31 1/234 5678\n"), 0o600)
	r = start(t, &App{Path: fixture(t), Size: func() (int, int) { return 160, 40 }, Proc: proc})
	r.send("\r")
	r.wait("Last result")
	r.wait("waited          no PSI (kernel or psi=0)")
	r.send("\x03")
	r.finish()
}

// A low wide terminal keeps the machine block whole: target and result go to one line, then the block goes.
func TestStatusPanelLow(t *testing.T) {
	for _, c := range []struct {
		h       int
		machine bool
		result  string
	}{{24, true, "connection refused"}, {19, false, "connection refused"}, {22, true, "connect:~"}} {
		r := start(t, &App{Path: footFixture(t), Size: func() (int, int) { return 130, c.h }, Proc: fakeProc(t, true)})
		r.send("\rjjjjjj")
		r.wait("Target    http://192.0.2.12:2283/")
		f := r.frame()
		rs := checkSize(t, f, 130, c.h)
		has(t, f, "Telegram  last message", c.result)
		if c.machine {
			has(t, f, "right now", "load 1/5/15 min", "waited", "  disk ", "up since boot")
			if !strings.Contains(rs[c.h-6], "up since boot") {
				t.Fatalf("%d rows: last line of the machine block missing:\n%s", c.h, f)
			}
		} else {
			lacks(t, f, "right now", "load 1/5/15", "waited", "up since boot")
		}
		r.send("\x03")
		r.finish()
	}
}

// An idle wide Status is redrawn at most once per sysEvery although /proc changes all the time.
func TestStatusPanelIdleRedraws(t *testing.T) {
	old := sysEvery
	sysEvery = 300 * time.Millisecond
	proc := fakeProc(t, true)
	r := start(t, &App{Path: fixture(t), Size: func() (int, int) { return 130, 30 }, Proc: proc})
	r.send("\r")
	r.wait("Last result")
	r.wait("load 1/5/15 min")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { // PSI avg10 moves every few milliseconds
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			os.WriteFile(filepath.Join(proc, "pressure", "cpu"), []byte(fmt.Sprintf("some avg10=%d.00 avg60=0.50 avg300=0.10 total=1\n", i%90)), 0o600)
		}
	}()
	n0 := strings.Count(r.out.String(), "\x1b[H")
	time.Sleep(1500 * time.Millisecond)
	n := strings.Count(r.out.String(), "\x1b[H") - n0
	close(stop)
	<-done
	r.send("\x03")
	r.finish()
	sysEvery = old
	if n > 1500/300+1 {
		t.Fatalf("%d frames in 1.5 s with sysEvery 300ms", n)
	}
	if n == 0 {
		t.Fatal("the panel never re-read /proc")
	}
}

func TestStatusPanelASCII(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: func() (int, int) { return 130, 30 }, Proc: fakeProc(t, true),
		Getenv: env("TERM", "linux", "LANG", "C")})
	r.send("\r")
	r.wait("Last result")
	r.wait("load 1/5/15 min")
	f := r.frame()
	rs := checkSize(t, f, 130, 30)
	tw, _ := panelSplit(130, 128)
	if col(rs[5], 1+tw) != "|" {
		t.Fatalf("%q", rs[5])
	}
	lacks(t, f, "│", "╭", "·")
	r.send("\x03")
	r.finish()
}

func TestStatusNoPanelWithoutState(t *testing.T) {
	for name, cfg := range map[string]string{
		"no state": newCfg(t, "STATE_FILE="+filepath.Join(t.TempDir(), "none.json")+"\n"),
		"invalid":  newCfg(t, "INTERVAL=banana\n"),
	} {
		r := start(t, &App{Path: cfg, Size: func() (int, int) { return 130, 30 }, Proc: fakeProc(t, true)})
		r.send("\r")
		r.wait("Status")
		time.Sleep(100 * time.Millisecond)
		f := r.frame()
		lacks(t, f, "load 1/5/15", "right now")
		if n := strings.Count(rows(f)[5], "│"); n != 2 {
			t.Fatalf("%s: separator drawn:\n%s", name, f)
		}
		r.send("\x03")
		r.finish()
	}
}

func TestStatusNoPanelBelow120(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {119, 30}} {
		r := start(t, &App{Path: fixture(t), Size: func() (int, int) { return sz[0], sz[1] }, Proc: fakeProc(t, true)})
		r.send("\r")
		r.wait("Services")
		f := r.frame()
		rs := checkSize(t, f, sz[0], sz[1])
		lacks(t, f, "load 1/5/15", "waited", "Type      cpu")
		if n := strings.Count(rs[5], "│"); n != 2 {
			t.Fatalf("%v: %d bars in %q", sz, n, rs[5])
		}
		r.send("?")
		r.wait("[ Close ]")
		lacks(t, r.frame(), "120+ columns")
		r.send("\x03")
		r.finish()
	}
}

func TestReadSysThrottled(t *testing.T) {
	proc := fakeProc(t, true)
	m := &model{a: &App{Proc: proc}}
	t0 := time.Now()
	if s := m.readSys(t0); s.load[0] != "0.52" || s.psi[2][0] != "4.00%" || s.up != 273600500*time.Millisecond {
		t.Fatalf("%+v", *s)
	}
	os.WriteFile(filepath.Join(proc, "loadavg"), []byte("9.99 9.99 9.99 1/1 1\n"), 0o600)
	if s := m.readSys(t0.Add(sysEvery - time.Millisecond)); s.load[0] != "0.52" {
		t.Fatal("re-read within sysEvery")
	}
	if s := m.readSys(t0.Add(sysEvery)); s.load[0] != "9.99" {
		t.Fatal("no re-read after sysEvery")
	}
	// a huge file is read only up to the buffer
	os.WriteFile(filepath.Join(proc, "loadavg"), []byte("1.00 2.00 3.00 "+strings.Repeat("x", 1<<20)), 0o600)
	if s := m.readSys(t0.Add(3 * sysEvery)); s.load != [3]string{"1.00", "2.00", "3.00"} || len(m.sysBuf) != 1024 {
		t.Fatalf("%+v", s.load)
	}
	if _, e := parsePSI([]byte("full avg10=1\n")); e == "" {
		t.Fatal("no some line accepted")
	}
	for d, want := range map[time.Duration]string{12 * time.Minute: "12 min", 5*time.Hour + 12*time.Minute: "5 h 12 min", 71 * time.Hour: "2 d 23 h"} {
		if got := upText2(d); got != want {
			t.Errorf("upText2(%v) = %q, want %q", d, got, want)
		}
	}
}

// little-wd's own cost sits in the menu footer only, not in the panel's machine block.
func TestFootprintInFooterNotPanel(t *testing.T) {
	r := start(t, &App{Path: footFixture(t), Size: func() (int, int) { return 130, 30 }, Proc: fakeProc(t, true)})
	r.wait("little-wd: 7 MB RAM")
	has(t, rows(r.frame())[24], "little-wd: 7 MB RAM · ", "6.0 disk writes/h")
	r.send("\r")
	r.wait("Type      cpu")
	f := r.frame()
	has(t, f, "up since boot")
	lacks(t, f, "little-wd       ", "7 MB RAM", "writes/h")
	r.send("\x03")
	r.finish()
}

// From maxWidth on the screen ends at a fixed width and sits in the middle.
func TestCentredWide(t *testing.T) {
	for _, s := range []string{"", "\r"} { // menu, Status
		r := start(t, &App{Path: fixture(t), Size: func() (int, int) { return 200, 40 }, Proc: fakeProc(t, true)})
		r.send(s)
		r.wait("right now")
		rs := checkSize(t, r.frame(), 200, 40)
		pad := strings.Repeat(" ", 20)
		for _, y := range []int{0, 1, 10, 34} {
			if !strings.HasPrefix(rs[y], pad) || !strings.HasSuffix(rs[y], pad) {
				t.Fatalf("row %d not centred: %q", y, rs[y])
			}
		}
		if col(rs[1], 20) != "╭" || col(rs[1], 179) != "╮" || col(rs[35], 20) != "╰" {
			t.Fatalf("frame not at 20..179:\n%s", r.frame())
		}
		has(t, rs[0], pad+" little-wd")
		has(t, rs[0], "1 FAIL "+pad)
		r.send("\x03")
		r.finish()
	}
}
