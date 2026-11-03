// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/state"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

// withTG adds a telegram object to a state file.
func withTG(t *testing.T, sf, tg string) {
	t.Helper()
	b, _ := os.ReadFile(sf)
	s := strings.TrimSuffix(string(b), "}") + `,"telegram":` + tg + "}"
	os.WriteFile(sf, []byte(s), 0o600)
}

func hm(t time.Time) string { return t.Local().Format("15:04") }

func TestTelegramMenuValue(t *testing.T) {
	ago := time.Now().Add(-90 * time.Minute)
	ts := ago.UTC().Format(time.RFC3339)
	freshT := time.Now().Add(-5 * time.Minute)
	fresh := freshT.UTC().Format(time.RFC3339)
	cases := []struct {
		name, tg, pending, cfg, want string
		beat                         bool
		pair                         bool
	}{
		{"no token", "", "", "", "not set up", true, false},
		{"pairing", "", "", "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n", "pairing in progress", true, true},
		{"stale", "", "", "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n", "linked, daemon not running", false, false},
		{"waiting", `{"err":"timeout","err_since":"` + ts + `"}`, `,"pending":["a","b"]`, "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n", "2 waiting: not reachable since " + hm(ago), true, false},
		{"failed", `{"err":"timeout","err_since":"` + ts + `","err_at":"` + fresh + `"}`, "", "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n", "last send failed " + hm(freshT), true, false},
		{"old failure, nothing waits", `{"err":"timeout","err_since":"` + ts + `"}`, "", "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n", "linked, a send failed " + hm(ago) + ", none since", true, false},
		{"old failure after a message", `{"err":"timeout","err_since":"` + ts + `","last_ok_at":"` + ts + `"}`, "", "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n", "last message " + hm(ago) + "; a send failed " + hm(ago), true, false},
		{"ok", `{"last_ok_at":"` + ts + `"}`, "", "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n", "linked, last message " + hm(ago), true, false},
		{"nothing", "", "", "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n", "linked, nothing sent yet", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
			if c.pending != "" {
				b, _ := os.ReadFile(sf)
				os.WriteFile(sf, []byte(strings.Replace(string(b), `"last_run"`, `"x":1`+c.pending+`,"last_run"`, 1)), 0o600)
			}
			if c.tg != "" {
				withTG(t, sf, c.tg)
			}
			if c.beat {
				state.Beat(sf, time.Now())
			} else {
				state.Beat(sf, time.Now().Add(-time.Hour))
			}
			p := newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\n"+c.cfg)
			if c.pair {
				config.MarkPairing(p)
			}
			r := start(t, &App{Path: p, Size: sized})
			r.wait("Telegram")
			r.wait(c.want)
			r.send("\x03")
			r.finish()
		})
	}
}

func TestHeaderHealth(t *testing.T) {
	mk := func(rep string, tg string) *rig {
		sf := writeState(t, ent{"tcp", "b", rep, "open", 0, time.Time{}, time.Time{}})
		if tg != "" {
			withTG(t, sf, tg)
		}
		state.Beat(sf, time.Now())
		return start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\nTCP_B=h:1\nTELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n"), Size: sized})
	}
	r := mk("OK", "")
	r.wait("all OK")
	has(t, r.raw(), "1;38;5;114;48;5;234m all OK ")
	rs := check80(t, r.frame())
	has(t, rs[0], "all OK")
	r.send("\x03")
	r.finish()
	// FAIL beats an unreachable Telegram
	e := `{"err":"x","err_since":"` + time.Now().UTC().Format(time.RFC3339) + `"}`
	r = mk("FAIL", e)
	r.wait("1 FAIL")
	lacks(t, r.frame(), "Telegram not reachable")
	r.send("\x03")
	r.finish()
	r = mk("OK", e)
	r.wait("Telegram not reachable")
	has(t, r.raw(), "1;38;5;203;48;5;234m Telegram not reachable ")
	r.send("\x03")
	r.finish()
}

func TestStatusShowsValueTime(t *testing.T) {
	sf := writeState(t, ent{"cpu", "cpu", "OK", "12% (15m avg)", 0, time.Time{}, time.Time{}})
	state.Beat(sf, time.Now())
	r := start(t, &App{Path: newCfg(t, "STATE_FILE="+sf+"\n"), Size: sized})
	r.send("\r")
	r.wait("This machine")
	rs := check80(t, r.frame())
	has(t, rs[3], "[OK]", "12% (15m avg) · "+hm(time.Now().Add(-3*time.Minute)))
	r.send("\x03")
	r.finish()
}

func TestLinkingCountsAsSentMessage(t *testing.T) {
	f, srv := testfake.Start("1:a")
	defer srv.Close()
	p := aliveCfg(t, "TELEGRAM_API_URL="+srv.URL+"\n")
	r := start(t, &App{Path: p, PollWindow: time.Minute, Size: sized})
	r.send("jjjj\r")
	r.wait("Bot token")
	r.send("1:a\r")
	r.wait("waiting until ")
	_, rest, _ := strings.Cut(r.frame(), "/start ")
	f.AddMessage(777, "/start "+rest[:6])
	r.wait("Saved")
	r.send("x")
	r.wait("Menu")
	r.wait(fmt.Sprintf("linked, last message %s", hm(time.Now())))
	r.send("\x03")
	r.finish()
}

func TestDeadmanMenuValueAndHeader(t *testing.T) {
	const url = "http://127.0.0.1:9/ping-abcdef123456"
	fp := checks.URLFP(url)
	ago := time.Now().Add(-30 * time.Minute)
	ts := ago.UTC().Format(time.RFC3339)
	cases := []struct {
		name, url, meta, want, header string
		beat                          bool
	}{
		{"off", "", "", "off", "all OK", true},
		{"stale", url, "", "daemon not running", "daemon not running", false},
		{"no ping", url, "", "every 2m, no ping yet", "all OK", true},
		{"ok", url, `{"url_fp":"` + fp + `","last_ok_at":"` + ts + `"}`, "every 2m, last ping " + hm(ago), "all OK", true},
		{"failing", url, `{"url_fp":"` + fp + `","err":"HTTP 404","err_since":"` + ts + `"}`, "last ping failed " + hm(ago) + ": HTTP 404", "Dead-man ping failing", true},
		{"other url", url, `{"url_fp":"http://x/…0000","err":"HTTP 404","err_since":"` + ts + `"}`, "every 2m, no ping yet", "all OK", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sf := writeState(t, ent{"tcp", "b", "OK", "open", 0, time.Time{}, time.Time{}})
			if c.meta != "" {
				b, _ := os.ReadFile(sf)
				os.WriteFile(sf, []byte(strings.TrimSuffix(string(b), "}")+`,"deadman":`+c.meta+"}"), 0o600)
			}
			state.Beat(sf, map[bool]time.Time{true: time.Now(), false: time.Now().Add(-time.Hour)}[c.beat])
			cfg := "STATE_FILE=" + sf + "\nTCP_B=h:1\nTELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n"
			if c.url != "" {
				cfg += "DEADMAN_URL=" + c.url + "\n"
			}
			r := start(t, &App{Path: newCfg(t, cfg), Size: sized})
			r.wait("Dead-man switch")
			r.wait(c.want)
			has(t, check80(t, r.frame())[0], c.header)
			if strings.Contains(r.out.String(), "ping-abcdef123456") {
				t.Fatal("the secret part of the URL was drawn")
			}
			r.send("\x03")
			r.finish()
		})
	}
}
