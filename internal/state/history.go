// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package state

import (
	"math"
	"strings"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
)

// The 24 h history: one cell per 10 minute bucket, per check and per host value.
// It lives in memory and goes into the state file with the writes that are due anyway.
const (
	histStep    = 10 * time.Minute
	histBuckets = 144 // 24 h
	histMax     = 15000
	histBack    = 6 // a clock this many buckets in the past means the old cells are wrong
)

// Names of the host series.
const (
	HostCPU  = "cpu"
	HostRAM  = "ram"
	HostDisk = "disk"
	HostTemp = "temp"
	HostPSI  = "psi"
)

var hostNames = []string{HostCPU, HostRAM, HostDisk, HostTemp, HostPSI}

// History is the file form. Every series is a string of 4-character cells, the last one is
// bucket End (unix seconds / Step), the ones before it the buckets before. Characters are
// base 64 digits. Check cell: ok count, fail count (1 char each, up to 63), mean ms of the
// OK runs (2 chars, up to 4095). Host cell: mean, peak (2 chars each, tenths); "~~~~" = no value.
type History struct {
	Step   int               `json:"step"`
	End    int64             `json:"end"`
	Checks map[string]string `json:"checks,omitempty"`
	Host   map[string]string `json:"host,omitempty"`
}

type chCell struct {
	ok, fail uint16
	msSum    uint32
}

type hCell struct {
	n         uint16
	sum, peak float64
}

type hist struct {
	end    int64
	checks map[string][]chCell
	host   map[string][]hCell
}

func newHist() *hist {
	return &hist{checks: map[string][]chCell{}, host: map[string][]hCell{}}
}

func bucketOf(t time.Time) int64 { return t.Unix() / int64(histStep/time.Second) }

// advance moves the open bucket to b. A jump of the clock back by more than histBack
// buckets drops the cells (they stand for a time that did not happen); a smaller one is
// folded into the open bucket.
func (h *hist) advance(b int64) {
	switch {
	case h.end == 0:
		h.end = b
	case b < h.end-histBack:
		*h = *newHist()
		h.end = b
	case b > h.end:
		gap := b - h.end
		for id, c := range h.checks {
			h.checks[id] = grow(c, gap)
		}
		for id, c := range h.host {
			h.host[id] = grow(c, gap)
		}
		h.end = b
	}
}

func grow[T any](c []T, gap int64) []T {
	if gap >= histBuckets {
		return nil
	}
	c = append(c, make([]T, gap)...)
	if len(c) > histBuckets {
		c = c[len(c)-histBuckets:]
	}
	return c
}

// openCheck is the open cell of a check.
func (h *hist) openCheck(id string) *chCell {
	c := h.checks[id]
	if len(c) == 0 {
		c = make([]chCell, 1)
		h.checks[id] = c
	}
	return &c[len(c)-1]
}

func (h *hist) openHost(name string) *hCell {
	c := h.host[name]
	if len(c) == 0 {
		c = make([]hCell, 1)
		h.host[name] = c
	}
	return &c[len(c)-1]
}

func (h *hist) addCheck(id string, st checks.Status, took time.Duration, now time.Time) {
	if st != checks.OK && st != checks.FAIL {
		return
	}
	h.advance(bucketOf(now))
	c := h.openCheck(id)
	if st == checks.FAIL {
		c.fail = min(c.fail+1, math.MaxUint16)
		return
	}
	c.ok = min(c.ok+1, math.MaxUint16)
	c.msSum += uint32(min(max(took.Milliseconds(), 0), 4095))
}

func (h *hist) addHost(name string, v float64, now time.Time) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	h.advance(bucketOf(now))
	c := h.openHost(name)
	if c.n == 0 || v > c.peak {
		c.peak = v
	}
	c.n++
	c.sum += v
}

