// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import "strings"

// sty names a look; the theme turns it into SGR codes for the terminal at hand.
type sty uint8

const (
	sBg sty = iota
	sHead
	sHeadInfo
	sBox
	sCat
	sRowDim
	sRowBold
	sFocus
	sFocusDim
	sFail
	sOK
	sWarn // yellow: unstable
	sHint
	sKeys
	sKey
	sDlg
	sDbox
	sDtitle
	sDbold
	sDdim
	sDfail
	sBtn
	sBtnOn
	sCaret // the edit position in a text field
	nSty
)

// SGR per style: 256 colours, 16 colours, monochrome (NO_COLOR: bold and reverse only).
var looks = [nSty][3]string{
	sBg:       {"38;5;252;48;5;234", "37;40", ""},
	sHead:     {"1;38;5;234;48;5;80", "1;30;46", "1;7"},
	sHeadInfo: {"38;5;234;48;5;80", "30;46", "7"},
	sBox:      {"38;5;80;48;5;234", "96;40", ""},
	sCat:      {"1;38;5;80;48;5;234", "1;96;40", "1"},
	sRowDim:   {"38;5;245;48;5;234", "90;40", ""},
	sRowBold:  {"1;38;5;231;48;5;234", "1;37;40", "1"},
	sFocus:    {"38;5;234;48;5;80", "30;46", "7"},
	sFocusDim: {"38;5;252;48;5;240", "30;47", "7"},
	sFail:     {"1;38;5;203;48;5;234", "1;91;40", "1"},
	sOK:       {"1;38;5;114;48;5;234", "1;92;40", "1"},
	sWarn:     {"1;38;5;220;48;5;234", "1;93;40", "1"},
	sHint:     {"38;5;252;48;5;236", "37;40", ""},
	sKeys:     {"38;5;245;48;5;236", "90;40", ""},
	sKey:      {"1;38;5;80;48;5;236", "1;96;40", "1"},
	sDlg:      {"38;5;252;48;5;236", "37;40", ""},
	sDbox:     {"38;5;80;48;5;236", "96;40", ""},
	sDtitle:   {"1;38;5;80;48;5;236", "1;96;40", "1"},
	sDbold:    {"1;38;5;231;48;5;236", "1;37;40", "1"},
	sDdim:     {"38;5;245;48;5;236", "90;40", ""},
	sDfail:    {"1;38;5;203;48;5;236", "1;91;40", "1"},
	sBtn:      {"38;5;252;48;5;238", "30;47", ""},
	sBtnOn:    {"1;38;5;234;48;5;80", "1;30;46", "1;7"},
	sCaret:    {"1;38;5;234;48;5;231", "1;30;47", "7"},
}

// glyphs are the box lines and marks; ASCII where the terminal cannot draw Unicode.
type glyphs struct {
	tl, tr, bl, br, h, v string
	on, off              string // [✓] / [ ]
	radOn, radOff        string
	arrow, bar, updown   string
	dot                  string // between a value and its time
	good, bad            string // a check result in a form
}

type theme struct {
	depth int // 0 monochrome, 1 16 colours, 2 256 colours
	g     glyphs
}

var uniGlyphs = glyphs{"╭", "╮", "╰", "╯", "─", "│", "[✓]", "[ ]", "(•)", "( )", "›", "│", "↑↓", "·", "✓", "✗"}
var asciiGlyphs = glyphs{"+", "+", "+", "+", "-", "|", "[x]", "[ ]", "(*)", "( )", ">", "|", "^v", "-", "OK", "FAIL"}

var lowColourTerms = map[string]bool{"linux": true, "vt100": true, "vt102": true, "vt220": true, "ansi": true, "screen": true, "tmux": true, "cons25": true}

// plainTerms cannot show box drawing characters whatever the locale says.
var plainTerms = map[string]bool{"": true, "dumb": true, "vt100": true, "vt102": true, "cons25": true}

// newTheme decides colours and glyphs from NO_COLOR, TERM and the locale.
func newTheme(getenv func(string) string) theme {
	term := getenv("TERM")
	th := theme{depth: 1}
	switch {
	case getenv("NO_COLOR") != "" || term == "" || term == "dumb":
		th.depth = 0
	case lowColourTerms[term] || strings.HasPrefix(term, "vt") || strings.HasPrefix(term, "linux"):
	case getenv("COLORTERM") == "truecolor" || getenv("COLORTERM") == "24bit" || strings.Contains(term, "256"),
		strings.HasPrefix(term, "kitty") || strings.HasPrefix(term, "alacritty") || strings.HasPrefix(term, "foot") ||
			strings.HasPrefix(term, "wezterm") || strings.HasPrefix(term, "ghostty"):
		th.depth = 2
	}
	th.g = asciiGlyphs
	if utf8Locale(getenv) && !plainTerms[term] {
		th.g = uniGlyphs
	}
	return th
}

// utf8Locale: the first of LC_ALL, LC_CTYPE, LANG that is set names the character set.
func utf8Locale(getenv func(string) string) bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			v = strings.ToLower(strings.ReplaceAll(v, "-", ""))
			return strings.Contains(v, "utf8")
		}
	}
	return false
}

func (t theme) sgr(s sty) string {
	i := map[int]int{0: 2, 1: 1, 2: 0}[t.depth]
	if looks[s][i] == "" {
		return "\x1b[0m"
	}
	return "\x1b[0;" + looks[s][i] + "m"
}
