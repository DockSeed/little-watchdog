// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/state"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestSavedSaysSoWhenNoDaemonRuns(t *testing.T) {
	p := newCfg(t, "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n") // default state file: no heartbeat
	r := start(t, &App{Path: p})
	r.send("j\r")
	r.wait("Save and check")
	web := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer web.Close()
	r.send(web.URL + "\r\r")
	r.wait("start little-watchdog.")
	lacks(t, r.frame(), "applies it within one round")
	r.send("\x03")
	r.finish()
}

func TestHeaderAndStatusSayStaleDaemon(t *testing.T) {
	sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
	state.Beat(sf, time.Now().Add(-time.Hour))
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\n"), Size: sized})
	r.wait("service not running")
	has(t, r.frame(), "Status      ", "service not running")
	r.send("\x03")
	r.finish()

	// the daemon since 0.6.1 beats in the runtime directory, whatever the state file's place
	ram := t.TempDir()
	t.Setenv("RUNTIME_DIRECTORY", ram)
	state.Beat(filepath.Join(ram, "state.json"), time.Now())
	r = start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\n"), Size: sized})
	r.wait("Status      ")
	lacks(t, r.frame(), "service not running")
	r.send("\x03")
	r.finish()
}

func TestSaveRefusedWhenFileChangedUnderneath(t *testing.T) {
	p := newCfg(t, "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n")
	r := start(t, &App{Path: p})
	r.wait("Add check")
	os.WriteFile(p, []byte("TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\nHTTP_X=http://x/\n"), 0o600) // an editor
	r.send("j\r")
	r.wait("Save and check")
	web := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer web.Close()
	r.send(web.URL + "\ry\r")
	r.wait("changed on disk")
	if !strings.Contains(read(t, p), "HTTP_X") || strings.Contains(read(t, p), "HTTP_Y") {
		t.Fatal(read(t, p))
	}
	r.send("\x03")
	r.finish()
}

func TestSaveWithoutDirectorySaysHowToInstall(t *testing.T) {
	f, srv := testfake.Start("1:a")
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "nodir", "lw.env")
	r := start(t, &App{Path: p, Size: sized, APIURL: srv.URL, PollWindow: time.Minute})
	r.skipWelcome()
	r.wait("Bot token")
	r.send("1:a\r")
	r.wait("waiting until ")
	_, rest, _ := strings.Cut(r.frame(), "/start ")
	f.AddMessage(777, "/start "+rest[:6])
	r.wait("Cannot write")
	has(t, r.frame(), "install.sh")
	if _, err := os.Stat(filepath.Dir(p)); err == nil {
		t.Fatal("directory was created")
	}
	r.send("x")
	r.send("\x03")
	r.finish()
}

func TestGroupHintNamesTheExactCommand(t *testing.T) {
	skipRoot(t)
	p := newCfg(t, "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n")
	os.Chmod(p, 0)
	t.Cleanup(func() { os.Chmod(p, 0o600) })
	r := start(t, &App{Path: p, Size: sized})
	r.send("j")
	r.wait("Needs group little-wd: sudo usermod -aG little-wd alice, then log out")
	r.send("\x03")
	r.finish()
}

func TestGroupHintSaysLogInAgainWhenTheGroupIsListedButNotActive(t *testing.T) {
	skipRoot(t)
	p := newCfg(t, "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n")
	os.Chmod(p, 0)
	t.Cleanup(func() { os.Chmod(p, 0o600) })
	// /etc/group lists alice, this session lacks gid 55555
	r := startAs(t, &App{Path: p, Size: sized}, "bob,alice", "alice", []int{1000})
	r.send("j")
	r.wait("You are in the group little-wd, but this login does not know it yet")
	lacks(t, r.frame(), "usermod")
	r.send("\x03")
	r.finish()
}

func TestStatusSaysHowToStartAStoppedDaemon(t *testing.T) {
	sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}}) // state, but no heartbeat
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\n"), Size: sized})
	r.wait("sudo systemctl start little-watchdog") // hint under the menu
	r.send("\r")
	r.wait("Services")
	has(t, r.frame(), "Service not running", "sudo systemctl start little-watchdog")
	r.send("\x03")
	r.finish()
}
