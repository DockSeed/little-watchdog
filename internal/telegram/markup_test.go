// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package telegram

import (
	"strings"
	"testing"

	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestMarkupHelpersEscapeHostileText(t *testing.T) {
	hostile := "<b>x</b> &amp; </code>\x1b[2J"
	for name, got := range map[string]string{
		"B": B(hostile), "Code": Code(hostile), "Msg": Msg(hostile, "t"), "Head": Head(hostile, hostile), "On": On(hostile),
	} {
		if err := testfake.CheckHTML(got); err != nil {
			t.Errorf("%s: %v: %q", name, err, got)
		}
		if strings.Contains(got, "<b>x") || strings.Count(got, "</code>") > strings.Count(got, "<code>") {
			t.Errorf("%s leaks markup: %q", name, got)
		}
	}
	if got := Head("", "no checks are set up."); got != "<b>No checks are set up.</b>" {
		t.Fatalf("%q", got)
	}
	if got := Head("box", "1 check, none failing."); got != "<b>box: 1 check, none failing.</b>" {
		t.Fatalf("%q", got)
	}
}

func TestMsg(t *testing.T) {
	if got := Msg("box", "watching 1 check."); got != "<b>box</b>: watching 1 check." {
		t.Fatalf("%q", got)
	}
	if got := Msg("", "cannot start: x"); got != "Cannot start: x" {
		t.Fatalf("%q", got)
	}
	if got := Msg("", ""); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestEscCleansControlCharsButKeepsNewlineAndTab(t *testing.T) {
	if got := Esc("a\x1bb\x7fc\u0085d\te\nf & <g> ü"); got != "a b c d\te\nf &amp; &lt;g&gt; ü" {
		t.Fatalf("%q", got)
	}
	if got := Esc("a\u202eb\u200bc\u2066d"); got != "a b c d" {
		t.Fatalf("%q", got)
	}
	if got := Esc("plain"); got != "plain" {
		t.Fatal(got)
	}
}
