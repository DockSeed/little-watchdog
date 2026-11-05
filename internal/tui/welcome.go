// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

// The dog on the welcome screen; it is drawn nowhere else. Braille dots (one cell
// wide) fill the silhouette of a sitting dog in profile, as tall as the text next
// to it. Without UTF-8 there is no dog: ASCII art of this kind only looks worse than none.
//
// The shape is traced from a CC0 icon, so the repository stays free of licence questions.
// Source: https://commons.wikimedia.org/wiki/File:Dog_sitting_Pinhead_icon.svg
// Author: Quincy Morgan (Pinhead icon set, https://github.com/waysidemapping/pinhead). Licence: CC0 1.0.
var dogUni = []string{
	"            ⢠⣾⡇",
	"           ⢠⣿⣿⣷⣤⣀",
	"          ⢠⣿⣿⣿⣿⣿⣿⣿⣶⣤",
	"       ⢀⣤⣾⣿⣿⣿⣿⣿⣿⠿⠿⠿⠋",
	"     ⣠⣾⣿⣿⣿⣿⣿⣿⣿⣿⠃",
	"⢀⣴  ⣼⣿⣿⣿⣿⣿⣿⣿⣿⣿⡇",
	"⣿⣿⡀⢸⣿⣿⣿⣿⣿⣿⠟⠛⣿⣿⡇",
	"⢻⣿⣷⣾⣿⣿⣿⣿⣿⡏  ⣿⣿⣇",
	" ⠙⠿⣿⣿⣿⣿⣿⣿⣷⡆ ⠻⣿⣿⣷",
}

// dogMaxW is the width of the art; the layout reserves it.
const dogMaxW = 20

func (t theme) dog() []string {
	if t.dots {
		return dogUni
	}
	return nil
}

const welcomeText = "It keeps an eye on the machines and services you name and tells you on Telegram when one stops answering."

// drawWelcome shows the greeting; the dog stands left of the text and is left out
// where the terminal has no room for both.
func (m *model) drawWelcome(fr *frame) {
	const gap = 4
	textW := min(fr.iw-dogMaxW-gap-2, 36)
	art := m.th.dog()
	dog := art != nil && textW >= 24 && fr.ih >= len(art)
	rows := []string{"Welcome to", "Little Watchdog", ""}
	if !dog {
		textW = min(fr.iw-4, 60)
	}
	rows = append(rows, wrap(welcomeText, textW)...)
	rows = append(rows, "", "What should it watch first?", "Press Enter and give it an address.")
	w := textW
	if dog {
		w += dogMaxW + gap
	}
	x := max((fr.iw-w)/2, 0)
	top := max((fr.ih-max(len(rows), len(art)))/2, 0)
	tx := x
	if dog {
		tx = x + dogMaxW + gap
		for i, l := range art {
			fr.put(top+i, x, l, sBox)
		}
	}
	for i, l := range rows {
		st := sBg
		switch i {
		case 0:
			st = sRowDim
		case 1:
			st = sCat
		case len(rows) - 2:
			st = sRowBold
		}
		fr.put(top+i, tx, l, st)
	}
}
