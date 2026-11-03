// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveOnlyWhenChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	e := NewEngine(2, 1)
	c := fake{"a", "http"}
	t0 := time.Unix(1_000_000, 0)
	e.Observe(c, ok, t0)
	if err := e.SaveIfDue(path, t0); err != nil || e.Writes() != 1 {
		t.Fatal(err, e.Writes())
	}
	for i := 1; i <= 5; i++ {
		e.Observe(c, ok, t0.Add(time.Duration(i)*time.Minute))
		e.SaveIfDue(path, t0.Add(time.Duration(i)*time.Minute))
	}
	if e.Writes() != 1 {
		t.Fatalf("unchanged state written %d times", e.Writes())
	}
	e.Observe(c, bad, t0.Add(6*time.Minute)) // a failure streak starts: the TUI shows 1/2
	e.SaveIfDue(path, t0.Add(6*time.Minute))
	if e.Writes() != 2 {
		t.Fatalf("streak start not written: %d", e.Writes())
	}
	e.Observe(c, bad, t0.Add(7*time.Minute)) // reported FAIL
	e.SaveIfDue(path, t0.Add(7*time.Minute))
	if e.Writes() != 3 {
		t.Fatal(e.Writes())
	}
	for i := 7; i <= 40; i++ { // steady failure: no further counter change
		e.Observe(c, bad, t0.Add(time.Duration(i)*time.Minute))
		e.SaveIfDue(path, t0.Add(time.Duration(i)*time.Minute))
	}
	if e.Writes() > 3+1+3 { // alarm + refresh every 10 min
		t.Fatalf("writes %d", e.Writes())
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "\n ") {
		t.Fatal("state file is indented")
	}
}

func TestFailedSaveKeepsDirty(t *testing.T) {
	e := NewEngine(1, 1)
	e.Observe(fake{"a", "http"}, bad, time.Now())
	blocker := filepath.Join(t.TempDir(), "f")
	os.WriteFile(blocker, nil, 0o600)
	if err := e.save(filepath.Join(blocker, "s.json"), time.Now()); err == nil {
		t.Fatal("save below a file worked")
	}
	good := filepath.Join(t.TempDir(), "s.json")
	if err := e.SaveIfDue(good, time.Now()); err != nil {
		t.Fatal(err)
	}
	if e.Writes() != 1 {
		t.Fatal("dirty flag was lost by the failed save")
	}
}

func TestHeartbeatAndStale(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	now := time.Now()
	if _, stale := Stale(p, 150*time.Second, now); !stale {
		t.Fatal("no heartbeat must count as stale")
	}
	if err := Beat(p, now.Add(-30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := Beat(p, now.Add(-10*time.Second)); err != nil { // second beat only touches the mtime
		t.Fatal(err)
	}
	if age, stale := Stale(p, 150*time.Second, now); stale || age < 9*time.Second || age > 11*time.Second {
		t.Fatalf("age %v stale %v", age, stale)
	}
	if _, stale := Stale(p, 150*time.Second, now.Add(5*time.Minute)); !stale {
		t.Fatal("old heartbeat must be stale")
	}
	if st, _ := os.Stat(p + BeatSuffix); st.Size() != 0 {
		t.Fatal("heartbeat carries data")
	}
}

func TestFlappingCheckWritesOnlyOnStreakEventsAndSendsNothing(t *testing.T) {
	e := NewEngine(3, 2)
	c := fake{"a", "http"}
	p := filepath.Join(t.TempDir(), "s.json")
	t0 := time.Unix(1_000_000, 0)
	e.Observe(c, ok, t0)
	e.SaveIfDue(p, t0)
	for i := 1; i <= 30; i++ { // fail, ok, fail, ok ...: never three in a row
		r := ok
		if i%2 == 1 {
			r = bad
		}
		at := t0.Add(time.Duration(i) * 10 * time.Second)
		e.Observe(c, r, at)
		e.SaveIfDue(p, at)
	}
	// one write per flip (a streak starts or ends), none of them an alarm
	if e.Writes() > 31 || len(e.PendingMessages()) != 0 {
		t.Fatalf("%d writes for a flapping check", e.Writes())
	}
}
