// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package state holds the reported state per check, the hysteresis logic and
// the atomic state file.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/telegram"
)

const maxPending = 20

// fileVersion is the state file format. 2: Pending and LastMsg hold Telegram HTML;
// 1 (and files without a version) held plain text, which Load converts once.
const fileVersion = 2

type Entry struct {
	ID        string        `json:"-"` // the key in the state file, set when read
	Name      string        `json:"name"`
	Type      string        `json:"type"`
	Reported  checks.Status `json:"reported"`
	Since     time.Time     `json:"since"`
	Fails     int           `json:"fails"`
	OKs       int           `json:"oks"`
	DownSince time.Time     `json:"down_since,omitzero"` // start of the failing streak
	Value     string        `json:"value,omitempty"`
	NoValue   bool          `json:"no_value,omitempty"`  // the last result was UNKNOWN: Value is no verdict (warm-up, read error)
	VerdictAt time.Time     `json:"verdict_at,omitzero"` // last OK or FAIL result
	Detail    string        `json:"detail,omitempty"`    // latest error of the running failure streak
	Help      string        `json:"help,omitempty"`      // hint about Detail for the TUI (additive: old readers ignore it)
	Odd       bool          `json:"odd,omitempty"`       // OK, but shown yellow: it answers, not as promised (additive)
	LastRun   time.Time     `json:"last_run"`
	LastMsg   string        `json:"last_msg,omitempty"`
	LastMsgAt time.Time     `json:"last_msg_at,omitzero"`
	Pending   []string      `json:"pending,omitempty"`    // messages not delivered yet
	PendingAt []time.Time   `json:"pending_at,omitempty"` // when each pending message happened (same order)
}

// Telegram is how the daemon's sends went; the TUI shows it. Additive: old readers ignore it.
type Telegram struct {
	LastOKAt time.Time `json:"last_ok_at,omitzero"`
	Err      string    `json:"err,omitempty"`      // short, sanitized; set while sends fail
	ErrSince time.Time `json:"err_since,omitzero"` // first failure of the current streak
	ErrAt    time.Time `json:"err_at,omitzero"`    // latest failure (in memory until the next write)
}

// Updates is how /status polling (getUpdates) goes; only a conflict is recorded.
type Updates struct {
	Conflict string    `json:"conflict,omitempty"` // ConflictWebhook, ConflictPoller or ConflictUnknown
	Since    time.Time `json:"since,omitzero"`
}

// Kinds of getUpdates conflict (HTTP 409).
const (
	ConflictWebhook = "webhook" // a webhook is set for the bot
	ConflictPoller  = "poller"  // another program polls the bot
	ConflictUnknown = "unknown" // 409 without a text that tells which
)

// Footprint is what the daemon itself costs; it rides on every state write.
type Footprint struct {
	Started time.Time `json:"started"`
	At      time.Time `json:"at"`
	Writes  int       `json:"writes"`           // write rounds since Started, this one included; each writes the state and its fine history
	RSSKB   int64     `json:"rss_kb,omitempty"` // resident memory
	CPUSec  float64   `json:"cpu_s,omitempty"`  // CPU time used since Started
}

// Deadman is how the dead-man pings went; the URL itself is never stored, only its fingerprint.
type Deadman struct {
	URLFP    string    `json:"url_fp,omitempty"`
	LastOKAt time.Time `json:"last_ok_at,omitzero"`
	Err      string    `json:"err,omitempty"`
	ErrSince time.Time `json:"err_since,omitzero"`
}

type file struct {
	Version  int               `json:"version"`
	Checks   map[string]*Entry `json:"checks"`
	Telegram *Telegram         `json:"telegram,omitempty"`
	Deadman  *Deadman          `json:"deadman,omitempty"`
	Updates  *Updates          `json:"updates,omitempty"`
	Foot     *Footprint        `json:"footprint,omitempty"`
	Hist     *History          `json:"history,omitempty"` // 24 h in 10 minute buckets (additive: 0.4.0 ignores it)
}

