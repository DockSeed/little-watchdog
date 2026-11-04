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

func sysRig(t *testing.T, cfg string) (*rig, string) {
	sf := writeState(t,
		ent{"cpu", "cpu", "OK", "5% (15m avg)", 0, time.Time{}, time.Time{}},
		ent{"mem", "memory", "FAIL", "3% available", 3, time.Time{}, time.Time{}},
		ent{"disk", "disk /", "OK", "77% free", 0, time.Time{}, time.Time{}})
	state.Beat(sf, time.Now())
	p := newCfg(t, "STATE_FILE="+sf+"\n"+cfg)
	r := start(t, &App{Path: p, Size: sized})
	r.send("jjj\r")
	r.wait("Space turns a check on or off")
	return r, p
}

func TestSystemChecksListWithOffRow(t *testing.T) {
	r, _ := sysRig(t, "TEMP_MAX_C=0\n")
	rs := check80(t, r.frame())
	has(t, rs[0], "little-wd  › System checks")
	has(t, rs[2], "State", "Check", "Alarm")
	has(t, rs[3], "[OK]", "cpu", "alarm above 90% (15m average)")
	has(t, rs[4], "[FAIL]", "memory", "alarm below 5% available")
	has(t, rs[5], "[OK]", "disk", "alarm below 10% free")
	has(t, rs[6], "temperature", "off")
	lacks(t, rs[6], "[")
	has(t, rs[20], "Space turns a check on or off")
	has(t, rs[22], "↑↓ move", "Enter change", "Space on/off", "Esc menu", "? help")
	r.send("\x1b")
	r.wait("Menu")
	has(t, r.frame(), "System checks", "3 on, 1 off")
	r.send("\x03")
	r.finish()
}

func TestSystemChecksEditCPU(t *testing.T) {
	r, p := sysRig(t, "")
	r.send("\r")
	r.wait("Percent")
	has(t, r.frame(), "Percent", "90", "Window", "15m")
	r.send("\x7f\x7f50\r\x7f\x7f\x7f30m\r")
	r.wait("Saved")
	got := read(t, p)
	if !strings.Contains(got, "CPU_MAX_PCT=50\n") || !strings.Contains(got, "CPU_WINDOW=30m\n") {
		t.Fatal(got)
	}
	r.send("x")
	r.wait("Menu")
	r.send("\x03")
	r.finish()
}

func TestSystemChecksRejectsBadValues(t *testing.T) {
	r, p := sysRig(t, "")
	before := read(t, p)
	r.send("\r")
	r.wait("Percent")
	r.send("\x7f\x7f150\r\r") // 150 %
	r.wait("Error:")
	r.send("\x1b[A\x7f\x7f\x7f\x7f90\r\x7f\x7f\x7f10s\r") // window below INTERVAL
	r.wait("INTERVAL")
	if read(t, p) != before {
		t.Fatal("config changed")
	}
	r.send("\x1b")
	r.wait("Menu")
	r.send("\x03")
	r.finish()
}

func TestSystemChecksSpaceTogglesAndStaysOnTheList(t *testing.T) {
	r, p := sysRig(t, "")
	r.send(" ")
	r.wait("cpu is off.")
	if !strings.Contains(read(t, p), "CPU_MAX_PCT=0\n") {
		t.Fatal(read(t, p))
	}
	rs := check80(t, r.frame())
	has(t, rs[3], "cpu", "off")
	r.send(" ")
	r.wait("cpu is on.")
	if !strings.Contains(read(t, p), "CPU_MAX_PCT=90\n") {
		t.Fatal(read(t, p))
	}
	has(t, check80(t, r.frame())[3], "alarm above 90% (15m average)")
	r.send("\x03")
	r.finish()
}

func TestSystemChecksLockedWithoutWriteAccess(t *testing.T) {
	skipRoot(t)
	p := newCfg(t, "TCP_B=h:1\n")
	dir := filepath.Dir(p)
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	r := start(t, &App{Path: p, Size: sized})
	r.wait("read-only")
	r.send("jjj\r")
	r.wait("eeds group")
	lacks(t, r.frame(), "alarm above")
	r.send("x")
	r.send("\x03")
	r.finish()
	// explore mode: config unreadable
	p = newCfg(t, "TCP_B=h:1\n")
	os.Chmod(p, 0)
	t.Cleanup(func() { os.Chmod(p, 0o600) })
	r = start(t, &App{Path: p, Size: sized})
	r.wait("explore mode")
	r.send("jjj\r")
	r.wait("eeds group")
	lacks(t, r.frame(), "alarm above")
	r.send("x")
	r.send("\x03")
	r.finish()
}
