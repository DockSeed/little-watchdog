// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestWelcomeOnFirstStartOnly(t *testing.T) {
	r := start(t, &App{Path: etcPath(t), Size: sized})
	r.wait("Welcome to")
	f := r.frame()
	rs := check80(t, f)
	has(t, f, "Little Watchdog", "What should it watch first?", "Enter continue", "Esc skip", "little-wd  › Welcome", "⣀")
	has(t, rs[0], "little-wd")
	lacks(t, f, "Bot token", "Address")
	r.send("\x03")
	r.finish()
	for _, body := range map[string]string{"checks": "PING_A=192.0.2.1\n", "empty file": "\n"} {
		r := start(t, &App{Path: newCfg(t, body), Size: sized})
		r.wait("Dead-man switch")
		lacks(t, r.frame(), "Welcome to", "⣀")
		r.send("\x03")
		r.finish()
	}
}

func TestWelcomeLeadsToAddressThenTelegram(t *testing.T) {
	p := etcPath(t)
	r := start(t, &App{Path: p, Size: sized})
	r.wait("Welcome to")
	r.send("\r")
	r.wait("Check and save")
	has(t, r.frame(), "Address")
	r.send("127.0.0.1\r\r") // address, name; the ping may fail, the check stays saved
	r.wait("Saved as")
	if got := read(t, p); !strings.Contains(got, "PING_") {
		t.Fatalf("nothing saved:\n%s", got)
	}
	r.send("\x1b")
	r.wait("Bot token") // Telegram setup, as before
	r.send("\x1b")
	r.wait("Dead-man switch")
	has(t, r.frame(), "Status")
	lacks(t, r.frame(), "Welcome to")
	r.send("q")
	r.finish()
}

func TestWelcomeSkipAndCancelGoToTelegram(t *testing.T) {
	r := start(t, &App{Path: etcPath(t), Size: sized})
	r.skipWelcome()
	r.wait("Bot token")
	r.send("\x03")
	r.finish()
	r = start(t, &App{Path: etcPath(t), Size: sized})
	r.wait("Welcome to")
	r.send("\r")
	r.wait("Check and save")
	r.send("\x1b") // Esc in the address form
	r.wait("Bot token")
	r.send("\x03")
	r.finish()
}

func TestWelcomeFits80x24(t *testing.T) {
	for name, e := range map[string]func(string) string{
		"utf8":  env("TERM", "xterm-256color", "LANG", "C.UTF-8"),
		"ascii": env("TERM", "xterm-256color", "LANG", "C"),
		"vt100": env("TERM", "vt100", "LANG", "C.UTF-8"),
	} {
		t.Run(name, func(t *testing.T) {
			r := start(t, &App{Path: etcPath(t), Size: sized, Getenv: e})
			r.wait("Little Watchdog")
			f := r.frame()
			rs := check80(t, f)
			has(t, f, "Welcome to", "stops answering.", "Press Enter and give it an address.")
			if name == "utf8" {
				has(t, f, "⣀")
			} else {
				for _, l := range rs { // no dog, and no braille either
					if !isASCII(l) {
						t.Fatalf("not ASCII: %q", l)
					}
				}
			}
			dogRows := 0
			for _, l := range rs[2:20] {
				if strings.HasPrefix(l, "│ ") || strings.HasPrefix(l, "| ") {
					dogRows++
				}
			}
			if dogRows != 17 {
				t.Fatalf("inner rows: %d\n%s", dogRows, f)
			}
			r.send("\x03")
			r.finish()
		})
	}
}

func isASCII(s string) bool { return utf8.RuneCountInString(s) == len(s) }

func TestDogArtIsOneCellWide(t *testing.T) {
	for _, art := range [][]string{dogUni} {
		if len(art) != 9 {
			t.Fatalf("%d rows", len(art))
		}
		for _, l := range art {
			if n := utf8.RuneCountInString(l); n > dogMaxW {
				t.Fatalf("%d cells: %q", n, l)
			}
			for _, r := range l {
				// a space, or braille: one cell wide in every terminal that draws it
				if r != ' ' && (r < 0x2801 || r > 0x28ff) {
					t.Fatalf("%q in %q", r, l)
				}
			}
		}
	}
}

func TestNoDogWithoutUTF8(t *testing.T) {
	for _, e := range []func(string) string{
		env("TERM", "xterm-256color", "LANG", "C"),
		env("TERM", "vt100", "LANG", "C.UTF-8"),
		env("TERM", "linux", "LANG", "C.UTF-8"), // the console has no braille glyphs
		env("TERM", "xterm-256color", "LANG", "C.UTF-8", "NO_COLOR", "1"),
	} {
		th := newTheme(e)
		if (th.dog() != nil) != (e("LANG") == "C.UTF-8" && e("TERM") == "xterm-256color") {
			t.Fatalf("TERM %s LANG %s: dog %v", e("TERM"), e("LANG"), th.dog() != nil)
		}
	}
}

func TestWelcomeSmallTerminalDropsTheDog(t *testing.T) {
	small := func() (int, int) { return 60, 14 }
	r := start(t, &App{Path: etcPath(t), Size: small})
	r.wait("Little Watchdog")
	lacks(t, r.frame(), "⣀")
	has(t, r.frame(), "What should it watch first?")
	r.send("\x03")
	r.finish()
}

func TestDogOnlyOnTheWelcomeScreen(t *testing.T) {
	r := start(t, &App{Path: fixture(t), Size: sized})
	r.wait("Dead-man switch")
	for _, keys := range []string{"", "\r", "\x1b?", "\x1b"} {
		r.send(keys)
		time.Sleep(100 * time.Millisecond)
		lacks(t, r.frame(), "⣀", "⡇")
	}
	r.send("\x03")
	r.finish()
}

func TestDogIsAsTallAsTheText(t *testing.T) {
	r := start(t, &App{Path: etcPath(t), Size: sized})
	r.wait("Little Watchdog")
	rs := check80(t, r.frame())
	first, last := -1, -1
	for i, l := range rs {
		if strings.Contains(l, "Welcome to") {
			first = i
		}
		if strings.Contains(l, "Press Enter and give it an address.") {
			last = i
		}
	}
	if first < 0 || last-first+1 != len(dogUni) {
		t.Fatalf("text rows %d..%d, dog %d rows\n%s", first, last, len(dogUni), r.frame())
	}
	if !strings.ContainsAny(rs[first], "⣿⣾⣼⣀") || !strings.ContainsAny(rs[last], "⣿⠻⡆⡇⠿") {
		t.Fatalf("dog does not span the text:\n%s", r.frame())
	}
	r.send("\x03")
	r.finish()
}
