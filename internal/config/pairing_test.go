// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPairingMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.env")
	now := time.Now()
	if PairingActive(p, now) {
		t.Fatal("active without marker")
	}
	release, err := MarkPairing(p)
	if err != nil || !PairingActive(p, now) {
		t.Fatal(err)
	}
	if PairingActive(p, now.Add(time.Hour)) {
		t.Fatal("a crashed setup must not pause the daemon for good")
	}
	release()
	if PairingActive(p, now) {
		t.Fatal("marker stays")
	}
}

func TestTelegramOnlyReadsTokenFromABrokenFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.env")
	os.WriteFile(p, []byte("TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=-5\nGARBAGE\nTCP_X=zz\n"), 0o600)
	if tok, chat, api := TelegramOnly(p); tok != "1:a" || chat != -5 || api != "https://api.telegram.org" {
		t.Fatal(tok, chat, api)
	}
	os.WriteFile(p, []byte("TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\nTELEGRAM_API_URL=http://127.0.0.1:9/\nTCP_X=zz\n"), 0o600)
	if _, _, api := TelegramOnly(p); api != "http://127.0.0.1:9" {
		t.Fatal(api)
	}
}