type Engine struct {
	mu           sync.Mutex
	entries      map[string]*Entry
	dirty        bool // reported state, pending or meta changed since the last write
	cdirty       bool // only streak counters changed (written at most every counterEvery)
	cAt          time.Time
	savedAt      time.Time
	writes       int
	Started      time.Time // daemon start, for the footprint; read it with StartedAt
	FinePath     string    // where the fine history goes (RAM); empty or unwritable: next to the state file
	startFixed   bool
	Proc         func() (int64, float64) // RSS in KB and CPU seconds of this process
	PSI          func() (float64, bool)  // worst pressure now (percent), false where the kernel has none
	hist         *hist
	fine         fine // the last 30 minutes per check; written next to the state file
	saveMu       sync.Mutex
	FailAfter    int
	RecoverAfter int
	Host         string // machine name for messages; empty = leave out
	tg           Telegram
	dm           Deadman
	up           Updates
}

func NewEngine(failAfter, recoverAfter int) *Engine {
	return &Engine{entries: map[string]*Entry{}, FailAfter: failAfter, RecoverAfter: recoverAfter, Started: time.Now(), Proc: ReadProc, PSI: ReadPSI, hist: newHist(), fine: fine{}}
}

// Load reads the state file and keeps only ids in valid. A missing file is
// normal; a broken one yields a warning and an empty state.
func (e *Engine) Load(path string, valid map[string]bool) (warn string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries = map[string]*Entry{}
	e.hist = newHist()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		return fmt.Sprintf("state file unreadable (%v), starting empty", err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Sprintf("state file corrupt (%v), starting empty", err)
	}
	for id, en := range f.Checks {
		if en != nil && valid[id] {
			if f.Version < fileVersion {
				en.LastMsg = telegram.Esc(en.LastMsg)
				for i, m := range en.Pending {
					en.Pending[i] = telegram.Esc(m)
				}
			}
			e.entries[id] = en
		}
	}
	e.hist = decode(f.Hist, valid) // a history it cannot read is left out, never an error
	if f.Telegram != nil {
		e.tg = *f.Telegram
	}
	if f.Deadman != nil {
		e.dm = *f.Deadman
	}
	if f.Updates != nil {
		e.up = *f.Updates
	}
	return ""
}

// refreshEvery bounds how stale Value/LastRun may get on disk.
const refreshEvery = 10 * time.Minute

// counterEvery is the shortest gap between two writes caused only by streak counters
// (a flapping check); changes of the reported state and messages are written at once.
const counterEvery = 5 * time.Minute

// Writes counts write rounds; each writes the state file and its fine history.
func (e *Engine) Writes() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.writes
}

// SaveIfDue writes only when something that matters changed or the file is older than refreshEvery.
func (e *Engine) SaveIfDue(path string, now time.Time) error {
	e.mu.Lock()
	due := e.dirty || e.savedAt.IsZero() || now.Sub(e.savedAt) >= refreshEvery || (e.cdirty && now.Sub(e.cAt) >= counterEvery)
	e.mu.Unlock()
	if !due {
		return nil
	}
	return e.save(path, now)
}

// Save always writes the state atomically.
func (e *Engine) Save(path string) error { return e.save(path, time.Now()) }

// save writes temp file, fsync, rename, mode 0640 (the group may read; no secrets in it).
func (e *Engine) save(path string, now time.Time) (rerr error) {
	e.saveMu.Lock()
	defer e.saveMu.Unlock()
	e.mu.Lock()
	tg, dm := e.tg, e.dm
	f := file{Version: fileVersion, Checks: e.entries, Telegram: &tg}
	if dm.URLFP != "" {
		f.Deadman = &dm
	}
	if e.up != (Updates{}) {
		up := e.up
		f.Updates = &up
	}
	f.Foot = e.footprint(now)
	f.Hist = e.hist.encode()
	fb := e.fine.encode(now)
	b, err := json.Marshal(f)
	if e.cdirty {
		e.cAt = now
	}
	e.dirty, e.cdirty = false, false
	e.mu.Unlock()
	defer func() { // any failure keeps the state dirty, so the next cycle writes again
		if rerr != nil {
			e.mu.Lock()
			e.dirty = true
			e.mu.Unlock()
		}
	}()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	// kept: STATE_FILE may name a directory that does not exist yet (existing configs); the temp
	// file plus rename below replaces a link at the final name instead of following it.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := atomicWrite(path, b, ".state-*.tmp", true); err != nil {
		return err
	}
	e.writeFine(path, fb)
	e.mu.Lock()
	e.savedAt, e.writes = now, e.writes+1
	e.mu.Unlock()
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// writeFine puts the fine history in RAM (FinePath), without fsync: it means nothing after a
// reboot and may be torn (ParseFine copes). Without a usable FinePath it goes next to the state
// file. A failure costs the history only, never the state.
func (e *Engine) writeFine(path string, fb []byte) {
	if e.FinePath != "" && atomicWrite(e.FinePath, fb, ".fine-*.tmp", false) == nil {
		return
	}
	atomicWrite(path+FineSuffix, fb, ".fine-*.tmp", false)
}

