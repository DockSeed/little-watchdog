// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package state

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
)

func TestFineTrimsByTime(t *testing.T) {
	f := fine{}
	for i := 0; i <= 40; i++ { // one measurement a minute for 40 minutes
		f.add("a", checks.OK, 20*time.Millisecond, "", t0.Add(time.Duration(i)*time.Minute))
	}
	s := f["a"]
	last := t0.Add(40 * time.Minute).Unix()
	if len(s) != 31 || s[0].at != last-1800 || s[len(s)-1].at != last {
		t.Fatalf("%d points, %d..%d, want 31 ending %d", len(s), s[0].at, s[len(s)-1].at, last)
	}
	f.add("a", checks.OK, 0, "", t0.Add(41*time.Hour)) // a long gap empties the window but for the new point
	if len(f["a"]) != 1 {
		t.Fatalf("%d points after a gap", len(f["a"]))
	}
	f.add("a", checks.UNKNOWN, 0, "", t0.Add(42*time.Hour)) // no verdict, no point
	if len(f["a"]) != 1 {
		t.Fatal("UNKNOWN was kept")
	}
	f.add("a", checks.OK, 0, "", t0) // the clock stepped back: the old points stand for a time that did not happen
	if len(f["a"]) != 1 || f["a"][0].at != t0.Unix() {
		t.Fatalf("%+v", f["a"])
	}
}

func TestFineCapPerCheck(t *testing.T) {
	f := fine{}
	for i := 0; i < 3000; i++ { // a round every second for 50 minutes
		f.add("a", checks.OK, time.Millisecond, "", t0.Add(time.Duration(i)*time.Second))
	}
	s := f["a"]
	if len(s) != fineMax {
		t.Fatalf("%d points, cap %d", len(s), fineMax)
	}
	if s[len(s)-1].at != t0.Unix()+2999 || s[0].at != t0.Unix()+2999-int64(fineMax-1) {
		t.Fatalf("the cap must drop the oldest: %d..%d", s[0].at, s[len(s)-1].at)
	}
	g := fine{} // 5 s keeps the 30 minutes up to its first point
	for i := 0; i < 1000; i++ {
		g.add("a", checks.OK, time.Millisecond, "", t0.Add(time.Duration(i)*5*time.Second))
	}
	if n := len(g["a"]); n != fineMax {
		t.Fatalf("5 s round keeps %d points", n)
	}
}

// 20 checks at the worst case the cap allows must fit the budget of about 50 KB.
func TestFineBudget20Checks(t *testing.T) {
	f := fine{}
	for i := 0; i < 1000; i++ {
		for c := 0; c < 20; c++ {
			f.add(fmt.Sprintf("http:service-%02d", c), checks.OK, time.Duration(i%900)*time.Millisecond, "", t0.Add(time.Duration(i)*5*time.Second))
		}
	}
	b := f.encode(t0.Add(1000 * 5 * time.Second))
	t.Logf("20 checks, 5 s rounds, 30 min: %d bytes", len(b))
	if len(b) > 50*1024 {
		t.Fatalf("%d bytes, budget %d", len(b), 50*1024)
	}
	if len(b) < 30*1024 {
		t.Fatalf("only %d bytes: the test did not fill the series", len(b))
	}
}

