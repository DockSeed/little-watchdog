// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package checks implements the individual probes. All measurements read
// /proc, /sys and syscall.Statfs directly; no external programs are run.
package checks

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/DockSeed/little-watchdog/internal/config"
)

type Status string

const (
	OK      Status = "OK"
	FAIL    Status = "FAIL"
	UNKNOWN Status = "UNKNOWN"
)

// Result is the outcome of one probe run.
type Result struct {
	Status   Status
	Detail   string // reason for FAIL, e.g. "HTTP 502"
	Value    string // last measured value for the status table
	NoAnswer bool   // message becomes "no answer since X"
	// Hold: OK, but inside the hysteresis band. A check currently reported
	// as FAIL stays FAIL until a sample is outside the band.
	Hold bool
}

type Check interface {
	ID() string   // unique key in the state file
	Name() string // name shown in messages
	Type() string
	Run(ctx context.Context) Result
}

// Grace is how long past the timeout RunAll still waits for slow checks.
const Grace = 500 * time.Millisecond

// MaxParallel bounds how many checks run at the same time.
const MaxParallel = config.ParallelChecks

// RunAll runs the checks, at most MaxParallel at a time. A cycle never takes longer than
// ceil(n/MaxParallel) * (timeout+Grace); checks still unfinished then are FAIL "no answer" and left behind.
func RunAll(ctx context.Context, cs []Check, timeout time.Duration) []Result {
	type indexed struct {
		i int
		r Result
	}
	ch := make(chan indexed, len(cs))
	todo := make(chan int, len(cs))
	for i := range cs {
		todo <- i
	}
	close(todo)
	for w := min(MaxParallel, len(cs)); w > 0; w-- {
		go func() {
			for i := range todo {
				ch <- indexed{i, cs[i].Run(ctx)}
			}
		}()
	}
	res := make([]Result, len(cs))
	done := make([]bool, len(cs))
	waves := (len(cs) + MaxParallel - 1) / MaxParallel
	t := time.NewTimer(time.Duration(max(waves, 1)) * (timeout + Grace))
	defer t.Stop()
collect:
	for n := 0; n < len(cs); n++ {
		select {
		case x := <-ch:
			res[x.i], done[x.i] = x.r, true
		case <-t.C:
			break collect
		}
	}
	for i := range res {
		if !done[i] {
			res[i] = Result{Status: FAIL, NoAnswer: true, Detail: "no answer within " + timeout.String(), Value: "timeout"}
		}
	}
	return res
}

// Build creates all checks for cfg. logf receives start-up notes.
func Build(cfg *config.Config, logf func(string, ...any)) []Check {
	var cs []Check
	for _, s := range cfg.Checks {
		switch s.Type {
		case "http":
			cs = append(cs, NewHTTP(s.Name, s.Target, s.Contains, cfg.Timeout))
		case "tcp":
			cs = append(cs, &TCP{name: s.Name, addr: s.Target, timeout: cfg.Timeout})
		case "host":
			cs = append(cs, &TCP{name: s.Name, addr: s.Target, timeout: cfg.Timeout, host: true})
		}
	}
	if cfg.DiskMinFree > 0 {
		for _, p := range cfg.DiskPaths {
			cs = append(cs, &Disk{Path: p, MinFreePct: cfg.DiskMinFree})
		}
	}
	if cfg.MemMinAvail > 0 {
		cs = append(cs, &Mem{Path: "/proc/meminfo", MinAvailPct: cfg.MemMinAvail})
	}
	if cfg.TempMaxC > 0 {
		t := &Temp{Sensor: cfg.TempSensor, Glob: "/sys/class/thermal/thermal_zone*/temp", MaxC: cfg.TempMaxC}
		if _, err := t.read(); err != nil {
			logf("temperature check disabled: %v", err)
		} else {
			cs = append(cs, t)
		}
	}
	if cfg.CPUMaxPct > 0 {
		cs = append(cs, &CPU{Path: "/proc/stat", MaxPct: cfg.CPUMaxPct, Window: cfg.CPUWindow, Now: time.Now})
	}
	return cs
}

// short turns a network error into text without URLs (they may hold secrets).
func short(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "timeout"
		}
		err = ue.Err
	}
	var to interface{ Timeout() bool }
	if errors.As(err, &to) && to.Timeout() {
		return "timeout"
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return filepath.Base(pe.Path) + ": " + pe.Err.Error()
	}
	return fmt.Sprint(err)
}
