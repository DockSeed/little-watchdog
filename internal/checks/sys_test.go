// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestDiskHangNotRestarted(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	d := &Disk{Path: "/", MinFreePct: 1, statfs: func(string, *syscall.Statfs_t) error {
		calls.Add(1)
		<-release
		return nil
	}}
	go d.Run(context.Background())
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	r := d.Run(context.Background())
	if r.Status != FAIL || !r.NoAnswer || calls.Load() != 1 {
		t.Fatalf("%+v calls=%d", r, calls.Load())
	}
	close(release)
}

func TestCPUUnknownUntilWindowFull(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stat")
	write := func(busy, idle int) {
		os.WriteFile(p, []byte(fmt.Sprintf("cpu %d 0 0 %d 0 0 0 0\n", busy, idle)), 0o644)
	}
	now := time.Unix(1000, 0)
	c := &CPU{Path: p, MaxPct: 90, Window: 100 * time.Second, Now: func() time.Time { return now }}
	write(0, 0)
	if r := c.Run(context.Background()); r.Status != UNKNOWN {
		t.Fatalf("first sample: %+v", r)
	}
	now = now.Add(50 * time.Second)
	write(500, 0)
	if r := c.Run(context.Background()); r.Status != UNKNOWN || r.Value != "100% (50s of 1m40s avg)" {
		t.Fatalf("filling window: %+v", r)
	}
}

func TestDiskNotMounted(t *testing.T) {
	if r := (&Disk{Path: t.TempDir()}).Run(context.Background()); r.Status != FAIL || !strings.Contains(r.Detail, "not mounted") {
		t.Fatalf("plain directory: %+v", r)
	}
	if r := (&Disk{Path: "/", MinFreePct: 0}).Run(context.Background()); r.Status != OK {
		t.Fatalf("root: %+v", r)
	}
	if r := (&Disk{Path: "/dev/shm", MinFreePct: 0}).Run(context.Background()); r.Status == FAIL && strings.Contains(r.Detail, "not mounted") {
		t.Skip("/dev/shm is not a separate mount here")
	}
}

func TestTempNeverEchoesContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "temp")
	os.WriteFile(p, []byte("TOPSECRET\x1b[2J"), 0o644)
	r := (&Temp{Sensor: p, MaxC: 85}).Run(context.Background())
	if r.Value != "not a number" {
		t.Fatalf("%+v", r)
	}
}

func TestTempHangGuard(t *testing.T) {
	tp := &Temp{Sensor: "/nonexistent", MaxC: 80}
	tp.busy.Store(true)
	if r := tp.Run(context.Background()); r.Status != UNKNOWN || !strings.Contains(r.Value, "hanging") {
		t.Fatalf("%+v", r)
	}
}

func TestSensorReadIsSmallAndRegular(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big")
	os.WriteFile(big, []byte(strings.Repeat("1", 1<<20)), 0o600)
	b, err := readSmall(big)
	if err != nil || len(b) != 64 {
		t.Fatal(len(b), err)
	}
	link := filepath.Join(dir, "link")
	os.Symlink(big, link)
	if _, err := readSmall(link); err == nil {
		t.Fatal("symlink read")
	}
	if _, err := readSmall(dir); err == nil {
		t.Fatal("directory read")
	}
}

func TestCPUValueTexts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stat")
	write := func(busy, idle int) {
		os.WriteFile(p, []byte(fmt.Sprintf("cpu %d 0 0 %d 0 0 0 0\n", busy, idle)), 0o644)
	}
	now := time.Unix(1000, 0)
	c := &CPU{Path: p, MaxPct: 90, Window: 15 * time.Minute, Every: time.Minute, Now: func() time.Time { return now }}
	write(0, 0)
	if r := c.Run(context.Background()); r.Value != "first value in 1 min" {
		t.Fatal(r)
	}
	now = now.Add(3 * time.Minute)
	write(12, 88)
	if r := c.Run(context.Background()); r.Value != "12% (3m of 15m avg)" {
		t.Fatal(r)
	}
	now = now.Add(12 * time.Minute)
	write(24, 176)
	if r := c.Run(context.Background()); r.Status != OK || r.Value != "12% (15m avg)" {
		t.Fatal(r)
	}
}
