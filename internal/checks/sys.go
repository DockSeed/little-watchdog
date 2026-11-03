// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Disk: free space of a mount point via Statfs (Bavail, like df).
type Disk struct {
	Path       string
	MinFreePct float64

	statfs func(string, *syscall.Statfs_t) error // test hook
	busy   atomic.Bool                           // a hung statfs is not started again
}

func (d *Disk) ID() string   { return "disk:" + d.Path }
func (d *Disk) Name() string { return "disk " + d.Path }
func (d *Disk) Type() string { return "disk" }

// notMounted reports a path on the same device as its parent (not "/").
func notMounted(path string) (bool, error) {
	if path == "/" {
		return false, nil
	}
	var a, b syscall.Stat_t
	if err := syscall.Stat(path, &a); err != nil {
		return false, err
	}
	if err := syscall.Stat(filepath.Dir(filepath.Clean(path)), &b); err != nil {
		return false, err
	}
	return a.Dev == b.Dev, nil
}

func (d *Disk) Run(ctx context.Context) Result {
	if !d.busy.CompareAndSwap(false, true) {
		return Result{Status: FAIL, NoAnswer: true, Detail: "the last disk query is still stuck", Value: "hanging"}
	}
	defer d.busy.Store(false)
	var st syscall.Statfs_t
	sf := d.statfs
	if sf == nil {
		sf = syscall.Statfs
	}
	if nm, err := notMounted(d.Path); err != nil {
		return Result{Status: FAIL, Detail: "cannot read " + d.Path + ": " + short(err), Value: short(err)}
	} else if nm {
		return Result{Status: FAIL, Detail: d.Path + " is not mounted", Value: "not mounted"}
	}
	if err := sf(d.Path, &st); err != nil {
		return Result{Status: FAIL, Detail: "cannot read " + d.Path + ": " + short(err), Value: short(err)}
	}
	if st.Blocks == 0 {
		return Result{Status: UNKNOWN, Value: "no blocks"}
	}
	free := float64(st.Bavail) / float64(st.Blocks) * 100
	val := fmt.Sprintf("%.0f%% free", free)
	if free < d.MinFreePct {
		return Result{Status: FAIL, Detail: fmt.Sprintf("only %.0f%% free, limit %g%%", free, d.MinFreePct), Value: val}
	}
	return Result{Status: OK, Value: val}
}

// Mem: MemAvailable / MemTotal.
type Mem struct {
	Path        string
	MinAvailPct float64
}

func (m *Mem) ID() string   { return "sys:mem" }
func (m *Mem) Name() string { return "memory" }
func (m *Mem) Type() string { return "mem" }

// ParseMeminfo returns MemTotal and MemAvailable in kB.
func ParseMeminfo(r io.Reader) (total, avail uint64, err error) {
	sc := bufio.NewScanner(r)
	var gotT, gotA bool
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		switch k {
		case "MemTotal":
			total, err = strconv.ParseUint(f[0], 10, 64)
			gotT = err == nil
		case "MemAvailable":
			avail, err = strconv.ParseUint(f[0], 10, 64)
			gotA = err == nil
		}
		if err != nil {
			return 0, 0, fmt.Errorf("meminfo %s: %w", k, err)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if !gotT || !gotA || total == 0 {
		return 0, 0, errors.New("meminfo: MemTotal/MemAvailable missing")
	}
	return total, avail, nil
}

func (m *Mem) Run(ctx context.Context) Result {
	f, err := os.Open(m.Path)
	if err != nil {
		return Result{Status: UNKNOWN, Value: short(err)}
	}
	defer f.Close()
	total, avail, err := ParseMeminfo(f)
	if err != nil {
		return Result{Status: UNKNOWN, Value: err.Error()}
	}
	p := float64(avail) / float64(total) * 100
	val := fmt.Sprintf("%.0f%% available", p)
	if p < m.MinAvailPct {
		return Result{Status: FAIL, Detail: fmt.Sprintf("only %.0f%% memory available, limit %g%%", p, m.MinAvailPct), Value: val}
	}
	return Result{Status: OK, Value: val}
}

// Temp: highest thermal zone (or one explicit sensor), millidegrees in sysfs.
type Temp struct {
	Sensor string // explicit file; empty = Glob
	Glob   string
	MaxC   float64

	busy atomic.Bool // a hung sysfs read is not started again
}

func (t *Temp) ID() string   { return "sys:temp" }
func (t *Temp) Name() string { return "temperature" }
func (t *Temp) Type() string { return "temp" }

// readSmall reads at most 64 bytes of a regular file (a sensor value).
func readSmall(path string) ([]byte, error) {
	if st, err := os.Lstat(path); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64))
}

