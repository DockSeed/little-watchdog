// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package tui is a small full-screen terminal UI on raw ANSI sequences (stdlib only).
package tui

import (
	"io"
	"unicode/utf8"
)

type Code int

const (
	Rune Code = iota
	Up
	Down
	Left
	Right
	Enter
	Esc
	Backspace
	Tab
	BackTab
	CtrlC
)

type Key struct {
	Code Code
	R    rune
}

// Decode turns one chunk of terminal input into keys; unknown sequences are dropped.
func Decode(b []byte) []Key {
	var ks []Key
	for len(b) > 0 {
		c := b[0]
		switch {
		case c == 0x1b && len(b) >= 3 && (b[1] == '[' || b[1] == 'O'):
			i := 2
			for i < len(b)-1 && b[i] >= 0x20 && b[i] <= 0x3f { // parameter bytes
				i++
			}
			switch b[i] {
			case 'A':
				ks = append(ks, Key{Code: Up})
			case 'B':
				ks = append(ks, Key{Code: Down})
			case 'C':
				ks = append(ks, Key{Code: Right})
			case 'D':
				ks = append(ks, Key{Code: Left})
			case 'Z':
				ks = append(ks, Key{Code: BackTab})
			}
			b = b[i+1:]
			continue
		case c == 0x1b:
			ks = append(ks, Key{Code: Esc})
		case c == '\r' || c == '\n':
			ks = append(ks, Key{Code: Enter})
		case c == 0x7f || c == 0x08:
			ks = append(ks, Key{Code: Backspace})
		case c == '\t':
			ks = append(ks, Key{Code: Tab})
		case c == 3 || c == 4:
			ks = append(ks, Key{Code: CtrlC})
		case c >= 0x20:
			r, n := utf8.DecodeRune(b)
			ks = append(ks, Key{Code: Rune, R: r})
			b = b[n:]
			continue
		}
		b = b[1:]
	}
	return ks
}

// readKeys decodes in until EOF, then closes out.
func readKeys(in io.Reader, out chan<- Key, done <-chan struct{}) {
	defer close(out)
	buf := make([]byte, 256)
	for {
		n, err := in.Read(buf)
		for _, k := range Decode(buf[:n]) {
			select {
			case out <- k:
			case <-done:
				return
			}
		}
		if err != nil {
			return
		}
	}
}
