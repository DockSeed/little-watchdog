// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package clock lets time-dependent code run without real waiting in tests.
package clock

import (
	"context"
	"sync"
	"time"
)

type Clock struct {
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

// Real is the wall clock; Sleep returns early with ctx.Err() when ctx ends.
func Real() Clock {
	return Clock{Now: time.Now, Sleep: func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}}
}

// Fake is a virtual clock: Sleep advances it instantly and records the request.
type Fake struct {
	mu     sync.Mutex
	t      time.Time
	Slept  []time.Duration
	OnWait func(time.Duration) // optional hook, called without the lock
}

func NewFake() *Fake { return &Fake{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)} }

func (f *Fake) Clock() Clock {
	return Clock{Now: f.now, Sleep: func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f.mu.Lock()
		f.t = f.t.Add(d)
		f.Slept = append(f.Slept, d)
		h := f.OnWait
		f.mu.Unlock()
		if h != nil {
			h(d)
		}
		return nil
	}}
}

func (f *Fake) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }

// Advance moves the clock without recording a sleep.
func (f *Fake) Advance(d time.Duration) { f.mu.Lock(); f.t = f.t.Add(d); f.mu.Unlock() }

func (f *Fake) Sleeps() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.Slept...)
}