func (t *Temp) read() (float64, error) {
	files := []string{t.Sensor}
	if t.Sensor == "" {
		var err error
		files, err = filepath.Glob(t.Glob)
		if err != nil {
			return 0, err
		}
	}
	best, found := 0.0, false
	var lastErr error = errors.New("no temperature sensor found")
	for _, f := range files {
		b, err := readSmall(f)
		if err != nil {
			lastErr = err
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err != nil {
			lastErr = errors.New("not a number")
			continue
		}
		v /= 1000
		if !found || v > best {
			best, found = v, true
		}
	}
	if !found {
		return 0, lastErr
	}
	return best, nil
}

func (t *Temp) Run(ctx context.Context) Result {
	if !t.busy.CompareAndSwap(false, true) {
		return Result{Status: UNKNOWN, Value: "previous sensor read still hanging"}
	}
	defer t.busy.Store(false)
	v, err := t.read()
	if err != nil {
		return Result{Status: UNKNOWN, Value: short(err)}
	}
	val := fmt.Sprintf("%.1f C", v)
	switch {
	case v > t.MaxC:
		return Result{Status: FAIL, Detail: fmt.Sprintf("%.1f C, limit %g C", v, t.MaxC), Value: val}
	case v >= t.MaxC-5:
		return Result{Status: OK, Value: val, Hold: true}
	}
	return Result{Status: OK, Value: val}
}

// CPU: busy share over a sliding window of /proc/stat samples.
type CPU struct {
	Path   string
	MaxPct float64
	Window time.Duration
	Every  time.Duration // check interval; 0 = unknown
	Now    func() time.Time

	mu      sync.Mutex
	samples []cpuSample
}

type cpuSample struct {
	t           time.Time
	busy, total uint64
}

// firstValue tells when the first number will show up.
func (c *CPU) firstValue() string {
	if c.Every <= 0 {
		return "first value after the next round"
	}
	return "first value in " + Dur(c.Every)
}

// Compact renders 3m0s as 3m and 1h0m0s as 1h.
func Compact(d time.Duration) string {
	s := d.Round(time.Second).String()
	s = strings.TrimSuffix(s, "m0s")
	if s != d.Round(time.Second).String() {
		s += "m"
	}
	if t := strings.TrimSuffix(s, "h0m"); t != s {
		s = t + "h"
	}
	return s
}

func (c *CPU) ID() string   { return "sys:cpu" }
func (c *CPU) Name() string { return "cpu" }
func (c *CPU) Type() string { return "cpu" }

// ParseStat reads the aggregate "cpu" line of /proc/stat. Guest columns are
// already part of user/nice and are not added again.
func ParseStat(r io.Reader) (busy, total uint64, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || f[0] != "cpu" {
			continue
		}
		if len(f) < 5 {
			return 0, 0, errors.New("stat: short cpu line")
		}
		var idle uint64
		for i := 1; i < len(f) && i <= 8; i++ {
			v, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("stat: %w", err)
			}
			total += v
			if i == 4 || i == 5 { // idle, iowait
				idle += v
			}
		}
		return total - idle, total, nil
	}
	return 0, 0, errors.New("stat: no cpu line")
}

func (c *CPU) Run(ctx context.Context) Result {
	f, err := os.Open(c.Path)
	if err != nil {
		return Result{Status: UNKNOWN, Value: short(err)}
	}
	busy, total, err := ParseStat(f)
	f.Close()
	if err != nil {
		return Result{Status: UNKNOWN, Value: err.Error()}
	}
	now := c.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, cpuSample{now, busy, total})
	for len(c.samples) >= 3 && now.Sub(c.samples[1].t) >= c.Window {
		c.samples = c.samples[1:]
	}
	if len(c.samples) < 2 {
		return Result{Status: UNKNOWN, Value: c.firstValue()}
	}
	old := c.samples[0]
	dt := total - old.total
	if dt == 0 || total < old.total {
		return Result{Status: UNKNOWN, Value: "no cpu time delta"}
	}
	p := float64(busy-old.busy) / float64(dt) * 100
	age := now.Sub(old.t)
	full := age >= c.Window-c.Window/20
	label := fmt.Sprintf("%.0f%% (%s avg)", p, Compact(age))
	switch {
	case !full:
		return Result{Status: UNKNOWN, Value: fmt.Sprintf("%.0f%% (%s of %s avg)", p, Compact(age), Compact(c.Window))}
	case p > c.MaxPct:
		return Result{Status: FAIL, Detail: fmt.Sprintf("%.0f%% busy over %s, limit %g%%", p, Compact(c.Window), c.MaxPct), Value: label}
	case p >= c.MaxPct-10:
		return Result{Status: OK, Value: label, Hold: true}
	}
	return Result{Status: OK, Value: label}
}
