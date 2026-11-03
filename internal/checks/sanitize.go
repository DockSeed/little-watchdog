// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import "strings"

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
