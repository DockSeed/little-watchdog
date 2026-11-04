// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/state"
)

// panelMin is the terminal width from which the Status screen gets the side panel.
const panelMin = 120

// sysEvery is how often the panel re-reads /proc: the cadence of loadavg, so an idle
// screen is redrawn at most this often. A var for the tests.
var sysEvery = 5 * time.Second

// panelSplit divides the inner width iw into the status table and the panel (pw 0 = no panel).
// The separator takes the one column between them.
func panelSplit(w, iw int) (tw, pw int) {
	if w < panelMin {
		return iw, 0
	}
	pw = min(max(iw*2/5, 44), 64)
	return iw - pw - 1, pw
}

// drawStatusPage is the Status screen: the table, and on a wide terminal the side panel.
func (m *model) drawStatusPage(fr *frame, now time.Time) string {
	tw, pw := panelSplit(fr.w, fr.iw)
	if _, ents := m.statusRows(); pw == 0 || m.snap.cfg == nil || len(ents) == 0 { // nothing to show beside it
		return m.drawStatus(fr, now)
	}
	tf := *fr
	tf.iw = tw
	hint := m.drawStatus(&tf, now)
	sx := fr.ix + tw
	for y := 0; y < fr.ih; y++ {
		fr.cv.text(sx, fr.iy+y, fr.th.g.v, sBox)
	}
	pf := *fr
	pf.ix, pf.iw = sx+1, pw
	m.drawPanel(&pf, now)
	return hint
}

// prow is one panel line: runs of text in their styles.
type prow []seg

// drawPanel draws the check under the cursor at the top and this machine at the bottom.
func (m *model) drawPanel(fr *frame, now time.Time) {
	w := fr.iw - 1 // one cell of air after the separator
	sys := m.machineBlock(now, w)
	top := m.checkBlock(now, w, 3)
	if len(top)+1+len(sys) > fr.ih { // low terminal: target and result on one line first
		top = m.checkBlock(now, w, 1)
	}
	if len(top)+1+len(sys) > fr.ih { // still too low: the machine block goes as a whole, never half
		top, sys = m.checkBlock(now, w, 3), nil
	}
	for i, r := range top {
		fr.prow(i, r)
	}
	y0 := fr.ih - len(sys)
	for i, r := range sys {
		fr.prow(y0+i, r)
	}
}

func (fr *frame) prow(y int, r prow) {
	x := 1
	for _, s := range r {
		if x >= fr.iw {
			return
		}
		x = fr.put(y, x, s.s, s.st) - fr.ix
	}
}

// labRows puts label l before the first of the lines and indents the rest.
func labRows(l string, lw int, lines []string, st sty) (rs []prow) {
	for i, p := range lines {
		if i > 0 {
			l = ""
		}
		rs = append(rs, prow{{fmt.Sprintf("%-*s", lw, l), sRowDim}, {p, st}})
	}
	return
}

// wrapAll keeps s if it fits, else breaks it at spaces, and words longer than w (URLs, paths) inside; at most n lines.
func wrapAll(s string, w, n int) []string {
	if len([]rune(s)) <= w {
		return []string{s} // fits: keep its spacing
	}
	var out []string
	for _, l := range wrap(s, w) {
		out = append(out, hardWrap(l, w)...)
	}
	if len(out) > n {
		out = out[:n]
		out[n-1] = fit(out[n-1]+"~", w)
	}
	return out
}

