// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
)

// MaxText is the longest detail or value that is stored, sent or shown.
const MaxText = 200

// Clean replaces control characters (C0, DEL, C1) so foreign text cannot drive a terminal.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return ' '
		}
		return r
	}, s)
}

// Sanitize cleans s and cuts it to MaxText characters.
func Sanitize(s string) string {
	s = Clean(s)
	if r := []rune(s); len(r) > MaxText {
		return string(r[:MaxText-1]) + "…"
	}
	return s
}

// Host is this machine's name, cleaned; empty when unknown.
func Host() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(Sanitize(h))
}

// Msg prefixes text with "HOST: "; without a host the text stands alone, capitalised.
func Msg(host, text string) string {
	if host != "" {
		return host + ": " + text
	}
	r := []rune(text)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

// On is " on HOST", or "" without a host.
func On(host string) string {
	if host == "" {
		return ""
	}
	return " on " + host
}

// Dur renders a duration for people: "45 s", "3 min", "1 h 30 min".
func Dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour && d%time.Minute == 0:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < time.Hour:
		return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

// Count is "1 check" or "N checks".
func Count(n int) string {
	if n == 1 {
		return "1 check"
	}
	return fmt.Sprintf("%d checks", n)
}
