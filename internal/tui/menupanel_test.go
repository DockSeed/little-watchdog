// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/state"
)

// From 120 columns the menu has the panel: what needs a look, then this machine.
func TestMenuPanel120(t *testing.T) {
	r := start(t, &App{Path: footFixture(t), Size: func() (int, int) { return 120, 30 }, Proc: fakeProc(t, true)})
	r.wait("right now")
	f := r.frame()
	rs := checkSize(t, f, 120, 30)
	tw, pw := panelSplit(120, 118)
	if pw == 0 {
		t.Fatal("no panel at 120")
	}
	for y := 2; y < 25; y++ {
		if col(rs[y], 1+tw) != "│" {
			t.Fatalf("row %d: no separator at %d: %q", y, 1+tw, rs[y])
		}
	}
	// the menu keeps its place and its values
	has(t, rs[2], "Status", "1 FAIL: photos")
	has(t, rs[3], "Add check", "Watch an address")
	has(t, rs[24], "little-wd: 7 MB RAM") // footer, left of the panel
	// the panel: the failing check with since when and why
	has(t, rs[2], "│ Needs a look")
	has(t, rs[3], "│ [FAIL] photos")
	has(t, rs[4], "FAIL since", "(3 of 3 tries)")
	has(t, rs[5], "dial tcp 192.0.2.12:2283:")
	has(t, f, "right now", "load 1/5/15 min", "up since boot")
	lacks(t, f, "All OK")
	has(t, r.raw(), "1;38;5;203;48;5;234m[FAIL]")
	// the panel stays while the cursor moves, and Status still has its own
	r.send("jj")
	r.wait("Pick a check")
	has(t, r.frame(), "[FAIL] photos")
	r.send("kk\r")
	r.wait("Type      cpu")
	lacks(t, r.frame(), "Needs a look")
	r.send("\x03")
	r.finish()
}

// Unstable and recovering checks are listed too, with the red ones first.
func TestMenuPanelUnstableAndRecovering(t *testing.T) {
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+shaky(t)+"\nHTTP_NAS=http://b/\nHTTP_PHOTOS=http://p/\nTCP_DNS=h:53\n"),
		Size: func() (int, int) { return 130, 30 }, Proc: fakeProc(t, true)})
	r.wait("Needs a look")
	f := r.frame()
	checkSize(t, f, 130, 30)
	has(t, f, "[FAIL] photos", "FAIL since", "back 1/2", "HTTP 502",
		"[2/3]  nas", "last check failed (2 of 3 tries), not", "reported yet", "no answer within 10 s")
	if strings.Index(f, "[FAIL] photos") > strings.Index(f, "[2/3]  nas") {
		t.Fatalf("FAIL not first:\n%s", f)
	}
	lacks(t, f, "dns  ", "[OK]")
	r.send("\x03")
	r.finish()
}

// All fine: one calm line, no list.
func TestMenuPanelAllOK(t *testing.T) {
	sf := writeState(t,
		ent{"tcp", "dns", "OK", "open", 0, time.Time{}, time.Time{}},
		ent{"tcp", "nas", "OK", "open", 0, time.Time{}, time.Time{}})
	state.Beat(sf, time.Now())
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nTCP_DNS=h:53\nTCP_NAS=h:22\n"),
		Size: func() (int, int) { return 160, 40 }, Proc: fakeProc(t, true)})
	r.wait("right now")
	f := r.frame()
	rs := checkSize(t, f, 160, 40)
	has(t, rs[2], "│ All OK, 2 checks.")
	has(t, rs[3], "│  ") // nothing after the calm line
	lacks(t, rs[3], "Needs", "[")
	lacks(t, f, "Needs a look")
	has(t, r.raw(), "1;38;5;114;48;5;234mAll OK, 2 checks.")
	r.send("\x03")
	r.finish()
}

// A long list on a low terminal drops the machine block, then cuts the list.
func TestMenuPanelLow(t *testing.T) {
	var es []ent
	cfg := ""
	for i := range 6 {
		es = append(es, ent{"tcp", fmt.Sprintf("s%d", i), "FAIL", "connection refused", 3, time.Now().Add(-time.Hour), time.Time{}})
		cfg += fmt.Sprintf("TCP_S%d=h:%d\n", i, i+1)
	}
	sf := writeState(t, es...)
	state.Beat(sf, time.Now())
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\n"+cfg), Size: func() (int, int) { return 120, 24 }, Proc: fakeProc(t, true)})
	r.wait("Needs a look")
	f := r.frame()
	rs := checkSize(t, f, 120, 24)
	lacks(t, f, "right now")
	has(t, rs[18], "more in Status")
	has(t, f, "[FAIL] s0", "[FAIL] s3")
	r.send("\x03")
	r.finish()
}

// Below 120 columns the menu is one column, as before.
func TestMenuNoPanelBelow120(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {119, 30}} {
		r := start(t, &App{Path: fixture(t), Size: func() (int, int) { return sz[0], sz[1] }, Proc: fakeProc(t, true)})
		r.wait("Dead-man switch")
		f := r.frame()
		rs := checkSize(t, f, sz[0], sz[1])
		lacks(t, f, "Needs a look", "right now", "load 1/5/15")
		if n := strings.Count(rs[5], "│"); n != 2 {
			t.Fatalf("%v: %d bars in %q", sz, n, rs[5])
		}
		r.send("\x03")
		r.finish()
	}
}

// A stopped daemon: the panel says the results may be old before it calls them fine.
func TestMenuPanelStale(t *testing.T) {
	sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
	state.Beat(sf, time.Now().Add(-time.Hour))
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\n"), Size: func() (int, int) { return 130, 30 }, Proc: fakeProc(t, true)})
	r.wait("right now")
	rs := checkSize(t, r.frame(), 130, 30)
	has(t, rs[2], "│ Daemon not running, results may be old.")
	has(t, rs[4], "│ All OK, 1 check.")
	r.send("\x03")
	r.finish()
}