func TestFineRoundTripThroughStateFile(t *testing.T) {
	e := NewEngine(2, 1)
	c := fake{"http:web", "http"}
	timeout := checks.Result{Status: checks.FAIL, NoAnswer: true, Took: 5 * time.Second, Help: checks.Help(checks.KindTimeout)}
	e.Observe(c, took(21), t0)
	e.Observe(c, took(480), t0.Add(time.Minute))
	e.Observe(c, timeout, t0.Add(2*time.Minute))
	e.Observe(c, bad, t0.Add(3*time.Minute)) // a FAIL without a known kind
	e.Observe(c, unk, t0.Add(4*time.Minute))
	e.Observe(fake{"cpu", "cpu"}, lvl(50), t0) // system checks are not in it
	dir := t.TempDir()
	sp := filepath.Join(dir, "state.json")
	p := sp + FineSuffix
	now := t0.Add(5 * time.Minute)
	if err := e.save(sp, now); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFine(p)
	if err != nil {
		t.Fatal(err)
	}
	got := f.Check("http:web", now)
	want := []FinePoint{
		{At: t0, Ms: 21},
		{At: t0.Add(time.Minute), Ms: 480},
		{At: t0.Add(2 * time.Minute), Fail: true, Kind: checks.KindTimeout, Ms: 4095},
		{At: t0.Add(3 * time.Minute), Fail: true},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if !got[i].At.Equal(want[i].At) || got[i].Fail != want[i].Fail || got[i].Kind != want[i].Kind || got[i].Ms != want[i].Ms {
			t.Errorf("point %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(f.Check("cpu", now)) != 0 || len(f.Check("http:web", now.Add(time.Hour))) != 0 {
		t.Fatal("cpu or an old window in the file")
	}
	// the fine history is written with the state, next to it, and leaves no temp file
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Fatalf("want state.json and state.json.fine only: %v", ents)
	}
	// a restart takes the points back, trimmed by time, for the checks that still exist
	e2 := NewEngine(2, 1)
	e2.LoadFine(p, map[string]bool{"http:web": true}, now)
	if n := len(e2.fine["http:web"]); n != 4 {
		t.Fatalf("%d points after a restart", n)
	}
	e2.LoadFine(p, map[string]bool{"http:web": true}, now) // a second load replaces, it does not append
	if n := len(e2.fine["http:web"]); n != 4 {
		t.Fatalf("%d points after a second load", n)
	}
	if !f.End().Equal(now) {
		t.Fatalf("end %v, want %v", f.End(), now)
	}
	e3 := NewEngine(2, 1)
	e3.LoadFine(p, map[string]bool{"http:web": true}, now.Add(time.Hour))
	if len(e3.fine["http:web"]) != 0 {
		t.Fatal("old points came back")
	}
	e.Retain(map[string]bool{})
	if len(e.fine) != 0 {
		t.Fatal("a removed check keeps its points")
	}
}

func TestFineBadFileIsQuiet(t *testing.T) {
	for _, s := range []string{"", "nope\n", "lwfine 1 x\n", "lwfine 1 100\nhttp:a\tshor\n"} {
		f, err := ParseFine([]byte(s))
		if err == nil && len(f.m) != 0 {
			t.Errorf("%q gave %+v", s, f.m)
		}
	}
}

// Bytes written per hour with 20 checks: the state file alone before, with the fine history after.
// The number of writes does not change (they are the state's own), only their size.
func TestFineWriteBytesPerHour(t *testing.T) {
	for _, every := range []time.Duration{60 * time.Second, 10 * time.Second} {
		e := NewEngine(2, 1)
		dir := t.TempDir()
		sp := filepath.Join(dir, "state.json")
		var now time.Time
		for i := 0; i < int(time.Hour/every); i++ {
			now = t0.Add(time.Duration(i) * every)
			for c := 0; c < 20; c++ {
				e.Observe(fake{fmt.Sprintf("http:service-%02d", c), "http"}, took(10+i%300), now)
			}
		}
		if err := e.save(sp, now); err != nil {
			t.Fatal(err)
		}
		a, _ := os.Stat(sp)
		b, _ := os.Stat(sp + FineSuffix)
		const perHour = 6 // one write every 10 minutes while nothing changes
		t.Logf("20 checks, round every %v: state %d B, fine %d B; per hour %d B before, %d B after (%d writes)",
			every, a.Size(), b.Size(), perHour*a.Size(), perHour*(a.Size()+b.Size()), perHour)
		if b.Size() > 50*1024 {
			t.Fatalf("fine file %d B", b.Size())
		}
	}
}
