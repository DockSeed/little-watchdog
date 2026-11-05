// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

const caretSGR = `\x1b\[0;1;38;5;234;48;5;231m`

// carets counts the caret cells in a raw frame.
func carets(raw string) (n int) { return len(regexp.MustCompile(caretSGR).FindAllString(raw, -1)) }

func addRig(t *testing.T, a *App) *rig {
	a.Size = sized
	r := start(t, a)
	r.send("j\r")
	r.wait("Check and save")
	return r
}

func TestCaretInEmptyFieldAfterTypingAndMovingLeft(t *testing.T) {
	r := addRig(t, &App{Path: newCfg(t, "TELEGRAM_TOKEN=1:a\n")})
	raw := r.raw()
	if carets(raw) != 1 || !regexp.MustCompile(caretSGR+` \x1b`).MatchString(raw) {
		t.Fatalf("no caret cell in the empty field:\n%q", raw)
	}
	r.send("abc")
	r.wait("abc ")
	if !regexp.MustCompile(`abc` + caretSGR + ` `).MatchString(r.raw()) {
		t.Fatal("caret not after the typed text")
	}
	r.send("\x1b[D")
	waitRaw(t, r, `ab`+caretSGR+`c`)
	r.send("\x1b[D\x1b[D\x1b[D") // stops at the start
	waitRaw(t, r, caretSGR+`a`)
	r.send("\x1b[F") // End
	waitRaw(t, r, `abc`+caretSGR+` `)
	r.send("\x1b[H\x1b[C\x7f") // Home, Right, Backspace removes a
	waitRaw(t, r, caretSGR+`b`)
	r.send("\x1b[3~") // Delete removes b
	waitRaw(t, r, caretSGR+`c`)
	r.send("X") // inserted at the caret
	waitRaw(t, r, `X`+caretSGR+`c`)
	check80(t, r.frame())
	r.send("\x03")
	r.finish()
}

func TestCaretOnlyInTheFocusedField(t *testing.T) {
	r := addRig(t, &App{Path: newCfg(t, "TELEGRAM_TOKEN=1:a\n")})
	r.send("ab\x1b[B") // Name field focused, Address unfocused
	r.wait("Optional, any text")
	if carets(r.raw()) != 1 || regexp.MustCompile(`ab`+caretSGR).MatchString(r.raw()) {
		t.Fatalf("caret stays in the Address field: %q", r.raw())
	}
	r.send("\x1b[B") // Check and save: a button
	r.wait("[ Check and save ]")
	for i := 0; i < 400 && carets(r.raw()) != 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := carets(r.raw()); n != 0 {
		t.Fatalf("%d carets while a button has the focus", n)
	}
	r.send("\x03")
	r.finish()
}

func TestCaretInSixteenColoursAndAscii(t *testing.T) {
	r := addRig(t, &App{Path: newCfg(t, "TELEGRAM_TOKEN=1:a\n"), Getenv: env("TERM", "linux", "LANG", "C.UTF-8")})
	r.send("ab\x1b[D")
	waitRaw(t, r, `a\x1b\[0;1;30;47mb`)
	r.send("\x03")
	r.finish()
	r = addRig(t, &App{Path: newCfg(t, "TELEGRAM_TOKEN=1:a\n"), Getenv: env("TERM", "dumb")})
	r.send("ab\x1b[D")
	waitRaw(t, r, `a\x1b\[0;7mb`)
	check80(t, r.frame())
	r.send("\x03")
	r.finish()
}

func waitRaw(t *testing.T, r *rig, pat string) {
	t.Helper()
	re := regexp.MustCompile(pat)
	for i := 0; i < 400; i++ {
		if re.MatchString(r.raw()) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never saw %q in:\n%q", pat, strings.ReplaceAll(r.raw(), "\r\n", "\n"))
}
