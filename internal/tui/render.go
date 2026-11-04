// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/state"
)

// snapshot is the config and state as last read; it feeds the header, menu and status.
type snapshot struct {
	cfg     *config.Config
	cfgErr  string
	es      []state.Entry
	esErr   string
	ok, bad int
	failed  []string
	shaky   []string // failing, but not reported yet (below FAIL_AFTER)
	lastMsg time.Time
	rows    []srow
	ents    []state.Entry
	stale   bool // the daemon has not beaten for two intervals
	age     time.Duration
	tg      state.Telegram // how the daemon's sends went
	dm      state.Deadman  // how the dead-man pings went
	linked  time.Time      // the setup's test message
	pairing bool           // setup polls the bot right now
	pending int            // messages waiting for Telegram
	up      state.Updates  // getUpdates conflict, if any
	foot    *state.Footprint
}

// errStale is how long an old send error stays red when nothing is waiting to be sent.
const errStale = 30 * time.Minute

// errAt is the time of the latest failed send.
func (s *snapshot) errAt() time.Time {
	if !s.tg.ErrAt.IsZero() {
		return s.tg.ErrAt
	}
	return s.tg.ErrSince
}

// tgErr is the current send problem; a test message after it counts as a fix.
func (s *snapshot) tgErr() string {
	if s.tg.Err == "" || s.linked.After(s.errAt()) || s.tgOld() {
		return ""
	}
	return s.tg.Err
}

// tgOld: the last send failed long ago and nothing waits, so no new attempt was made since.
func (s *snapshot) tgOld() bool {
	return s.tg.Err != "" && s.pending == 0 && time.Since(s.errAt()) > errStale
}

// tgLast is the last message that reached Telegram.
func (s *snapshot) tgLast() time.Time {
	if s.linked.After(s.tg.LastOKAt) {
		return s.linked
	}
	return s.tg.LastOKAt
}

// refresh re-reads what changed: the config only when its text differs, the state only when its file did.
func (m *model) refresh() {
	s := m.snap
	if txt := m.file.Text(); !m.cached || txt != m.cfgText || (m.mode == modeExplore) != m.cfgExplore {
		cfg, err := config.Parse(strings.NewReader(txt), false)
		if m.mode == modeExplore {
			cfg, err = config.Defaults(), nil // config unreadable: try the default state file
		}
		s.cfg, s.cfgErr = cfg, ""
		if err != nil {
			s.cfg, s.cfgErr = nil, err.Error()
		}
		m.cached, m.cfgText, m.cfgExplore, m.stSig = true, txt, m.mode == modeExplore, stateSig{}
	}
	if s.cfg == nil {
		s.es, s.esErr = nil, ""
	} else if sig := statSig(s.cfg.StateFile); sig != m.stSig || sig == (stateSig{}) {
		m.stSig = sig
		var err error
		var st state.Snap
		s.es, s.esErr, s.tg, s.dm, s.up, s.foot = nil, "", state.Telegram{}, state.Deadman{}, state.Updates{}, nil
		if st, err = state.ReadAll(s.cfg.StateFile); err != nil {
			s.esErr = err.Error()
		} else {
			s.es = st.Entries
			if st.Telegram != nil {
				s.tg = *st.Telegram
			}
			if st.Deadman != nil {
				s.dm = *st.Deadman
			}
			if st.Updates != nil {
				s.up = *st.Updates
			}
			s.foot = st.Foot
		}
	}
	s.ok, s.bad, s.failed, s.shaky, s.lastMsg, s.pending = 0, 0, nil, nil, time.Time{}, 0
	for _, e := range s.es {
		switch e.Reported {
		case checks.OK, checks.UNKNOWN:
			if e.Fails > 0 {
				s.shaky = append(s.shaky, e.Name)
			} else if e.Reported == checks.OK {
				s.ok++
			}
		case checks.FAIL:
			s.bad++
			s.failed = append(s.failed, e.Name)
		}
		s.pending += len(e.Pending)
		if e.LastMsgAt.After(s.lastMsg) {
			s.lastMsg = e.LastMsgAt
		}
	}
	s.rows, s.ents = buildRows(s.es)
	s.stale, s.age, s.pairing, s.linked = false, 0, false, time.Time{}
	if s.cfg != nil {
		s.pairing = config.PairingActive(m.file.Path, time.Now())
		s.linked, _ = config.LinkedAt(m.file.Path)
		s.age, s.stale = state.Stale(s.cfg.StateFile, s.cfg.StaleAfter(), time.Now())
	}
	m.snap = s
}

type stateSig struct {
	mtime time.Time
	size  int64
}

func statSig(path string) stateSig {
	st, err := os.Stat(path)
	if err != nil {
		return stateSig{}
	}
	return stateSig{st.ModTime(), st.Size()}
}

func (m *model) size() (int, int) {
	if m.a.Size == nil {
		return 80, 24
	}
	return m.a.Size()
}

var serviceTypes = state.ServiceTypes
var hostTypes = state.MachineTypes

func rank(list []string, t string) int { return state.Rank(list, t) }

type srow struct {
	hdr string // section header, or
	ent int    // index into the entry list when hdr is empty
}

// statusRows groups the entries into "This machine" and "Services" (as /status does); ents is in display order.
func (m *model) statusRows() ([]srow, []state.Entry) { return m.snap.rows, m.snap.ents }

