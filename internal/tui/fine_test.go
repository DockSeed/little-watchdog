// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/state"
)

type fakeCheck struct{ id, typ string }

func (f fakeCheck) ID() string                        { return f.id }
func (f fakeCheck) Name() string                      { return f.id }
func (f fakeCheck) Type() string                      { return f.typ }
func (f fakeCheck) Run(context.Context) checks.Result { return checks.Result{} }

// putFine writes the fine history of "http:photos" next to the state file of cfg: 22 ms until
// 12 minutes ago, then slow, and four failed tries from 4 minutes ago.
func putFine(t *testing.T, cfg string) {
	t.Helper()
	e := state.NewEngine(3, 1)
	c := fakeCheck{"http:photos", "http"}
	now := time.Now()
	for m := 29; m >= 1; m-- {
		r := checks.Result{Status: checks.OK, Took: 22 * time.Millisecond}
		switch {
		case m <= 4:
			r = checks.Result{Status: checks.FAIL, Took: 5 * time.Second, Help: checks.Help(checks.KindTimeout)}
		case m <= 12:
			r.Took = time.Duration(300+20*(12-m)) * time.Millisecond
		}
		e.Observe(c, r, now.Add(-time.Duration(m)*time.Minute))
	}
	tmp := filepath.Join(t.TempDir(), "s.json")
	if err := e.Save(tmp); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(tmp + state.FineSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgValue(t, cfg, "STATE_FILE")+state.FineSuffix, b, 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestDetailsNoFine80(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.send("\rjjjjjj\r")
	r.wait("[ Close ]")
	f := r.frame()
	check80(t, f)
	lacks(t, f, "30 min", "Before the first failed try")
	r.send("\r")
	r.wait("↑↓ move  Enter details")
	r.send("\x03")
	r.finish()
}

func TestDetailsFine80(t *testing.T) {
	cfg := fixture(t)
	putFine(t, cfg)
	r := start(t, &App{Path: cfg, Size: sized})
	r.send("\rjjjjjj\r")
	r.wait("[ Close ]")
	f := r.frame()
	rs := check80(t, f)
	has(t, f, "30 min", "!!!!", "max 440 ms", "Before the first failed try at", "FAIL since")
	bottom := 0
	for i, l := range rs {
		if strings.Contains(l, "╰") {
			bottom = i
		}
	}
	if bottom == 0 || bottom > 21 {
		t.Fatalf("dialog bottom %d:\n%s", bottom, f)
	}
	has(t, r.raw(), "1;38;5;203;48;5;236m!!!!") // the failures are red
	t.Log("\n" + strings.ReplaceAll(f, "\r\n", "\n"))
	r.send("\r")
	r.wait("↑↓ move  Enter details")
	r.send("\x03")
	r.finish()
}

func TestDetailsFineWide(t *testing.T) {
	for _, sz := range [][2]int{{120, 30}, {160, 40}} {
		cfg := fixture(t)
		putFine(t, cfg)
		r := start(t, &App{Path: cfg, Size: func() (int, int) { return sz[0], sz[1] }})
		r.send("\rjjjjjj")
		r.wait("30 min") // the side panel shows the selected check
		r.send("\r")
		r.wait("[ Close ]")
		has(t, r.frame(), "30 min", "Before the first failed try at")
		for i, l := range rows(r.frame()) {
			if n := len([]rune(l)); n != sz[0] {
				t.Fatalf("%d wide: row %d has %d cells", sz[0], i, n)
			}
		}
		r.send("\r")
		r.wait("↑↓ move  Enter details")
		r.send("\x03")
		r.finish()
	}
}

func fp(now time.Time, m int, ms int, fail bool, k checks.Kind) state.FinePoint {
	return state.FinePoint{At: now.Add(-time.Duration(m) * time.Minute), Ms: ms, Fail: fail, Kind: k}
}

func TestFineWhy(t *testing.T) {
	now := time.Date(2026, 11, 5, 23, 0, 0, 0, time.Local)
	var rise, flap, calm []state.FinePoint
	for m := 20; m > 3; m-- {
		ms := 22
		if m <= 9 {
			ms = 400
		}
		rise = append(rise, fp(now, m, ms, false, ""))
		calm = append(calm, fp(now, m, 25, false, ""))
		flap = append(flap, fp(now, m, 25, m == 15 || m == 12, checks.KindTimeout))
	}
	fail := fp(now, 2, 5000, true, checks.KindTimeout)
	for _, c := range []struct {
		ps   []state.FinePoint
		want string
	}{
		{append(rise, fail), "Before the first failed try at 22:58 (no answer): response time rose from 22:51, from about 22 ms to 400 ms."},
		{append(calm, fail), "Before the first failed try at 22:58 (no answer): nothing unusual, it answered in about 25 ms until then."},
		{append(flap, fail), "Before the first failed try at 22:58 (no answer): 2 tries failed earlier, first at 22:45, answers in between."},
		{[]state.FinePoint{fp(now, 5, 20, false, ""), fail}, "Before the first failed try at 22:58 (no answer): too few measurements before it, the history starts at 22:55."},
		{calm, ""},
	} {
		if got := fineWhy(c.ps, now); got != c.want {
			t.Errorf("\n got %q\nwant %q", got, c.want)
		}
	}
}

// The file is written with the state, at rest every 10 minutes: the curve ends with the file
// and says when that was, instead of a blank right end that reads as missing data.
func TestFineCurveEndsWithTheFile(t *testing.T) {
	e := state.NewEngine(3, 1)
	c := fakeCheck{"http:web", "http"}
	now := time.Now()
	for m := 30; m >= 0; m-- {
		e.Observe(c, checks.Result{Status: checks.OK, Took: 20 * time.Millisecond}, now.Add(-time.Duration(m)*time.Minute))
	}
	tmp := filepath.Join(t.TempDir(), "s.json")
	if err := e.Save(tmp); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(tmp + state.FineSuffix)
	if err != nil {
		t.Fatal(err)
	}
	// the same file, written 9 minutes ago: points are relative to its end, so all move with it
	head, rest, _ := strings.Cut(string(b), "\n")
	var end int64
	fmt.Sscanf(head, "lwfine 1 %d", &end)
	f, err := state.ParseFine([]byte(fmt.Sprintf("lwfine 1 %d\n%s", end-540, rest)))
	if err != nil {
		t.Fatal(err)
	}
	m := &model{}
	m.snap.fine = f
	rs := m.fineRows(state.Entry{ID: "http:web", Type: "http"}, time.Unix(end, 0), 10, 70)
	if len(rs) == 0 {
		t.Fatal("no rows")
	}
	var line string
	for _, sg := range rs[0] {
		line += sg.s
	}
	if !strings.Contains(line, "as of "+time.Unix(end-540, 0).Format("15:04")) {
		t.Fatalf("no as-of time: %q", line)
	}
	if strings.Count(line, "█") < 29 { // flat answers fill every cell up to the file's end
		t.Fatalf("the curve does not reach the file's end: %q", line)
	}
}

func TestMsTextCap(t *testing.T) {
	if got := msText(state.FineMaxMs); got != "over 4 s" {
		t.Fatal(got)
	}
	if got := msText(380); got != "380 ms" {
		t.Fatal(got)
	}
}
