// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/config"
)

func TestParseMeminfo(t *testing.T) {
	f, _ := os.Open("testdata/meminfo")
	defer f.Close()
	tot, av, err := ParseMeminfo(f)
	if err != nil || tot != 8000000 || av != 400000 {
		t.Fatal(tot, av, err)
	}
	if _, _, err := ParseMeminfo(strings.NewReader("MemTotal: 5 kB\n")); err == nil {
		t.Fatal("want error")
	}
	if _, _, err := ParseMeminfo(strings.NewReader("MemTotal: x kB\nMemAvailable: 1 kB\n")); err == nil {
		t.Fatal("want error")
	}
}

func TestMemRun(t *testing.T) {
	m := &Mem{Path: "testdata/meminfo", MinAvailPct: 5}
	if r := m.Run(context.Background()); r.Status != OK { // 5.0% exactly
		t.Fatal(r)
	}
	m.MinAvailPct = 6
	if r := m.Run(context.Background()); r.Status != FAIL || !strings.Contains(r.Detail, "limit 6%") {
		t.Fatal(r)
	}
	m.Path = "testdata/nope"
	if r := m.Run(context.Background()); r.Status != UNKNOWN {
		t.Fatal(r)
	}
}

func TestParseStat(t *testing.T) {
	for _, tt := range []struct {
		file        string
		busy, total uint64
	}{{"testdata/stat1", 200, 1000}, {"testdata/stat2", 700, 1600}} {
		f, _ := os.Open(tt.file)
		b, tot, err := ParseStat(f)
		f.Close()
		if err != nil || b != tt.busy || tot != tt.total {
			t.Fatalf("%s: %d %d %v", tt.file, b, tot, err)
		}
	}
	if _, _, err := ParseStat(strings.NewReader("intr 1\n")); err == nil {
		t.Fatal("want error")
	}
}

func TestCPUWindow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stat")
	write := func(busy, idle int) {
		s := "cpu " + itoa(busy) + " 0 0 " + itoa(idle) + " 0 0 0 0\n"
		os.WriteFile(p, []byte(s), 0o644)
	}
	now := time.Unix(1000, 0)
	c := &CPU{Path: p, MaxPct: 90, Window: 100 * time.Second, Now: func() time.Time { return now }}
	ctx := context.Background()
	write(0, 0)
	if r := c.Run(ctx); r.Value != "first value after the next round" || r.Status != UNKNOWN {
		t.Fatal(r)
	}
	// 100% busy but window not full -> UNKNOWN
	now = now.Add(50 * time.Second)
	write(500, 0)
	if r := c.Run(ctx); r.Status != UNKNOWN || r.Value != "100% (50s of 1m40s avg)" {
		t.Fatal(r)
	}
	// window full, still saturated -> FAIL
	now = now.Add(50 * time.Second)
	write(1000, 0)
	if r := c.Run(ctx); r.Status != FAIL {
		t.Fatal(r)
	}
	// in hysteresis band 85% -> OK+Hold
	now = now.Add(100 * time.Second)
	write(1000+85, 15)
	if r := c.Run(ctx); r.Status != OK || !r.Hold {
		t.Fatalf("%+v", r)
	}
	// low load
	now = now.Add(100 * time.Second)
	write(1085+10, 15+90)
	if r := c.Run(ctx); r.Status != OK || r.Hold {
		t.Fatalf("%+v", r)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestTemp(t *testing.T) {
	dir := t.TempDir()
	w := func(name, v string) { os.WriteFile(filepath.Join(dir, name), []byte(v), 0o644) }
	w("z0", "50000\n")
	w("z1", "86000\n")
	tmp := &Temp{Glob: filepath.Join(dir, "z*"), MaxC: 85}
	ctx := context.Background()
	if r := tmp.Run(ctx); r.Status != FAIL || r.Value != "86.0 C" {
		t.Fatal(r)
	}
	w("z1", "82000\n")
	if r := tmp.Run(ctx); r.Status != OK || !r.Hold {
		t.Fatal(r)
	}
	w("z1", "70000\n")
	if r := tmp.Run(ctx); r.Status != OK || r.Hold {
		t.Fatal(r)
	}
	tmp = &Temp{Glob: filepath.Join(dir, "none*"), MaxC: 85}
	if _, err := tmp.read(); err == nil {
		t.Fatal("want error")
	}
	tmp = &Temp{Sensor: filepath.Join(dir, "z0"), MaxC: 85}
	if r := tmp.Run(ctx); r.Value != "50.0 C" {
		t.Fatal(r)
	}
}

func TestDisk(t *testing.T) {
	d := &Disk{Path: "/", MinFreePct: 0}
	if r := d.Run(context.Background()); r.Status != OK || !strings.HasSuffix(r.Value, "% free") {
		t.Fatal(r)
	}
	d.MinFreePct = 101
	if r := d.Run(context.Background()); r.Status != FAIL || !strings.Contains(r.Detail, "limit 101%") {
		t.Fatal(r)
	}
	d = &Disk{Path: "/nonexistent-xyz"}
	if r := d.Run(context.Background()); r.Status != FAIL {
		t.Fatal(r)
	}
}

func TestHTTP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello installed")) })
	mux.HandleFunc("/bad", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) })
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/bad", 302) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) { time.Sleep(500 * time.Millisecond) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tests := []struct {
		name, path, contains string
		want                 Status
		detail               string
	}{
		{"ok", "/ok", "", OK, ""},
		{"contains", "/ok", "installed", OK, ""},
		{"missing text", "/ok", "nope", FAIL, "expected text was not found"},
		{"502", "/bad", "", FAIL, "answered with HTTP 502"},
		{"redirect ok", "/redir", "", OK, ""},
		{"timeout", "/slow", "", FAIL, "no answer within 200ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHTTP("x", srv.URL+tt.path, tt.contains, 200*time.Millisecond)
			r := h.Run(context.Background())
			if r.Status != tt.want || !strings.Contains(r.Detail, tt.detail) {
				t.Fatalf("%+v", r)
			}
		})
	}
	h := NewHTTP("x", "http://127.0.0.1:1/secret-path", "", time.Second)
	if r := h.Run(context.Background()); r.Status != FAIL || strings.Contains(r.Detail, "secret-path") {
		t.Fatalf("%+v", r)
	}
}