func buildRows(es []state.Entry) (rows []srow, ents []state.Entry) {
	for _, sec := range []struct {
		name  string
		types []string
	}{{state.MachineHead, hostTypes}, {state.ServiceHead, serviceTypes}} {
		var part []state.Entry
		for _, e := range es {
			if rank(sec.types, e.Type) < len(sec.types) {
				part = append(part, e)
			}
		}
		sort.SliceStable(part, func(i, j int) bool { return rank(sec.types, part[i].Type) < rank(sec.types, part[j].Type) })
		if len(part) > 0 {
			rows = append(rows, srow{hdr: sec.name})
		}
		for _, e := range part {
			rows = append(rows, srow{ent: len(ents)})
			ents = append(ents, e)
		}
	}
	return
}

func clock(t, now time.Time) string {
	t = t.Local()
	if y1, m1, d1 := t.Date(); y1 == now.Year() && m1 == now.Month() && d1 == now.Day() {
		return t.Format("15:04")
	}
	if t.Year() != now.Year() {
		return t.Format("Jan 2 2006 15:04")
	}
	return t.Format("Jan 2 15:04")
}

func (m *model) target(e state.Entry) (spec config.CheckSpec, ok bool) {
	if m.snap.cfg != nil {
		for _, s := range m.snap.cfg.Checks {
			if s.Type == e.Type && s.Name == e.Name {
				return s, true
			}
		}
	}
	return
}

func (m *model) tries(e state.Entry) string {
	n := 0
	if m.snap.cfg != nil {
		n = m.snap.cfg.FailAfter
	}
	if n == 0 {
		return fmt.Sprint(e.Fails, " tries")
	}
	return fmt.Sprintf("%d of %d tries", e.Fails, n)
}

