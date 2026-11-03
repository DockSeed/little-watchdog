// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// fake-telegram: local stand-in for the Bot API, for trying the TUI and the daemon
// without a real bot: set TELEGRAM_API_URL=http://ADDR in the config.
// Usage: go run ./tools/fake-telegram ADDR TOKEN
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: fake-telegram ADDR TOKEN")
	}
	f := &testfake.Telegram{Token: os.Args[2], PollHold: 30 * time.Second}
	log.Fatal(http.ListenAndServe(os.Args[1], f.Handler()))
}
