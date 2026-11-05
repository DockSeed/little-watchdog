// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
)

var t0 = time.Unix(1_700_000_400, 0) // on a bucket edge: 1_700_000_400 / 600 is whole

func took(ms int) checks.Result {
	return checks.Result{Status: checks.OK, Took: time.Duration(ms) * time.Millisecond}
}

func lvl(v float64) checks.Result {
	return checks.Result{Status: checks.OK, Level: v, HasLevel: true}
}

func saveRead(t *testing.T, e *Engine, now time.Time) Snap {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.json")
	if err := e.save(p, now); err != nil {
		t.Fatal(err)
	}
	s, err := ReadAll(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHistBucketsAndMean(t *testing.T) {
	e := NewEngine(2, 1)
	c := fake{"a", "http"}
	e.PSI = nil
	e.Observe(c, took(10), t0)
	e.Observe(c, took(30), t0.Add(time.Minute))
	e.Observe(c, bad, t0.Add(2*time.Minute))
	e.Observe(c, took(50), t0.Add(11*time.Minute)) // next bucket
	s := saveRead(t, e, t0.Add(12*time.Minute))
	cells := s.Hist.Check("a", t0.Add(12*time.Minute))
	if len(cells) != histBuckets {
		t.Fatal(len(cells))
	}
	if got := cells[histBuckets-2]; got != (CheckCell{OK: 2, Fail: 1, Ms: 20}) {
		t.Fatalf("first bucket %+v", got)
	}
	if got := cells[histBuckets-1]; got != (CheckCell{OK: 1, Ms: 50}) {
		t.Fatalf("open bucket %+v", got)
	}
	if got := cells[0]; got != (CheckCell{}) {
		t.Fatalf("empty %+v", got)
	}
}

func TestHistUnknownLeavesNothing(t *testing.T) {
	e := NewEngine(2, 1)
	e.Observe(fake{"a", "http"}, checks.Result{Status: checks.UNKNOWN}, t0)
	e.Observe(fake{"cpu", "cpu"}, checks.Result{Status: checks.UNKNOWN, Value: "warm-up"}, t0)
	if e.hist.encode() != nil {
		t.Fatal("history from nothing")
	}
}

func TestHistHostMeanPeakAndMissing(t *testing.T) {
	e := NewEngine(2, 1)
	cpu := fake{"sys:cpu", "cpu"}
	e.Observe(cpu, lvl(10), t0)
	e.Observe(cpu, lvl(30), t0.Add(time.Minute))
	e.PSI = func() (float64, bool) { return 2.5, true }
	e.SamplePSI(t0)
	s := saveRead(t, e, t0)
	c := s.Hist.Host(HostCPU, t0)
	if g := c[histBuckets-1]; !g.Has || g.Mean != 20 || g.Peak != 30 {
		t.Fatalf("%+v", g)
	}
	if g := s.Hist.Host(HostPSI, t0)[histBuckets-1]; g.Mean != 2.5 || g.Peak != 2.5 {
		t.Fatalf("%+v", g)
	}
	if s.Hist.Host(HostTemp, t0) != nil { // no sensor: no series, no zeros
		t.Fatal("temperature without a sensor")
	}
}

func TestHistNoPSI(t *testing.T) {
	e := NewEngine(2, 1)
	e.PSI = func() (float64, bool) { return 0, false }
	e.SamplePSI(t0)
	if e.hist.encode() != nil {
		t.Fatal("PSI recorded without PSI")
	}
	if _, ok := readPSI(t.TempDir()); ok {
		t.Fatal("readPSI found files in an empty dir")
	}
}

func TestReadPSI(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "cpu"), []byte("some avg10=1.25 avg60=0.50 avg300=0.10 total=1\nfull avg10=9.00 avg60=0 avg300=0 total=0\n"), 0o600)
	os.WriteFile(filepath.Join(d, "io"), []byte("some avg10=4.00 avg60=2.00 avg300=1.00 total=9\n"), 0o600)
	if v, ok := readPSI(d); !ok || v != 4 {
		t.Fatal(v, ok)
	}
}

func TestHistRolloverAndCap(t *testing.T) {
	e := NewEngine(2, 1)
	c := fake{"a", "http"}
	for i := 0; i < histBuckets+30; i++ { // 24 h and a bit
		e.Observe(c, took(5), t0.Add(time.Duration(i)*histStep))
	}
	if n := len(e.hist.checks["a"]); n != histBuckets {
		t.Fatalf("%d cells", n)
	}
	// a gap of three buckets
	e.Observe(c, took(5), t0.Add(time.Duration(histBuckets+33)*histStep))
	cs := e.hist.checks["a"]
	if len(cs) != histBuckets || cs[histBuckets-2].ok != 0 || cs[histBuckets-4].ok != 0 || cs[histBuckets-5].ok != 1 {
		t.Fatal("gap not kept as empty cells")
	}
	// longer than 24 h away: everything old is gone
	e.Observe(c, took(5), t0.Add(time.Duration(3*histBuckets)*histStep))
	if cs := e.hist.checks["a"]; len(cs) != 1 {
		t.Fatalf("%d cells after a long gap", len(cs))
	}
}

func TestHistClockBack(t *testing.T) {
	e := NewEngine(2, 1)
	c := fake{"a", "http"}
	e.Observe(c, took(5), t0)
	e.Observe(c, took(5), t0.Add(-2*histStep)) // small step back: folded in
	if cs := e.hist.checks["a"]; len(cs) != 1 || cs[0].ok != 2 {
		t.Fatalf("%+v", cs)
	}
	e.Observe(c, took(5), t0.Add(-100*histStep)) // far back: the old cells stood for another time
	if cs := e.hist.checks["a"]; len(cs) != 1 || cs[0].ok != 1 {
		t.Fatalf("%+v", cs)
	}
}

