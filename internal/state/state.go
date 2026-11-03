// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package state holds the reported state per check, the hysteresis logic and
// the atomic state file.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
)

const maxPending = 20

type Entry struct {
	Name      string        `json:"name"`
	Type      string        `json:"type"`
	Reported  checks.Status `json:"reported"`
	Since     time.Time     `json:"since"`
	Fails     int           `json:"fails"`
	OKs       int           `json:"oks"`
	DownSince time.Time     `json:"down_since,omitzero"` // start of the failing streak
	Value     string        `json:"value,omitempty"`
	LastRun   time.Time     `json:"last_run"`
	LastMsg   string        `json:"last_msg,omitempty"`
	LastMsgAt time.Time     `json:"last_msg_at,omitzero"`
	Pending   []string      `json:"pending,omitempty"` // messages not delivered yet
}

type file struct {
	Version int               `json:"version"`
	Checks  map[string]*Entry `json:"checks"`
}

type Engine struct {
	mu           sync.Mutex
	entries      map[string]*Entry
	dirty        bool // reported state, counters or pending changed since the last write
	savedAt      time.Time
	writes       int
	saveMu       sync.Mutex
	FailAfter    int
	RecoverAfter int
}

func NewEngine(failAfter, recoverAfter int) *Engine {
	return &Engine{entries: map[string]*Entry{}, FailAfter: failAfter, RecoverAfter: recoverAfter}
}

// Load reads the state file and keeps only ids in valid. A missing file is
// normal; a broken one yields a warning and an empty state.
func (e *Engine) Load(path string, valid map[string]bool) (warn string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries = map[string]*Entry{}
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
			e.entries[id] = en
		}
	}
	return ""
}

// refreshEvery bounds how stale Value/LastRun may get on disk.
const refreshEvery = 10 * time.Minute

// Writes counts state file writes.
func (e *Engine) Writes() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.writes
}