// checkBlock is the panel's top: everything the state file knows about the check under the cursor.
// Target and result take at most n lines.
func (m *model) checkBlock(now time.Time, w, n int) (rs []prow) {
	_, ents := m.statusRows()
	e := ents[min(m.scur, len(ents)-1)]
	const lw = 10
	vw := max(w-lw, 1)
	rs = append(rs, prow{{e.Name, sCat}})
	rs = append(rs, labRows("Type", lw, []string{e.Type}, sBg)...)
	if s, ok := m.target(e); ok && s.Target != "" {
		rs = append(rs, labRows("Target", lw, wrapAll(shortTarget(s.Target), vw, min(n, 2)), sBg)...)
	}
	st := m.sinceText(e, now)
	if e.Reported == checks.FAIL {
		since := e.DownSince
		if since.IsZero() {
			since = e.Since
		}
		st = fmt.Sprintf("FAIL since %s (%s)", clock(since, now), m.tries(e))
	}
	srs := labRows("State", lw, wrapAll(st, vw, 3), sBg)
	if rep := string(e.Reported); len(srs) > 0 && strings.HasPrefix(srs[0][1].s, rep) {
		ms := map[checks.Status]sty{checks.OK: sOK, checks.FAIL: sFail}[e.Reported]
		if ms == sBg {
			ms = sRowDim
		}
		srs[0] = prow{srs[0][0], {rep, ms}, {strings.TrimPrefix(srs[0][1].s, rep), sBg}}
	}
	rs = append(rs, srs...)
	value, vs := e.Value, sBg
	if unstable(e) && e.Detail != "" {
		value = e.Detail
	}
	if e.NoValue {
		value, vs = e.Value, sRowDim
	}
	if value == "" {
		value = "-"
	}
	rs = append(rs, labRows("Result", lw, wrapAll(value, vw, n), vs)...)
	checked := "not yet"
	if !e.LastRun.IsZero() {
		checked = clock(e.LastRun, now)
	}
	rs = append(rs, labRows("Checked", lw, []string{checked}, sBg)...)
	tg := "no message yet"
	switch c := m.snap.cfg; {
	case c.Token == "" || c.ChatID == 0:
		tg = "not set up"
	case !e.LastMsgAt.IsZero():
		tg = "last message " + clock(e.LastMsgAt, now)
	}
	rs = append(rs, labRows("Telegram", lw, []string{tg}, sBg)...)
	if e.Reported == checks.FAIL && e.OKs > 0 {
		rs = append(rs, prow{{fmt.Sprintf("%-*s", lw, "Recovery"), sRowDim},
			{backText(e, m.snap.cfg.RecoverAfter), sWarn}, {" good tries after the FAIL", sBg}})
	}
	if e.NoValue {
		rs = append(rs, labRows("Verdict", lw, wrapAll("none yet (warm-up or read error)", vw, 2), sRowDim)...)
	}
	return rs
}

// sysInfo is what the panel read from /proc; each part carries its own error text.
type sysInfo struct {
	at      time.Time
	load    [3]string
	loadErr string
	psi     [3][3]string // cpu, memory, io: avg10, avg60, avg300
	psiErr  [3]string
	noPSI   string // why there is no pressure, "" = there is
	up      time.Duration
	upErr   string
}

var psiNames = [3]string{"cpu", "memory", "io"}

func (m *model) procRoot() string {
	if m.a.Proc != "" {
		return m.a.Proc
	}
	return "/proc"
}

// readSys re-reads /proc when the last read is older than sysEvery.
func (m *model) readSys(now time.Time) *sysInfo {
	if d := now.Sub(m.sys.at); !m.sys.at.IsZero() && d >= 0 && d < sysEvery {
		return &m.sys
	}
	if m.sysBuf == nil {
		m.sysBuf = make([]byte, 1024) // the files read here are far smaller
	}
	root, buf := m.procRoot(), m.sysBuf
	s := sysInfo{at: now}
	if b, err := readProc(filepath.Join(root, "loadavg"), buf); err != nil {
		s.loadErr = procErr(err)
	} else if f := bytes.Fields(b); len(f) < 3 {
		s.loadErr = "unexpected format"
	} else {
		s.load = [3]string{string(f[0]), string(f[1]), string(f[2])}
	}
	if b, err := readProc(filepath.Join(root, "uptime"), buf); err != nil {
		s.upErr = procErr(err)
	} else if f := bytes.Fields(b); len(f) < 1 {
		s.upErr = "unexpected format"
	} else if v, err := strconv.ParseFloat(string(f[0]), 64); err != nil || v < 0 {
		s.upErr = "unexpected format"
	} else {
		s.up = time.Duration(v * float64(time.Second))
	}
	for i, n := range psiNames {
		b, err := readProc(filepath.Join(root, "pressure", n), buf)
		if os.IsNotExist(err) || errors.Is(err, syscall.EOPNOTSUPP) {
			s.noPSI = "no PSI (kernel or psi=0)"
			if s.loadErr == "not found" { // not even loadavg: a container or a restricted /proc
				s.noPSI = "/proc limited"
			}
			break
		}
		if err != nil {
			s.psiErr[i] = procErr(err)
			continue
		}
		s.psi[i], s.psiErr[i] = parsePSI(b)
	}
	m.sys = s
	return &m.sys
}

