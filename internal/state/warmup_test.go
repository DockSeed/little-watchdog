// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
)

var warm = checks.Result{Status: checks.UNKNOWN, Value: "first value in 1 min"}

// restarted saves what a previous engine saw, then loads it into an engine started later.
func restarted(t *testing.T, r checks.Result, t0, start time.Time) *Engine {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state.json")
	old := NewEngine(1, 1)
	old.Started, old.Proc = t0, nil
	old.Observe(fake{"cpu", "cpu"}, r, t0)
	if err := old.Save(p); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(1, 1)
	e.Started, e.Proc = start, nil
	if w := e.Load(p, map[string]bool{"cpu": true}); w != "" {
		t.Fatal(w)
	}
	return e
}

func lastLine(s string) string { return s[strings.LastIndex(s, "\n")+1:] }

func TestBriefWarmUpAfterARestart(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 18, 0, 0, 0, time.Local)
	start := time.Date(2026, 11, 4, 20, 56, 0, 0, time.Local)
	now := start.Add(10 * time.Second)
	e := restarted(t, checks.Result{Status: checks.OK, Value: "5% (5m avg)"}, t0, start)
	e.Observe(fake{"cpu", "cpu"}, warm, now)
	got := lastLine(Brief("h", e.Snapshot(), 2, e.Started, now))
	t.Log("warm-up after restart: " + got)
	if got != "✅ <b>cpu</b>: ok before the restart at 20:56, first value in 1 min" {
		t.Fatal(got)
	}
	p := filepath.Join(t.TempDir(), "s.json")
	if err := e.Save(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), `"no_value":true`) {
		t.Fatal("marker not stored:", string(b))
	}
	e.Observe(fake{"cpu", "cpu"}, checks.Result{Status: checks.OK, Value: "7% (1m avg)"}, now.Add(time.Minute))
	got = lastLine(Brief("h", e.Snapshot(), 2, e.Started, now.Add(time.Minute)))
	t.Log("normal OK: " + got)
	if got != "✅ <b>cpu</b>: 7% (1m avg)" || e.Snapshot()[0].NoValue {
		t.Fatal("marker not cleared:", got)
	}
	// another day: When adds the date
	if got := lastLine(Brief("h", []Entry{{Name: "cpu", Type: "cpu", Reported: checks.OK, Since: t0, NoValue: true, Value: "x"}}, 2, start, start.Add(24*time.Hour))); got != "✅ <b>cpu</b>: ok before the restart at Nov 4 20:56, x" {
		t.Fatal(got)
	}
}

func TestBriefWarmUpFreshInstallAndFail(t *testing.T) {
	start := time.Date(2026, 11, 4, 20, 56, 0, 0, time.Local)
	now := start.Add(10 * time.Second)
	e := NewEngine(1, 1)
	e.Started = start
	e.Observe(fake{"cpu", "cpu"}, warm, now)
	got := lastLine(Brief("h", e.Snapshot(), 2, e.Started, now))
	t.Log("fresh install warm-up: " + got)
	if got != "- <b>cpu</b>: first value in 1 min, not known yet" {
		t.Fatal(got)
	}
	e = restarted(t, checks.Result{Status: checks.FAIL, Value: "97% (5m avg)"}, start.Add(-time.Hour), start)
	e.Observe(fake{"cpu", "cpu"}, warm, now)
	got = lastLine(Brief("h", e.Snapshot(), 2, e.Started, now))
	t.Log("FAIL across restart: " + got)
	if got != "❌ <b>cpu</b>: down since 19:56, first value in 1 min" {
		t.Fatal(got)
	}
}

func TestEveryUnknownResultIsNoVerdict(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 19, 0, 0, 0, time.Local)
	start := t0.Add(116 * time.Minute) // 20:56
	e := restarted(t, checks.Result{Status: checks.OK, Value: "5%"}, t0, start)
	e.Observe(fake{"cpu", "cpu"}, checks.Result{Status: checks.UNKNOWN, Value: "97% (3m of 15m avg)"}, start.Add(3*time.Minute))
	if got := lastLine(Brief("h", e.Snapshot(), 2, start, start.Add(3*time.Minute))); got != "✅ <b>cpu</b>: ok before the restart at 20:56, 97% (3m of 15m avg)" {
		t.Fatal(got)
	}
	if out := Table(e.Snapshot(), 2, start, start); !strings.Contains(out, "(no verdict yet)") {
		t.Fatal(out)
	}
	// an OK after the start, then no verdict (a reload reset the samples): no restart wording
	e.Observe(fake{"cpu", "cpu"}, checks.Result{Status: checks.OK, Value: "5%"}, start.Add(20*time.Minute))
	e.Observe(fake{"cpu", "cpu"}, warm, start.Add(21*time.Minute))
	if got := lastLine(Brief("h", e.Snapshot(), 2, start, start.Add(21*time.Minute))); got != "✅ <b>cpu</b>: ok at 21:16, first value in 1 min" {
		t.Fatal(got)
	}
}

func TestClearingNoVerdictIsWrittenAtOnce(t *testing.T) {
	e := NewEngine(1, 1)
	e.Proc = nil
	now := time.Unix(1_000_000, 0)
	e.Observe(fake{"cpu", "cpu"}, checks.Result{Status: checks.OK, Value: "1%"}, now)
	p := filepath.Join(t.TempDir(), "s.json")
	if err := e.Save(p); err != nil {
		t.Fatal(err)
	}
	e.Observe(fake{"cpu", "cpu"}, warm, now)
	if e.dirty || !e.cdirty {
		t.Fatal("setting the marker must wait for the periodic write", e.dirty, e.cdirty)
	}
	if err := e.Save(p); err != nil {
		t.Fatal(err)
	}
	e.Observe(fake{"cpu", "cpu"}, warm, now.Add(time.Minute))
	if e.dirty || e.cdirty {
		t.Fatal("a check that stays without verdict caused a write")
	}
	e.Observe(fake{"cpu", "cpu"}, checks.Result{Status: checks.OK, Value: "2%"}, now.Add(2*time.Minute))
	if !e.dirty {
		t.Fatal("clearing the marker did not mark the state dirty")
	}
}
