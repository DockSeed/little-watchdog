// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"syscall"
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

func TestMarkersAreGroupWritableWhateverTheUmask(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.env")
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	release, err := MarkPairing(p)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := MarkLinked(p); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{PairingMarker(p), LinkedMarker(p)} {
		if st, err := os.Stat(m); err != nil || st.Mode().Perm() != 0o660 {
			t.Fatalf("%s: %v %v", m, st, err)
		}
	}
}

func TestMarkLinkedRefreshesTheTimeOfAnExistingMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.env")
	if err := MarkLinked(p); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(LinkedMarker(p), old, old)
	if err := MarkLinked(p); err != nil {
		t.Fatal(err)
	}
	if at, ok := LinkedAt(p); !ok || time.Since(at) > time.Minute {
		t.Fatalf("time not refreshed: %v", at)
	}
}

func TestMarkersNeverFollowASymlink(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.env")
	victim := filepath.Join(dir, "victim")
	os.WriteFile(victim, []byte("keep"), 0o600)
	for _, mark := range []func() error{
		func() error { return MarkLinked(p) },
		func() error { _, err := MarkPairing(p); return err },
	} {
		for _, m := range []string{LinkedMarker(p), PairingMarker(p)} {
			os.Remove(m)
			os.Symlink(victim, m)
		}
		if err := mark(); err == nil {
			t.Fatal("a symlink was accepted as marker")
		}
		if b, _ := os.ReadFile(victim); string(b) != "keep" {
			t.Fatalf("symlink target changed: %q", b)
		}
		if st, _ := os.Stat(victim); st.Mode().Perm() != 0o600 {
			t.Fatal("symlink target chmodded")
		}
	}
}

func TestTouchNeverWritesToAnExistingMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.env")
	m := LinkedMarker(p)
	os.WriteFile(m, []byte("keep"), 0o640)
	old := chtimes
	defer func() { chtimes = old }()
	chtimes = func(string, time.Time, time.Time) error { return os.ErrPermission } // foreign owner
	if err := MarkLinked(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(m); len(b) != 0 {
		t.Fatalf("old marker kept or written to: %q", b)
	}
}
