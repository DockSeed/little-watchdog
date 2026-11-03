// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	fakeTok = "123456789:fake-token-for-tests-only-x7Q2"
	fakeKey = "0b5e1c47-1f2e-4b1d-9c1e-aa77d5e237af"
)

func noSecrets(t *testing.T, r *rig, where string) {
	t.Helper()
	f := r.out.String()
	for _, s := range []string{fakeTok, "fake-token-for-tests-only-x7Q2", fakeKey, "0b5e1c47"} {
		if strings.Contains(f, s) {
			t.Fatalf("%s: secret %q was drawn", where, s)
		}
	}
}

func TestSecretsNeverFullyShown(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("OK")) }))
	defer web.Close()
	url := web.URL + "/" + fakeKey
	r := start(t, &App{Path: newCfg(t, "TELEGRAM_TOKEN="+fakeTok+"\nTELEGRAM_CHAT_ID=5\nDEADMAN_URL="+url+"\n"), Size: sized})
	r.wait("Dead-man switch")
	r.send("jjjj") // Telegram: the hint names the token
	r.wait("Linked: bot 123456789, …x7Q2, chat 5")
	r.send("j")
	r.wait("Pinging http://")
	has(t, r.frame(), "/…37af")
	// the dead-man form: current fingerprint, typed input masked
	r.send("\r")
	r.wait("[ Save ]")
	has(t, r.frame(), "current: http://", "/…37af", "leave empty to keep it", "Ctrl-R show/hide")
	check80(t, r.frame())
	r.send(url)
	r.wait("***********")
	noSecrets(t, r, "dead-man form")
	r.send("\x12") // Ctrl-R reveals what was typed
	r.wait(fakeKey)
	r.send("\x12")
	r.waitNot(fakeKey)
	// the Telegram form shows the token fingerprint
	r.send("\x1b")
	r.wait("Menu")
	r.send("k\r")
	r.wait("Bot token")
	has(t, r.frame(), "current: bot 123456789, …x7Q2 — leave empty to keep it")
	check80(t, r.frame())
	r.send(fakeTok)
	waitRaw(t, r, `\*{10}`)
	r.send("\x1b")
	r.wait("Menu")
	r.send("\x03")
	r.finish()
	s := r.out.String()
	// only the revealed frame may hold the key
	if n := strings.Count(s, fakeTok); n != 0 {
		t.Fatalf("token drawn %d times", n)
	}
}

func TestEmptyEnterKeepsTheDeadmanURL(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("OK")) }))
	defer web.Close()
	url := web.URL + "/" + fakeKey
	p := newCfg(t, "TELEGRAM_TOKEN=1:a\nDEADMAN_URL="+url+"\n")
	r := start(t, &App{Path: p, Size: sized})
	r.send("jjjjj\r")
	r.wait("[ Save ]")
	r.send("\r")
	r.wait("Saved")
	if !strings.Contains(read(t, p), "DEADMAN_URL="+url+"\n") {
		t.Fatal(read(t, p))
	}
	noSecrets(t, r, "result")
	r.send("x")
	r.send("\x03")
	r.finish()
}