// Writable reports why the directory of path cannot take a new file; it leaves nothing behind.
func Writable(path string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".probe-*.tmp")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}

// StartedAt is the daemon start, corrected once the wall clock has jumped.
func (e *Engine) StartedAt() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Started
}

// FixStart moves Started once when the wall clock jumped since the start (NTP
// on a board without a real-time clock). time.Now's monotonic reading does not jump.
func (e *Engine) FixStart() {
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.startFixed {
		return
	}
	if s, ok := fixStart(e.Started, now.Sub(e.Started), now.Round(0).Sub(e.Started.Round(0))); ok {
		e.Started, e.startFixed, e.dirty = s, true, true
	}
}

// fixStart is started moved by the jump when wall and monotonic elapsed time
// differ by more than a minute within the first hour (later it may be a suspend).
func fixStart(started time.Time, mono, wall time.Duration) (time.Time, bool) {
	if d := wall - mono; mono < time.Hour && (d > time.Minute || d < -time.Minute) {
		return started.Add(d), true
	}
	return started, false
}

// footprint is the daemon's own cost as of now; the caller holds e.mu.
func (e *Engine) footprint(now time.Time) *Footprint {
	fp := &Footprint{Started: e.Started, At: now, Writes: e.writes + 1}
	if e.Proc != nil {
		fp.RSSKB, fp.CPUSec = e.Proc()
	}
	return fp
}

// Observe feeds one result into the state machine. A changed reported state
// appends a message to the entry's Pending list and returns it.
func (e *Engine) Observe(c checks.Check, r checks.Result, now time.Time) (msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record(c, r, now)
	en := e.entries[c.ID()]
	if en == nil {
		en = &Entry{Reported: checks.UNKNOWN, Since: now}
		e.entries[c.ID()] = en
		e.dirty = true
	}
	// A changed streak counter (starts, grows below the threshold, ends) is written when the
	// last such write is 5 min old (the first one at once); a steady state or a flapping
	// check does not write each cycle.
	fails0, oks0 := en.Fails, en.OKs
	type sig struct {
		rep   checks.Status
		since time.Time
	}
	sigOf := func() sig { return sig{en.Reported, en.Since} }
	before := sigOf()
	npend := len(en.Pending)
	name, detail := checks.Sanitize(c.Name()), checks.Sanitize(r.Detail)
	bname, bdetail := telegram.B(name), telegram.Esc(detail) // messages are Telegram HTML
	nv0, v0, odd0 := en.NoValue, en.VerdictAt, en.Odd
	en.Odd = r.Status == checks.OK && r.Odd
	en.Name, en.Type, en.Value, en.NoValue, en.LastRun = name, c.Type(), checks.Sanitize(r.Value), r.Status == checks.UNKNOWN, now
	if !en.NoValue {
		en.VerdictAt = now
	}
	defer func() {
		switch {
		case sigOf() != before || len(en.Pending) != npend || msg != "" || nv0 && !en.NoValue && v0.Before(e.Started):
			e.dirty = true // also the first verdict after a start, so the screen drops the warm-up text
		case en.Fails != fails0 || en.OKs != oks0 || en.NoValue != nv0 || en.Odd != odd0:
			e.cdirty = true // a flapping check writes at most once per counterEvery
		}
	}()

	switch r.Status {
	case checks.FAIL:
		en.OKs = 0
		if en.Fails == 0 && en.Reported != checks.FAIL {
			en.DownSince = now // first failure of the streak
		}
		en.Detail, en.Help = detail, checks.Sanitize(r.Help)
		if detail == "" {
			en.Detail = checks.Sanitize(r.Value)
		}
		if en.Fails < e.FailAfter {
			en.Fails++
		}
		if en.Reported != checks.FAIL && en.Fails >= e.FailAfter {
			en.Reported, en.Since = checks.FAIL, now
			since := When(en.DownSince, now)
			switch {
			case r.NoAnswer:
				msg = fmt.Sprintf("❌ %s%s is not answering since %s.", bname, telegram.On(e.Host), since)
			case isService(c.Type()):
				msg = fmt.Sprintf("❌ %s%s is down since %s: %s.", bname, telegram.On(e.Host), since, bdetail)
			default:
				msg = fmt.Sprintf("❌ %s%s has a problem since %s: %s.", bname, telegram.On(e.Host), since, bdetail)
			}
		}
	case checks.OK:
		if r.Hold && en.Reported == checks.FAIL {
			en.OKs = 0 // inside hysteresis band: neither fail nor recover
			break
		}
		en.Fails, en.Detail, en.Help = 0, "", ""
		if en.Reported != checks.FAIL {
			en.DownSince = time.Time{}
		}
		switch en.Reported {
		case checks.UNKNOWN:
			en.Reported, en.Since = checks.OK, now
		case checks.FAIL:
			en.OKs++
			if en.OKs >= e.RecoverAfter {
				msg = fmt.Sprintf("✅ %s%s is back after %s.", bname, telegram.On(e.Host), dur(now.Sub(en.DownSince)))
				en.Reported, en.Since, en.OKs = checks.OK, now, 0
				en.DownSince = time.Time{}
			}
		}
	}
	if msg != "" {
		en.alignAt()
		en.Pending = append(en.Pending, msg)
		en.PendingAt = append(en.PendingAt, now)
		if len(en.Pending) > maxPending {
			en.Pending = en.Pending[len(en.Pending)-maxPending:]
			en.PendingAt = en.PendingAt[len(en.PendingAt)-maxPending:]
		}
	}
	return msg
}