func (m *model) entryHint(e state.Entry, now time.Time) string {
	var s string
	switch {
	case e.Reported == checks.FAIL:
		since := e.DownSince
		if since.IsZero() {
			since = e.Since
		}
		s = fmt.Sprintf("%s: failing since %s, %s.", e.Name, clock(since, now), m.tries(e))
	case e.Fails > 0:
		s = fmt.Sprintf("%s: last check failed, %s, not reported yet.", e.Name, m.tries(e))
	default:
		s = fmt.Sprintf("%s: %s.", e.Name, m.sinceText(e, now))
	}
	if e.LastMsgAt.IsZero() {
		return s + " No Telegram message yet."
	}
	return s + " Telegram sent at " + clock(e.LastMsgAt, now) + "."
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func every(d time.Duration) string { return checks.Compact(d) }

// menuValue is the live value next to a menu item; it needs no network.
func (m *model) menuValue(it string) string {
	v, _ := m.menuVal(it)
	return v
}

// menuVal is menuValue and its colour (sBg = plain).
func (m *model) menuVal(it string) (string, sty) {
	c := m.snap.cfg
	if it == "Quit" {
		return "", sBg
	}
	if m.mode == modeExplore && it != "Status" {
		return "locked, see the hint below", sBg
	}
	switch it {
	case "Status":
		switch {
		case m.snap.stale:
			return "daemon not running", sFail
		case len(m.snap.es) == 0:
			return "no state yet", sBg
		case m.snap.bad > 0 && len(m.snap.shaky) > 0:
			return fmt.Sprintf("%d FAIL: %s, %d unstable", m.snap.bad, strings.Join(m.snap.failed, ", "), len(m.snap.shaky)), sFail
		case m.snap.bad > 0:
			return fmt.Sprintf("%d FAIL: %s", m.snap.bad, strings.Join(m.snap.failed, ", ")), sFail
		case len(m.snap.shaky) > 0:
			return fmt.Sprintf("%d OK, %d unstable: %s", m.snap.ok, len(m.snap.shaky), strings.Join(m.snap.shaky, ", ")), sWarn
		}
		return "all OK", sOK
	case "Add check":
		return "Website, port or host", sBg
	}
	if c == nil {
		return m.snap.cfgErr, sFail
	}
	switch it {
	case "Remove check":
		return plural(len(allChecks(c)), "check", "checks"), sBg
	case "System checks":
		on := 0
		rows := sysChecks(c)
		for _, s := range rows {
			if s.on {
				on++
			}
		}
		if on == len(rows) {
			return fmt.Sprintf("%d on", on), sBg
		}
		return fmt.Sprintf("%d on, %d off", on, len(rows)-on), sBg
	case "Settings":
		return fmt.Sprintf("every %s, alarm after %d, all-clear after %d", every(c.Interval), c.FailAfter, c.RecoverAfter), sBg
	case "Telegram":
		return m.telegramValue(c, time.Now())
	case "Dead-man switch":
		return m.deadmanValue(c, time.Now())
	}
	return "", sBg
}

// dmMeta is the daemon's dead-man record, if it belongs to the URL in the config.
func (s *snapshot) dmMeta() (state.Deadman, bool) {
	return s.dm, s.cfg != nil && s.cfg.DeadmanURL != "" && s.dm.URLFP == checks.URLFP(s.cfg.DeadmanURL)
}

func (m *model) deadmanValue(c *config.Config, now time.Time) (string, sty) {
	d, mine := m.snap.dmMeta()
	ev := "every " + every(c.DeadmanEvery)
	switch {
	case c.DeadmanURL == "":
		return "off", sBg
	case m.snap.stale:
		return "daemon not running", sFail
	case mine && d.Err != "":
		return fmt.Sprintf("last ping failed %s: %s", clock(d.ErrSince, now), d.Err), sFail
	case mine && !d.LastOKAt.IsZero():
		return ev + ", last ping " + clock(d.LastOKAt, now), sBg
	}
	return ev + ", no ping yet", sBg
}

func (m *model) telegramValue(c *config.Config, now time.Time) (string, sty) {
	s := &m.snap
	switch err := s.tgErr(); {
	case c.Token == "" || c.ChatID == 0:
		return "not set up", sBg
	case s.pairing:
		return "pairing in progress", sBg
	case s.stale:
		return "linked, daemon not running", sFail
	case err != "" && s.pending > 0:
		return fmt.Sprintf("%d waiting: not reachable since %s", s.pending, clock(s.tg.ErrSince, now)), sFail
	case err != "":
		return "last send failed " + clock(s.errAt(), now), sFail
	case s.up.Conflict == state.ConflictWebhook:
		return "a webhook is set for this bot: /status does not work", sFail
	case s.up.Conflict != "":
		return "another program uses this bot: /status does not work", sFail
	case s.tgOld() && !s.tgLast().IsZero():
		return "linked, last message " + clock(s.tgLast(), now) + "; a send failed " + clock(s.errAt(), now), sWarn
	case !s.tgLast().IsZero():
		return "linked, last message " + clock(s.tgLast(), now), sBg
	case s.tgOld():
		return "linked, a send failed " + clock(s.errAt(), now) + ", none since", sWarn
	}
	return "linked, nothing sent yet", sBg
}

var about = map[string]string{
	"Status":          "Latest result of every check.",
	"Add check":       "Watch a website, a TCP port or a host. It is checked once before it is saved.",
	"Remove check":    "Pick a check and remove it from the config.",
	"System checks":   "Limits for this machine's own checks. Enter changes a limit, Space turns a check on or off.",
	"Settings":        "How often the checks run, and after how many rounds an alarm or all-clear goes out.",
	"Telegram":        "Set up the bot token and pair your chat with a one-time code.",
	"Dead-man switch": "Healthchecks-style URL pinged at a set time, so you notice if little-wd dies.",
	"Quit":            "Leave the program.",
}

type kv struct{ key, word string }

// layout of one frame; all coordinates are canvas cells.
type frame struct {
	cv         *canvas
	th         theme
	w, h       int
	x, y, bw   int // frame box
	bh         int
	ix, iy, iw int // inner area
	ih         int
}

func (m *model) render(out io.Writer) {
	w, h := m.size()
	cv := newCanvas(w, h)
	fr := &frame{cv: cv, th: m.th, w: cv.w, h: cv.h, bw: cv.w, bh: max(cv.h-5, 3), y: 1}
	fr.ix, fr.iy, fr.iw, fr.ih = 1, fr.y+1, fr.bw-2, fr.bh-2
	now := time.Now()

	crumb := ""
	hint, keys := "", []kv{}
	fr.frameBox(map[screen]string{scMenu: "Menu", scStatus: "Status", scRemove: "Remove check", scSys: "System checks", scBlank: m.flow}[m.scr])
	switch m.scr {
	case scMenu:
		hint, keys = m.drawMenu(fr), []kv{{fr.th.g.updown, "move"}, {"Enter", "open"}, {"Q", "quit"}, {"?", "help"}}
	case scStatus:
		crumb = "Status"
		hint, keys = m.drawStatusPage(fr, now), []kv{{fr.th.g.updown, "move"}, {"Enter", "details"}, {"Esc", "menu"}, {"?", "help"}}
	case scRemove:
		crumb = "Remove check"
		hint, keys = m.drawRemove(fr), []kv{{fr.th.g.updown, "move"}, {"Enter", "remove"}, {"Esc", "menu"}, {"?", "help"}}
	case scSys:
		crumb = "System checks"
		hint, keys = m.drawSys(fr), []kv{{fr.th.g.updown, "move"}, {"Enter", "change"}, {"Space", "on/off"}, {"Esc", "menu"}, {"?", "help"}}
	default:
		crumb = m.flow
	}

	switch {
	case m.dk == dForm:
		h2, k2 := m.drawForm(fr)
		hint, keys = h2, k2
	case m.dk == dDetails:
		m.drawDetails(fr, now)
		hint, keys = "", []kv{{"Enter", "close"}, {"Esc", "close"}}
	case m.dk == dTelegram:
		m.drawTelegram(fr, now)
		hint, keys = fmt.Sprintf("Waiting up to %s for the code.", windowText(m.a.PollWindow)), []kv{{"Esc", "cancel"}}
	case m.dk == dResult:
		hint, keys = m.drawResult(fr)
	}
	if m.cf != nil {
		m.drawConfirm(fr)
		keys = []kv{{"Tab", "next button"}, {"Enter", "choose"}, {"Esc", "cancel"}}
	}
	if m.help {
		m.drawHelp(fr)
		keys = []kv{{"Enter", "close"}, {"Esc", "close"}}
	}
	// In explore mode every change is locked, so the way out shows on every item.
	if m.scr == scMenu && m.dk == dNone && (m.mode == modeExplore || m.mode != modeFull && locked(menuItems[m.cur], m.mode)) {
		hint = strings.ToUpper(needGroup()[:1]) + needGroup()[1:] + "."
	}
	m.drawChrome(fr, crumb, hint, keys, now)
	if f := cv.emit(m.th); f != m.last {
		m.last = f
		io.WriteString(out, f)
	}
}

func windowText(d time.Duration) string {
	if d == 0 {
		d = 5 * time.Minute
	}
	if d%time.Minute == 0 {
		return plural(int(d/time.Minute), "minute", "minutes")
	}
	return d.String()
}

// drawChrome draws the header bar, the hint bar and the key line around the frame.
func (m *model) drawChrome(fr *frame, crumb, hint string, keys []kv, now time.Time) {
	cv, g := fr.cv, fr.th.g
	cv.fill(0, 0, fr.w, 1, sHeadInfo)
	left := cv.text(1, 0, "little-wd", sHead)
	if crumb != "" {
		cv.text(left, 0, "  "+g.arrow+" "+crumb, sHeadInfo)
	}
	var right string
	chip, chipSt := "", sBg
	switch {
	case m.mode == modeExplore:
		right = "explore mode, no changes"
	case m.mode == modeReadOnly:
		right = "read-only"
	case m.snap.stale:
		chip, chipSt = "daemon not running", sFail
	case m.snap.cfg == nil && m.snap.cfgErr != "":
		chip, chipSt = "config invalid", sFail
	case m.snap.cfg == nil && m.snap.es == nil:
		right = "no state"
	case len(m.snap.es) == 0:
		right = "no state yet"
	case m.snap.bad > 0:
		chip, chipSt = fmt.Sprintf("%d FAIL", m.snap.bad), sFail
	case m.snap.tgErr() != "" && m.snap.cfg != nil && m.snap.cfg.Token != "":
		chip, chipSt = "Telegram not reachable", sFail
	case m.snap.up.Conflict != "" && m.snap.cfg != nil && m.snap.cfg.Token != "":
		chip, chipSt = "/status not working", sFail
	case func() bool { d, ok := m.snap.dmMeta(); return ok && d.Err != "" }():
		chip, chipSt = "Dead-man ping failing", sFail
	case len(m.snap.shaky) > 0:
		chip, chipSt = fmt.Sprintf("%d unstable", len(m.snap.shaky)), sWarn
	default:
		chip, chipSt = "all OK", sOK
	}
	// the health chip sits at the right edge on a dark cell; host and plain text left of it
	if m.host != "" {
		if right != "" {
			right = m.host + " " + g.bar + " " + right
		} else {
			right = m.host
		}
	}
	if chip != "" {
		chip = " " + chip + " "
	}
	if avail := fr.w - left - 3; avail > 0 {
		chip = fit(chip, avail)
		x := fr.w - 1 - len([]rune(chip))
		if chip != "" {
			cv.text(x, 0, chip, chipSt)
			x--
		}
		if w := x - left - 3; w > 0 && right != "" {
			right = fit(right, w)
			cv.text(x-len([]rune(right)), 0, right, sHeadInfo)
		}
	}
	for i, l := range wrap(hint, max(fr.w-2, 1)) {
		if i < 2 && hint != "" {
			cv.fill(0, fr.h-4+i, fr.w, 1, sHint)
			cv.text(1, fr.h-4+i, l, sHint)
		}
	}
	x, y := 0, fr.h-2
	n := 1
	for _, k := range keys {
		n += len([]rune(k.key)) + len([]rune(k.word)) + 3
	}
	cv.fill(0, y, n, 1, sKeys)
	x = cv.text(x+1, y, "", sKeys)
	for _, k := range keys {
		x = cv.text(x, y, k.key, sKey)
		x = cv.text(x, y, " "+k.word+"  ", sKeys)
	}
}

func (fr *frame) frameBox(title string) {
	fr.cv.box(fr.x, fr.y, fr.bw, fr.bh, title, fr.th.g, sBox, sBox, sBg)
}

// row draws one inner line; the whole width gets style st (the cursor bar when focused).
func (fr *frame) row(i int, st sty) {
	if i >= fr.ih {
		return
	}
	fr.cv.fill(fr.ix, fr.iy+i, fr.iw, 1, st)
}

func (fr *frame) put(i, x int, s string, st sty) int {
	if i >= fr.ih { // never over the bottom border
		return fr.ix + x
	}
	return fr.cv.text(fr.ix+x, fr.iy+i, fit(s, fr.iw-x), st)
}

// scrollOff keeps row sel inside a window of room rows.
func scrollOff(sel, total, room int) int {
	return max(min(sel-room+1, total-room), 0)
}

func (m *model) drawMenu(fr *frame) string {
	for i, it := range menuItems {
		lock := locked(it, m.mode)
		lab, bar := sRowBold, sBg
		text, val := m.menuVal(it)
		if lock {
			lab, val = sRowDim, sRowDim
		}
		if i == m.cur {
			lab, val, bar = sFocus, sFocus, sFocus
			if lock {
				lab, val, bar = sFocusDim, sFocusDim, sFocusDim
			}
			fr.row(i, bar)
		}
		fr.put(i, 2, it, lab)
		fr.put(i, 22, text, val)
	}
	if m.snap.foot != nil && fr.ih > len(menuItems)+1 && !m.snap.stale {
		fr.put(fr.ih-1, 2, footLine(m.snap.foot), sRowDim)
	}
	if it := menuItems[m.cur]; m.snap.cfg == nil && m.snap.cfgErr != "" && it != "Status" && it != "Add check" && it != "Quit" {
		return "Fix " + m.file.Path + ": " + m.snap.cfgErr
	}
	if it := menuItems[m.cur]; it == "Status" && m.snap.stale {
		return daemonDown
	} else if it == "Status" && m.snap.cfg != nil {
		return fmt.Sprintf("Latest result of every check. Checks run every %s.", every(m.snap.cfg.Interval))
	}
	switch it := menuItems[m.cur]; {
	case it == "Telegram" && m.snap.up.Conflict != "" && !m.snap.stale:
		return "Telegram hands messages to one reader only: a webhook or another program has this bot. Use one bot per machine (docs/setup.md)."
	case it == "Telegram" && m.snap.cfg != nil && m.snap.cfg.Token != "" && m.mode == modeFull:
		return "Linked: " + checks.TokenFP(m.snap.cfg.Token) + ", chat " + fmt.Sprint(m.snap.cfg.ChatID) + ". " + about[it]
	case it == "Dead-man switch" && m.snap.cfg != nil && m.snap.cfg.DeadmanURL != "" && m.mode == modeFull:
		return "Pinging " + checks.URLFP(m.snap.cfg.DeadmanURL) + ". " + about[it]
	}
	return about[menuItems[m.cur]]
}

// footLine is what the daemon costs, from its last state write.
func footLine(f *state.Footprint) string {
	up := f.At.Sub(f.Started)
	s := "little-wd: " + ramText(f) + " RAM"
	if up >= time.Minute {
		s += fmt.Sprintf(" · %.1f%% CPU", f.CPUSec/up.Seconds()*100)
	}
	return s + " · " + writesText(f, "disk write", "disk writes") + " · up " + upText(up)
}

func upText(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d d", int(d.Hours()/24))
}

func (m *model) markOf(e state.Entry) (string, sty) {
	if unstable(e) && m.snap.cfg != nil {
		return fmt.Sprintf("[%d/%d]", e.Fails, m.snap.cfg.FailAfter), sWarn
	}
	switch e.Reported {
	case checks.OK:
		return "[OK]", sOK
	case checks.FAIL:
		return "[FAIL]", sFail
	}
	return "[?]", sRowDim
}

// unstable: failing, but below FAIL_AFTER, so not reported yet.
func unstable(e state.Entry) bool { return e.Reported != checks.FAIL && e.Fails > 0 }

// started is the daemon's start from its last state write (zero if unknown).
func (m *model) started() time.Time {
	if m.snap.foot == nil {
		return time.Time{}
	}
	return m.snap.foot.Started
}

// sinceText is "OK since 06:00", with the older time when the daemon restarted meanwhile.
// An unstable check also says since when it fails.
func (m *model) sinceText(e state.Entry, now time.Time) string {
	rep := e
	rep.DownSince = time.Time{} // the reported state, not the streak
	t, clamped := state.ShownSince(rep, m.started())
	s := fmt.Sprintf("%s since %s", e.Reported, clock(t, now))
	if clamped {
		s = fmt.Sprintf("%s since the daemon start at %s", e.Reported, clock(t, now))
		if b := clock(e.Since, now); b != clock(t, now) {
			s += " (before that since " + b + ")"
		}
	}
	if unstable(e) && !e.DownSince.IsZero() {
		s += fmt.Sprintf(", failing since %s (%s)", clock(e.DownSince, now), m.tries(e))
	}
	return s
}

// day is a clock time for today, else the date ("Nov 2").
func day(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	if c := clock(t, now); len(c) == 5 {
		return c
	}
	if t.Local().Year() != now.Year() {
		return t.Local().Format("Jan '06")
	}
	return t.Local().Format("Jan 2")
}

// Column positions shared by the check lists (Status, Remove check, System checks).
const (
	colMark = 4
	colName = colMark + 8
)

// heads draws dimmed column names on inner row y.
func (fr *frame) heads(y int, xs []int, labels []string) {
	for i, l := range labels {
		w := fr.iw - xs[i]
		if i+1 < len(labels) {
			w = xs[i+1] - xs[i] - 1
		}
		fr.put(y, xs[i], fit(l, max(w, 0)), sRowDim)
	}
}

// nameWidth sizes the name column to the longest name, between 14 and a third of the width.
func nameWidth(fr *frame, names func(func(string))) int {
	n := 14
	names(func(s string) { n = max(n, len([]rune(s))+1) })
	return min(n, max(fr.iw/3, 14))
}

// daemonDown is the hint when there is no fresh heartbeat.
const daemonDown = "Daemon not running. Start it: sudo systemctl start little-watchdog"

func (m *model) drawStatus(fr *frame, now time.Time) string {
	if m.snap.cfg == nil {
		fr.put(0, 2, "Config is invalid: "+m.snap.cfgErr, sBg)
		return ""
	}
	rows, ents := m.statusRows()
	if len(ents) == 0 {
		fr.put(0, 2, "No state yet. Is the daemon running? ("+m.snap.cfg.StateFile+")", sBg)
		if m.snap.stale {
			return daemonDown
		}
		return ""
	}
	m.scur = min(m.scur, len(ents)-1)
	sel := 0
	for i, r := range rows {
		if r.hdr == "" && r.ent == m.scur {
			sel = i
		}
	}
	if sel == 1 { // first entry: show its section header too
		sel = 0
	}
	nw := nameWidth(fr, func(yield func(string)) {
		for _, e := range ents {
			yield(e.Name)
		}
	})
	tx := colName + nw
	vx := tx + 7
	sx := fr.iw - 8 // Since
	cx := sx - 9    // Checked
	// when the result gets too narrow, Type goes first, then Checked
	if cx-vx-2 < 16 {
		vx = tx
	}
	if cx-vx-2 < 16 {
		cx = sx
	}
	xs, hs := []int{colMark, colName}, []string{"State", "Check"}
	if vx != tx {
		xs, hs = append(xs, tx), append(hs, "Type")
	}
	xs, hs = append(xs, vx), append(hs, "Last result")
	if cx != sx {
		xs, hs = append(xs, cx), append(hs, "Checked")
	}
	fr.heads(0, append(xs, sx), append(hs, "Since"))
	room := max(fr.ih-1, 1)
	off := scrollOff(sel, len(rows), room)
	for i := off; i < len(rows) && i-off < room; i++ {
		r, y := rows[i], i-off+1
		if r.hdr != "" {
			fr.put(y, 2, r.hdr, sCat)
			continue
		}
		e := ents[r.ent]
		mk, ms := m.markOf(e)
		ts, ds := sBg, sRowDim
		if r.ent == m.scur {
			ts, ms, ds = sFocus, sFocus, sFocusDim
			fr.row(y, ts)
		}
		fr.put(y, colMark, mk, ms)
		fr.put(y, colName, fit(e.Name, nw-1), ts)
		if vx != tx {
			fr.put(y, tx, e.Type, ts)
		}
		x, vw := vx, max(cx-vx-2, 0)
		value := e.Value
		if unstable(e) && e.Detail != "" {
			value = e.Detail
		}
		if e.Reported == checks.FAIL && e.OKs > 0 && m.snap.cfg != nil {
			back := backText(e, m.snap.cfg.RecoverAfter)
			ws := sWarn
			if r.ent == m.scur {
				ws = sFocus
			}
			fr.put(y, x, back, ws)
			x += len(back) + 1
			vw = max(vw-len(back)-1, 0)
		}
		vs := ts
		if e.NoValue { // no verdict: show it, dimmed, not an older failure text
			value, vs = e.Value, ds
		}
		fr.put(y, x, fit(value, vw), vs)
		if cx != sx && !e.LastRun.IsZero() {
			fr.put(y, cx, day(e.LastRun, now), ds)
		}
		since, _ := state.ShownSince(e, m.started())
		fr.put(y, sx, day(since, now), ts)
	}
	if m.snap.stale {
		return daemonDown
	}
	return m.entryHint(ents[m.scur], now)
}

func (m *model) drawRemove(fr *frame) string {
	if len(m.items) == 0 {
		fr.put(0, 2, "No checks configured.", sBg)
		return ""
	}
	type rrow struct {
		hdr string
		it  int
	}
	var rows []rrow
	sel := 0
	for i, it := range m.items {
		if i == 0 || it.host != m.items[i-1].host {
			rows = append(rows, rrow{hdr: map[bool]string{false: state.ServiceHead, true: state.MachineHead}[it.host]})
		}
		if i == m.lcur {
			sel = len(rows)
		}
		rows = append(rows, rrow{it: i})
	}
	if sel == 1 {
		sel = 0
	}
	nw := nameWidth(fr, func(yield func(string)) {
		for _, it := range m.items {
			yield(it.name)
		}
	})
	tx := colName + nw
	fr.heads(0, []int{colMark, colName, tx, tx + 7}, []string{"State", "Check", "Type", "Watches"})
	room := max(fr.ih-1, 1)
	off := scrollOff(sel, len(rows), room)
	for ri := off; ri < len(rows) && ri-off < room; ri++ {
		r, y := rows[ri], ri-off+1
		if r.hdr != "" {
			fr.put(y, 2, r.hdr, sCat)
			continue
		}
		it := m.items[r.it]
		mk, ms, ts := "", sBg, sBg
		for _, e := range m.snap.es {
			if e.Type == it.typ && e.Name == it.name {
				mk, ms = m.markOf(e)
			}
		}
		if r.it == m.lcur {
			ms, ts = sFocus, sFocus
			fr.row(y, sFocus)
		}
		fr.put(y, colMark, mk, ms)
		fr.put(y, colName, fit(it.name, nw-1), ts)
		fr.put(y, tx, it.typ, ts)
		tgt := it.target
		if it.typ == "temp" && mk == "" && len(m.snap.es) > 0 {
			tgt += " (not running: no sensor?)"
		}
		fr.put(y, tx+7, tgt, ts)
	}
	return "Removing an own-host check switches it off in the config."
}

// seg is a run of text in one style inside a dialog line.
type seg struct {
	s  string
	st sty
}

type drow struct {
	segs   []seg
	center bool
}

func line(s string, st sty) drow { return drow{segs: []seg{{s, st}}} }

func wrapRows(s string, w int, st sty) (rs []drow) {
	for _, l := range wrap(s, w) {
		rs = append(rs, line(l, st))
	}
	return
}

func (fr *frame) buttons(names []string, focus int) drow {
	r := drow{center: true}
	for i, n := range names {
		st := sBtn
		if i == focus {
			st = sBtnOn
		}
		if i > 0 {
			r.segs = append(r.segs, seg{"   ", sDlg})
		}
		r.segs = append(r.segs, seg{"[ " + n + " ]", st})
	}
	return r
}

// dialog draws a box with rows near the top of the frame, width w (cut to the frame).
func (fr *frame) dialog(title string, w int, rows []drow, dy int) {
	w = min(w, fr.iw-2)
	x, y := fr.ix+(fr.iw-w)/2, fr.iy+dy
	fr.cv.box(x, y, w, len(rows)+2, title, fr.th.g, sDbox, sDtitle, sDlg)
	for i, r := range rows {
		n := 0
		for _, s := range r.segs {
			n += len([]rune(s.s))
		}
		cx := x + 2
		if r.center {
			cx = x + (w-n)/2
		}
		for _, s := range r.segs {
			cx = fr.cv.text(cx, y+1+i, fit(s.s, x+w-2-cx), s.st)
		}
	}
}

func (m *model) drawDetails(fr *frame, now time.Time) {
	e := m.det
	lab := func(l string, v string, st sty) drow {
		return drow{segs: []seg{{fmt.Sprintf("%-10s", l), sDdim}, {v, st}}}
	}
	w := min(max(fr.iw-2, 40), 100)
	vw := w - 4 - 10
	rows := []drow{lab("Type", e.Type, sDlg)}
	if s, ok := m.target(e); ok {
		rows = append(rows, labWrap(lab, "Target", s.Target, vw)...)
		if s.Contains != "" {
			rows = append(rows, labWrap(lab, "Text", s.Contains, vw)...)
		}
	}
	if e.Reported == checks.FAIL {
		since := e.DownSince
		if since.IsZero() {
			since = e.Since
		}
		rows = append(rows, drow{segs: []seg{{fmt.Sprintf("%-10s", "State"), sDdim}, {"FAIL", sDfail}, {" since " + clock(since, now) + " (" + m.tries(e) + ")", sDlg}}})
	} else {
		rows = append(rows, labWrap(lab, "State", m.sinceText(e, now), vw)...)
	}
	rows = append(rows, labWrap(lab, "Last", e.Value, vw)...)
	if !e.LastRun.IsZero() {
		rows = append(rows, lab("Checked", clock(e.LastRun, now), sDlg))
	}
	tg := "no alarm so far"
	if !e.LastMsgAt.IsZero() {
		tg = "sent at " + clock(e.LastMsgAt, now)
	}
	rows = append(rows, lab("Telegram", tg, sDlg), line("", sDlg), fr.buttons([]string{"Close"}, 0))
	fr.dialog(e.Name, w, rows, 1)
}

// labWrap breaks v into lines of n runes (URLs have no spaces to break at).
func labWrap(lab func(string, string, sty) drow, l, v string, n int) (rs []drow) {
	for i, p := range hardWrap(v, n) {
		if i > 0 {
			l = ""
		}
		rs = append(rs, lab(l, p, sDlg))
	}
	return
}

// fieldView cuts v to fw cells. Focused, it returns the text before the caret, the
// caret cell and the rest (filled with "_"); a long value scrolls to keep the caret in view.
func fieldView(v string, fw, pos int, focus bool) (pre, cur, post string) {
	r := []rune(v)
	if !focus {
		if len(r) > fw {
			return string(r[:fw-1]) + ">", "", ""
		}
		return v, "", ""
	}
	p := min(max(pos, 0), len(r))
	st := 0
	if len(r)+1 > fw {
		st = max(p-fw+2, 0)
	}
	cells := make([]rune, fw)
	for i := range cells {
		switch c := st + i; {
		case c < len(r):
			cells[i] = r[c]
		default:
			cells[i] = '_'
		}
	}
	if st > 0 {
		cells[0] = '<'
	}
	if st+fw < len(r) {
		cells[fw-1] = '>'
	}
	cx := min(p-st, fw-1)
	c := " "
	if p < len(r) {
		c = string(r[p])
	}
	return string(cells[:cx]), c, string(cells[cx+1:])
}

func (m *model) drawForm(fr *frame) (string, []kv) {
	f := m.form
	ks := f.kinds[f.kind]
	w := max(fr.iw-2, 20)
	var rows []drow
	class, fi := f.at()
	if f.radio() == 1 {
		r := drow{segs: []seg{{fmt.Sprintf("%-10s", "Kind"), sDdim}}}
		for i, k := range f.kinds {
			mk, st := fr.th.g.radOff, sDlg
			if i == f.kind {
				mk, st = fr.th.g.radOn, sDbold
			}
			if class == -1 && i == f.kind {
				st = sBtnOn
			}
			r.segs = append(r.segs, seg{mk + " " + k.name, st}, seg{"  ", sDlg})
		}
		rows = append(rows, r, line("", sDlg))
	}
	lw := 10
	for _, l := range ks.labels {
		lw = max(lw, len(l)+2)
	}
	fw := max(w-4-lw, 1)
	for i, l := range ks.labels {
		v := f.vals[i]
		if f.secret && i == 0 && !f.reveal {
			v = strings.Repeat("*", len([]rune(v)))
		}
		r := drow{segs: []seg{{fmt.Sprintf("%-*s", lw, l), sDdim}}}
		if class == 0 && fi == i {
			r.segs[0].st = sDbold
			pre, cur, post := fieldView(v, fw, f.pos[i], true)
			r.segs = append(r.segs, seg{pre, sDbold}, seg{cur, sCaret}, seg{post, sDbold})
		} else {
			pre, _, _ := fieldView(v, fw, 0, false)
			r.segs = append(r.segs, seg{pre, sDlg})
		}
		rows = append(rows, r)
	}
	if f.cur != "" {
		rows = append(rows, line("", sDlg))
		rows = append(rows, wrapRows(f.cur, w-4, sDdim)...)
	}
	rows = append(rows, line("", sDlg))
	switch {
	case f.err != "":
		rows = append(rows, wrapRows("Error: "+f.err, w-4, sDbold)...)
	case f.note != "":
		rows = append(rows, line(f.note, sDdim))
	}
	focus := -1
	if class == 1 {
		focus = fi
	}
	rows = append(rows, line("", sDlg), fr.buttons(f.btns, focus))
	fr.dialog(f.title, w, rows, 1)
	keys := []kv{{fr.th.g.updown + " Tab", "move"}, {"Enter", "next"}, {"Esc", "cancel"}}
	if f.secret {
		keys = append(keys, kv{"Ctrl-R", "show/hide"})
	}
	hint := ""
	if class == 0 && fi < len(ks.hints) {
		hint = ks.hints[fi]
	} else if class == -1 {
		hint = "Left/Right chooses what to watch."
	}
	return hint, keys
}

func (m *model) drawTelegram(fr *frame, now time.Time) {
	t := m.tg
	if t == nil {
		t = &tgState{}
	}
	names := []string{"Bot token", "Webhook", "Link your chat", "Test message"}
	var rows []drow
	for i, n := range names {
		mk := fr.th.g.off
		if t.done[i] {
			mk = fr.th.g.on
		}
		r := drow{segs: []seg{{mk, sDbold}, {fmt.Sprintf(" %d  %-16s", i+1, n), sDlg}}}
		if t.note[i] != "" && (i != 2 || t.done[2]) {
			r.segs = append(r.segs, seg{t.note[i], sDdim})
		}
		if i == 2 && t.code != "" && !t.done[2] {
			r.segs = append(r.segs, seg{"send this to " + t.bot + ":", sDdim})
		}
		rows = append(rows, r)
		if i == 2 && t.code != "" && !t.done[2] {
			// No live countdown: a redraw every second clears a mouse selection,
			// and the code must stay copyable.
			rows = append(rows,
				drow{segs: []seg{{strings.Repeat(" ", 23+4), sDlg}, {"/start " + t.code, sDtitle}}},
				drow{segs: []seg{{strings.Repeat(" ", 23), sDlg}, {"waiting until " + clock(t.until, now), sDdim}}})
		}
	}
	rows = append(rows, line("", sDlg), fr.buttons([]string{"Cancel"}, 0))
	fr.dialog("Telegram setup", 65, rows, 1)
}

func (m *model) drawResult(fr *frame) (string, []kv) {
	w := max(fr.iw-2, 20)
	var rows []drow
	title, hint, keys := "Result", "", []kv{{"Any key", "back"}}
	if m.cancel != nil {
		title, hint, keys = "Working", "Working ... Esc cancels, nothing is saved then.", []kv{{"Esc", "cancel"}}
		rows = append(rows, line("Working ...", sDlg))
	}
	for _, l := range m.lines {
		rows = append(rows, wrapRows(l, w-4, sDlg)...)
	}
	fr.dialog(title, w, rows, 1)
	return hint, keys
}

func (m *model) drawConfirm(fr *frame) {
	c := m.cf
	var rows []drow
	for i, l := range c.lines {
		st := sDlg
		if i == 0 {
			st = sDbold
		}
		rows = append(rows, wrapRows(l, 48, st)...)
	}
	rows = append(rows, line("", sDlg), fr.buttons(c.btns[:], c.idx))
	fr.dialog(c.title, 52, rows, 2)
}

func (m *model) drawHelp(fr *frame) {
	g := fr.th.g
	k := func(a, b string) drow {
		return drow{segs: []seg{{fmt.Sprintf("%-12s", a), sKey2}, {b, sDlg}}}
	}
	rows := []drow{
		k(g.updown+" / j k", "move"),
		k("Enter", "open the entry, show details"),
		k("Tab", "next field or button"),
		k("Ctrl-R", "show or hide a secret you type"),
		k("Space", "System checks: turn a check on or off"),
		k("Esc", "back to the menu, close a window"),
		k("Q", "quit in the menu, back elsewhere"),
	}
	text := []drow{
		line("State: [OK] [FAIL], [2/3] = 2 of 3 tries failed, [?] = no", sDlg),
		line("result yet. back 1/2 = 1 of 2 good tries after a FAIL.", sDlg),
		line("Checked: time of the last result. Since: when that state", sDlg),
		line("began (an OK at most since the daemon start).", sDlg),
		line("Settings: check interval, alarm and all-clear rounds, timeout.", sDlg),
		line("Changes apply after you confirm, within one interval.", sDlg),
	}
	if len(rows)+len(text)+4 <= fr.ih { // a low terminal gets the keys only
		rows = append(rows, text...)
		pl := wrapRows("Status at 120+ columns: the right panel shows the selected check and this machine.", 62, sDlg)
		if _, pw := panelSplit(fr.w, fr.iw); pw > 0 && len(rows)+len(pl)+4 <= fr.ih { // only where the panel exists
			rows = append(rows, pl...)
		}
	}
	rows = append(rows, line("", sDlg), fr.buttons([]string{"Close"}, 0))
	fr.dialog("Keys", 66, rows, 1)
}

const sKey2 = sDtitle

// backText is "back 1/2"; capped while the daemon may still run an older RECOVER_AFTER.
func backText(e state.Entry, ra int) string {
	return fmt.Sprintf("back %d/%d", min(e.OKs, max(ra-1, 1)), ra)
}
