// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package state

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/DockSeed/little-watchdog/internal/testfake"
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
	for _, s := range []string{Table(es, 2, time.Time{}, time.Now()), Brief("h", es, 2, time.Time{}, time.Now())} {
		if strings.ContainsAny(s, "\x1b\u009b") {
			t.Fatalf("%q", s)
		}
	}
}

func TestMessageHasNoClockPrefixButKeepsEventTime(t *testing.T) {
	e := NewEngine(1, 1)
	at := time.Date(2026, 11, 2, 14, 7, 0, 0, time.Local)
	m := e.Observe(fake{"a", "http"}, bad, at)
	if !strings.HasPrefix(m, "❌ <b>a</b> is down since 14:07") {
		t.Fatalf("%q", m)
	}
	if q := e.Queue(); len(q) != 1 || !q[0].At.Equal(at) {
		t.Fatalf("the queue must carry the event time: %+v", q)
	}
}

func TestPendingTimeSurvivesRestartAndOldFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	e := NewEngine(1, 1)
	at := time.Date(2026, 11, 2, 14, 7, 0, 0, time.Local)
	e.Observe(fake{"a", "http"}, bad, at)
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(1, 1)
	e2.Load(path, map[string]bool{"a": true})
	if q := e2.Queue(); len(q) != 1 || !q[0].At.Equal(at) {
		t.Fatalf("%+v", q)
	}
	// a file without pending_at (older version) still loads; the time is unknown
	old := `{"version":1,"checks":{"a":{"name":"a","type":"http","reported":"FAIL","pending":["x","y"]}}}`
	os.WriteFile(path, []byte(old), 0o600)
	e3 := NewEngine(1, 1)
	e3.Load(path, map[string]bool{"a": true})
	q := e3.Queue()
	if len(q) != 2 || !q[0].At.IsZero() {
		t.Fatalf("%+v", q)
	}
	e3.Delivered("a", "x", time.Now())
	if q = e3.Queue(); len(q) != 1 || q[0].Text != "y" {
		t.Fatalf("%+v", q)
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
			msgs = append(msgs, m)
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
		{"3 fails alarm once", fake{"a", "http"}, []checks.Result{ok, bad, bad, bad, bad, bad}, []string{"❌ <b>a</b> is down since {1}: HTTP 502."}},
		{"recover needs 2", fake{"a", "http"}, []checks.Result{ok, bad, bad, bad, ok, bad, ok}, []string{"❌ <b>a</b> is down since {1}: HTTP 502."}},
		{"recover msg", fake{"a", "http"}, []checks.Result{ok, bad, bad, bad, ok, ok}, []string{"❌ <b>a</b> is down since {1}: HTTP 502.", "✅ <b>a</b> is back after 4 min."}},
		{"no answer since", fake{"n", "host"}, []checks.Result{ok, noan, noan, noan}, []string{"❌ <b>n</b> is not answering since {1}."}},
		{"unknown ignored", fake{"a", "http"}, []checks.Result{ok, unk, unk, unk, bad, unk}, nil},
		{"hold keeps fail", fake{"t", "temp"}, []checks.Result{ok, bad, bad, bad, hold, ok, hold, ok, ok}, []string{"❌ <b>t</b> has a problem since {1}: HTTP 502.", "✅ <b>t</b> is back after 7 min."}},
		{"hold while ok is ok", fake{"t", "temp"}, []checks.Result{ok, hold, hold, bad, hold, bad, bad}, nil},
		{"first obs fail alarms after 3", fake{"a", "http"}, []checks.Result{bad, bad, bad}, []string{"❌ <b>a</b> is down since {0}: HTTP 502."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEngine(3, 2)
			got := feed(e, tt.c, t0, tt.in...)
			want := make([]string, len(tt.want))
			for i, w := range tt.want {
				w = strings.ReplaceAll(w, "{0}", t0.Local().Format("15:04"))
				want[i] = strings.ReplaceAll(w, "{1}", t0.Add(time.Minute).Local().Format("15:04"))
			}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("got %q want %q", got, want)
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
	if got := feed(e3, c, t0.Add(20*time.Minute), bad, ok, ok); len(got) != 1 || !strings.HasPrefix(got[0], "✅ <b>a</b> is back after ") {
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
	if m != "❌ <b>a</b> is down since "+t0.Add(time.Minute).Local().Format("15:04")+": HTTP 502." {
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
	out := Table(e.Snapshot(), 2, time.Time{}, now.Add(5*time.Minute))
	if !strings.Contains(out, "NAME") || !strings.Contains(out, When(now, now)) || !strings.Contains(out, "OK") {
		t.Fatal(out)
	}
	if b := Brief("h", e.Snapshot(), 2, time.Time{}, now); !strings.Contains(b, "<b>Services</b>\n✅ <b>a</b>: v") {
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

func TestHostInMessagesAndBrief(t *testing.T) {
	e := NewEngine(1, 1)
	e.Host = "box"
	at := time.Date(2026, 11, 2, 14, 7, 0, 0, time.Local)
	if m := e.Observe(fake{"a", "http"}, bad, at); m != "❌ <b>a</b> on <b>box</b> is down since 14:07: HTTP 502." {
		t.Fatal(m)
	}
	b := Brief("box", e.Snapshot(), 2, time.Time{}, at)
	if want := "<b>box: 1 of 1 check failing.</b>\n<b>Services</b>\n❌ <b>a</b>: down since 14:07"; b != want {
		t.Fatalf("%q", b)
	}
	if b := Brief("", nil, 2, time.Time{}, at); b != "<b>No checks are set up.</b>" {
		t.Fatalf("%q", b)
	}
	if m := e.Observe(fake{"a", "http"}, ok, at.Add(5*time.Minute)); m != "✅ <b>a</b> on <b>box</b> is back after 5 min." {
		t.Fatal(m)
	}
}

func TestBriefUnknownCheckAndPrefix(t *testing.T) {
	es := []Entry{{Name: "a", Reported: checks.UNKNOWN}}
	now := time.Now()
	if got, want := Brief("box", es, 2, time.Time{}, now), "<b>box: 1 check, none failing.</b>\n<b>Services</b>\n- <b>a</b>: not known yet"; got != want {
		t.Fatalf("%q", got)
	}
	if got, want := Brief("", es, 2, time.Time{}, now), "<b>1 check, none failing.</b>\n<b>Services</b>\n- <b>a</b>: not known yet"; got != want {
		t.Fatalf("%q", got)
	}
}

func TestNoteSendTransitionsAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	e := NewEngine(1, 1)
	t0 := time.Unix(1_000_000, 0)
	e.NoteSend(nil, t0)
	if !e.dirty {
		t.Fatal("first success must be written")
	}
	e.save(path, t0)
	e.NoteSend(nil, t0.Add(time.Minute))
	if e.dirty {
		t.Fatal("a plain success must not mark dirty")
	}
	e.NoteSend(errors.New("dial\x1b[2J failed"), t0.Add(2*time.Minute))
	e.NoteSend(errors.New("again"), t0.Add(3*time.Minute))
	if !e.dirty {
		t.Fatal("ok to error must be written")
	}
	e.save(path, t0)
	st, err := ReadAll(path)
	tg := st.Telegram
	if err != nil || len(st.Entries) != 0 || tg == nil {
		t.Fatal(st, err)
	}
	if tg.Err != "again" || !tg.ErrSince.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("%+v", tg)
	}
	e.NoteSend(nil, t0.Add(4*time.Minute))
	if !e.dirty {
		t.Fatal("error to ok must be written")
	}
	// survives a restart; an old reader (checks only) still parses the file
	e.save(path, t0)
	e2 := NewEngine(1, 1)
	e2.Load(path, nil)
	if e2.tg.Err != "" || !e2.tg.LastOKAt.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("%+v", e2.tg)
	}
	b, _ := os.ReadFile(path)
	var old struct {
		Version int
		Checks  map[string]*Entry
	}
	if json.Unmarshal(b, &old) != nil || old.Version != fileVersion {
		t.Fatal(string(b))
	}
}

func TestNoteDeadmanTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	e := NewEngine(1, 1)
	t0 := time.Unix(1_000_000, 0)
	if !e.NoteDeadman("https://h/…abcd", nil, t0) || !e.dirty {
		t.Fatal("first success of a new URL must be reported and written")
	}
	e.save(path, t0)
	if e.NoteDeadman("https://h/…abcd", nil, t0.Add(time.Minute)) || e.dirty {
		t.Fatal("a plain success is neither first nor dirty")
	}
	e.NoteDeadman("https://h/…abcd", errors.New("HTTP 500"), t0.Add(2*time.Minute))
	e.NoteDeadman("https://h/…abcd", errors.New("HTTP 502"), t0.Add(3*time.Minute))
	if !e.dirty || e.dm.Err != "HTTP 502" || !e.dm.ErrSince.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("%+v", e.dm)
	}
	e.save(path, t0)
	e.NoteDeadman("https://h/…abcd", errors.New("HTTP 503"), t0.Add(4*time.Minute))
	if e.dirty {
		t.Fatal("a growing failure streak is not a transition")
	}
	if e.NoteDeadman("https://h/…abcd", nil, t0.Add(5*time.Minute)) || !e.dirty || e.dm.Err != "" {
		t.Fatal("error to ok is written but not first")
	}
	e.save(path, t0)
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"url_fp":"https://h/…abcd"`) {
		t.Fatal(string(b))
	}
	st, err := ReadAll(path)
	if err != nil || st.Deadman == nil || st.Deadman.LastOKAt.IsZero() {
		t.Fatal(st, err)
	}
	// restart with the same URL: not first; a changed URL: first again
	e2 := NewEngine(1, 1)
	e2.Load(path, nil)
	if e2.NoteDeadman("https://h/…abcd", nil, t0.Add(time.Hour)) {
		t.Fatal("restart announced again")
	}
	if !e2.NoteDeadman("https://h/…ffff", nil, t0.Add(time.Hour)) {
		t.Fatal("changed URL not announced")
	}
	e2.ClearDeadman()
	e2.save(path, t0)
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "deadman") {
		t.Fatal(string(b))
	}
}

func TestDownSinceIsTheFirstFailure(t *testing.T) {
	e := NewEngine(3, 2)
	e.Host = "homelab"
	c := fake{"photos", "http"}
	day := time.Date(2026, 11, 3, 17, 28, 0, 0, time.Local)
	at := func(m int) time.Time { return day.Add(time.Duration(m) * time.Minute) }
	e.Observe(c, ok, at(-1))
	e.Observe(c, bad, at(0))
	e.Observe(c, bad, at(1))
	m := e.Observe(c, checks.Result{Status: checks.FAIL, Detail: "no answer within 10 s"}, at(3))
	if want := "❌ <b>photos</b> on <b>homelab</b> is down since 17:28: no answer within 10 s."; m != want {
		t.Fatalf("%q", m)
	}
	if got := e.Snapshot()[0].Detail; got != "no answer within 10 s" {
		t.Fatalf("detail %q", got)
	}
	e.Observe(c, ok, at(20))
	if m := e.Observe(c, ok, at(21)); m != "✅ <b>photos</b> on <b>homelab</b> is back after 21 min." {
		t.Fatalf("%q", m)
	}
	if en := e.Snapshot()[0]; en.Detail != "" || !en.DownSince.IsZero() {
		t.Fatalf("%+v", en)
	}
	// another day: the date is named
	e.Observe(c, bad, day.Add(-30*time.Hour))
	e.Observe(c, bad, day.Add(-29*time.Hour))
	m = e.Observe(c, bad, day)
	if !strings.Contains(m, "is down since Nov 2 ") {
		t.Fatalf("%q", m)
	}
}

func TestStreakEventsMarkDirty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	e := NewEngine(3, 2)
	c := fake{"a", "http"}
	t0 := time.Unix(1_000_000, 0)
	var n int
	step := func(i int, r checks.Result) {
		at := t0.Add(time.Duration(i) * time.Minute)
		e.Observe(c, r, at)
		before := e.Writes()
		if err := e.SaveIfDue(path, at); err != nil {
			t.Fatal(err)
		}
		n += e.Writes() - before
	}
	step(0, ok) // new entry
	n = 0
	step(1, ok)
	if n != 0 {
		t.Fatal("steady OK must not write")
	}
	step(2, bad) // series begins: written at once
	if n != 1 {
		t.Fatalf("series start must write, writes %d", n)
	}
	step(3, bad) // 1 to 2, 1 min later: coalesced
	if n != 1 {
		t.Fatalf("a growing streak must not write every round, writes %d", n)
	}
	step(8, bad) // alarm: the reported state changes, always at once
	if n != 2 || len(e.PendingMessages()) != 1 {
		t.Fatalf("alarm must write, writes %d", n)
	}
	step(9, bad)
	step(10, bad)
	if n != 2 {
		t.Fatalf("steady FAIL must not write, writes %d", n)
	}
	// the counters the TUI shows reach the file after the next allowed write
	step(14, ok) // 6 min after the last counter write: goes out
	if es, _ := ReadFile(path); es[0].OKs != 1 {
		t.Fatalf("%+v", es)
	}
	step(30, ok) // 5+ min after the last write: recovery progress goes out
	if n < 3 {
		t.Fatalf("recovery start must write in the end, writes %d", n)
	}
}

func TestFlappingCheckWritesFarLessThanOncePerRound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	e := NewEngine(3, 2) // flaps below both thresholds: never alarms
	c := fake{"a", "http"}
	t0 := time.Unix(1_000_000, 0)
	rounds := 24 * 60 // one day at INTERVAL=60s
	for i := 0; i < rounds; i++ {
		r := ok
		if i%2 == 0 {
			r = bad
		}
		at := t0.Add(time.Duration(i) * time.Minute)
		e.Observe(c, r, at)
		if err := e.SaveIfDue(path, at); err != nil {
			t.Fatal(err)
		}
	}
	if w := e.Writes(); w > 24*60/5+2 || w < 24*60/10 {
		t.Fatalf("a flapping check wrote %d times in %d rounds, want at most one per 5 min", w, rounds)
	}
	if len(e.PendingMessages()) != 0 {
		t.Fatal("intermediate state must not message")
	}
}

func TestSteadyStateWritesOnlyTheRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	e := NewEngine(3, 2)
	c := fake{"a", "http"}
	t0 := time.Unix(1_000_000, 0)
	for i := 0; i < 24*60; i++ {
		at := t0.Add(time.Duration(i) * time.Minute)
		e.Observe(c, ok, at)
		e.SaveIfDue(path, at)
	}
	if w := e.Writes(); w > 24*6+1 {
		t.Fatalf("steady state wrote %d times a day, want at most one per 10 min", w)
	}
}

func TestStateFileCarriesFootprintAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	e := NewEngine(3, 2)
	t0 := time.Unix(1_000_000, 0)
	e.Started = t0
	e.Proc = func() (int64, float64) { return 6144, 12.5 }
	e.NoteUpdates(ConflictPoller, t0)
	if err := e.save(path, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	st, err := ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	f := st.Foot
	if f == nil || f.RSSKB != 6144 || f.CPUSec != 12.5 || f.Writes != 1 || f.At.Sub(f.Started) != time.Hour {
		t.Fatalf("%+v", f)
	}
	if st.Updates == nil || st.Updates.Conflict != ConflictPoller {
		t.Fatalf("%+v", st.Updates)
	}
	// the footprint rides along: it never causes a write by itself
	if err := e.SaveIfDue(path, t0.Add(time.Hour+time.Minute)); err != nil || e.Writes() != 1 {
		t.Fatalf("writes %d", e.Writes())
	}
	// a restart keeps the conflict (no second notice), a fixed bot clears it
	e2 := NewEngine(3, 2)
	e2.Load(path, nil)
	if e2.Conflict() != ConflictPoller {
		t.Fatal("conflict lost")
	}
	e2.NoteUpdates("", t0)
	if e2.Conflict() != "" || !e2.dirty {
		t.Fatal("a fixed conflict must be written")
	}
}

func TestReadProc(t *testing.T) {
	dir := t.TempDir()
	st, stat := filepath.Join(dir, "status"), filepath.Join(dir, "stat")
	os.WriteFile(st, []byte("Name:\tx\nVmRSS:\t   6144 kB\n"), 0o600)
	os.WriteFile(stat, []byte("42 (a b) c) S 1 2 3 4 5 6 7 8 9 10 250 50 0 0 20\n"), 0o600)
	rss, cpu := readProc(st, stat)
	if rss != 6144 || cpu != 3 {
		t.Fatalf("rss %d cpu %v", rss, cpu)
	}
	if rss, _ := ReadProc(); rss <= 0 {
		t.Fatal("own RSS must be readable")
	}
}

func TestReadAllSanitizesDetailAndLastMsg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(path, []byte(`{"version":1,"checks":{"a":{"name":"a","type":"http","reported":"FAIL","detail":"x\u001b[2Jy","last_msg":"z\u009bq"}}}`), 0o600)
	es, err := ReadFile(path)
	if err != nil || strings.ContainsAny(es[0].Detail+es[0].LastMsg, "\x1b\u009b") {
		t.Fatalf("%v %+v", err, es)
	}
}

func TestBriefGroupsMachineBeforeServices(t *testing.T) {
	es := []Entry{
		{Name: "web", Type: "http", Reported: checks.OK, Value: "200 OK"},
		{Name: "memory", Type: "mem", Reported: checks.OK, Value: "70% available"},
		{Name: "cpu", Type: "cpu", Reported: checks.OK, Value: "5% (15m avg)"},
		{Name: "box", Type: "host", Reported: checks.FAIL, DownSince: time.Date(2026, 11, 3, 14, 5, 0, 0, time.Local), Value: "timeout"},
		{Name: "temperature", Type: "temp", Reported: checks.UNKNOWN},
	}
	now := time.Date(2026, 11, 3, 15, 0, 0, 0, time.Local)
	want := "<b>homelab: 1 of 5 checks failing.</b>\n<b>This machine</b>\n✅ <b>cpu</b>: 5% (15m avg)\n✅ <b>memory</b>: 70% available\n- <b>temperature</b>: not known yet\n<b>Services</b>\n✅ <b>web</b>: 200 OK\n❌ <b>box</b>: timeout, down since 14:05"
	if got := Brief("homelab", es, 2, time.Time{}, now); got != want {
		t.Fatalf("\n%s\nwant\n%s", got, want)
	}
}

func TestBriefServicesFollowServiceTypes(t *testing.T) {
	es := []Entry{
		{Name: "n", Type: "host", Reported: checks.OK},
		{Name: "p", Type: "tcp", Reported: checks.OK},
		{Name: "w", Type: "http", Reported: checks.OK},
		{Name: "q", Type: "http", Reported: checks.OK},
	}
	got := Brief("h", es, 2, time.Time{}, time.Now())
	i := func(s string) int { return strings.Index(got, "✅ <b>"+s+"</b>") }
	if !(i("w") < i("q") && i("q") < i("p") && i("p") < i("n")) {
		t.Fatalf("order:\n%s", got)
	}
}

func TestHostileNamesAndDetailsRenderValidHTML(t *testing.T) {
	hostile := "<b>x</b> &amp; </code>\x1b[2J<"
	e := NewEngine(1, 1)
	e.Host = checks.Clean(hostile) // the app sets the sanitised host name
	t0 := time.Date(2026, 11, 3, 17, 0, 0, 0, time.Local)
	for _, typ := range []string{"http", "cpu"} {
		c := fake{hostile + typ, typ}
		msgs := []string{
			e.Observe(c, checks.Result{Status: checks.FAIL, Detail: hostile}, t0),
			e.Observe(c, checks.Result{Status: checks.OK}, t0.Add(time.Minute)),
			e.Observe(c, checks.Result{Status: checks.FAIL, NoAnswer: true}, t0.Add(2*time.Minute)),
		}
		for _, m := range msgs {
			if m == "" {
				t.Fatal("no message")
			}
			if err := testfake.CheckHTML(m); err != nil {
				t.Fatalf("%v: %q", err, m)
			}
			if strings.ContainsAny(m, "\x1b") || strings.Contains(m, "<b>x") || strings.Contains(m, "</code>") {
				t.Fatalf("hostile text leaks: %q", m)
			}
		}
	}
	e.Observe(fake{"u", "tcp"}, checks.Result{Status: checks.OK, Value: hostile}, t0)
	b := Brief(hostile, e.Snapshot(), 2, time.Time{}, t0)
	if err := testfake.CheckHTML(b); err != nil || strings.Contains(b, "</code>") {
		t.Fatalf("%v: %q", err, b)
	}
}

func TestLegacyPlainTextFileIsConvertedToHTMLOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	legacy := `{"checks":{"a":{"name":"a","type":"http","reported":"FAIL","last_msg":"old <b> & co","pending":["❌ a <x> & b > c is down","plain"]}}}`
	for _, file := range []string{legacy, `{"version":1,` + legacy[1:]} { // no version, and version 1
		os.WriteFile(path, []byte(file), 0o600)
		e := NewEngine(1, 1)
		e.Load(path, map[string]bool{"a": true})
		q := e.Queue()
		if len(q) != 2 || q[0].Text != "❌ a &lt;x&gt; &amp; b &gt; c is down" || q[1].Text != "plain" {
			t.Fatalf("%+v", q)
		}
		for _, m := range q {
			if err := testfake.CheckHTML(m.Text); err != nil {
				t.Fatalf("%v: %q", err, m.Text)
			}
		}
		if got := e.Snapshot()[0].LastMsg; got != "old &lt;b&gt; &amp; co" {
			t.Fatalf("%q", got)
		}
		// saved as version 2, and a second load leaves the text alone
		if err := e.Save(path); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(path)
		var f struct{ Version int }
		if json.Unmarshal(b, &f); f.Version != 2 {
			t.Fatalf("version %d", f.Version)
		}
		e2 := NewEngine(1, 1)
		e2.Load(path, map[string]bool{"a": true})
		if q2 := e2.Queue(); q2[0].Text != q[0].Text || e2.Snapshot()[0].LastMsg != "old &lt;b&gt; &amp; co" {
			t.Fatalf("converted twice: %+v", q2)
		}
	}
}

func TestBriefWhileRecovering(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 19, 56, 0, 0, time.Local)
	refused := checks.Result{Status: checks.FAIL, Value: "connection refused", Detail: "connection refused"}
	good := checks.Result{Status: checks.OK, Value: "HTTP 200 in 3ms"}
	line := func(e *Engine, i int) string {
		t.Helper()
		got := lastLine(Brief("h", e.Snapshot(), e.RecoverN(), time.Time{}, t0.Add(time.Duration(i)*time.Minute)))
		t.Log(got)
		return got
	}
	nas := fake{"nas", "http"}

	e := NewEngine(1, 2)
	feed(e, nas, t0, refused)
	if got, want := line(e, 0), "❌ <b>nas</b>: connection refused, down since 19:56"; got != want {
		t.Fatalf("still failing: got %q, want %q", got, want)
	}
	e.Observe(nas, good, t0.Add(time.Minute))
	if got, want := line(e, 1), "❌ <b>nas</b>: answers again, all-clear after 1 more good try. Down since 19:56."; got != want {
		t.Fatalf("recovering: got %q, want %q", got, want)
	}
	if out := Table(e.Snapshot(), e.RecoverN(), time.Time{}, t0.Add(time.Minute)); !strings.Contains(out, "HTTP 200 in 3ms (answers again, all-clear after 1 more good try)") {
		t.Fatalf("table: %s", out)
	}
	// a result without a verdict keeps its own wording, not the hint
	e.Observe(nas, checks.Result{Status: checks.UNKNOWN, Value: "first value in 1 min"}, t0.Add(2*time.Minute))
	if got, want := line(e, 2), "❌ <b>nas</b>: down since 19:56, first value in 1 min"; got != want {
		t.Fatalf("no verdict: got %q, want %q", got, want)
	}
	e.Observe(nas, good, t0.Add(3*time.Minute))
	if got, want := line(e, 3), "✅ <b>nas</b>: HTTP 200 in 3ms"; got != want {
		t.Fatalf("recovered: got %q, want %q", got, want)
	}

	// failing again after one good try: the hint goes and the file is written at once
	e = NewEngine(1, 2)
	feed(e, nas, t0, refused, good)
	e.dirty = false
	e.Observe(nas, refused, t0.Add(2*time.Minute))
	if got, want := line(e, 2), "❌ <b>nas</b>: connection refused, down since 19:56"; got != want || !e.cdirty {
		t.Fatalf("broken recovery: got %q, want %q, cdirty %v", got, want, e.cdirty)
	}
	// a reload lowers RECOVER_AFTER below the good tries so far
	e = NewEngine(1, 3)
	feed(e, nas, t0, refused, good, good)
	e.Configure(1, 2)
	if got, want := line(e, 2), "❌ <b>nas</b>: answers again, all-clear on the next good try. Down since 19:56."; got != want {
		t.Fatalf("lowered: got %q, want %q", got, want)
	}

	// a machine check is ok again, not answering
	e = NewEngine(1, 3)
	feed(e, fake{"disk", "disk"}, t0, bad, ok)
	if got, want := line(e, 1), "❌ <b>disk</b>: ok again, all-clear after 2 more good tries. Down since 19:56."; got != want {
		t.Fatalf("machine: got %q, want %q", got, want)
	}

	// RECOVER_AFTER=1: the first good result recovers, there is no in-between
	e = NewEngine(1, 1)
	feed(e, nas, t0, refused, good)
	if got, want := line(e, 1), "✅ <b>nas</b>: HTTP 200 in 3ms"; got != want {
		t.Fatalf("recover after 1: got %q, want %q", got, want)
	}
	// without a known RECOVER_AFTER there is no hint
	if got, _ := Recovering(Entry{Reported: checks.FAIL, OKs: 1, Type: "http"}, 0); got != "" {
		t.Fatalf("no threshold: %q", got)
	}
}