// alignAt pads PendingAt (a file from an older version has none) so it matches Pending.
func (en *Entry) alignAt() {
	if n := len(en.Pending) - len(en.PendingAt); n > 0 {
		en.PendingAt = append(make([]time.Time, n), en.PendingAt...)
	} else if n < 0 {
		en.PendingAt = en.PendingAt[-n:]
	}
}

// isService: a check of something that is reachable over the network (down), not a host limit (problem).
func isService(typ string) bool {
	return typ == "http" || typ == "tcp" || typ == "host" || typ == "ping"
}

// When is t as the clock time of today, with the date when it was another day.
func When(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if y1, m1, d1 := t.Date(); y1 == now.Year() && m1 == now.Month() && d1 == now.Day() {
		return t.Format("15:04")
	}
	if t.Year() != now.Year() {
		return t.Format("Jan 2 2006 15:04")
	}
	return t.Format("Jan 2 15:04")
}

// NoteSend records a send outcome. Only a change (first success, ok to error,
// error to ok) is written at once; the periodic refresh carries last_ok_at.
func (e *Engine) NoteSend(err error, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err == nil {
		if e.tg.Err != "" || e.tg.LastOKAt.IsZero() {
			e.dirty = true
		}
		e.tg = Telegram{LastOKAt: now}
		return
	}
	if e.tg.Err == "" {
		e.tg.ErrSince, e.dirty = now, true
	}
	e.tg.Err, e.tg.ErrAt = checks.Sanitize(err.Error()), now
}

// NoteUpdates records how getUpdates goes: "" = fine, else a Conflict kind. Only a change is written at once.
func (e *Engine) NoteUpdates(conflict string, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case conflict == "" && e.up != (Updates{}):
		e.up, e.dirty = Updates{}, true
	case conflict != "" && e.up.Conflict != conflict:
		e.up, e.dirty = Updates{Conflict: conflict, Since: now}, true
	}
}

// NoteDeadman records a dead-man ping outcome for the URL with fingerprint fp ("" = none).
// first is true when this is the first success for that URL: a new or changed URL,
// never one that already has a stored success (a restart). Only changes are written at once.
func (e *Engine) NoteDeadman(fp string, err error, now time.Time) (first bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dm.URLFP != fp {
		e.dm, e.dirty = Deadman{URLFP: fp}, true
	}
	if err == nil {
		first = e.dm.LastOKAt.IsZero()
		if first || e.dm.Err != "" {
			e.dirty = true
		}
		e.dm.LastOKAt, e.dm.Err, e.dm.ErrSince = now, "", time.Time{}
		return first
	}
	if e.dm.Err == "" {
		e.dm.ErrSince, e.dirty = now, true
	}
	e.dm.Err = checks.Sanitize(err.Error())
	return false
}

