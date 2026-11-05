// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/state"
)

const b64 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_"

// chSeries is a check series of the state file: n cells from f(i) = ok, fail, ms.
func chSeries(n int, f func(i int) (ok, fail, ms int)) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		ok, fail, ms := f(i)
		sb.WriteString(string([]byte{b64[ok], b64[fail], b64[ms>>6], b64[ms&63]}))
	}
	return sb.String()
}

// hSeries is a host series: f(i) = mean, peak in tenths.
func hSeries(n int, f func(i int) (mean, peak int)) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		m, p := f(i)
		sb.WriteString(string([]byte{b64[m>>6], b64[m&63], b64[p>>6], b64[p&63]}))
	}
	return sb.String()
}

// histFixture is fixture with 24 h of history: photos fails the last 2 h, cpu is busy at the start.
func histFixture(t *testing.T) string {
	t.Helper()
	cfg := fixture(t)
	var sf string
	for _, l := range strings.Split(read(t, cfg), "\n") {
		if v, ok := strings.CutPrefix(l, "STATE_FILE="); ok {
			sf = v
		}
	}
	end := time.Now().Unix() / 600
	h := fmt.Sprintf(`"history":{"step":600,"end":%d,"checks":{"http:nextcloud":%q,"http:photos":%q},"host":{"cpu":%q,"ram":%q,"disk":%q,"temp":%q,"psi":%q}},`,
		end,
		chSeries(144, func(i int) (int, int, int) { return 10, 0, 20 + i%30 }),
		chSeries(144, func(i int) (int, int, int) {
			if i >= 132 {
				return 0, 10, 0
			}
			return 10, 0, 100
		}),
		hSeries(144, func(i int) (int, int) { return 100 + i, 200 + 4*i }),
		hSeries(144, func(i int) (int, int) { return 500, 520 }),
		hSeries(144, func(i int) (int, int) { return 230, 230 }),
		hSeries(144, func(i int) (int, int) { return 400, 450 + i }),
		hSeries(144, func(i int) (int, int) { return 5, 50 }),
	)
	os.WriteFile(sf, []byte(strings.Replace(read(t, sf), `{"version":1,`, `{"version":2,`+h, 1)), 0o600)
	return cfg
}

func TestSparkUnits(t *testing.T) {
	for room, want := range map[int]int{100: 48, 48: 48, 47: 36, 24: 24, 23: 18, 12: 12, 6: 6, 5: 0, -3: 0} {
		if got := sparkWidth(room); got != want {
			t.Errorf("room %d: %d, want %d", room, got, want)
		}
	}
	if per(24) != "1 h" || per(48) != "30 min" || per(12) != "2 h" || per(16) != "1.5 h" || per(6) != "4 h" {
		t.Fatal(per(24), per(48), per(12), per(16), per(6))
	}
	if level(0, 0, 100) != '▁' || level(100, 0, 100) != '█' || level(50, 0, 100) != '▅' || level(5, 5, 5) != '▁' {
		t.Fatal("level")
	}
	cs := make([]state.CheckCell, 144)
	for i := range cs {
		cs[i] = state.CheckCell{OK: 10, Ms: 10}
	}
	cs[143] = state.CheckCell{OK: 9, Fail: 1, Ms: 10}
	cs[0] = state.CheckCell{}
	segs, share, ok := upCurve(cs, 144)
	if !ok || share >= 1 || segs[0].s != " " || segs[len(segs)-1].st != sFail || !strings.HasSuffix(segs[len(segs)-1].s, "▇") {
		t.Fatalf("%+v %v", segs, share) // one failure never draws a full bar
	}
	if s, avg, ok := timeCurve(cs, 24); !ok || avg != 10 || []rune(s)[5] != '█' {
		t.Fatal(s, avg)
	}
	if _, _, ok := hostCurve(make([]state.HostCell, 144), 24, 0, 100); ok {
		t.Fatal("curve without a measurement")
	}
	if s, top, _ := hostCurve([]state.HostCell{{Mean: 1, Peak: 2, Has: true}, {}, {Mean: 3, Peak: 4, Has: true}, {Mean: 1, Peak: 1, Has: true}}, 2, 0, 10); top != 4 || s != "▂▄" {
		t.Fatal(s, top)
	}
}

