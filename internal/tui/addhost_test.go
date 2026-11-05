// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DockSeed/little-watchdog/internal/config"
)

func TestAddrOf(t *testing.T) {
	for _, c := range []struct{ in, want string }{{"192.168.1.10", "192.168.1.10"}, {"nas.lan", "nas.lan"}, {"fe80::1", "[fe80::1]"}} {
		if got := addrOf(c.in); got != c.want {
			t.Errorf("addrOf(%q) = %q", c.in, got)
		}
		// the filled-in host plus a port is an address the form takes
		if w, err := config.Detect(addrOf(c.in) + ":8096"); err != nil || hostOf(w.Target) != c.in {
			t.Errorf("%q:8096: %v %v", addrOf(c.in), w, err)
		}
	}
}

func TestAddOnAGroupFillsInTheHost(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
	defer web.Close()
	port := web.URL[strings.LastIndex(web.URL, ":"):]
	p := groupCfg(t)
	r := start(t, &App{Path: p, Size: sized})
	r.send("\r")
	r.wait("127.0.0.1")
	rs := check80(t, r.frame())
	has(t, rs[6], "[OK]", "dns") // alone on h: no host line
	has(t, rs[7], "    127.0.0.1")
	has(t, rs[8], "web", "http")
	has(t, rs[9], "box", "ping")
	r.send("jjj") // cpu, dns, web, box
	r.wait("box: OK")
	r.send("a")
	r.wait("Check and save")
	waitRaw(t, r, `127\.0\.0\.1`+caretSGR+` `) // caret right after the host
	f := r.frame()
	check80(t, f)
	has(t, f, "Type a port, for example :8096, or a path.")
	r.send(port + "\rWeb two\r")
	r.wait("Saved as Web two.")
	if got := read(t, p); !strings.HasSuffix(got, "\nHTTP_WEB_TWO=http://127.0.0.1"+port+` name="Web two" mode=lenient`+"\n") {
		t.Fatalf("%q", got)
	}
	waitRaw(t, r, `127\.0\.0\.1`+caretSGR+` `) // ready for the next port on the same host
	r.send("\x1b")                             // Esc leads back to Status, not to the menu
	r.wait("Enter details")
	has(t, r.frame(), "A add", "127.0.0.1")
	r.send("\x03")
	r.finish()
}

func TestAddWithoutAHostStartsEmpty(t *testing.T) {
	p := groupCfg(t)
	r := start(t, &App{Path: p, Size: sized})
	r.send("\r")
	r.wait("cpu: OK")
	r.send("a") // cpu has no address
	r.wait("Check and save")
	if !regexp.MustCompile(`Address\s+` + caretSGR + ` `).MatchString(sgrKeep(r.raw())) {
		t.Fatalf("address not empty:\n%s", r.frame())
	}
	has(t, r.frame(), "An IP or name is pinged")
	r.send("\t\t\t\r") // Cancel leads back to Status too
	r.wait("Enter details")
	r.send("j") // dns: alone on h, its host is filled in all the same
	r.wait("dns: OK")
	r.send("A")
	r.wait("Check and save")
	waitRaw(t, r, `h`+caretSGR+` `)
	r.send("\x1b")
	r.wait("Enter details")
	r.send("\x03")
	r.finish()
}

// sgrKeep keeps the caret's SGR and drops every other one.
func sgrKeep(raw string) string {
	re := regexp.MustCompile(caretSGR)
	raw = re.ReplaceAllString(raw, "\x00")
	raw = sgrRe.ReplaceAllString(raw, "")
	return strings.ReplaceAll(raw, "\x00", "\x1b[0;1;38;5;234;48;5;231m")
}

func TestAddFromStatusIsLockedReadOnly(t *testing.T) {
	skipRoot(t)
	p := groupCfg(t)
	dir := filepath.Dir(p)
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	r := start(t, &App{Path: p, Size: sized})
	r.send("\r")
	r.wait("cpu: OK")
	r.send("jja")
	r.wait(wantGroup)
	lacks(t, r.frame(), "Check and save")
	r.send("\x03")
	r.finish()
}