// readProc reads at most len(buf) bytes of a /proc file into buf.
func readProc(path string, buf []byte) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, err := io.ReadFull(f, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return buf[:n], err
}

// parsePSI takes avg10/avg60/avg300 from the "some" line of a pressure file.
func parsePSI(b []byte) (v [3]string, errText string) {
	for _, l := range bytes.Split(b, []byte("\n")) {
		f := bytes.Fields(l)
		if len(f) < 4 || string(f[0]) != "some" {
			continue
		}
		for i, k := range []string{"avg10=", "avg60=", "avg300="} {
			if !bytes.HasPrefix(f[i+1], []byte(k)) {
				return v, "unexpected format"
			}
			v[i] = string(f[i+1][len(k):]) + "%"
		}
		return v, ""
	}
	return v, "unexpected format"
}

func procErr(err error) string {
	switch {
	case os.IsNotExist(err):
		return "not found"
	case os.IsPermission(err):
		return "no permission"
	}
	return "not readable"
}

// machineBlock is the panel's bottom: load, pressure and uptime from /proc, little-wd's own cost.
func (m *model) machineBlock(now time.Time, w int) (rs []prow) {
	s := m.readSys(now)
	const lw = 16
	vw := max(w-lw, 1)
	row := func(l, v string, st sty) { rs = append(rs, labRows(l, lw, wrapAll(v, vw, 2), st)...) }
	head := "Right now"
	if m.host != "" {
		head = m.host + " right now"
	}
	rs = append(rs, prow{{head, sCat}})
	if s.loadErr != "" {
		row("load 1/5/15 min", s.loadErr, sWarn)
	} else {
		row("load 1/5/15 min", fmt.Sprintf("%s %s %s on %s", s.load[0], s.load[1], s.load[2], plural(runtime.NumCPU(), "CPU", "CPUs")), sBg)
	}
	if s.noPSI != "" {
		row("Stalled (PSI)", s.noPSI, sRowDim)
	} else {
		row("Stalled (PSI)", fmt.Sprintf("%-7s%-7s%s", "10s", "60s", "5m"), sRowDim)
		for i, l := range [3]string{"  CPU", "  RAM", "  disk"} {
			if s.psiErr[i] != "" {
				row(l, s.psiErr[i], sWarn)
				continue
			}
			row(l, fmt.Sprintf("%-7s%-7s%s", s.psi[i][0], s.psi[i][1], s.psi[i][2]), sBg)
		}
	}
	if s.upErr != "" {
		row("up since boot", s.upErr, sWarn)
	} else {
		row("up since boot", upText2(s.up), sBg)
	}
	switch f := m.snap.foot; {
	case f == nil:
		row("little-wd", "no footprint in the state file yet", sRowDim)
	case m.snap.stale:
		row("little-wd", "daemon not running", sFail)
	default:
		row("little-wd", ramText(f)+" RAM, "+writesText(f, "write", "writes"), sBg)
	}
	return rs
}

// ramText is the daemon's resident memory, at least 1 MB.
func ramText(f *state.Footprint) string { return fmt.Sprintf("%d MB", max((f.RSSKB+512)/1024, 1)) }

// writesText is how often the daemon writes its state file ("6.0 writes/h").
func writesText(f *state.Footprint, one, many string) string {
	up := f.At.Sub(f.Started)
	if up < 10*time.Minute {
		return plural(f.Writes, one, many) + " since start"
	}
	r := float64(f.Writes) / up.Hours()
	if r < 10 {
		return fmt.Sprintf("%.1f %s/h", r, many)
	}
	return fmt.Sprintf("%.0f %s/h", r, many)
}

// upText2 is a duration in its two largest units: "2 d 23 h", "5 h 12 min", "12 min".
func upText2(d time.Duration) string {
	mins := int(d / time.Minute)
	switch {
	case mins < 60:
		return fmt.Sprintf("%d min", mins)
	case mins < 24*60:
		return fmt.Sprintf("%d h %d min", mins/60, mins%60)
	}
	return fmt.Sprintf("%d d %d h", mins/(24*60), mins/60%24)
}

// shortTarget hides a URL's user info and query in the always-visible panel; Enter shows all.
func shortTarget(t string) string {
	u, err := url.Parse(t)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User == nil && u.RawQuery == "" {
		return t
	}
	s := u.Scheme + "://"
	if u.User != nil {
		s += "***@"
	}
	s += u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		s += "?..."
	}
	return s
}
