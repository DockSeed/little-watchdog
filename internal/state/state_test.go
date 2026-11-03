// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
)

func TestObserveSanitizesForeignText(t *testing.T) {
	e := NewEngine(1, 1)
	evil := "\x1b[2Jboom\n" + strings.Repeat("y", 400)
	m := e.Observe(fake{"a", "http"}, checks.Result{Status: checks.FAIL, Detail: evil, Value: evil}, time.Now())
	en := e.Snapshot()[0]
	for _, s := range []string{m, en.Value, en.Pending[0]} {
		if strings.ContainsAny(s, "\x1b\n") {
			t.Fatalf("control char in %q", s)
		}
	}
	if len([]rune(en.Value)) > checks.MaxText {
		t.Fatal("value too long")
	}
}

func TestTableAndBriefCleanLoadedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(path, []byte(`{"version":1,"checks":{"a":{"name":"a\u001b[31m","type":"http","reported":"OK","value":"v\u001b[2J\u009b"}}}`), 0o600)
	es, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{Table(es, time.Now()), Brief(es, time.Now())} {
		if strings.ContainsAny(s, "\x1b\u009b") {
			t.Fatalf("%q", s)
		}
	}
}

func TestMessageCarriesLocalTime(t *testing.T) {
	e := NewEngine(1, 1)
	at := time.Date(2026, 11, 2, 14, 7, 0, 0, time.Local)
	m := e.Observe(fake{"a", "http"}, bad, at)
	if !strings.HasPrefix(m, "14:07 ❌ ") {
		t.Fatalf("%q", m)
	}
}

func TestDropKeepsLastMsg(t *testing.T) {
	e := NewEngine(1, 1)
	e.Observe(fake{"a", "http"}, bad, time.Now())
	e.Drop("a", e.Snapshot()[0].Pending[0])
	if len(e.PendingMessages()) != 0 || e.Snapshot()[0].LastMsg != "" {
		t.Fatal("drop must remove without recording as sent")
	}
}

type fake struct{ id, typ string }

func (f fake) ID() string { return f.id }

func (f fake) Name() string { return f.id }

func (f fake) Type() string { return f.typ }

func (f fake) Run(context.Context) checks.Result { return checks.Result{} }

var (
	ok   = checks.Result{Status: checks.OK, Value: "v"}
	bad  = checks.Result{Status: checks.FAIL, Detail: "HTTP 502"}
	noan = checks.Result{Status: checks.FAIL, NoAnswer: true}
	hold = checks.Result{Status: checks.OK, Hold: true}
	unk  = checks.Result{Status: checks.UNKNOWN}
)

func feed(e *Engine, c checks.Check, start time.Time, rs ...checks.Result) []string {
	var msgs []string
	for i, r := range rs {
		if m := e.Observe(c, r, start.Add(time.Duration(i)*time.Minute)); m != "" {
			msgs = append(msgs, m[len("15:04 "):]) // see TestMessageCarriesLocalTime
		}
	}
	return msgs
}

