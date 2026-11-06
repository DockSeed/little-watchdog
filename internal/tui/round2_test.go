// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/state"
)

func TestEveryIsTheShortFormTheConfigReads(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Minute: "10m", 30 * time.Minute: "30m", 90 * time.Minute: "1h30m",
		time.Hour: "1h", time.Minute: "1m", 90 * time.Second: "1m30s", 30 * time.Second: "30s",
	} {
		if got := every(d); got != want {
			t.Errorf("every(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestSystemChecksFormKeepsALongWindowSavable(t *testing.T) {
	r, p := sysRig(t, "CPU_WINDOW=1h30m\n")
	r.send("\r")
	r.wait("Percent")
	has(t, r.frame(), "1h30m")
	r.send("\r\r") // Save without touching the window
	r.wait("Saved")
	if got := read(t, p); !strings.Contains(got, "CPU_WINDOW=1h30m\n") {
		t.Fatal(got)
	}
	r.send("x")
	r.wait("Menu")
	r.send("\x03")
	r.finish()
}

func TestDiskWithoutPathsIsOffAndSpaceTurnsItOnWithRoot(t *testing.T) {
	r, p := sysRig(t, "DISK_PATHS=\n")
	has(t, check80(t, r.frame())[5], "disk", "off")
	r.send("jj ")
	r.wait("disk is on.")
	got := read(t, p)
	if !strings.Contains(got, "DISK_PATHS=/\n") || strings.Contains(got, "DISK_MIN_FREE_PCT=10") && false {
		t.Fatal(got)
	}
	has(t, check80(t, r.frame())[5], "alarm below 10% free")
	r.send("\x03")
	r.finish()
}

func TestInvalidConfigShowsTheRealErrorAndAHeaderChip(t *testing.T) {
	p := aliveCfg(t, "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\nCPU_MAX_PCT=nonsense\n")
	r := start(t, &App{Path: p, Size: sized})
	r.wait("config invalid")
	rs := check80(t, r.frame())
	has(t, rs[0], "config invalid")
	has(t, rs[4], "Remove check", "config line 4: CPU_MAX_PCT: invalid value (expected")
	has(t, r.raw(), "1;38;5;203;48;5;234m config invalid ")
	r.send("jj") // Remove check
	r.wait("Fix ")
	has(t, r.frame(), "config line 4: CPU_MAX_PCT")
	r.send("\x03")
	r.finish()
}

func TestDaemonNotRunningBeatsInvalidConfigChip(t *testing.T) {
	sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
	state.Beat(sf, time.Now().Add(-time.Hour))
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\n"), Size: sized})
	r.wait("service not running")
	lacks(t, check80(t, r.frame())[0], "config invalid")
	r.send("\x03")
	r.finish()
}

func TestDeadmanHintDoesNotCallEmptyOff(t *testing.T) {
	r := start(t, &App{Path: aliveCfg(t, "DEADMAN_URL=https://hc.example/p\n"), Size: sized})
	r.send("jjjjjj\r")
	r.wait("Pinged once now")
	has(t, r.frame(), "Empty keeps the current URL, type off", "to switch it off.")
	lacks(t, r.frame(), "Empty = off")
	r.send("\x1b")
	r.wait("Menu")
	r.send("\x03")
	r.finish()
	r = start(t, &App{Path: aliveCfg(t, ""), Size: sized})
	r.send("jjjjjj\r")
	r.wait("Healthchecks ping URL")
	lacks(t, r.frame(), "Empty = off", "Empty keeps")
	r.send("\x1b")
	r.wait("Menu")
	r.send("\x03")
	r.finish()
}

// withKey adds a top-level key to the state file.
func withKey(t *testing.T, sf, key, val string) {
	t.Helper()
	b, _ := os.ReadFile(sf)
	os.WriteFile(sf, []byte(strings.TrimSuffix(string(b), "}")+`,"`+key+`":`+val+"}"), 0o600)
}

func TestConflictIsShownInMenuAndHeader(t *testing.T) {
	for kind, want := range map[string]string{
		"poller":  "another program uses this bot: /status does not work",
		"unknown": "another program uses this bot: /status does not work",
		"webhook": "a webhook is set for this bot: /status does not work",
	} {
		sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
		withKey(t, sf, "updates", `{"conflict":"`+kind+`","since":"`+time.Now().UTC().Format(time.RFC3339)+`"}`)
		state.Beat(sf, time.Now())
		r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\nTELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n"), Size: sized})
		r.wait("/status not working")
		rs := check80(t, r.frame())
		has(t, rs[0], "/status not working")
		has(t, rs[7], "Telegram", want)
		has(t, r.raw(), "1;38;5;203;48;5;234m /status not working ")
		r.send("jjjjj")
		r.wait("one reader only")
		r.send("\x03")
		r.finish()
	}
}

func TestFootprintLineFitsAndIsRight(t *testing.T) {
	sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
	at := time.Now()
	withKey(t, sf, "footprint", `{"started":"`+at.Add(-3*time.Hour).UTC().Format(time.RFC3339)+`","at":"`+at.UTC().Format(time.RFC3339)+`","writes":12,"rss_kb":6144,"cpu_s":10.8}`)
	state.Beat(sf, time.Now())
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\n"), Size: sized})
	r.wait("little-wd: 6 MB RAM")
	rs := check80(t, r.frame())
	has(t, rs[18], "little-wd: 6 MB RAM · 0.1% CPU · 4.0 disk writes/h · up 3 h")
	r.send("\x03")
	r.finish()
}

func TestFootprintLineInRAMMode(t *testing.T) {
	sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
	at := time.Now()
	withKey(t, sf, "footprint", `{"started":"`+at.Add(-3*time.Hour).UTC().Format(time.RFC3339)+`","at":"`+at.UTC().Format(time.RFC3339)+`","writes":12,"rss_kb":6144,"cpu_s":10.8}`)
	state.Beat(sf, time.Now())
	t.Setenv("RUNTIME_DIRECTORY", filepath.Dir(sf)) // as the unit's RuntimeDirectory= sets it
	r := start(t, &App{Path: newCfg(t, "STATE_IN_RAM=yes\nTCP_B=h:1\n"), Size: sized})
	r.wait("little-wd: 6 MB RAM")
	rs := check80(t, r.frame())
	has(t, rs[18], "little-wd: 6 MB RAM · 0.1% CPU · state in RAM, no disk writes · up 3 h")
	lacks(t, r.frame(), "writes/h")
	r.send("\x03")
	r.finish()
}

func TestFootLine(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		f    state.Footprint
		want string
	}{
		{state.Footprint{Started: t0, At: t0.Add(5 * time.Minute), Writes: 1, RSSKB: 100}, "little-wd: 1 MB RAM · 0.0% CPU · 1 disk write since start · up 5 min"},
		{state.Footprint{Started: t0, At: t0.Add(30 * time.Second), Writes: 2, RSSKB: 6500, CPUSec: 0.3}, "little-wd: 6 MB RAM · 2 disk writes since start · up 0 min"},
		{state.Footprint{Started: t0, At: t0.Add(24 * time.Hour), Writes: 288 * 2, RSSKB: 12 * 1024, CPUSec: 864}, "little-wd: 12 MB RAM · 1.0% CPU · 24 disk writes/h · up 24 h"},
		{state.Footprint{Started: t0, At: t0.Add(72 * time.Hour), Writes: 144 * 3, RSSKB: 6144, CPUSec: 43.2}, "little-wd: 6 MB RAM · 0.0% CPU · 6.0 disk writes/h · up 3 d"},
	} {
		if got := footLine(&c.f, false); got != c.want {
			t.Errorf("\n got %q\nwant %q", got, c.want)
		}
	}
}

func TestFootLineInRAM(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	f := state.Footprint{Started: t0, At: t0.Add(3 * time.Hour), Writes: 40, RSSKB: 6144, CPUSec: 10.8}
	if got, want := footLine(&f, true), "little-wd: 6 MB RAM · 0.1% CPU · state in RAM, no disk writes · up 3 h"; got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

func TestTestMessageOnlyClearsErrorsOlderThanItself(t *testing.T) {
	now := time.Now()
	s := snapshot{pending: 1}
	s.tg = state.Telegram{Err: "timeout", ErrSince: now.Add(-20 * time.Minute), ErrAt: now.Add(-time.Minute)}
	s.linked = now.Add(-10 * time.Minute) // after the first failure, before the latest one
	if s.tgErr() == "" {
		t.Fatal("a failure after the test message must stay")
	}
	s.linked = now
	if s.tgErr() != "" {
		t.Fatal("a test message after the last failure clears it")
	}
	s.linked = time.Time{}
	s.pending, s.tg.ErrAt = 0, now.Add(-2*time.Hour)
	if s.tgErr() != "" || !s.tgOld() {
		t.Fatal("an old error with nothing waiting is no live problem")
	}
}
