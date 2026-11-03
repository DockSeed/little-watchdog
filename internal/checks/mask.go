// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"net/url"
	"strings"
	"unicode"
)

// tail is the last 4 runes of s, or "" when s is too short to give any away.
func tail(s string) string {
	r := []rune(s)
	if len(r) < 12 {
		return ""
	}
	return string(r[len(r)-4:])
}

// TokenFP names a bot token without giving it away: "bot 123456789, …x7Q2".
// The part before ":" is the public bot id; of the secret part only 4 characters show.
func TokenFP(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	id, secret, ok := strings.Cut(token, ":")
	if !ok || id == "" || strings.IndexFunc(id, func(r rune) bool { return !unicode.IsDigit(r) }) >= 0 {
		if t := tail(token); t != "" {
			return "token …" + t
		}
		return "token"
	}
	if t := tail(secret); t != "" {
		return "bot " + Clean(id) + ", …" + t
	}
	return "bot " + Clean(id)
}

// URLFP names a ping URL without giving it away: "https://hc-ping.com/…37af".
// Only scheme, host and the last 4 characters of the path show; user, query and fragment never.
func URLFP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return "invalid URL"
	}
	s := Clean(u.Scheme + "://" + u.Host)
	p := strings.Trim(u.Path, "/")
	if p == "" {
		return s
	}
	return s + "/…" + tail(p)
}
