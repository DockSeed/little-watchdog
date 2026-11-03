// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	in := "a\x1b[31mb\nc\x7fd\u0085e\u009f" + strings.Repeat("x", 300)
	got := Sanitize(in)
	for _, r := range got {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("control rune %q left in %q", r, got)
		}
	}
	if n := len([]rune(got)); n > MaxText {
		t.Fatalf("len %d", n)
	}
	if !strings.HasPrefix(got, "a [31mb c d e ") {
		t.Fatalf("%q", got)
	}
	if Clean("ok ü") != "ok ü" {
		t.Fatal("changed clean text")
	}
}

func TestMsg(t *testing.T) {
	if got := Msg("box", "watching 1 check."); got != "box: watching 1 check." {
		t.Fatalf("%q", got)
	}
	if got := Msg("", "cannot start: x"); got != "Cannot start: x" {
		t.Fatalf("%q", got)
	}
	if got := Msg("", ""); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestFingerprints(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"123456789:fake-token-for-tests-only-x7Q2", "bot 123456789, …x7Q2"},
		{"1:a", "bot 1"},
		{"123456789:short", "bot 123456789"},
		{"fake-token-for-tests-only-x7Q2", "token …x7Q2"},
		{"abc", "token"},
		{"x1:fake-token-for-tests-only-x7Q2", "token …x7Q2"},
		{"", ""},
	} {
		if got := TokenFP(c.in); got != c.want {
			t.Errorf("TokenFP(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"https://hc-ping.com/0b5e1c47-1f2e-4b1d-9c1e-aa77d5e237af", "https://hc-ping.com/…37af"},
		{"https://hc-ping.com/0b5e1c47-1f2e-4b1d-9c1e-aa77d5e237af/fail?x=secret", "https://hc-ping.com/…fail"},
		{"https://u:pw@hc.example:8443/abc", "https://hc.example:8443/…"},
		{"https://hc-ping.com", "https://hc-ping.com"},
		{"https://hc-ping.com/", "https://hc-ping.com"},
		{"not a url", "invalid URL"},
		{"", ""},
	} {
		if got := URLFP(c.in); got != c.want {
			t.Errorf("URLFP(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