// Conflict is the current getUpdates conflict kind, "" if none.
func (e *Engine) Conflict() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.up.Conflict
}

// ClearDeadman forgets the dead-man meta (the URL was removed).
func (e *Engine) ClearDeadman() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dm != (Deadman{}) {
		e.dm, e.dirty = Deadman{}, true
	}
}

// PendingMessages lists undelivered messages in check-id order.
func (e *Engine) PendingMessages() (ids []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, en := range e.entries {
		if len(en.Pending) > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Queued is one undelivered message, the check it belongs to and when it happened (zero = unknown).
type Queued struct {
	ID, Text string
	At       time.Time
}

// Queue lists all undelivered messages: by check id, oldest first within a check.
func (e *Engine) Queue() (q []Queued) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, en := range e.entries {
		en.alignAt()
		for i, m := range en.Pending {
			q = append(q, Queued{id, m, en.PendingAt[i]})
		}
	}
	sort.SliceStable(q, func(i, j int) bool { return q[i].ID < q[j].ID })
	return q
}

// Peek returns the first undelivered message of id.
func (e *Engine) Peek(id string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	en := e.entries[id]
	if en == nil || len(en.Pending) == 0 {
		return "", false
	}
	return en.Pending[0], true
}

// pop removes the first pending message of id equal to text. Matching by text and
// not by position keeps a message that was queued meanwhile (or one that fell off
// the front of a full queue) from being removed by mistake.
func (e *Engine) pop(id, text string) (*Entry, bool) {
	en := e.entries[id]
	if en == nil {
		return nil, false
	}
	en.alignAt()
	for i, m := range en.Pending {
		if m == text {
			en.Pending = append(en.Pending[:i:i], en.Pending[i+1:]...)
			en.PendingAt = append(en.PendingAt[:i:i], en.PendingAt[i+1:]...)
			e.dirty = true
			return en, true
		}
	}
	return en, false
}

// Delivered removes the pending message text of id and records it as the last one sent.
func (e *Engine) Delivered(id, text string, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if en, ok := e.pop(id, text); ok {
		en.LastMsg, en.LastMsgAt = text, now
	}
}

// Drop removes the pending message text of id without recording it as sent.
func (e *Engine) Drop(id, text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pop(id, text)
}

// Snapshot returns copies sorted by type then name.
func (e *Engine) Snapshot() []Entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Entry, 0, len(e.entries))
	for id, en := range e.entries {
		c := *en
		c.ID = id
		out = append(out, c)
	}
	sortEntries(out)
	return out
}

func sortEntries(s []Entry) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Type != s[j].Type {
			return s[i].Type < s[j].Type
		}
		return s[i].Name < s[j].Name
	})
}

// ReadFile loads a state file for the status command (no filtering).
func ReadFile(path string) ([]Entry, error) {
	st, err := ReadAll(path)
	return st.Entries, err
}

// Snap is a state file as read: the checks and the Telegram and dead-man meta (nil if never written).
type Snap struct {
	Entries  []Entry
	Telegram *Telegram
	Deadman  *Deadman
	Updates  *Updates
	Foot     *Footprint
	Hist     *Hist // nil when the file has none
}

// ReadAll is ReadFile plus the meta.
func ReadAll(path string) (st Snap, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return st, fmt.Errorf("state file corrupt: %w", err)
	}
	if f.Telegram != nil {
		t := *f.Telegram
		t.Err = checks.Sanitize(t.Err)
		st.Telegram = &t
	}
	if f.Deadman != nil {
		d := *f.Deadman
		d.Err, d.URLFP = checks.Sanitize(d.Err), checks.Sanitize(d.URLFP)
		st.Deadman = &d
	}
	if f.Updates != nil {
		u := *f.Updates
		u.Conflict = checks.Clean(u.Conflict)
		st.Updates = &u
	}
	st.Foot = f.Foot
	st.Hist = f.Hist.Hist()
	out := []Entry(nil)
	for id, en := range f.Checks {
		if en != nil {
			c := *en
			c.ID = id
			c.Name, c.Value = checks.Sanitize(c.Name), checks.Sanitize(c.Value)
			c.Detail, c.LastMsg = checks.Sanitize(c.Detail), checks.Sanitize(c.LastMsg)
			c.Help = checks.Sanitize(c.Help)
			out = append(out, c)
		}
	}
	sortEntries(out)
	st.Entries = out
	return st, nil
}

