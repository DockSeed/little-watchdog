// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"github.com/DockSeed/little-watchdog/internal/state"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const wipe = "\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f"

func settingsRig(t *testing.T, cfg string) (*rig, string) {
	p := aliveCfg(t, cfg)
	r := start(t, &App{Path: p, Size: sized})
	r.wait("Dead-man switch")
	r.send("jjjj\r")
	r.wait("Timeout per check")
	return r, p
}

func TestSettingsMenuSummary(t *testing.T) {
	r := start(t, &App{Path: aliveCfg(t, "INTERVAL=5m\nFAIL_AFTER=4\n"), Size: sized})
	r.wait("Dead-man switch")
	rs := check80(t, r.frame())
	has(t, rs[5], "System checks")
	has(t, rs[6], "Settings", "every 5m, alarm after 4, all-clear after 2")
	has(t, rs[7], "Telegram")
	r.send("jjjj")
	r.wait("after how many rounds an alarm")
	r.send("\x03")
	r.finish()
}

func TestSettingsSave(t *testing.T) {
	r, p := settingsRig(t, "# keep\n")
	rs := check80(t, r.frame())
	has(t, rs[0], "little-wd  › Settings")
	has(t, r.frame(), "Check every        1m", "Alarm after        3", "All-clear after    2", "Timeout per check  10s", "[ Save ]")
	has(t, rs[20], "How often every check runs")
	r.send("\x1b[B")
	r.wait("Failed rounds in a row, then a Telegram alarm")
	r.send("\x1b[B")
	r.wait("Good rounds in a row after an alarm, then the all-clear")
	r.send("\x1b[B")
	r.wait("counts as failed")
	r.send("\x1b[A\x1b[A\x1b[A" + wipe + "2m\r" + wipe + "5\r\r\r\r")
	r.wait("Saved")
	got := read(t, p)
	if !strings.Contains(got, "# keep\nINTERVAL=2m\nFAIL_AFTER=5\n") || strings.Contains(got, "RECOVER_AFTER") || strings.Contains(got, "TIMEOUT") {
		t.Fatalf("%q\n%s", got, r.frame()) // unchanged values are not written
	}
	has(t, r.frame(), "Settings saved to ", "applies it within one interval")
	r.send("x")
	r.wait("every 2m, alarm after 5, all-clear after 2")
	r.send("\x03")
	r.finish()
}

func TestSettingsRejectInvalid(t *testing.T) {
	for _, c := range []struct {
		field     int
		val, want string
	}{
		{0, "5s", "INTERVAL: invalid value (expected duration"},
		{0, "20m", "CPU_WINDOW: must be at least INTERVAL"},
		{1, "0", "FAIL_AFTER: invalid value (expected integer"},
		{2, "x", "RECOVER_AFTER: invalid value (expected integer"},
		{3, "11m", "TIMEOUT: invalid value (expected duration"},
		{3, "", "Timeout per check: enter a value"},
	} {
		t.Run(c.val, func(t *testing.T) {
			r, p := settingsRig(t, "")
			before := read(t, p)
			r.send(strings.Repeat("\x1b[B", c.field) + wipe + c.val + strings.Repeat("\x1b[B", 5-c.field) + "\r")
			r.wait("Error: ")
			has(t, r.frame(), c.want, "[ Save ]")
			check80(t, r.frame())
			if got := read(t, p); got != before {
				t.Fatalf("saved %q", got)
			}
			r.send("\x03")
			r.finish()
		})
	}
}

func TestSettingsLocked(t *testing.T) {
	skipRoot(t)
	p := newCfg(t, "TCP_B=h:1\n")
	dir := filepath.Dir(p)
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	r := start(t, &App{Path: p, Size: sized})
	r.wait("read-only")
	r.send("jjjj")
	r.wait("Needs group little-wd")
	r.send("\r")
	r.wait("Any key back")
	has(t, r.frame(), wantGroup)
	lacks(t, r.frame(), "Check every")
	r.send("x")
	r.send("\x03")
	r.finish()
}

func TestSettings60x20(t *testing.T) {
	r := start(t, &App{Path: aliveCfg(t, ""), Size: func() (int, int) { return 60, 20 }})
	r.wait("Dead-man switch")
	check(t, r.frame(), 60, 20)
	r.send("jjjj\r")
	r.wait("Timeout per check")
	has(t, r.frame(), "Check every", "All-clear after", "[ Save ]", "How often every check runs")
	check(t, r.frame(), 60, 20)
	r.send("\x03")
	r.finish()
}

func check(t *testing.T, f string, w, h int) {
	t.Helper()
	rs := rows(f)
	if len(rs) != h {
		t.Fatalf("rows: %d\n%s", len(rs), f)
	}
	for i, l := range rs {
		if n := len([]rune(l)); n != w {
			t.Fatalf("row %d has %d cells: %q", i, n, l)
		}
	}
}

func TestDeadmanEvery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("OK")) }))
	defer srv.Close()
	p := aliveCfg(t, "DEADMAN_URL="+srv.URL+"/p\n")
	r := start(t, &App{Path: p, Size: sized})
	r.send("jjjjjj\r")
	r.wait("[ Save ]")
	has(t, r.frame(), "Every     2m")
	r.send("\r")
	r.wait("Time between pings")
	r.send(wipe + "30s\r")
	r.wait("DEADMAN_EVERY: invalid value (expected duration")
	if strings.Contains(read(t, p), "DEADMAN_EVERY") {
		t.Fatal(read(t, p))
	}
	r.send(wipe + "5m\r")
	r.wait("Saved")
	if got := read(t, p); !strings.Contains(got, "DEADMAN_EVERY=5m\n") || !strings.Contains(got, "DEADMAN_URL="+srv.URL+"/p\n") {
		t.Fatal(got)
	}
	r.send("x")
	r.wait("every 5m")
	r.send("\x03")
	r.finish()
}

