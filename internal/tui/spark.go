// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/DockSeed/little-watchdog/internal/state"
)

// The history has 144 cells of 10 minutes. A curve shows them as 48, 36, 24, 18, 16, 12, 9, 8 or 6
// cells (30 min down to 4 h each), the widest that fits, so no cell mixes half a bucket.
const (
	histCells = 144
	sparkMax  = 48
	sparkMin  = 6
)

var sparkSizes = [...]int{48, 36, 24, 18, 16, 12, 9, 8, 6}

const bars = "▁▂▃▄▅▆▇█"

// sparkWidth is the number of cells for room columns, 0 when even the narrowest does not fit.
func sparkWidth(room int) int {
	for _, n := range sparkSizes {
		if n <= room {
			return n
		}
	}
	return 0
}

// per says what one cell of an n cell curve stands for: "30 min", "1 h", "2 h".
func per(n int) string {
	d := time.Duration(histCells/n) * 10 * time.Minute
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	if d%time.Hour != 0 {
		return fmt.Sprintf("%.1f h", d.Hours())
	}
	return fmt.Sprintf("%d h", int(d/time.Hour))
}

// level is the bar of v on lo..hi; a value on the floor still draws the lowest bar.
func level(v, lo, hi float64) rune {
	r := []rune(bars)
	if hi <= lo {
		return r[0]
	}
	i := int(math.Floor((v - lo) / (hi - lo) * float64(len(r))))
	return r[min(max(i, 0), len(r)-1)]
}

// upCurve draws how often the check answered: a full bar when every run of the cell was OK,
// lower with every failure; a cell with a failure is red; no run, no bar. share is OK over all runs.
func upCurve(cs []state.CheckCell, n int) (rs []seg, share float64, any bool) {
	k := len(cs) / n
	var okAll, all int
	var run strings.Builder
	red := false
	flush := func() {
		if run.Len() > 0 {
			st := sBg
			if red {
				st = sFail
			}
			rs = append(rs, seg{run.String(), st})
			run.Reset()
		}
	}
	for i := 0; i < n; i++ {
		var ok, fail int
		for _, c := range cs[i*k : (i+1)*k] {
			ok, fail = ok+c.OK, fail+c.Fail
		}
		okAll, all = okAll+ok, all+ok+fail
		if ok+fail == 0 {
			flush()
			rs = append(rs, seg{" ", sBg})
			continue
		}
		bad := fail > 0
		if bad != red {
			flush()
			red = bad
		}
		r := level(float64(ok)/float64(ok+fail), 0, 1)
		if bad && r == '█' {
			r = '▇'
		}
		run.WriteRune(r)
	}
	flush()
	if all > 0 {
		share = float64(okAll) / float64(all)
	}
	return rs, share, all > 0
}

// timeCurve draws the mean answer time per cell on 0..max; avg is the mean over all answers.
func timeCurve(cs []state.CheckCell, n int) (s string, avg int, any bool) {
	k := len(cs) / n
	means := make([]float64, n)
	has := make([]bool, n)
	var sum, cnt int
	var hi float64
	for i := 0; i < n; i++ {
		var ms, ok int
		for _, c := range cs[i*k : (i+1)*k] {
			ms, ok = ms+c.Ms*c.OK, ok+c.OK
		}
		if ok > 0 {
			means[i], has[i] = float64(ms)/float64(ok), true
			sum, cnt = sum+ms, cnt+ok
			hi = math.Max(hi, means[i])
		}
	}
	if cnt == 0 {
		return "", 0, false
	}
	var sb strings.Builder
	for i := range means {
		if has[i] {
			sb.WriteRune(level(means[i], 0, hi))
		} else {
			sb.WriteByte(' ')
		}
	}
	return sb.String(), (sum + cnt/2) / cnt, true
}