// Record keeps the result of a check in the history: answered service checks (count and time)
// and the host values of the system checks. A result without a measurement leaves no trace.
func (e *Engine) record(c checks.Check, r checks.Result, now time.Time) {
	if e.hist == nil {
		e.hist = newHist()
	}
	switch typ := c.Type(); {
	case isService(typ):
		e.hist.addCheck(c.ID(), r.Status, r.Took, now)
	case r.HasLevel:
		name := map[string]string{"cpu": HostCPU, "mem": HostRAM, "disk": HostDisk, "temp": HostTemp}[typ]
		if name != "" {
			e.hist.addHost(name, r.Level, now)
		}
	}
}

// SamplePSI adds the pressure of this round: the worst "some avg10" of cpu, memory and io.
// Nothing is recorded where the kernel has no PSI.
func (e *Engine) SamplePSI(now time.Time) {
	if e.PSI == nil {
		return
	}
	v, ok := e.PSI()
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hist.addHost(HostPSI, v, now)
}

const b64 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_"

func put2(sb *strings.Builder, v int) {
	sb.WriteByte(b64[v>>6&63])
	sb.WriteByte(b64[v&63])
}

var b64idx = func() (m [256]int8) {
	for i := range m {
		m[i] = -1
	}
	for i := 0; i < len(b64); i++ {
		m[b64[i]] = int8(i)
	}
	return
}()

func encCheck(cs []chCell) string {
	var sb strings.Builder
	sb.Grow(4 * len(cs))
	for _, c := range cs {
		ok, fail := int(c.ok), int(c.fail)
		for ok > 63 || fail > 63 { // keep the ratio
			ok, fail = (ok+1)/2, (fail+1)/2
		}
		ms := 0
		if c.ok > 0 {
			ms = int((c.msSum + uint32(c.ok)/2) / uint32(c.ok))
		}
		sb.WriteByte(b64[ok])
		sb.WriteByte(b64[fail])
		put2(&sb, min(ms, 4095))
	}
	return sb.String()
}

func encHost(cs []hCell) string {
	var sb strings.Builder
	sb.Grow(4 * len(cs))
	for _, c := range cs {
		if c.n == 0 {
			sb.WriteString("~~~~")
			continue
		}
		tenths := func(v float64) int { return min(max(int(math.Round(v*10)), 0), 4095) }
		put2(&sb, tenths(c.sum/float64(c.n)))
		put2(&sb, tenths(c.peak))
	}
	return sb.String()
}

// encode is the file form, at most histMax bytes: when the checks need more, the oldest buckets go.
func (h *hist) encode() *History {
	if h == nil || h.end == 0 || len(h.checks)+len(h.host) == 0 {
		return nil
	}
	n, over := histBuckets, 0
	for id := range h.checks {
		over += len(id) + 6
	}
	for name := range h.host {
		over += len(name) + 6
	}
	if per := 4 * (len(h.checks) + len(h.host)); over+per*n > histMax {
		n = (histMax - over) / per
	}
	if n < 1 {
		return nil
	}
	out := &History{Step: int(histStep / time.Second), End: h.end}
	for id, c := range h.checks {
		if len(c) == 0 {
			continue
		}
		if out.Checks == nil {
			out.Checks = map[string]string{}
		}
		out.Checks[id] = encCheck(c[max(len(c)-n, 0):])
	}
	for name, c := range h.host {
		if len(c) == 0 {
			continue
		}
		if out.Host == nil {
			out.Host = map[string]string{}
		}
		out.Host[name] = encHost(c[max(len(c)-n, 0):])
	}
	return out
}

func dec2(s string) (int, bool) {
	a, b := b64idx[s[0]], b64idx[s[1]]
	return int(a)<<6 | int(b), a >= 0 && b >= 0
}

func decCheck(s string) ([]chCell, bool) {
	if len(s)%4 != 0 {
		return nil, false
	}
	s = s[max(len(s)-4*histBuckets, 0):]
	out := make([]chCell, len(s)/4)
	for i := range out {
		c := s[4*i : 4*i+4]
		ok, fail := b64idx[c[0]], b64idx[c[1]]
		ms, good := dec2(c[2:])
		if ok < 0 || fail < 0 || !good {
			return nil, false
		}
		out[i] = chCell{ok: uint16(ok), fail: uint16(fail), msSum: uint32(ms) * uint32(ok)}
	}
	return out, true
}

