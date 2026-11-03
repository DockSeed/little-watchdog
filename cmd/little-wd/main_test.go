// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/state"
)

type stub struct{}

func (stub) ID() string                        { return "http:x" }
func (stub) Name() string                      { return "x\x1b[2J" }
func (stub) Type() string                      { return "http" }
func (stub) Run(context.Context) checks.Result { return checks.Result{} }

func TestCheckLineStripsControlCharacters(t *testing.T) {
	l := checkLine(stub{}, checks.Result{Status: checks.FAIL, Detail: "bad\x1b[31m\n" + strings.Repeat("z", 300)})
	if strings.ContainsAny(l, "\x1b\n") || len([]rune(l)) > 300 {
		t.Fatalf("%q", l)
	}
}

func TestNoOsExecInCode(t *testing.T) {
	err := filepath.WalkDir("../..", func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "main_test.go") {
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), "\"os/"+"exec\"") {
				t.Errorf("%s imports os/exec", p)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTuneRuntime(t *testing.T) {
	for _, tt := range []struct {
		env        map[string]string
		procs, lim bool
	}{
		{nil, true, true},
		{map[string]string{"GOMAXPROCS": "4"}, false, true},
		{map[string]string{"GOMEMLIMIT": "64MiB"}, true, false},
		{map[string]string{"GOMAXPROCS": "2", "GOMEMLIMIT": "1GiB"}, false, false},
	} {
		var gotProcs int
		var gotLim int64
		tuneRuntime(func(k string) string { return tt.env[k] },
			func(n int) int { gotProcs = n; return 0 },
			func(n int64) int64 { gotLim = n; return 0 })
		if (gotProcs == 1) != tt.procs || (gotProcs != 0 && gotProcs != 1) {
			t.Errorf("%v: procs %d", tt.env, gotProcs)
		}
		if (gotLim == softLimit) != tt.lim || (gotLim != 0 && gotLim != softLimit) {
			t.Errorf("%v: limit %d", tt.env, gotLim)
		}
	}
}

func TestExplainMissingConfigIsFriendly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "none.env")
	_, err := config.Load(p, false)
	want := "no config yet at " + p + ": run little-wd to set it up"
	if got := explain(p, err); got != want {
		t.Fatalf("%q", got)
	}
	// another missing file (say the state file) keeps its plain error
	_, err = state.ReadFile(filepath.Join(t.TempDir(), "state.json"))
	if got := explain(p, err); !strings.HasPrefix(got, "error: ") {
		t.Fatalf("%q", got)
	}
}

// capture runs fn with stdout redirected and returns what it printed.
func capture(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), err
}

func TestStatusPrintsTheTableAndAStaleWarning(t *testing.T) {
	dir := t.TempDir()
	sf := filepath.Join(dir, "state.json")
	os.WriteFile(sf, []byte(`{"version":1,"checks":{"tcp:b":{"name":"b","type":"tcp","reported":"OK","since":"2026-11-01T10:00:00Z","value":"open"}}}`), 0o600)
	cfg := filepath.Join(dir, "c.env")
	os.WriteFile(cfg, []byte("STATE_FILE="+sf+"\nTCP_B=192.0.2.1:22\n"), 0o600)
	out, err := capture(t, func() error { return cmdStatus(cfg) })
	if err != nil || !strings.Contains(out, "NAME") || !strings.Contains(out, "open") || !strings.Contains(out, "STALE") {
		t.Fatalf("%v\n%s", err, out)
	}
	state.Beat(sf, time.Now())
	out, _ = capture(t, func() error { return cmdStatus(cfg) })
	if strings.Contains(out, "STALE") {
		t.Fatalf("fresh heartbeat reported stale:\n%s", out)
	}
	os.WriteFile(sf, []byte(`{"version":1,"checks":{}}`), 0o600)
	if out, _ = capture(t, func() error { return cmdStatus(cfg) }); !strings.Contains(out, "empty") {
		t.Fatal(out)
	}
}

func TestCheckRunsOnceAndReportsFailures(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cfg := filepath.Join(t.TempDir(), "c.env")
	body := "DISK_MIN_FREE_PCT=0\nMEM_MIN_AVAIL_PCT=0\nTEMP_MAX_C=0\nCPU_MAX_PCT=0\nTCP_UP=" + l.Addr().String() + "\n"
	os.WriteFile(cfg, []byte(body), 0o600)
	out, err := capture(t, func() error { return cmdCheck(cfg) })
	if err != nil || !strings.Contains(out, "up") || !strings.Contains(out, "OK") {
		t.Fatalf("%v\n%s", err, out)
	}
	os.WriteFile(cfg, []byte(body+"TCP_DOWN=127.0.0.1:1\n"), 0o600)
	if out, err = capture(t, func() error { return cmdCheck(cfg) }); err == nil || !strings.Contains(out, "FAIL") {
		t.Fatalf("a failing check must give an error: %v\n%s", err, out)
	}
}

func TestTellOnceSendsOneMessageToTheGivenAPI(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = r.URL.Path + " " + string(b)
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	tellOnce("1:abc", 7, srv.URL, "hello")
	if !strings.Contains(got, "/bot1:abc/sendMessage") || !strings.Contains(got, "hello") || !strings.Contains(got, `"chat_id":7`) {
		t.Fatal(got)
	}
}