// SaveIfDue writes only when something that matters changed or the file is older than refreshEvery.
func (e *Engine) SaveIfDue(path string, now time.Time) error {
	e.mu.Lock()
	due := e.dirty || e.savedAt.IsZero() || now.Sub(e.savedAt) >= refreshEvery
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
	b, err := json.Marshal(file{Version: 1, Checks: e.entries})
	e.dirty = false
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
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	e.mu.Lock()
	e.savedAt, e.writes = now, e.writes+1
	e.mu.Unlock()
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// Observe feeds one result into the state machine. A changed reported state
// appends a message to the entry's Pending list and returns it.
func (e *Engine) Observe(c checks.Check, r checks.Result, now time.Time) (msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	en := e.entries[c.ID()]
	if en == nil {
		en = &Entry{Reported: checks.UNKNOWN, Since: now}
		e.entries[c.ID()] = en
		e.dirty = true
	}
	// counters (Fails, OKs) are not worth a write each cycle; the refresh saves them
	type sig struct {
		rep   checks.Status
		since time.Time
	}
	sigOf := func() sig { return sig{en.Reported, en.Since} }
	before := sigOf()
	npend := len(en.Pending)
	name, detail := checks.Sanitize(c.Name()), checks.Sanitize(r.Detail)
	en.Name, en.Type, en.Value, en.LastRun = name, c.Type(), checks.Sanitize(r.Value), now
	hm := now.Local().Format("15:04")
	defer func() {
		if sigOf() != before || len(en.Pending) != npend || msg != "" {
			e.dirty = true
		}
	}()

	switch r.Status {
	case checks.FAIL:
		en.OKs = 0
		if en.Fails == 0 && en.Reported != checks.FAIL {
			en.DownSince = now
		}
		if en.Fails < e.FailAfter {
			en.Fails++
		}
		if en.Reported != checks.FAIL && en.Fails >= e.FailAfter {
			en.Reported, en.Since = checks.FAIL, now
			if r.NoAnswer {
				msg = fmt.Sprintf("%s ❌ %s: no answer since %s", hm, name, dur(now.Sub(en.DownSince)))
			} else {
				msg = fmt.Sprintf("%s ❌ %s: %s", hm, name, detail)
			}
		}
	case checks.OK:
		if r.Hold && en.Reported == checks.FAIL {
			en.OKs = 0 // inside hysteresis band: neither fail nor recover
			break
		}
		en.Fails = 0
		switch en.Reported {
		case checks.UNKNOWN:
			en.Reported, en.Since = checks.OK, now
		case checks.FAIL:
			en.OKs++
			if en.OKs >= e.RecoverAfter {
				msg = fmt.Sprintf("%s ✅ %s: back after %s", hm, name, dur(now.Sub(en.DownSince)))
				en.Reported, en.Since, en.OKs = checks.OK, now, 0
				en.DownSince = time.Time{}
			}
		}
	}
	if msg != "" {
		en.Pending = append(en.Pending, msg)
		if len(en.Pending) > maxPending {
			en.Pending = en.Pending[len(en.Pending)-maxPending:]
		}
	}
	return msg
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

// Queued is one undelivered message and the check it belongs to.
type Queued struct{ ID, Text string }

// Queue lists all undelivered messages: by check id, oldest first within a check.
func (e *Engine) Queue() (q []Queued) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, en := range e.entries {
		for _, m := range en.Pending {
			q = append(q, Queued{id, m})
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
	for i, m := range en.Pending {
		if m == text {
			en.Pending = append(en.Pending[:i:i], en.Pending[i+1:]...)
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
	for _, en := range e.entries {
		out = append(out, *en)
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
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("state file corrupt: %w", err)
	}
	var out []Entry
	for _, en := range f.Checks {
		if en != nil {
			c := *en
			c.Name, c.Value = checks.Sanitize(c.Name), checks.Sanitize(c.Value)
			out = append(out, c)
		}
	}
	sortEntries(out)
	return out, nil
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

// Ago renders a relative time for tables.
func Ago(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return dur(now.Sub(t)) + " ago"
}

// Table renders entries as an aligned text table.
func Table(es []Entry, now time.Time) string {
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tSTATE\tSINCE\tVALUE")
	for _, en := range es {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", checks.Sanitize(en.Name), checks.Clean(en.Type), checks.Clean(string(en.Reported)), Ago(en.Since, now), checks.Sanitize(en.Value))
	}
	tw.Flush()
	return sb.String()
}

// Brief renders entries for the Telegram /status reply.
func Brief(es []Entry, now time.Time) string {
	var sb strings.Builder
	for _, en := range es {
		mark := "✅"
		switch en.Reported {
		case checks.FAIL:
			mark = "❌"
		case checks.UNKNOWN:
			mark = "❔"
		}
		fmt.Fprintf(&sb, "%s %s: %s", mark, checks.Sanitize(en.Name), checks.Clean(string(en.Reported)))
		if en.Value != "" {
			fmt.Fprintf(&sb, " (%s)", checks.Sanitize(en.Value))
		}
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		return "no checks"
	}
	return strings.TrimRight(sb.String(), "\n")
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
func Beat(statePath string, now time.Time) error {
	p := statePath + BeatSuffix
	if err := os.Chtimes(p, now, now); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	f.Close()
	return os.Chtimes(p, now, now)
}

// LastBeat returns the daemon's last heartbeat; ok is false if there never was one.
func LastBeat(statePath string) (t time.Time, ok bool) {
	st, err := os.Stat(statePath + BeatSuffix)
	if err != nil {
		return time.Time{}, false
	}
	return st.ModTime(), true
}

// Stale reports a daemon whose last heartbeat is older than after
// (config.Config.StaleAfter: interval plus the time the slowest round may take).
func Stale(statePath string, after time.Duration, now time.Time) (age time.Duration, stale bool) {
	t, ok := LastBeat(statePath)
	if !ok {
		return 0, true
	}
	age = max(now.Sub(t), 0)
	return age, age > after
}