func dur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

// ShownSince is when the shown state began: the first failed check of a
// failing streak, else the last change. An older OK counts from the daemon
// start (clamped), since nothing was checked while the daemon was down.
func ShownSince(e Entry, started time.Time) (t time.Time, clamped bool) {
	if !e.DownSince.IsZero() {
		return e.DownSince, false
	}
	if !started.IsZero() && !e.Since.IsZero() && e.Since.Before(started) {
		return started, true
	}
	return e.Since, false
}

// Recovering is the hint for a reported FAIL that has good results again but not yet
// recoverAfter of them in a row: "answers again" (a machine check: "ok again") and
// "all-clear after 1 more good try". Both are empty otherwise.
func Recovering(en Entry, recoverAfter int) (again, progress string) {
	if en.Reported != checks.FAIL || en.OKs <= 0 || recoverAfter <= 0 {
		return "", ""
	}
	again = "ok again"
	if isService(en.Type) {
		again = "answers again"
	}
	if en.OKs >= recoverAfter { // RECOVER_AFTER was lowered by a reload
		return again, "all-clear on the next good try"
	}
	if n := recoverAfter - en.OKs; n > 1 {
		return again, fmt.Sprintf("all-clear after %d more good tries", n)
	}
	return again, "all-clear after 1 more good try"
}

// RecoverN is the current RECOVER_AFTER (it changes on a config reload).
func (e *Engine) RecoverN() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.RecoverAfter
}

