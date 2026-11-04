// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package state

import (
	"strings"
	"testing"
	"time"
)

func TestShownSince(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	start := t0.Add(time.Hour)
	if got, c := ShownSince(Entry{Since: t0}, start); !got.Equal(start) || !c {
		t.Fatal("old OK not counted from the start", got, c)
	}
	if got, c := ShownSince(Entry{Since: t0, DownSince: t0.Add(time.Minute)}, start); !got.Equal(t0.Add(time.Minute)) || c {
		t.Fatal("failing streak moved", got, c)
	}
	if got, c := ShownSince(Entry{Since: start.Add(time.Minute)}, start); !got.Equal(start.Add(time.Minute)) || c {
		t.Fatal(got, c)
	}
}

func TestWhenShowsTheYearOfAnOlderDate(t *testing.T) {
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.Local)
	if got := When(time.Date(2025, 12, 30, 9, 5, 0, 0, time.Local), now); got != "Dec 30 2025 09:05" {
		t.Fatal(got)
	}
	if got := When(time.Date(2026, 1, 1, 9, 5, 0, 0, time.Local), now); got != "Jan 1 09:05" {
		t.Fatal(got)
	}
	if out := Table([]Entry{{Name: "a", Type: "http", Reported: "OK", Since: now.Add(-time.Hour)}}, 2, time.Time{}, now); !strings.Contains(out, "09:00") {
		t.Fatal(out)
	}
}
