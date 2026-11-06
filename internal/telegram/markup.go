// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package telegram

import (
	"strings"
	"unicode"

	"github.com/DockSeed/little-watchdog/internal/checks"
)

// Esc makes foreign or dynamic text safe for a Telegram HTML message. It does two
// things, in one pass: control characters (C0, DEL, C1) and bidi or zero-width marks
// (checks.Hidden) except newline and tab become
// spaces, then & < > are escaped. Messages are Telegram HTML: every such part goes
// through Esc, B or Code, only our own tags stay raw.
func Esc(s string) string {
	var sb strings.Builder
	for i, r := range s {
		ctl := (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) || checks.Hidden(r)
		if !ctl && r != '&' && r != '<' && r != '>' {
			if sb.Len() > 0 {
				sb.WriteRune(r)
			}
			continue
		}
		if sb.Len() == 0 {
			sb.Grow(len(s) + 8)
			sb.WriteString(s[:i])
		}
		switch {
		case ctl:
			sb.WriteByte(' ')
		case r == '&':
			sb.WriteString("&amp;")
		case r == '<':
			sb.WriteString("&lt;")
		default:
			sb.WriteString("&gt;")
		}
	}
	if sb.Len() == 0 {
		return s
	}
	return sb.String()
}

// B is s, escaped, in bold (check names, host names).
func B(s string) string { return "<b>" + Esc(s) + "</b>" }

// Code is s, escaped, in a fixed font (config paths, commands).
func Code(s string) string { return "<code>" + Esc(s) + "</code>" }

// Msg prefixes text (already HTML) with "HOST: " (host in bold); without a host the text
// stands alone, capitalised.
func Msg(host, text string) string {
	if host != "" {
		return B(host) + ": " + text
	}
	return capital(text)
}

// Head is plain text as one bold line, with "HOST: " in front when there is a host.
func Head(host, text string) string {
	if host != "" {
		text = Esc(host) + ": " + Esc(text)
	} else {
		text = capital(Esc(text))
	}
	return "<b>" + text + "</b>"
}

func capital(text string) string {
	r := []rune(text)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

// On is " on HOST" (host in bold), or "" without a host.
func On(host string) string {
	if host == "" {
		return ""
	}
	return " on " + B(host)
}