func TestTempSensor(t *testing.T) {
	const zone = "/sys/class/thermal/thermal_zone1/temp"
	r, p := sysRig(t, "")
	r.send("jjj\r")
	r.wait("Degrees C")
	r.send("\r")
	r.wait("Empty = the hottest sensor.")
	r.send("/etc/passwd\r")
	r.wait("TEMP_SENSOR: expected a path under")
	if strings.Contains(read(t, p), "TEMP_SENSOR") {
		t.Fatal(read(t, p))
	}
	r.send(strings.Repeat("\x7f", 11) + zone + "\r")
	r.wait("Saved")
	if !strings.Contains(read(t, p), "TEMP_SENSOR="+zone+"\n") {
		t.Fatal(read(t, p))
	}
	r.send("x")
	r.wait("Menu")
	r.send("\r") // System checks, cursor still on it
	r.wait("Space turns")
	r.send("jjj\r\r")
	r.wait(zone)
	r.send(strings.Repeat("\x7f", len(zone)) + "\r") // empty: back to the hottest sensor
	r.wait("Saved")
	if strings.Contains(read(t, p), "TEMP_SENSOR") {
		t.Fatal(read(t, p))
	}
	r.send("x")
	r.send("\x03")
	r.finish()
}

func TestSettingsKeepUntouchedValuesAsWritten(t *testing.T) {
	r, p := settingsRig(t, "INTERVAL=60s\nTIMEOUT=1500ms\n")
	has(t, r.frame(), "Check every        60s", "Timeout per check  1500ms")
	r.send("\x1b[B" + wipe + "4\r\r\r\r")
	r.wait("Saved")
	if got := read(t, p); !strings.HasSuffix(got, "\nINTERVAL=60s\nTIMEOUT=1500ms\nFAIL_AFTER=4\n") {
		t.Fatalf("%q", got)
	}
	r.send("\x03")
	r.finish()
}

func TestSettingsStateInRAMSwitch(t *testing.T) {
	r, p := settingsRig(t, "# keep\n")
	has(t, r.frame(), "State in RAM       no")
	r.send("\x1b[B\x1b[B\x1b[B\x1b[B")
	r.wait("0 disk writes. no = on disk")
	r.send(wipe + "yes\r")
	r.wait("Saved")
	if got := read(t, p); !strings.HasSuffix(got, "# keep\nSTATE_IN_RAM=yes\n") {
		t.Fatalf("%q", got)
	}
	has(t, r.frame(), "The state now lives in RAM", "a reboot loses the history", "alarm may be sent", "missing: the installed", "applies it within one interval")
	r.send("\x03")
	r.finish()

	// and back: only the value changes, the sentence says what it costs
	r, p = settingsRig(t, "STATE_IN_RAM=yes\n")
	has(t, r.frame(), "State in RAM       yes")
	r.send("\x1b[B\x1b[B\x1b[B\x1b[B" + wipe + "no\r")
	r.wait("Saved")
	if got := read(t, p); !strings.HasSuffix(got, "STATE_IN_RAM=no\n") {
		t.Fatalf("%q", got)
	}
	has(t, r.frame(), "back on disk", "16 writes an hour")
	r.send("\x03")
	r.finish()
}

func TestSettingsStateInRAMRejectsJunk(t *testing.T) {
	r, p := settingsRig(t, "")
	before := read(t, p)
	r.send("\x1b[B\x1b[B\x1b[B\x1b[B" + wipe + "maybe\r")
	r.wait("STATE_IN_RAM: invalid value (expected yes or no)")
	if read(t, p) != before {
		t.Fatal("config changed")
	}
	r.send("\x03")
	r.finish()
}

// Between saving the switch and the daemon's move the new place has no heartbeat yet: the old
// place is read, so the screen shows the checks and no "daemon not running", both ways.
func TestSettingsSwitchWindowIsNoFalseAlarm(t *testing.T) {
	ram := t.TempDir()
	t.Setenv("RUNTIME_DIRECTORY", ram)
	r, p := settingsRig(t, "")
	r.send("\x1b[B\x1b[B\x1b[B\x1b[B" + wipe + "yes\r")
	r.wait("Saved")
	lacks(t, r.frame(), "does not seem to run")
	r.send("x")
	r.wait("every 1m")
	lacks(t, r.frame(), "daemon not running")
	r.send("\x03")
	r.finish()

	// back: the daemon has moved the state to RAM, the disk place is empty for a moment
	sf := filepath.Join(ram, "state.json")
	state.Beat(sf, time.Now())
	r = start(t, &App{Path: newCfg(t, "STATE_IN_RAM=yes\nSTATE_FILE="+filepath.Join(t.TempDir(), "disk.json")+"\n"), Size: sized})
	r.wait("Dead-man switch")
	r.send("jjjj\r")
	r.wait("Timeout per check")
	r.send("\x1b[B\x1b[B\x1b[B\x1b[B" + wipe + "no\r")
	r.wait("Saved")
	lacks(t, r.frame(), "does not seem to run")
	r.send("x")
	r.wait("every 1m")
	lacks(t, r.frame(), "daemon not running")
	r.send("\x03")
	r.finish()
	_ = p
}
