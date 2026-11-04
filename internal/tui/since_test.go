// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/state"
)

func TestEscInTheMenuAsksBeforeQuitting(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.wait("Dead-man switch")
	r.send("\x1b")
	r.wait("Quit little-wd?")
	has(t, r.frame(), "keeps running in the background", "[ Quit ]", "[ Cancel ]")
	r.send("\x1b") // Esc again cancels
	r.waitNot("Quit little-wd?")
	if r.restored.Load() {
		t.Fatal("quit on Esc")
	}
	r.send("\x1b\r") // Enter on the default button quits
	r.finish()
}

func TestSinceCountsFromTheDaemonStartAfterARestart(t *testing.T) {
	sf := writeState(t,
		ent{"cpu", "cpu", "OK", "5%", 0, time.Time{}, time.Time{}},
		ent{"http", "photos", "FAIL", "refused", 3, time.Now().Add(-2 * time.Hour), time.Time{}})
	// the OK began 29 s ago; pretend the daemon started 10 s ago
	started := time.Now().Add(-10 * time.Second)
	b, _ := os.ReadFile(sf)
	foot := `,"footprint":{"started":"` + started.Format(time.RFC3339) + `","at":"` + time.Now().Format(time.RFC3339) + `","writes":1}}`
	os.WriteFile(sf, []byte(strings.TrimSuffix(string(b), "}")+foot), 0o600)
	state.Beat(sf, time.Now())
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nHTTP_PHOTOS=http://p/\n"), Size: sized})
	r.send("\r")
	r.wait("cpu: OK since the daemon start at " + hm(started))
	rs := check80(t, r.frame())
	has(t, rs[4], "cpu", hm(started))
	hasAt(t, rs[6], -2*time.Hour) // a failure keeps its first failed check
	r.send("\x03")
	r.finish()
}

func TestQuitDialogIgnoresLetterKeys(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.wait("Dead-man switch")
	r.send("\x1bjy")
	r.wait("Quit little-wd?")
	time.Sleep(100 * time.Millisecond)
	if r.restored.Load() {
		t.Fatal("a letter quit")
	}
	r.send("\x1b")
	r.waitNot("Quit little-wd?")
	r.send("\x03")
	r.finish()
}

func TestUnstableDetailsKeepTheOKTime(t *testing.T) {
	sf := writeState(t, ent{"http", "nas", "OK", "x", 1, time.Now().Add(-2 * time.Hour), time.Time{}})
	state.Beat(sf, time.Now())
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nHTTP_NAS=http://b/\n"), Size: sized})
	r.send("\r\r")
	r.wait("[ Close ]")
	has(t, r.frame(), "OK since "+hm(time.Now().Add(-29*time.Second)), "failing since "+hm(time.Now().Add(-2*time.Hour)), "1 of 3 tries")
	r.send("\x03")
	r.finish()
}

func TestHelpFitsALowTerminal(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: func() (int, int) { return 80, 20 }})
	r.wait("Dead-man switch")
	r.send("?")
	r.wait("[ Close ]")
	rs := rows(r.frame())
	has(t, rs[len(rs)-5], "╰") // the frame's bottom border is not drawn over
	lacks(t, r.frame(), "began, at most")
	r.send("\x03")
	r.finish()
}
