// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package setup

import (
	"testing"
)

func TestIsStart(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"/start AB12CD", true},
		{"  /start   AB12CD ", true},
		{"/start@MyBot AB12CD", true},
		{"/start@mybot AB12CD", true},
		{"/start@OtherBot AB12CD", false},
		{"/start ab12cd", false},
		{"/start", false},
		{"/start AB12CD extra", false},
		{"hallo", false},
	}
	for _, c := range cases {
		if got := isStart(c.text, "MyBot", "AB12CD"); got != c.want {
			t.Errorf("isStart(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
