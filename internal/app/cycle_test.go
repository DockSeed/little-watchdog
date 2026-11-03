// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/state"
)

func TestCancelledCycleDiscardsResults(t *testing.T) {
	var buf bytes.Buffer
	a, _ := newApp(t, "http://127.0.0.1:1", "", &buf)
	a.Checks = []checks.Check{ctxCheck{}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	a.Check(ctx)
	if n := len(a.Eng.Snapshot()); n != 0 {
		t.Fatalf("observed %d results of an aborted cycle", n)
	}
}

type ctxCheck struct{}

func (ctxCheck) ID() string { return "http:c" }

func (ctxCheck) Name() string { return "c" }

func (ctxCheck) Type() string { return "http" }

func (ctxCheck) Run(ctx context.Context) checks.Result {
	<-ctx.Done()
	return checks.Result{Status: checks.FAIL, Detail: "context canceled"}
}

type counting struct {
	*scripted
	n atomic.Int32
}

func (c *counting) Run(ctx context.Context) checks.Result { c.n.Add(1); return c.scripted.Run(ctx) }

func TestCycleBoundedByTimeout(t *testing.T) {
	var buf bytes.Buffer
	a, _ := newApp(t, "http://127.0.0.1:1", "", &buf)
	a.Cfg.Timeout = 200 * time.Millisecond
	a.Checks = []checks.Check{hang{}}
	start := time.Now()
	a.Check(context.Background())
	if el := time.Since(start); el > a.Cfg.Timeout+checks.Grace+300*time.Millisecond {
		t.Fatalf("cycle took %s", el)
	}
	if en := a.Eng.Snapshot(); len(en) != 1 || en[0].Value != "timeout" {
		t.Fatalf("%+v", en)
	}
}

type hang struct{}

func (hang) ID() string { return "disk:/x" }

func (hang) Name() string { return "disk /x" }

func (hang) Type() string { return "disk" }

func (hang) Run(context.Context) checks.Result { time.Sleep(10 * time.Second); return checks.Result{} }

func TestFailedStateSaveStaysDirtyAndLogsOnce(t *testing.T) {
	var buf bytes.Buffer
	a, _ := newApp(t, "http://127.0.0.1:1", "", &buf)
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o600)
	a.Cfg.StateFile = filepath.Join(blocker, "state.json") // a directory cannot be made below a file
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		a.Check(ctx)
		fk.Advance(time.Minute)
	}
	if n := strings.Count(buf.String(), "saving state failed"); n != 1 {
		t.Fatalf("%d warnings:\n%s", n, buf.String())
	}
	good := filepath.Join(t.TempDir(), "state.json")
	a.Cfg.StateFile = good
	a.Check(ctx)
	if _, err := os.Stat(good); err != nil {
		t.Fatal("dirty state was not written after the fix")
	}
	if !strings.Contains(buf.String(), "state file saving works again") {
		t.Fatal(buf.String())
	}
}

func TestHeartbeatAfterEveryCycle(t *testing.T) {
	var buf bytes.Buffer
	a, _ := newApp(t, "http://127.0.0.1:1", "", &buf)
	a.Now = func() time.Time { return time.Now() }
	a.Check(context.Background())
	if _, ok := state.LastBeat(a.Cfg.StateFile); !ok {
		t.Fatal("no heartbeat")
	}
	if _, stale := state.Stale(a.Cfg.StateFile, a.Cfg.StaleAfter(), time.Now()); stale {
		t.Fatal("fresh heartbeat is stale")
	}
}

func TestHeartbeatSurvivesAFailingSave(t *testing.T) {
	var buf bytes.Buffer
	a, _ := newApp(t, "http://127.0.0.1:1", "", &buf)
	// state.json is a non-empty directory: every save fails, the directory itself is writable
	if err := os.MkdirAll(filepath.Join(a.Cfg.StateFile, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	a.Now = time.Now
	a.Check(context.Background())
	if !strings.Contains(buf.String(), "saving state failed") {
		t.Fatal("test setup: the save did not fail")
	}
	if _, stale := state.Stale(a.Cfg.StateFile, a.Cfg.StaleAfter(), time.Now()); stale {
		t.Fatal("a running daemon with a failing save is shown as dead")
	}
}
