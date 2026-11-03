// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/state"
)

// shaky writes a state with one unstable, one recovering, one failing and one fine check.
func shaky(t *testing.T) string {
	sf := writeState(t,
		ent{"http", "nas", "OK", "no answer within 10 s", 2, time.Time{}, time.Time{}},
		ent{"http", "photos", "FAIL", "HTTP 502", 3, time.Now().Add(-time.Hour), time.Time{}},
		ent{"tcp", "dns", "OK", "open in 0ms", 0, time.Time{}, time.Time{}},
	)
	b, _ := os.ReadFile(sf)
	s := strings.Replace(string(b), `"fails":3,"value":"HTTP 502"`, `"fails":0,"oks":1,"value":"HTTP 502"`, 1)
	s = strings.Replace(s, `"fails":2,"value":"no answer within 10 s"`, `"fails":2,"value":"x","detail":"no answer within 10 s"`, 1)
	os.WriteFile(sf, []byte(s), 0o600)
	state.Beat(sf, time.Now())
	return sf
}

func TestStatusListShowsIntermediateStates(t *testing.T) {
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+shaky(t)+"\nHTTP_NAS=http://b/\nHTTP_PHOTOS=http://p/\nTCP_DNS=h:53\n"), Size: sized})
	r.wait("Dead-man switch")
	rs := check80(t, r.frame())
	has(t, rs[2], "Status", "1 FAIL: photos, 1 unstable")
	has(t, rs[0], "1 FAIL")
	r.send("\r")
	r.wait("Services")
	f := r.frame()
	check80(t, f)
	has(t, f, "[2/3]  nas", "no answer within 10 s", "[FAIL] photos", "back 1/2", "HTTP 502", "[OK]")
	r.send("jj") // the bar moves off the rows to look at
	r.wait("dns: OK")
	raw := r.raw()
	has(t, raw, "1;38;5;220;48;5;234m[2/3]", "1;38;5;220;48;5;234mback 1/2", "1;38;5;203;48;5;234m[FAIL]")
	r.send("\x03")
	r.finish()
}

func TestUnstableMenuValueAndHeader(t *testing.T) {
	sf := writeState(t,
		ent{"http", "nas", "OK", "x", 1, time.Time{}, time.Time{}},
		ent{"tcp", "dns", "OK", "open", 0, time.Time{}, time.Time{}},
		ent{"tcp", "dns2", "OK", "open", 0, time.Time{}, time.Time{}},
		ent{"tcp", "dns3", "OK", "open", 0, time.Time{}, time.Time{}})
	state.Beat(sf, time.Now())
	p := newCfg(t, "STATE_FILE="+sf+"\nHTTP_NAS=http://b/\nTCP_DNS=h:53\n")
	for _, c := range []struct{ term, sgr string }{{"xterm-256color", "1;38;5;220;48;5;234m 1 unstable"}, {"linux", "1;93;40m 1 unstable"}} {
		r := start(t, &App{Path: p, Size: sized, Getenv: env("TERM", c.term, "LANG", "C.UTF-8")})
		r.wait("3 OK, 1 unstable: nas")
		has(t, check80(t, r.frame())[0], "1 unstable")
		has(t, r.raw(), c.sgr)
		r.send("\x03")
		r.finish()
	}
	r := start(t, &App{Path: p, Size: sized, Getenv: env("TERM", "dumb")})
	r.wait("3 OK, 1 unstable: nas")
	r.send("\r")
	r.wait("[1/3]  nas")
	r.send("\x03")
	r.finish()
}