func TestStatusPanelCurves(t *testing.T) {
	r := start(t, &App{Path: histFixture(t), Size: func() (int, int) { return 130, 40 }, Proc: fakeProc(t, true)})
	r.send("\r")
	r.wait("Last result")
	r.wait("Last 24 h, peak per")
	f := r.frame()
	rs := checkSize(t, f, 130, 40)
	has(t, f, "  CPU    ▂", "  RAM    ▅", "  disk   ▂", "  temp   ▄", "  waited ▅", "peak 77%", "peak 52%", "peak 23%", "peak 59 C", "peak 5.0%")
	has(t, f, "24 h      ▂") // the cursor is on cpu: its host curve
	r.send("jjjjj")
	r.wait("Up 24 h")
	f = r.frame()
	has(t, f, "Type      http", "Up 24 h   ", "Time 24 h ", " avg 3", "100%")
	lacks(t, f, "Type      cpu")
	r.send("j") // photos: failing since two hours
	r.wait("Target    http://192.0.2.12:2283/")
	f = r.frame()
	has(t, f, "Up 24 h   ", "91.6%", "Time 24 h ", "avg 100 ms")
	has(t, r.raw(), "▇")
	for _, row := range rs {
		if n := len([]rune(row)); n != 130 {
			t.Fatalf("%d cells: %q", n, row)
		}
	}
	r.send("\x03")
	r.finish()
}

// At 120 columns the curves are narrower and nothing runs over the edge; at 24 rows they give way.
func TestStatusPanelCurvesSizes(t *testing.T) {
	for _, c := range []struct {
		w, h   int
		curves bool
	}{{120, 40, true}, {120, 32, true}, {160, 40, true}, {120, 24, false}, {130, 22, false}} {
		r := start(t, &App{Path: histFixture(t), Size: func() (int, int) { return c.w, c.h }, Proc: fakeProc(t, true)})
		r.send("\r")
		r.wait("Last result")
		r.wait("load 1/5/15 min")
		f := r.frame()
		checkSize(t, f, c.w, c.h)
		if strings.Contains(f, "Last 24 h") != c.curves {
			t.Fatalf("%dx%d: curves %v\n%s", c.w, c.h, c.curves, f)
		}
		if !c.curves && c.h == 24 { // the old layout is all there
			has(t, f, "right now", "waited", "  disk ", "up since boot")
		}
		r.send("\x03")
		r.finish()
	}
}

func TestMenuPanelCurves(t *testing.T) {
	r := start(t, &App{Path: histFixture(t), Size: func() (int, int) { return 130, 40 }, Proc: fakeProc(t, true)})
	r.wait("Needs a look")
	r.wait("Last 24 h, peak per")
	has(t, r.frame(), "  waited ▅", "peak 59 C")
	r.send("\x03")
	r.finish()
}

// A state file from before the history, or one with a broken history, shows the old panel.
func TestPanelWithoutHistory(t *testing.T) {
	cfg := histFixture(t)
	var sf string
	for _, l := range strings.Split(read(t, cfg), "\n") {
		if v, ok := strings.CutPrefix(l, "STATE_FILE="); ok {
			sf = v
		}
	}
	b := strings.Replace(read(t, sf), `"step":600`, `"step":5`, 1)
	os.WriteFile(sf, []byte(b), 0o600)
	r := start(t, &App{Path: cfg, Size: func() (int, int) { return 130, 40 }, Proc: fakeProc(t, true)})
	r.send("\r")
	r.wait("load 1/5/15 min")
	lacks(t, r.frame(), "Last 24 h", "peak")
	r.send("\x03")
	r.finish()
}

// Enter on a check shows its curves at 80x24 too.
func TestDetailsCurves80(t *testing.T) {
	r := start(t, &App{Path: histFixture(t), Size: sized, Proc: fakeProc(t, true)})
	r.send("\r")
	r.wait("Services")
	r.send("\r") // cursor on cpu
	r.wait("Type      cpu")
	f := r.frame()
	checkSize(t, f, 80, 24)
	has(t, f, "24 h      ▂", "peak 77%")
	r.send("\x1b")
	r.wait("Services")
	r.send("jjjjj\r") // nextcloud
	r.wait("Type      http")
	f = r.frame()
	checkSize(t, f, 80, 24)
	has(t, f, "Up 24 h   ", "100%", "Time 24 h ", " avg 3")
	r.send("\x1b")
	r.wait("Services")
	r.send("j\r") // photos: failing, the curve shows the red cells
	r.wait("Telegram  sent at")
	f = r.frame()
	checkSize(t, f, 80, 24)
	has(t, f, "Up 24 h   ", "91.6%")
	has(t, r.raw(), "▁")
	r.send("\x03")
	r.finish()
}
