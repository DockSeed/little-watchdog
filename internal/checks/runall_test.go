// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type slowCheck struct {
	d    time.Duration
	name string
}

func (s slowCheck) ID() string { return "t:" + s.name }

func (s slowCheck) Name() string { return s.name }

func (s slowCheck) Type() string { return "t" }

func (s slowCheck) Run(ctx context.Context) Result {
	time.Sleep(s.d)
	return Result{Status: OK, Value: s.name}
}

func TestRunAllDeadlineAndParallel(t *testing.T) {
	cs := []Check{slowCheck{150 * time.Millisecond, "a"}, slowCheck{150 * time.Millisecond, "b"}, slowCheck{150 * time.Millisecond, "c"}, slowCheck{5 * time.Second, "hang"}}
	start := time.Now()
	rs := RunAll(context.Background(), cs, 300*time.Millisecond)
	el := time.Since(start)
	if el > 300*time.Millisecond+Grace+300*time.Millisecond {
		t.Fatalf("took %s", el)
	}
	if el < 300*time.Millisecond || el > time.Second {
		t.Fatalf("not parallel with deadline: %s", el)
	}
	for i := 0; i < 3; i++ {
		if rs[i].Status != OK {
			t.Fatalf("%d: %+v", i, rs[i])
		}
	}
	if r := rs[3]; r.Status != FAIL || !r.NoAnswer || r.Detail == "" {
		t.Fatalf("straggler: %+v", r)
	}
}

type slow struct {
	id        string
	cur, peak *atomic.Int32
	d         time.Duration
}

func (s slow) ID() string { return s.id }

func (s slow) Name() string { return s.id }

func (s slow) Type() string { return "http" }

func (s slow) Run(context.Context) Result {
	n := s.cur.Add(1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(s.d)
	s.cur.Add(-1)
	return Result{Status: OK}
}

func TestRunAllBoundsConcurrency(t *testing.T) {
	var cur, peak atomic.Int32
	var cs []Check
	for i := 0; i < 100; i++ {
		cs = append(cs, slow{id: "c" + itoa(i), cur: &cur, peak: &peak, d: 5 * time.Millisecond})
	}
	rs := RunAll(context.Background(), cs, 5*time.Second)
	if peak.Load() > MaxParallel {
		t.Fatalf("%d checks at once", peak.Load())
	}
	for i, r := range rs {
		if r.Status != OK {
			t.Fatalf("check %d: %+v (queued checks must not time out)", i, r)
		}
	}
}

func TestRunAllCycleIsBounded(t *testing.T) { // stuck checks end the cycle after the documented time
	var cur, peak atomic.Int32
	cs := make([]Check, 2*MaxParallel)
	for i := range cs {
		cs[i] = slow{id: "c" + itoa(i), cur: &cur, peak: &peak, d: time.Hour}
	}
	start := time.Now()
	rs := RunAll(context.Background(), cs, 20*time.Millisecond)
	if el := time.Since(start); el > 2*(20*time.Millisecond+Grace)+time.Second {
		t.Fatalf("cycle took %v", el)
	}
	if rs[0].Status != FAIL || !rs[0].NoAnswer {
		t.Fatalf("%+v", rs[0])
	}
}
