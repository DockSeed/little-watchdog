// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/state"
)

func TestHostOf(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"192.168.1.10", "192.168.1.10"},
		{"192.168.1.10:8096", "192.168.1.10"},
		{"http://192.168.1.10:2283/", "192.168.1.10"},
		{"https://192.168.1.10/health?x=1", "192.168.1.10"},
		{"http://user:pw@nas.lan:5000/a/b", "nas.lan"},
		{"NAS.lan", "nas.lan"},
		{"nas.lan.", "nas.lan"},
		{"nas.lan:22", "nas.lan"},
		{"https://Example.org", "example.org"},
		{"fe80::1", "fe80::1"},
		{"[fe80::1]", "fe80::1"},
		{"[fe80::1]:22", "fe80::1"},
		{"http://[2001:db8::5]:8080/x", "2001:db8::5"},
		{"https://[2001:db8::5]/", "2001:db8::5"},
		{"", ""},
	} {
		if got := hostOf(c.in); got != c.want {
			t.Errorf("hostOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// rowText is the rows as text: "# head", "@ host", or the entry's name.
func rowText(rows []srow, ents []state.Entry) (out []string) {
	for _, r := range rows {
		switch {
		case r.host:
			out = append(out, "@ "+r.hdr)
		case r.hdr != "":
			out = append(out, "# "+r.hdr)
		default:
			out = append(out, ents[r.ent].Name)
		}
	}
	return
}

func TestBuildRowsGroupsByHost(t *testing.T) {
	e := func(typ, name string, rep checks.Status) state.Entry {
		return state.Entry{ID: typ + ":" + name, Type: typ, Name: name, Reported: rep}
	}
	es := []state.Entry{ // in state file order: type, then name
		e("cpu", "cpu", checks.OK),
		e("http", "immich", checks.OK), e("http", "jellyfin", checks.OK), e("http", "router web", checks.OK),
		e("http", "site", checks.OK), e("http", "v6 web", checks.OK),
		e("ping", "nas", checks.OK), e("ping", "router", checks.OK), e("ping", "v6", checks.OK),
		e("tcp", "gone", checks.OK), e("tcp", "ssh", checks.OK),
	}
	hosts := hostsOf(&config.Config{Checks: []config.CheckSpec{
		{Type: "http", Name: "immich", Target: "http://192.168.1.10:2283/"},
		{Type: "http", Name: "jellyfin", Target: "http://192.168.1.10:8096"},
		{Type: "http", Name: "router web", Target: "http://192.168.1.9/"},
		{Type: "http", Name: "site", Target: "https://example.org/health"},
		{Type: "http", Name: "v6 web", Target: "http://[fe80::1]:80/"},
		{Type: "ping", Name: "nas", Target: "192.168.1.10"},
		{Type: "ping", Name: "router", Target: "192.168.1.9"},
		{Type: "ping", Name: "v6", Target: "fe80::1"},
		{Type: "tcp", Name: "ssh", Target: "NAS.lan:22"}, // alone on its host
		// tcp:gone is in the state only: no host, it stays with the single checks
	}})
	rows, ents := buildRows(es, hosts)
	want := []string{
		"# This machine", "cpu",
		"# Services", "site", "gone", "ssh",
		"@ 192.168.1.9", "router web", "router",
		"@ 192.168.1.10", "immich", "jellyfin", "nas",
		"@ fe80::1", "v6 web", "v6",
	}
	if got := rowText(rows, ents); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n%q\nwant\n%q", got, want)
	}
	// a state change moves nothing
	for i := range es {
		es[i].Reported, es[i].Fails = checks.FAIL, 3
	}
	rows2, ents2 := buildRows(es, hosts)
	if got := rowText(rows2, ents2); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows moved after a state change:\n%q", got)
	}
}

// groupCfg: two checks on 127.0.0.1, one alone on h, and cpu.
func groupCfg(t *testing.T) string {
	sf := writeState(t,
		ent{"cpu", "cpu", "OK", "5% (20s avg)", 0, time.Time{}, time.Time{}},
		ent{"ping", "box", "OK", "ping answers, 1 ms", 0, time.Time{}, time.Time{}},
		ent{"http", "web", "OK", "web 200, 2 ms", 0, time.Time{}, time.Time{}},
		ent{"tcp", "dns", "OK", "port open, 0 ms", 0, time.Time{}, time.Time{}},
	)
	state.Beat(sf, time.Now())
	return newCfg(t, "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\nSTATE_FILE="+sf+
		"\nPING_BOX=127.0.0.1\nHTTP_WEB=http://127.0.0.1:1/ name=\"web\" mode=lenient\nTCP_DNS=h:53\n")
}

func TestGroupsWithThePanel(t *testing.T) {
	r := start(t, &App{Path: groupCfg(t), Size: func() (int, int) { return 130, 30 }, Proc: fakeProc(t, true)})
	r.send("\r")
	r.wait("Last result")
	r.wait("load 1/5/15 min")
	rs := checkSize(t, r.frame(), 130, 30)
	tw, _ := panelSplit(130, 128)
	has(t, rs[7], "    127.0.0.1")
	if col(rs[7], 1+tw) != "│" {
		t.Fatalf("separator gone on the host line: %q", rs[7])
	}
	r.send("jj")
	r.wait("Address   http://127.0.0.1:1/")
	r.send("\x03")
	r.finish()
}

// With many checks the list scrolls; the host line of the first group shows at the top.
func TestGroupsScroll(t *testing.T) {
	var es []ent
	var cfg strings.Builder
	for i := range 30 { // c00 .. c29 on 10.0.0.1 to 10.0.0.3 by turns
		n := fmt.Sprintf("c%02d", i)
		es = append(es, ent{"http", n, "OK", "answers", 0, time.Time{}, time.Time{}})
		fmt.Fprintf(&cfg, "HTTP_%s=http://10.0.0.%d:80/\n", strings.ToUpper(n), i%3+1)
	}
	sf := writeState(t, es...)
	state.Beat(sf, time.Now())
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\n"+cfg.String()), Size: sized})
	r.send("\r")
	r.wait("10.0.0.1")
	rs := check80(t, r.frame())
	has(t, rs[3], "Services")
	has(t, rs[4], "    10.0.0.1")
	r.send(strings.Repeat("j", 29))
	r.wait("c29: OK") // the last check of 10.0.0.3, at the bottom of the window
	rs = check80(t, r.frame())
	has(t, rs[18], "c29")
	has(t, r.frame(), "    10.0.0.3")
	lacks(t, r.frame(), "Services")
	r.send(strings.Repeat("k", 29))
	r.wait("c00: OK")
	rs = check80(t, r.frame())
	has(t, rs[3], "Services")
	has(t, rs[4], "    10.0.0.1")
	r.send("\x03")
	r.finish()
}
