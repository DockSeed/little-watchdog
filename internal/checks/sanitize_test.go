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
