// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The example file must stay valid when a user removes the # of any line.
func TestExampleConfigIsValidWithEveryLineActive(t *testing.T) {
	b, err := os.ReadFile("../../contrib/little-watchdog.env.example")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^#([A-Z_]+=)`)
	if _, err := Parse(strings.NewReader(re.ReplaceAllString(string(b), "$1")), true); err != nil {
		t.Fatal(err)
	}
}
