// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"os"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestTelegramUsesTheConfigsAPIURLAndMarksPairing(t *testing.T) {
	f, srv := testfake.Start("1:a")
	defer srv.Close()
	p := newCfg(t, "TELEGRAM_API_URL="+srv.URL+"\n")
	r := start(t, &App{Path: p, PollWindow: time.Minute}) // no App.APIURL: the URL must come from the file
	r.send("jjjjj\r")
	r.wait("Bot token")
	r.send("1:a\r")
	r.wait("waiting until ")
	// nothing redraws while the code waits, or the terminal drops a mouse selection
	n := len(r.out.String())
	time.Sleep(1200 * time.Millisecond)
	if m := len(r.out.String()); m != n {
		t.Fatalf("screen redrawn while waiting for the code (%d new bytes)", m-n)
	}
	if !config.PairingActive(p, time.Now()) {
		t.Fatal("daemon is not asked to step aside")
	}
	_ = f
	r.send("\x1b")
	r.wait("Cancelled")
	waitGone(t, config.PairingMarker(p))
	r.send("x")
	r.send("\x03")
	r.finish()
}

func waitGone(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if _, err := os.Stat(path); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s stays", path)
}
