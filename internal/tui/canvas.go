// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"strings"

	"github.com/DockSeed/little-watchdog/internal/checks"
)

type cell struct {
	r rune
	s sty
}

// canvas is a grid of styled cells; every write is clipped to it.
type canvas struct {
	w, h int
	c    []cell
}

func newCanvas(w, h int) *canvas {
	cv := &canvas{w: max(w, 1), h: max(h, 1)}
	cv.c = make([]cell, cv.w*cv.h)
	for i := range cv.c {
		cv.c[i] = cell{' ', sBg}
	}
	return cv
}

// text writes s at x,y and returns the column after it; foreign text is cleaned first.
func (cv *canvas) text(x, y int, s string, st sty) int {
	for _, r := range checks.Clean(s) {
		if y >= 0 && y < cv.h && x >= 0 && x < cv.w {
			cv.c[y*cv.w+x] = cell{r, st}
		}
		x++
	}
	return x
}

func (cv *canvas) fill(x, y, w, h int, st sty) {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			if j >= 0 && j < cv.h && i >= 0 && i < cv.w {
				cv.c[j*cv.w+i] = cell{' ', st}
			}
		}
	}
}

// box draws a rounded frame with a centred title and clears the inside to fill.
func (cv *canvas) box(x, y, w, h int, title string, g glyphs, line, ttl, fill sty) {
	if w < 2 || h < 2 {
		return
	}
	cv.fill(x, y, w, h, fill)
	cv.text(x, y, g.tl+strings.Repeat(g.h, w-2)+g.tr, line)
	cv.text(x, y+h-1, g.bl+strings.Repeat(g.h, w-2)+g.br, line)
	for j := y + 1; j < y+h-1; j++ {
		cv.text(x, j, g.v, line)
		cv.text(x+w-1, j, g.v, line)
	}
	if title = fit(" "+title+" ", w-4); title != "" {
		cv.text(x+(w-len([]rune(title)))/2, y, title, ttl)
	}
}

// emit renders the grid as one ANSI frame, setting a style only where it changes.
func (cv *canvas) emit(th theme) string {
	var sb strings.Builder
	sb.WriteString("\x1b[H")
	for y := 0; y < cv.h; y++ {
		last := sty(255)
		for x := 0; x < cv.w; x++ {
			c := cv.c[y*cv.w+x]
			if c.s != last {
				sb.WriteString(th.sgr(c.s))
				last = c.s
			}
			sb.WriteRune(c.r)
		}
		sb.WriteString("\x1b[0m")
		if y < cv.h-1 {
			sb.WriteString("\r\n")
		}
	}
	return sb.String()
}

// fit cuts s to w runes, marking the cut with "~".
func fit(s string, w int) string {
	s = checks.Clean(s)
	if r := []rune(s); len(r) > w {
		return string(r[:max(w-1, 0)]) + "~"
	}
	return s
}

// wrap breaks s at spaces into lines of at most w runes.
func wrap(s string, w int) []string {
	var out []string
	cur := ""
	for _, word := range strings.Fields(s) {
		if cur != "" && len([]rune(cur))+1+len([]rune(word)) > w {
			out = append(out, cur)
			cur = ""
		}
		if cur != "" {
			cur += " "
		}
		cur += word
	}
	return append(out, cur)
}

// hardWrap cuts s into pieces of n runes.
func hardWrap(s string, n int) []string {
	r := []rune(checks.Clean(s))
	var out []string
	for len(r) > max(n, 1) {
		out = append(out, string(r[:max(n, 1)]))
		r = r[max(n, 1):]
	}
	return append(out, string(r))
}
