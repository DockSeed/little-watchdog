// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/state"
)

// fineSizes are the cell counts of the 30 minute curve: 1, 2, 3 or 5 minutes per cell.
var fineSizes = [...]int{30, 15, 10, 6}

// readFine loads the fine history when its file changed. The file sits next to the state file the
// screen reads. It has its own stat: it changes with the state writes, but not only the ones the
// state file's own stat sees. No file is no error; in RAM mode with no runtime directory (the
// installed unit has no RuntimeDirectory=) fineHint says so, once.
func (m *model) readFine(s *snapshot) {
	if s.cfg == nil {
		s.fine, s.fineHint = nil, false
		return
	}
	p := config.RAMStateFile() + state.FineSuffix // the daemon's place; next to the state file where it cannot write there
	if _, err := os.Stat(p); err != nil {
		p = statePath(s.cfg) + state.FineSuffix
	}
	sig := statSig(p)
	if sig == m.fineSig && sig != (stateSig{}) {
		return
	}
	m.fineSig = sig
	s.fine, s.fineHint = nil, false
	if f, err := state.ReadFine(p); err == nil {
		s.fine = f
	} else if os.IsNotExist(err) {
		s.fineHint = s.cfg.StateInRAM && m.fineMissing(p)
	}
}

// fineMissing: the directory of the fine history file does not exist.
func (m *model) fineMissing(p string) bool {
	_, err := os.Stat(filepath.Dir(p))
	return err != nil
}

// fineCurve draws the response time of the last 30 minutes: n cells, the slowest answer of each
// on 0..max; a cell with a failed try is a red "!"; no measurement, no mark. max is the slowest answer.
func fineCurve(ps []state.FinePoint, n int, now time.Time) (rs []seg, max int) {
	cell := state.FineSpan / time.Duration(n)
	ms := make([]int, n)
	has, bad := make([]bool, n), make([]bool, n)
	for _, p := range ps {
		i := int(p.At.Sub(now.Add(-state.FineSpan)) / cell)
		if i < 0 || i >= n {
			continue
		}
		if p.Fail {
			bad[i] = true
			continue
		}
		has[i], ms[i] = true, slices.Max([]int{ms[i], p.Ms})
		max = slices.Max([]int{max, p.Ms})
	}
	var run []rune
	red := false
	flush := func() {
		if len(run) > 0 {
			st := sBg
			if red {
				st = sFail
			}
			rs = append(rs, seg{string(run), st})
			run = nil
		}
	}
	for i := 0; i < n; i++ {
		if bad[i] != red {
			flush()
			red = bad[i]
		}
		switch {
		case bad[i]:
			run = append(run, '!')
		case has[i]:
			run = append(run, level(float64(ms[i]), 0, float64(max)))
		default:
			run = append(run, ' ')
		}
	}
	flush()
	return rs, max
}

// kindWord says a failure kind in plain words.
func kindWord(k checks.Kind) string {
	switch k {
	case checks.KindTimeout:
		return "no answer"
	case checks.KindDNS:
		return "name not found"
	case checks.KindTLS:
		return "certificate"
	case checks.KindHTTP4xx:
		return "HTTP 4xx"
	case checks.KindHTTP5xx:
		return "HTTP 5xx"
	case checks.KindRefused, checks.KindBlocked, checks.KindUnreachable:
		return string(k)
	}
	return "error"
}

// median of ms values of ps (0 for none).
func median(ps []state.FinePoint) int {
	v := make([]int, len(ps))
	for i, p := range ps {
		v[i] = p.Ms
	}
	slices.Sort(v)
	if len(v) == 0 {
		return 0
	}
	return v[len(v)/2]
}

// fineWhy says in one sentence what the measurements show before the last failure in ps: a rise in
// response time (it began when the answers stayed above twice the usual, at least 50 ms more), earlier
// failures, or nothing unusual. "" when ps holds no failure.
func fineWhy(ps []state.FinePoint, now time.Time) string {
	last := -1
	for i, p := range ps {
		if p.Fail {
			last = i
		}
	}
	if last < 0 {
		return ""
	}
	first := last // the failing run the last failure belongs to
	for first > 0 && ps[first-1].Fail {
		first--
	}
	head := fmt.Sprintf("Before the first failed try at %s (%s): ", clock(ps[first].At, now), kindWord(ps[first].Kind))
	var ok []state.FinePoint
	earlier := 0
	var firstBad time.Time
	for _, p := range ps[:first] {
		if p.Fail {
			if earlier++; earlier == 1 {
				firstBad = p.At
			}
			continue
		}
		ok = append(ok, p)
	}
	if len(ok) < 4 {
		return head + "too few measurements before it, the history starts at " + clock(ps[0].At, now) + "."
	}
	usual := median(ok[:len(ok)/2])
	limit := max(2*usual, usual+50)
	run := 0
	for i := len(ok) - 1; i >= 0 && ok[i].Ms > limit; i-- {
		run++
	}
	switch {
	case run >= 2:
		top := median(ok[len(ok)-run:])
		return head + fmt.Sprintf("response time rose from %s, from about %d ms to %s.", clock(ok[len(ok)-run].At, now), usual, msText(top))
	case earlier > 0:
		return head + fmt.Sprintf("%s failed earlier, first at %s, answers in between.", plural(earlier, "try", "tries"), clock(firstBad, now))
	}
	return head + fmt.Sprintf("nothing unusual, it answered in about %d ms until then.", median(ok))
}

// fineRows is the fine history of a service check: the curve of the last 30 minutes (when the file
// has two or more measurements of it) and what came before the last failure; or the one hint
// that there is no fine history. lw is the label width, w the room. nil for other checks.
func (m *model) fineRows(e state.Entry, now time.Time, lw, w int) (rs []prow) {
	if !isServiceType(e.Type) {
		return nil
	}
	// the file is written with the state, at rest every 10 minutes: the curve ends where it ends
	at := now
	if end := m.snap.fine.End(); !end.IsZero() && end.Before(now) {
		at = end
	}
	ps := m.snap.fine.Check(e.ID, at)
	if len(ps) < 2 {
		if m.snap.fine == nil && m.snap.fineHint {
			rs = labRows("30 min", lw, wrapAll("no fine history: the runtime directory is missing, update the package", w-lw, 2), sRowDim)
		}
		return rs
	}
	n := 0
	for _, c := range fineSizes {
		if c <= w-lw-1-len(" max over 4 s, as of 12:34") {
			n = c
			break
		}
	}
	if n == 0 {
		return nil
	}
	segs, top := fineCurve(ps, n, at)
	row := prow{{fmt.Sprintf("%-*s", lw, "30 min"), sRowDim}}
	row = append(row, segs...)
	tail := " max " + msText(top)
	if now.Sub(at) >= time.Minute {
		tail += ", as of " + clock(at, now)
	}
	rs = append(rs, append(row, seg{tail, sRowDim}))
	if why := fineWhy(ps, at); why != "" {
		for _, l := range wrapAll(why, w-lw, 3) { // the longest reason needs three lines at 120 columns
			rs = append(rs, prow{{fmt.Sprintf("%-*s", lw, ""), sBg}, {l, sBg}})
		}
	}
	return rs
}

func isServiceType(t string) bool { return t == "http" || t == "tcp" || t == "host" || t == "ping" }

// msText is a response time from the fine history; the file caps it, so the cap reads as "over".
func msText(ms int) string {
	if ms >= state.FineMaxMs {
		return "over 4 s"
	}
	return fmt.Sprintf("%d ms", ms)
}
