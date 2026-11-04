// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/config"
)

// sysCheck is one own-host check as the System checks screen lists it, on or off.
type sysCheck struct {
	name, typ string
	key       string // config key of the limit; 0 = off
	def       float64
	cur       float64 // limit in the config (0 = off)
	on        bool
	limit     string
}

// sysChecks lists the four own-host checks with their limits; off checks stay in the list.
func sysChecks(c *config.Config) []sysCheck {
	d := config.Defaults()
	mk := func(name, typ, key string, def, v float64, f string, a ...any) sysCheck {
		s := sysCheck{name: name, typ: typ, key: key, def: def, cur: v, on: v > 0, limit: "off"}
		if s.on {
			s.limit = fmt.Sprintf(f, a...)
		}
		return s
	}
	rows := []sysCheck{
		mk("cpu", "cpu", "CPU_MAX_PCT", d.CPUMaxPct, c.CPUMaxPct, "alarm above %g%% (%s average)", c.CPUMaxPct, every(c.CPUWindow)),
		mk("memory", "mem", "MEM_MIN_AVAIL_PCT", d.MemMinAvail, c.MemMinAvail, "alarm below %g%% available", c.MemMinAvail),
		mk("disk", "disk", "DISK_MIN_FREE_PCT", d.DiskMinFree, c.DiskMinFree, "alarm below %g%% free", c.DiskMinFree),
		mk("temperature", "temp", "TEMP_MAX_C", d.TempMaxC, c.TempMaxC, "alarm above %g C", c.TempMaxC),
	}
	if len(c.DiskPaths) == 0 { // a limit without a disk to watch checks nothing
		rows[2].on, rows[2].limit = false, "off"
	}
	return rows
}

func (m *model) sysList() {
	m.ccur, m.sysNote = 0, ""
	if m.snap.cfg == nil {
		m.flow, m.scr, m.dk, m.lines = "System checks", scBlank, dResult, []string{"Config is invalid: " + m.snap.cfgErr}
		return
	}
	m.scr = scSys
}

func (m *model) sysKey(k Key) {
	m.sysNote = ""
	if m.snap.cfg == nil {
		m.scr = scMenu
		return
	}
	rows := sysChecks(m.snap.cfg)
	m.ccur = move(k, m.ccur, len(rows))
	s := rows[m.ccur]
	switch {
	case isBack(k):
		m.scr = scMenu
	case k.Code == Enter:
		m.sysForm(s)
	case k.Code == Rune && k.R == ' ':
		tmp := m.file.Clone()
		v := "0"
		if !s.on {
			v = fmt.Sprintf("%g", s.def)
			if s.cur > 0 {
				v = fmt.Sprintf("%g", s.cur) // only the paths were missing: keep the limit
			}
			if s.typ == "disk" && len(m.snap.cfg.DiskPaths) == 0 {
				tmp.Put("DISK_PATHS", "/")
			}
		}
		tmp.Put(s.key, v)
		m.sysSave(tmp, s.name+map[bool]string{true: " is on.", false: " is off."}[!s.on])
	}
}

// sysSave writes tmp and stays on the list; only a problem opens a dialog.
func (m *model) sysSave(tmp *config.File, note string) {
	if _, err := config.Parse(strings.NewReader(tmp.Text()), false); err != nil {
		m.sysNote = "Not saved: " + err.Error()
		return
	}
	if err := tmp.Save(); err != nil {
		m.flow, m.scr, m.dk, m.lines = "System checks", scBlank, dResult, []string{fmt.Sprintf("Cannot write %s: %v", tmp.Path, err)}
		if os.IsPermission(err) {
			m.lines = append(m.lines, needGroup())
		}
		return
	}
	m.file = tmp
	m.refresh()
	m.sysNote = note + " little-wd applies it within one interval."
}