func TestTCPAndHost(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	ctx := context.Background()
	if r := (&TCP{name: "a", addr: addr, timeout: time.Second}).Run(ctx); r.Status != OK {
		t.Fatal(r)
	}
	ln.Close()
	if r := (&TCP{name: "a", addr: addr, timeout: time.Second}).Run(ctx); r.Status != FAIL {
		t.Fatal(r)
	}
	// refused: tcp = FAIL, host = OK
	r := (&TCP{name: "h", addr: addr, timeout: time.Second, host: true}).Run(ctx)
	if r.Status != OK || !strings.Contains(r.Value, "alive") {
		t.Fatal(r)
	}
	// unreachable (TEST-NET-1) with short timeout: host = FAIL NoAnswer
	r = (&TCP{name: "h", addr: "192.0.2.1:9", timeout: 300 * time.Millisecond, host: true}).Run(ctx)
	if r.Status != FAIL || !r.NoAnswer {
		t.Skipf("no routing to TEST-NET in this sandbox: %+v", r)
	}
}

func TestBuildSkipsSwitchedOffHostChecks(t *testing.T) {
	cfg := config.Defaults()
	count := func() map[string]int {
		m := map[string]int{}
		for _, c := range Build(cfg, func(string, ...any) {}) {
			m[c.Type()]++
		}
		return m
	}
	if m := count(); m["mem"] != 1 || m["cpu"] != 1 || m["disk"] != 1 {
		t.Fatalf("defaults: %v", m)
	}
	cfg.CPUMaxPct, cfg.MemMinAvail = 0, 0
	cfg.DiskPaths = []string{"/", "/data"}
	if m := count(); m["mem"] != 0 || m["cpu"] != 0 || m["disk"] != 2 {
		t.Fatalf("%v", m)
	}
	cfg.DiskMinFree = 0
	if m := count(); m["disk"] != 0 {
		t.Fatalf("%v", m)
	}
	cfg.DiskMinFree, cfg.DiskPaths = 10, nil
	if m := count(); m["disk"] != 0 {
		t.Fatalf("%v", m)
	}
}