func decHost(s string) ([]hCell, bool) {
	if len(s)%4 != 0 {
		return nil, false
	}
	s = s[max(len(s)-4*histBuckets, 0):]
	out := make([]hCell, len(s)/4)
	for i := range out {
		c := s[4*i : 4*i+4]
		if c == "~~~~" {
			continue
		}
		m, ok1 := dec2(c[:2])
		p, ok2 := dec2(c[2:])
		if !ok1 || !ok2 {
			return nil, false
		}
		out[i] = hCell{n: 1, sum: float64(m) / 10, peak: float64(p) / 10}
	}
	return out, true
}

// decode reads the file form. Anything it does not understand is left out; it never fails.
// Check series are kept only for ids in valid.
func decode(f *History, valid map[string]bool) *hist {
	h := newHist()
	if f == nil || f.Step != int(histStep/time.Second) || f.End <= 0 {
		return h
	}
	h.end = f.End
	for id, s := range f.Checks {
		if c, ok := decCheck(s); ok && valid[id] && len(c) > 0 {
			h.checks[id] = c
		}
	}
	for _, name := range hostNames {
		if c, ok := decHost(f.Host[name]); ok && len(c) > 0 {
			h.host[name] = c
		}
	}
	return h
}

// CheckCell is one bucket of a check: how often it answered and failed, and the mean time of the answers (ms).
type CheckCell struct{ OK, Fail, Ms int }

// HostCell is one bucket of a host value; Has is false when nothing was measured.
type HostCell struct {
	Mean, Peak float64
	Has        bool
}

// Hist is the history as the screen reads it.
type Hist struct {
	end    int64
	checks map[string][]CheckCell
	host   map[string][]HostCell
}

// Hist turns the file form into the reading form (nil when there is none).
func (f *History) Hist() *Hist {
	if f == nil {
		return nil
	}
	h := decode(f, func() map[string]bool {
		m := make(map[string]bool, len(f.Checks))
		for id := range f.Checks {
			m[id] = true
		}
		return m
	}())
	out := &Hist{end: h.end, checks: map[string][]CheckCell{}, host: map[string][]HostCell{}}
	for id, cs := range h.checks {
		v := make([]CheckCell, len(cs))
		for i, c := range cs {
			v[i] = CheckCell{int(c.ok), int(c.fail), 0}
			if c.ok > 0 {
				v[i].Ms = int(c.msSum / uint32(c.ok))
			}
		}
		out.checks[id] = v
	}
	for name, cs := range h.host {
		v := make([]HostCell, len(cs))
		for i, c := range cs {
			if c.n > 0 {
				v[i] = HostCell{c.sum / float64(c.n), c.peak, true}
			}
		}
		out.host[name] = v
	}
	return out
}

// window right-aligns cells that end at bucket end to the 24 h before now: histBuckets cells, the last is now's bucket.
func window[T any](cs []T, end int64, now time.Time) []T {
	out := make([]T, histBuckets)
	shift := bucketOf(now) - end // cells are this many buckets too far left
	for i, c := range cs {
		j := histBuckets - len(cs) + i - int(shift)
		if j >= 0 && j < histBuckets {
			out[j] = c
		}
	}
	return out
}

// Check is the 24 h of a check ending at now (histBuckets cells; empty where nothing ran).
func (h *Hist) Check(id string, now time.Time) []CheckCell {
	if h == nil || len(h.checks[id]) == 0 {
		return nil
	}
	return window(h.checks[id], h.end, now)
}

// Host is the 24 h of a host value (HostCPU and so on), like Check.
func (h *Hist) Host(name string, now time.Time) []HostCell {
	if h == nil || len(h.host[name]) == 0 {
		return nil
	}
	return window(h.host[name], h.end, now)
}

func (h *hist) checksOrNil() map[string][]chCell {
	if h == nil {
		return nil
	}
	return h.checks
}
