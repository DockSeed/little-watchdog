// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/state"
)

func names(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ns []string
	for _, e := range ents {
		ns = append(ns, e.Name())
	}
	sort.Strings(ns)
	return strings.Join(ns, " ")
}

// On disk only the state file is written. The heartbeat and the fine history change every
// round and mean nothing after a reboot: they go to the runtime directory.
func TestHeartbeatAndFineHistoryGoToTheRuntimeDir(t *testing.T) {
	ram := t.TempDir()
	t.Setenv("RUNTIME_DIRECTORY", ram)
	var buf bytes.Buffer
	a, s := newApp(t, "http://127.0.0.1:1", "", &buf)
	a.Cfg.Token = "" // no Telegram: the history is all this test looks at
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		a.Check(ctx)
	}
	s.set(checks.Result{Status: checks.FAIL, Detail: "x", Help: checks.Help(checks.KindRefused)})
	a.Check(ctx)
	if got := names(t, filepath.Dir(a.Cfg.StateFile)); got != "state.json" {
		t.Fatalf("disk files: %s", got)
	}
	if got := names(t, ram); got != "state.json.alive state.json.fine" {
		t.Fatalf("runtime files: %s", got)
	}
	rf := filepath.Join(ram, "state.json")
	f, err := state.ReadFine(rf + state.FineSuffix)
	if err != nil {
		t.Fatal(err)
	}
	ps := f.Check("http:web", a.Now())
	if len(ps) != 4 || ps[0].Fail || !ps[3].Fail || ps[3].Kind != checks.KindRefused {
		t.Fatalf("%+v", ps)
	}
	if _, stale := state.Stale(a.Cfg.StateFile, time.Minute, time.Now(), rf); stale {
		t.Fatal("the heartbeat in the runtime dir is not seen")
	}
	// a restart of the service (the directory stays) takes the history back
	a2, _ := newApp(t, "http://127.0.0.1:1", "", &buf)
	a2.Cfg.StateFile = a.Cfg.StateFile
	a2.LoadState()
	a2.Check(ctx)
	f, _ = state.ReadFine(rf + state.FineSuffix)
	if n := len(f.Check("http:web", a2.Now())); n != 5 {
		t.Fatalf("%d points after a restart, want 5", n)
	}
}

// STATE_IN_RAM=yes without a runtime directory: the daemon falls back to the disk state file,
// the fine history goes next to that one, and nothing appears under the missing directory.
func TestFineHistoryRAMModeWithoutRuntimeDir(t *testing.T) {
	var buf bytes.Buffer
	a, _ := newApp(t, "http://127.0.0.1:1", "", &buf)
	a.Cfg.Token = ""
	root := t.TempDir()
	a.Cfg.StateInRAM = true
	a.Cfg.StateFile = filepath.Join(root, "run", "state.json")
	a.Cfg.DiskState = filepath.Join(root, "disk", "state.json")
	a.fallbackToDisk()
	a.Check(context.Background())
	if got := names(t, root); got != "disk" {
		t.Fatalf("under the root: %s (the runtime directory must not appear)", got)
	}
	if got := names(t, filepath.Join(root, "disk")); got != "state.json state.json.alive state.json.fine" {
		t.Fatalf("disk files: %s", got)
	}
}

// With a runtime directory the fine history lives there, beside the RAM state file, and the disk stays untouched.
func TestFineHistoryRAMMode(t *testing.T) {
	var buf bytes.Buffer
	a, _ := newApp(t, "http://127.0.0.1:1", "", &buf)
	a.Cfg.Token = ""
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "run"), 0o750)
	a.Cfg.StateInRAM = true
	a.Cfg.StateFile = filepath.Join(root, "run", "state.json")
	a.Cfg.DiskState = filepath.Join(root, "disk", "state.json")
	a.fallbackToDisk()
	a.Check(context.Background())
	if got := names(t, root); got != "run" {
		t.Fatalf("under the root: %s", got)
	}
	if got := names(t, filepath.Join(root, "run")); got != "state.json state.json.alive state.json.fine" {
		t.Fatalf("run files: %s", got)
	}
}

// Not started by the unit (no RUNTIME_DIRECTORY): the daemon never writes into /run/little-watchdog
// or any other RAM directory, even a writable one -- it belongs to the installed service, and
// systemd removes it when that service stops. The state stays on disk and says why.
func TestNoRuntimeDirectoryMeansNoRAMWrites(t *testing.T) {
	t.Setenv("RUNTIME_DIRECTORY", "")
	var buf bytes.Buffer
	a, _ := newApp(t, "http://127.0.0.1:1", "", &buf)
	a.Cfg.Token = ""
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "run"), 0o750) // writable, and still not ours
	a.Cfg.StateInRAM = true
	a.Cfg.StateFile = filepath.Join(root, "run", "state.json")
	a.Cfg.DiskState = filepath.Join(root, "disk", "state.json")
	a.fallbackToDisk()
	a.Check(context.Background())
	if got := names(t, filepath.Join(root, "run")); got != "" {
		t.Fatalf("written into the RAM directory: %s", got)
	}
	if a.Cfg.StateFile != a.Cfg.DiskState || !strings.Contains(buf.String(), "RUNTIME_DIRECTORY is not set") {
		t.Fatalf("%s: %s", a.Cfg.StateFile, buf.String())
	}
}