// hostCurve draws the peak per cell on lo..hi (the scale grows to the highest peak when that is above hi);
// top is the highest peak. Without any value it returns false.
func hostCurve(cs []state.HostCell, n int, lo, hi float64) (s string, top float64, any bool) {
	k := len(cs) / n
	peaks := make([]float64, n)
	has := make([]bool, n)
	for i := 0; i < n; i++ {
		for _, c := range cs[i*k : (i+1)*k] {
			if c.Has && (!has[i] || c.Peak > peaks[i]) {
				peaks[i], has[i] = c.Peak, true
			}
		}
		if has[i] {
			top, any = math.Max(top, peaks[i]), true
		}
	}
	if !any {
		return "", 0, false
	}
	hi = math.Max(hi, top)
	var sb strings.Builder
	for i := range peaks {
		if has[i] {
			sb.WriteRune(level(peaks[i], lo, hi))
		} else {
			sb.WriteByte(' ')
		}
	}
	return sb.String(), top, true
}

// hostRows describes the five host curves: label, series, scale and the unit of the peak text.
var hostRows = []struct {
	label, name string
	lo, hi      float64
	unit        string
}{
	{"  CPU", state.HostCPU, 0, 100, "%"},
	{"  RAM", state.HostRAM, 0, 100, "%"},
	{"  disk", state.HostDisk, 0, 100, "%"},
	{"  temp", state.HostTemp, 20, 80, " C"},
	{"  waited", state.HostPSI, 0, 10, "%"}, // a floor of 10 %: a quiet machine stays flat
}

func peakText(v float64, unit string) string {
	if v >= 10 || unit == " C" {
		return fmt.Sprintf("peak %.0f%s", v, unit)
	}
	return fmt.Sprintf("peak %.1f%s", v, unit)
}

// historyBlock is the host curves under "right now": a head and one row per value that was measured.
// nil when there is no history or no room for even the narrowest curve in w columns.
func (m *model) historyBlock(now time.Time, w int) (rs []prow) {
	h := m.snap.hist
	const lw = 9
	n := sparkWidth(w - lw - 1 - len("peak 100%"))
	if h == nil || n == 0 {
		return nil
	}
	for _, r := range hostRows {
		cs := h.Host(r.name, now)
		if cs == nil {
			continue
		}
		s, top, ok := hostCurve(cs, n, r.lo, r.hi)
		if !ok {
			continue
		}
		rs = append(rs, prow{{fmt.Sprintf("%-*s", lw, r.label), sRowDim}, {s, sBg}, {" " + peakText(top, r.unit), sRowDim}})
	}
	if len(rs) == 0 {
		return nil
	}
	return append([]prow{{{"Last 24 h, peak per " + per(n), sRowDim}}}, rs...)
}

// checkHistory is the curves of the check under the cursor: how often it answered and how fast,
// or for a system check the matching host curve. Label width lw is the panel's.
func (m *model) checkHistory(e state.Entry, now time.Time, lw, w int) (rs []prow) {
	h := m.snap.hist
	if h == nil {
		return nil
	}
	if name := map[string]string{"cpu": state.HostCPU, "mem": state.HostRAM, "disk": state.HostDisk, "temp": state.HostTemp}[e.Type]; name != "" {
		cs := h.Host(name, now)
		n := sparkWidth(w - lw - 1 - len("peak 100%"))
		if cs == nil || n == 0 {
			return nil
		}
		for _, r := range hostRows {
			if r.name == name {
				if s, top, ok := hostCurve(cs, n, r.lo, r.hi); ok {
					return []prow{{{fmt.Sprintf("%-*s", lw, "24 h"), sRowDim}, {s, sBg}, {" " + peakText(top, r.unit), sRowDim}}}
				}
			}
		}
		return nil
	}
	cs := h.Check(e.ID, now)
	n := sparkWidth(w - lw - 1 - len("avg 1234 ms"))
	if cs == nil || n == 0 {
		return nil
	}
	if segs, share, ok := upCurve(cs, n); ok {
		pct := fmt.Sprintf("%.1f%%", math.Floor(share*1000)/10) // never 100.0% with a failure in it
		if share == 1 {
			pct = "100%"
		}
		rs = append(rs, append(prow{{fmt.Sprintf("%-*s", lw, "Up 24 h"), sRowDim}}, append(segs, seg{" " + pct, sRowDim})...))
	}
	if s, avg, ok := timeCurve(cs, n); ok {
		rs = append(rs, prow{{fmt.Sprintf("%-*s", lw, "Time 24 h"), sRowDim}, {s, sBg}, {fmt.Sprintf(" avg %d ms", avg), sRowDim}})
	}
	return rs
}