func TestHistSizeCap(t *testing.T) {
	e := NewEngine(2, 1)
	valid := map[string]bool{}
	for b := 0; b < histBuckets; b++ {
		at := t0.Add(time.Duration(b) * histStep)
		for i := 0; i < 20; i++ {
			id := fmt.Sprintf("http_service_%d", i)
			valid[id] = true
			e.Observe(fake{id, "http"}, took(12+b), at)
		}
		for _, n := range []string{"cpu", "mem", "disk", "temp"} {
			e.Observe(fake{"sys:" + n, n}, lvl(float64(b%100)), at)
		}
	}
	e.PSI = func() (float64, bool) { return 1, true }
	e.SamplePSI(t0.Add((histBuckets - 1) * histStep))
	b, _ := json.Marshal(e.hist.encode())
	t.Logf("20 checks, 24 h, host: %d bytes", len(b))
	if len(b) > 16<<10 || len(b) < 10000 {
		t.Fatalf("%d bytes", len(b))
	}
	// 200 checks: the oldest buckets go, the size stays
	e = NewEngine(2, 1)
	for b := 0; b < histBuckets; b++ {
		for i := 0; i < 200; i++ {
			e.Observe(fake{fmt.Sprintf("check_%03d", i), "tcp"}, took(3), t0.Add(time.Duration(b)*histStep))
		}
	}
	h := e.hist.encode()
	b, _ = json.Marshal(h)
	if len(b) > 16<<10 || len(h.Checks["check_000"]) == 0 || len(h.Checks["check_000"]) >= 4*histBuckets {
		t.Fatalf("%d bytes, %d chars", len(b), len(h.Checks["check_000"]))
	}
}

func TestHistSurvivesRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	e := NewEngine(2, 1)
	c := fake{"a", "http"}
	e.Observe(c, took(10), t0)
	e.Observe(c, took(30), t0.Add(time.Minute))
	e.Observe(c, bad, t0.Add(2*time.Minute))
	if err := e.save(p, t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(2, 1)
	if w := e2.Load(p, map[string]bool{"a": true}); w != "" {
		t.Fatal(w)
	}
	e2.Observe(c, took(50), t0.Add(4*time.Minute)) // same bucket continues
	cs := e2.hist.checks["a"]
	if len(cs) != 1 || cs[0].ok != 3 || cs[0].fail != 1 || cs[0].msSum != 90 {
		t.Fatalf("%+v", cs)
	}
	e3 := NewEngine(2, 1) // the check is gone from the config
	e3.Load(p, map[string]bool{})
	if len(e3.hist.checks) != 0 {
		t.Fatal("history of a removed check kept")
	}
}

// oldFile is the state file of 0.4.0: it has no history field.
type oldFile struct {
	Version int               `json:"version"`
	Checks  map[string]*Entry `json:"checks"`
	Foot    *Footprint        `json:"footprint,omitempty"`
}

func TestHistOldReaderIgnoresIt(t *testing.T) {
	e := NewEngine(2, 1)
	e.Observe(fake{"a", "http"}, took(10), t0)
	e.Observe(fake{"sys:cpu", "cpu"}, lvl(5), t0)
	p := filepath.Join(t.TempDir(), "s.json")
	if err := e.save(p, t0); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"history"`) {
		t.Fatal("no history in the file")
	}
	var o oldFile
	if err := json.Unmarshal(b, &o); err != nil || o.Checks["a"] == nil || o.Foot == nil {
		t.Fatalf("0.4.0 cannot read it: %v", err)
	}
	// the other way: a file without history (0.4.0) starts clean
	nb, _ := json.Marshal(o)
	os.WriteFile(p, nb, 0o600)
	e2 := NewEngine(2, 1)
	if w := e2.Load(p, map[string]bool{"a": true}); w != "" || len(e2.hist.checks) != 0 {
		t.Fatal(w)
	}
}

func TestHistCorruptNeverStops(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	for _, h := range []string{
		`"history":{"step":600,"end":2833333,"checks":{"a":"0000zz"},"host":{"cpu":"!!!!"}}`,
		`"history":{"step":7,"end":1}`,
		`"history":{"step":600,"end":-5,"checks":{"a":"0000"}}`,
		`"history":"nonsense"`,
		`"history":{"step":600,"end":2833333,"checks":{"a":"A0 0"}}`,
	} {
		js := `{"version":2,"checks":{"a":{"name":"a","type":"http","reported":"OK"}},` + h + `}`
		os.WriteFile(p, []byte(js), 0o600)
		e := NewEngine(2, 1)
		w := e.Load(p, map[string]bool{"a": true})
		if strings.Contains(h, "nonsense") { // a wrong type is a corrupt file as before: starts empty with a warning
			if w == "" {
				t.Fatal("no warning")
			}
			continue
		}
		if w != "" || e.entries["a"] == nil || len(e.hist.checks["a"]) != 0 && !strings.Contains(h, `"0000"`) {
			t.Fatalf("%s: %q %v", h, w, e.hist.checks)
		}
		if _, err := ReadAll(p); err != nil {
			t.Fatal(err)
		}
		e.Observe(fake{"a", "http"}, took(1), t0) // and it records on
		if err := e.save(p, t0); err != nil {
			t.Fatal(err)
		}
	}
}
