// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package state

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
)

// The fine history: every measurement of the last 30 minutes per service check (time, status,
// response time, kind of error), so the detail view can show what came before a FAIL. It lives
// in memory and goes into its own file next to the state file (FineSuffix), in the write that is
// due anyway: no extra write, only more bytes in it. With STATE_IN_RAM that is /run, so no disk.
// The TUI is another process and reads that file.
// FineSuffix names the fine history file next to the state file.
const FineSuffix = ".fine"

const (
	FineSpan = 30 * time.Minute
	// fineMax is the most points kept per check: a round every 5 s fills the 30 minutes (360).
	// The shortest INTERVAL (10 s) needs 180; a faster clock loses the oldest points, not the newest.
	fineMax    = 360
	fineMagic  = "lwfine 1"
	fineOther  = 63 // failure whose kind is not known
	fineMaxMs  = 4095
	fineCellsz = 5
)

// FinePoint is one measurement. Kind is "" for OK and for a FAIL without a known kind.
type FinePoint struct {
	At   time.Time
	Fail bool
	Kind checks.Kind
	Ms   int // response time, up to 4095
}

type fpt struct {
	at   int64 // unix seconds
	code uint8 // 0 = OK, 1.. = index in checks.Kinds()+1, fineOther = FAIL of unknown kind
	ms   uint16
}

// fine holds the points per check id, oldest first.
type fine map[string][]fpt

func kindCode(k checks.Kind) uint8 {
	for i, x := range checks.Kinds() {
		if x == k {
			return uint8(i + 1)
		}
	}
	return fineOther
}

func codeKind(c uint8) checks.Kind {
	if ks := checks.Kinds(); c >= 1 && int(c) <= len(ks) {
		return ks[c-1]
	}
	return ""
}

// add keeps a measurement and drops what is older than FineSpan before it and what is over
// fineMax. A clock that stepped back drops the series: its points stand for a time that did not happen.
func (f fine) add(id string, st checks.Status, took time.Duration, kind checks.Kind, now time.Time) {
	if st != checks.OK && st != checks.FAIL {
		return
	}
	p := fpt{at: now.Unix(), ms: uint16(min(max(took.Milliseconds(), 0), fineMaxMs))}
	if st == checks.FAIL {
		p.code = kindCode(kind)
	}
	s := f[id]
	if n := len(s); n > 0 && p.at < s[n-1].at {
		s = nil
	}
	s = append(s, p)
	cut := 0
	for cut < len(s)-1 && s[cut].at < p.at-int64(FineSpan/time.Second) {
		cut++
	}
	cut = max(cut, len(s)-fineMax)
	if cut > 0 { // copy down: the backing array does not grow by the dropped points
		s = append(s[:0], s[cut:]...)
	}
	f[id] = s
}

// encode is the file text as of now. Header "lwfine 1 <unix>", then one line per check: id, tab,
// cells of 5 characters in base 64: seconds since now-FineSpan (2), error code (1: 0 = OK), ms (2).
func (f fine) encode(now time.Time) []byte {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %d\n", fineMagic, now.Unix())
	ids := make([]string, 0, len(f))
	for id := range f {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	t0 := now.Unix() - int64(FineSpan/time.Second)
	for _, id := range ids {
		if strings.ContainsAny(id, "\t\n") {
			continue
		}
		var cells strings.Builder
		for _, p := range f[id] {
			if p.at < t0 || p.at > now.Unix() {
				continue
			}
			put2(&cells, int(p.at-t0))
			cells.WriteByte(b64[p.code&63])
			put2(&cells, int(p.ms))
		}
		if cells.Len() > 0 {
			sb.WriteString(id + "\t" + cells.String() + "\n")
		}
	}
	return []byte(sb.String())
}

// Fine is the reading form of the fine history file.
type Fine struct {
	end time.Time
	m   map[string][]FinePoint
}

// End is when the file was written; nothing after it is in it.
func (f *Fine) End() time.Time {
	if f == nil {
		return time.Time{}
	}
	return f.end
}

// FineMaxMs is the longest response time the file can hold; slower answers read as this.
const FineMaxMs = fineMaxMs

// ParseFine reads the file text; text it cannot read gives an error and no history.
func ParseFine(b []byte) (*Fine, error) {
	head, rest, _ := strings.Cut(string(b), "\n")
	ts, ok := strings.CutPrefix(head, fineMagic+" ")
	end, err := strconv.ParseInt(ts, 10, 64)
	if !ok || err != nil {
		return nil, errors.New("fine history: unknown file")
	}
	out := &Fine{end: time.Unix(end, 0), m: map[string][]FinePoint{}}
	t0 := end - int64(FineSpan/time.Second)
	for _, l := range strings.Split(rest, "\n") {
		id, cells, ok := strings.Cut(l, "\t")
		if !ok || len(cells)%fineCellsz != 0 {
			continue // a damaged line costs that check's history only
		}
		ps := make([]FinePoint, 0, len(cells)/fineCellsz)
		for i := 0; i+fineCellsz <= len(cells); i += fineCellsz {
			c := cells[i : i+fineCellsz]
			dt, ok1 := dec2(c[:2])
			code := strings.IndexByte(b64, c[2])
			ms, ok2 := dec2(c[3:])
			if !ok1 || !ok2 || code < 0 {
				ps = nil
				break
			}
			ps = append(ps, FinePoint{At: time.Unix(t0+int64(dt), 0), Fail: code != 0, Kind: codeKind(uint8(code)), Ms: ms})
		}
		if len(ps) > 0 {
			out.m[id] = ps
		}
	}
	return out, nil
}

// ReadFine reads the fine history file.
func ReadFine(path string) (*Fine, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseFine(b)
}

// Check is the points of a check inside the 30 minutes before now, oldest first.
func (f *Fine) Check(id string, now time.Time) []FinePoint {
	if f == nil {
		return nil
	}
	var out []FinePoint
	for _, p := range f.m[id] {
		if !p.At.Before(now.Add(-FineSpan)) && !p.At.After(now) {
			out = append(out, p)
		}
	}
	return out
}

// LoadFine takes the points of the file the former run left (on disk, or in /run after a restart)
// for the ids in valid. Whatever is not readable, or older than FineSpan, is left out.
func (e *Engine) LoadFine(path string, valid map[string]bool, now time.Time) {
	f, err := ReadFine(path)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fine = fine{} // a second load replaces, it does not append
	for id, ps := range f.m {
		if !valid[id] {
			continue
		}
		for _, p := range ps {
			if p.At.Before(now.Add(-FineSpan)) || p.At.After(now) {
				continue
			}
			q := fpt{at: p.At.Unix(), ms: uint16(p.Ms)}
			if p.Fail {
				q.code = kindCode(p.Kind)
			}
			e.fine[id] = append(e.fine[id], q)
		}
	}
}
