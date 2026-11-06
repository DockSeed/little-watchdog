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

func TestTempWithoutSensorIsMarkedInTheRemoveList(t *testing.T) {
	sf := writeState(t, ent{"cpu", "cpu", "OK", "1%", 0, time.Time{}, time.Time{}})
	state.Beat(sf, time.Now())
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\n"), Size: sized})
	r.send("jj\r")
	r.wait("This machine")
	has(t, r.frame(), "temperature", "not running: no sensor?")
	lacks(t, r.frame(), "cpu           cpu    alarm above 90% (15m average) (not")
	r.send("\x03")
	r.finish()
}

func TestIdleScreenIsNotRedrawn(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.wait("Dead-man's switch")
	time.Sleep(200 * time.Millisecond)
	n := len(r.out.String())
	time.Sleep(400 * time.Millisecond) // eight ticks
	if got := len(r.out.String()); got != n {
		t.Fatalf("%d bytes written while idle", got-n)
	}
	r.send("q")
	r.finish()
}

func TestStateIsReadOnlyWhenItsFileChanges(t *testing.T) { // a changed file still shows up
	sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
	state.Beat(sf, time.Now())
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\n"), Size: sized})
	r.wait("all OK")
	b, _ := os.ReadFile(sf)
	os.WriteFile(sf, []byte(strings.Replace(string(b), `"OK"`, `"FAIL"`, 1)), 0o600)
	r.wait("1 FAIL: b")
	r.send("\x03")
	r.finish()
}

func TestDialogTopFits(t *testing.T) {
	for ih := 1; ih <= 60; ih++ {
		for h := 3; h <= 60; h++ {
			for dy := 1; dy <= 2; dy++ {
				top := dialogTop(ih, h, dy)
				if top < dy || (dy+h <= ih && top+h > ih) {
					t.Fatalf("ih %d h %d dy %d: top %d", ih, h, dy, top)
				}
			}
		}
	}
	if got := dialogTop(18, 10, 1); got != 3 {
		t.Fatalf("80x24 details: %d", got)
	}
}