// Table renders entries as an aligned text table; started is the daemon start,
// recoverAfter the RECOVER_AFTER for the recovery hint (0 = no hint).
func Table(es []Entry, recoverAfter int, started, now time.Time) string {
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tSTATE\tSINCE\tVALUE")
	for _, en := range es {
		since := "-"
		if t, _ := ShownSince(en, started); !t.IsZero() {
			since = When(t, now)
		}
		val := checks.Sanitize(en.Value)
		if en.NoValue && en.Reported != checks.UNKNOWN {
			val += " (no verdict yet)"
		} else if again, n := Recovering(en, recoverAfter); again != "" {
			val += " (" + again + ", " + n + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", checks.Sanitize(en.Name), checks.Clean(en.Type), checks.Clean(string(en.Reported)), since, val)
	}
	tw.Flush()
	return sb.String()
}

// Section lists for /status and the TUI: this machine first, then the services.
var (
	MachineTypes = []string{"cpu", "mem", "disk", "temp"}
	ServiceTypes = []string{"http", "tcp", "host", "ping"}
)

// Headings of the two groups, in /status and in the TUI.
const (
	MachineHead = "This machine"
	ServiceHead = "Services"
)

// Rank is the position of t in list; len(list) if it is not there.
func Rank(list []string, t string) int {
	for i, x := range list {
		if x == t {
			return i
		}
	}
	return len(list)
}

// Brief renders entries for the Telegram /status reply (Telegram HTML): a bold summary line, then the
// machine's own checks and the services under a heading each. started is the daemon start,
// recoverAfter the RECOVER_AFTER for the recovery hint (0 = no hint).
func Brief(host string, es []Entry, recoverAfter int, started, now time.Time) string {
	if len(es) == 0 {
		return telegram.Head(host, "no checks are set up.")
	}
	var sb strings.Builder
	failing := 0
	for _, en := range es {
		if en.Reported == checks.FAIL {
			failing++
		}
	}
	switch {
	case failing == 0:
		sb.WriteString(telegram.Head(host, checks.Count(len(es))+", none failing.") + "\n")
	default:
		sb.WriteString(telegram.Head(host, fmt.Sprintf("%d of %s failing.", failing, checks.Count(len(es)))) + "\n")
	}
	var mach, svc []Entry
	for _, en := range es {
		if Rank(MachineTypes, en.Type) < len(MachineTypes) {
			mach = append(mach, en)
		} else {
			svc = append(svc, en)
		}
	}
	sort.SliceStable(mach, func(i, j int) bool { return Rank(MachineTypes, mach[i].Type) < Rank(MachineTypes, mach[j].Type) })
	sort.SliceStable(svc, func(i, j int) bool { return Rank(ServiceTypes, svc[i].Type) < Rank(ServiceTypes, svc[j].Type) })
	for _, g := range []struct {
		head string
		es   []Entry
	}{{MachineHead, mach}, {ServiceHead, svc}} {
		if len(g.es) == 0 {
			continue
		}
		sb.WriteString("<b>" + g.head + "</b>\n")
		for _, en := range g.es {
			sb.WriteString(briefLine(en, recoverAfter, started, now) + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// briefLine is "MARK name: value" (Telegram HTML) with the state added only where it is not plain ok.
func briefLine(en Entry, recoverAfter int, started, now time.Time) string {
	name, val := telegram.B(checks.Sanitize(en.Name)), telegram.Esc(checks.Sanitize(en.Value))
	with := func(state string) string { // value first; its own brackets stay single
		if val == "" {
			return state
		}
		return val + ", " + state
	}
	switch en.Reported {
	case checks.FAIL:
		state := "failing"
		if !en.DownSince.IsZero() {
			state = "down since " + When(en.DownSince, now)
		}
		if en.NoValue && val != "" { // no verdict: state first
			return "❌ " + name + ": " + state + ", " + val
		}
		if again, n := Recovering(en, recoverAfter); again != "" { // short: no good value
			return "❌ " + name + ": " + again + ", " + n + ". " + strings.ToUpper(state[:1]) + state[1:] + "."
		}
		return "❌ " + name + ": " + with(state)
	case checks.UNKNOWN:
		return "- " + name + ": " + with("not known yet")
	}
	if en.NoValue { // the ok is from an earlier result
		s := "✅ " + name + ": ok so far"
		switch v := en.VerdictAt; {
		case !started.IsZero() && v.Before(started) && en.Since.Before(started):
			s = "✅ " + name + ": ok before the restart at " + When(started, now)
		case !v.IsZero():
			s = "✅ " + name + ": ok at " + When(v, now)
		}
		if val != "" {
			s += ", " + val
		}
		return s
	}
	if val == "" {
		val = "ok"
	}
	return "✅ " + name + ": " + val
}

// Retain forgets every entry whose id is not in valid.
func (e *Engine) Retain(valid map[string]bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id := range e.entries {
		if !valid[id] {
			delete(e.entries, id)
			e.dirty = true
		}
	}
	for id := range e.hist.checksOrNil() {
		if !valid[id] {
			delete(e.hist.checks, id)
		}
	}
	for id := range e.fine {
		if !valid[id] {
			delete(e.fine, id)
		}
	}
}

// Configure changes the hysteresis thresholds.
func (e *Engine) Configure(failAfter, recoverAfter int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.FailAfter, e.RecoverAfter = failAfter, recoverAfter
}

// BeatSuffix names the heartbeat file next to the state file.
const BeatSuffix = ".alive"

// Beat marks the daemon alive: it only touches the mtime of <state>.alive (no data, no fsync).
// It never follows a link at that name (the directory may be group-writable): the open fails
// instead, and the time is set on the opened file, not on the path.
func Beat(statePath string, now time.Time) error {
	f, err := os.OpenFile(statePath+BeatSuffix, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return errors.New("heartbeat file is not a regular file")
	}
	tv := syscall.NsecToTimeval(now.UnixNano())
	return syscall.Futimes(int(f.Fd()), []syscall.Timeval{tv, tv})
}

// LastBeat returns the daemon's latest heartbeat among the given state file names (a daemon
// beats in /run, or next to its state file where that is not possible); ok is false if there
// never was one.
func LastBeat(statePaths ...string) (t time.Time, ok bool) {
	for _, p := range statePaths {
		if st, err := os.Stat(p + BeatSuffix); err == nil && st.ModTime().After(t) {
			t, ok = st.ModTime(), true
		}
	}
	return t, ok
}

// Stale reports a daemon whose last heartbeat, at statePath or at one of alt, is older than
// after (config.Config.StaleAfter: interval plus the time the slowest round may take).
func Stale(statePath string, after time.Duration, now time.Time, alt ...string) (age time.Duration, stale bool) {
	t, ok := LastBeat(append([]string{statePath}, alt...)...)
	if !ok {
		return 0, true
	}
	age = max(now.Sub(t), 0)
	return age, age > after
}

// atomicWrite writes b to path as temp file, fsync (if fsync), rename, mode 0640. The rename
// replaces a link at the final name instead of following it.
func atomicWrite(path string, b []byte, pattern string, fsync bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after successful rename
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if fsync {
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