// sysForm edits the limit of s; the CPU also has its averaging window.
func (m *model) sysForm(s sysCheck) {
	c := m.snap.cfg
	d := config.Defaults()
	val := func(v, def float64) string {
		if v <= 0 {
			v = def
		}
		return fmt.Sprintf("%g", v)
	}
	var spec kindSpec
	var keys []string
	var vals []string
	switch s.typ {
	case "cpu":
		w := c.CPUWindow
		spec = kindSpec{labels: []string{"Percent", "Window"}, hints: []string{"Alarm when the CPU average is above this percent (0 = off).", "Average over this long, for example 15m. At least INTERVAL."}}
		keys, vals = []string{"CPU_MAX_PCT", "CPU_WINDOW"}, []string{val(c.CPUMaxPct, d.CPUMaxPct), every(w)}
	case "mem":
		spec = kindSpec{labels: []string{"Percent"}, hints: []string{"Alarm when less than this percent of memory is available (0 = off)."}}
		keys, vals = []string{"MEM_MIN_AVAIL_PCT"}, []string{val(c.MemMinAvail, d.MemMinAvail)}
	case "disk":
		spec = kindSpec{labels: []string{"Percent"}, hints: []string{"Alarm when less than this percent is free on any watched disk (0 = off)."}}
		keys, vals = []string{"DISK_MIN_FREE_PCT"}, []string{val(c.DiskMinFree, d.DiskMinFree)}
	default:
		spec = kindSpec{labels: []string{"Degrees C", "Sensor"}, hints: []string{"Alarm when the temperature is above this (0 = off).",
			"Optional: a temperature file under /sys. Empty = the hottest sensor."}}
		keys, vals = []string{"TEMP_MAX_C", "TEMP_SENSOR"}, []string{val(c.TempMaxC, d.TempMaxC), c.TempSensor}
	}
	spec.name = s.name
	f := m.newForm("System checks: "+s.name, []kindSpec{spec}, []string{"Save", "Cancel"}, func(f *form) string {
		tmp := m.file.Clone()
		for i, k := range keys {
			v := strings.TrimSpace(f.vals[i])
			if k == "TEMP_SENSOR" {
				tmp.Set(k, v) // empty = the hottest sensor
				continue
			}
			if v == "" {
				return spec.labels[i] + ": enter a value"
			}
			tmp.Put(k, v)
		}
		if _, err := config.Parse(strings.NewReader(tmp.Text()), false); err != nil {
			return err.Error()
		}
		m.commit(tmp, s.name+" limit saved.")
		return ""
	})
	copy(f.vals, vals)
	f.note = "0 turns the check off."
}

func (m *model) drawSys(fr *frame) string {
	c := m.snap.cfg
	if c == nil {
		fr.put(0, 2, "Config is invalid: "+m.snap.cfgErr, sBg)
		return ""
	}
	rows := sysChecks(c)
	m.ccur = min(m.ccur, len(rows)-1)
	nw := nameWidth(fr, func(yield func(string)) {
		for _, s := range rows {
			yield(s.name)
		}
	})
	fr.heads(0, []int{colMark, colName, colName + nw}, []string{"State", "Check", "Alarm"})
	for i, s := range rows {
		i := i + 1
		mk, ms, ts := "", sBg, sBg
		switch {
		case !s.on:
			mk, ms, ts = "", sRowDim, sRowDim
		default:
			seen := false
			for _, e := range m.snap.es {
				if e.Type == s.typ {
					if mk == "" || e.Reported == checks.FAIL {
						mk, ms = m.markOf(e)
					}
					seen = true
				}
			}
			if !seen && s.typ == "temp" && len(m.snap.es) > 0 {
				s.limit += " (not running: no sensor?)"
			}
		}
		if i-1 == m.ccur {
			ms = sFocus
			ts = sFocus
			if !s.on {
				ts = sFocusDim
			}
			fr.row(i, ts)
			if !s.on {
				ms = ts
			}
		}
		fr.put(i, colMark, mk, ms)
		fr.put(i, colName, fit(s.name, nw-1), ts)
		fr.put(i, colName+nw, s.limit, ts)
	}
	if m.sysNote != "" {
		return m.sysNote
	}
	return "Space turns a check on or off. A check that is off sends no alarms."
}