func TestHysteresis(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	tests := []struct {
		name string
		c    checks.Check
		in   []checks.Result
		want []string
	}{
		{"quiet ok", fake{"a", "http"}, []checks.Result{ok, ok, ok}, nil},
		{"2 fails not enough", fake{"a", "http"}, []checks.Result{ok, bad, bad, ok}, nil},
		{"3 fails alarm once", fake{"a", "http"}, []checks.Result{ok, bad, bad, bad, bad, bad}, []string{"❌ a: HTTP 502"}},
		{"recover needs 2", fake{"a", "http"}, []checks.Result{ok, bad, bad, bad, ok, bad, ok}, []string{"❌ a: HTTP 502"}},
		{"recover msg", fake{"a", "http"}, []checks.Result{ok, bad, bad, bad, ok, ok}, []string{"❌ a: HTTP 502", "✅ a: back after 4 min"}},
		{"no answer since", fake{"n", "host"}, []checks.Result{ok, noan, noan, noan}, []string{"❌ n: no answer since 2 min"}},
		{"unknown ignored", fake{"a", "http"}, []checks.Result{ok, unk, unk, unk, bad, unk}, nil},
		{"hold keeps fail", fake{"t", "temp"}, []checks.Result{ok, bad, bad, bad, hold, ok, hold, ok, ok}, []string{"❌ t: HTTP 502", "✅ t: back after 7 min"}},
		{"hold while ok is ok", fake{"t", "temp"}, []checks.Result{ok, hold, hold, bad, hold, bad, bad}, nil},
		{"first obs fail alarms after 3", fake{"a", "http"}, []checks.Result{bad, bad, bad}, []string{"❌ a: HTTP 502"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEngine(3, 2)
			got := feed(e, tt.c, t0, tt.in...)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestRestartKeepsState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "dir")
	path := filepath.Join(dir, "state.json")
	t0 := time.Unix(1_000_000, 0)
	c := fake{"a", "http"}
	e := NewEngine(3, 2)
	feed(e, c, t0, ok, bad, bad) // 2 fails, not yet reported
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}
	dst, _ := os.Stat(dir)
	if dst.Mode().Perm() != 0o750 {
		t.Fatalf("dir mode %v", dst.Mode())
	}
	e2 := NewEngine(3, 2)
	if w := e2.Load(path, map[string]bool{"a": true}); w != "" {
		t.Fatal(w)
	}
	// third fail after restart alarms exactly once
	if got := feed(e2, c, t0.Add(10*time.Minute), bad, bad); len(got) != 1 {
		t.Fatalf("%q", got)
	}
	e2.Save(path)
	// restart while FAIL: no second alarm, recovery reported
	e3 := NewEngine(3, 2)
	e3.Load(path, map[string]bool{"a": true})
	if got := feed(e3, c, t0.Add(20*time.Minute), bad, ok, ok); len(got) != 1 || !strings.HasPrefix(got[0], "✅") {
		t.Fatalf("%q", got)
	}
}

func TestPendingSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	t0 := time.Unix(1_000_000, 0)
	c := fake{"a", "http"}
	e := NewEngine(1, 1)
	feed(e, c, t0, ok, bad)
	e.Save(path)
	e2 := NewEngine(1, 1)
	e2.Load(path, map[string]bool{"a": true})
	ids := e2.PendingMessages()
	if len(ids) != 1 {
		t.Fatal(ids)
	}
	m, _ := e2.Peek("a")
	if !strings.HasSuffix(m, " ❌ a: HTTP 502") {
		t.Fatal(m)
	}
	e2.Delivered("a", m, t0)
	if len(e2.PendingMessages()) != 0 || e2.Snapshot()[0].LastMsg != m {
		t.Fatal("not delivered")
	}
}

func TestLoadPrunesAndSurvivesCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	t0 := time.Unix(1_000_000, 0)
	e := NewEngine(3, 2)
	feed(e, fake{"a", "http"}, t0, ok)
	feed(e, fake{"gone", "http"}, t0, ok)
	e.Save(path)
	e2 := NewEngine(3, 2)
	e2.Load(path, map[string]bool{"a": true})
	if s := e2.Snapshot(); len(s) != 1 || s[0].Name != "a" {
		t.Fatal(s)
	}
	os.WriteFile(path, []byte("{not json"), 0o600)
	e3 := NewEngine(3, 2)
	if w := e3.Load(path, map[string]bool{"a": true}); !strings.Contains(w, "corrupt") || len(e3.Snapshot()) != 0 {
		t.Fatal(w)
	}
	if w := e3.Load(filepath.Join(t.TempDir(), "missing"), nil); w != "" {
		t.Fatal(w)
	}
}

func TestTable(t *testing.T) {
	e := NewEngine(3, 2)
	now := time.Unix(1_000_000, 0)
	feed(e, fake{"a", "http"}, now, ok)
	out := Table(e.Snapshot(), now.Add(5*time.Minute))
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "5 min ago") || !strings.Contains(out, "OK") {
		t.Fatal(out)
	}
	if b := Brief(e.Snapshot(), now); !strings.Contains(b, "✅ a: OK (v)") {
		t.Fatal(b)
	}
}

func TestDeliveredRemovesTheMessageThatWasSentNotTheFirstOne(t *testing.T) {
	e := NewEngine(1, 1)
	c := fake{"a", "http"}
	t0 := time.Unix(1_000_000, 0)
	e.Observe(c, bad, t0)
	q := e.Queue()
	if len(q) != 1 {
		t.Fatal(q)
	}
	sent := q[0].Text
	// while it is being sent, the check flaps so often that the full queue drops its front
	for i := 0; i < 2*maxPending; i++ {
		r := ok
		if i%2 == 0 {
			r = bad
		}
		e.Observe(c, r, t0.Add(time.Duration(i+1)*time.Minute))
	}
	before := e.Queue()
	e.Delivered("a", sent, t0)
	e.Drop("a", sent)
	after := e.Queue()
	if len(after) != len(before) {
		t.Fatalf("a message that was not the one sent was removed: %d -> %d", len(before), len(after))
	}
	e.Delivered("a", after[0].Text, t0)
	if got := e.Queue(); len(got) != len(after)-1 || (len(got) > 0 && got[0].Text == after[0].Text) {
		t.Fatal("the delivered message is still queued")
	}
}
