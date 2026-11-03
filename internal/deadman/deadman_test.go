// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package deadman

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/clock"
)

type rec struct {
	mu      sync.Mutex
	paths   []string
	methods []string
	body    string
	status  int
}

func (r *rec) srv() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		r.paths = append(r.paths, q.URL.Path)
		r.methods = append(r.methods, q.Method)
		body, status := r.body, r.status
		r.mu.Unlock()
		if body == "" {
			body = "OK\n"
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		w.Write([]byte(body))
	}))
}

func newSw(url string, every time.Duration, t *testing.T) (*Switch, *clock.Fake) {
	fk := clock.NewFake()
	s := New(url, every, t.Logf)
	s.Clk = fk.Clock()
	return s, fk
}

func TestCycle(t *testing.T) {
	r := &rec{}
	srv := r.srv()
	defer srv.Close()
	s, fk := newSw(srv.URL+"/uuid/", 2*time.Minute, t)
	probeOK := func(context.Context) error { return nil }
	probeBad := func(context.Context) error { return errors.New("down") }
	ctx := context.Background()
	cyc := func(p func(context.Context) error) bool { fk.Advance(2 * time.Minute); return s.Cycle(ctx, p) }

	if cyc(probeBad) {
		t.Fatal("fail mode")
	}
	s.NoteSend(false)
	s.NoteSend(false)
	if cyc(probeBad) {
		t.Fatal("fail mode")
	}
	s.NoteSend(false)
	if !cyc(probeBad) {
		t.Fatal("want fail mode")
	}
	if !cyc(probeBad) || s.Fails() != 3 {
		t.Fatal("want fail mode", s.Fails())
	}
	// Telegram back (no send needed): counter reset, normal ping (regression test)
	if cyc(probeOK) || s.Fails() != 0 {
		t.Fatal("not recovered", s.Fails())
	}
	got := strings.Join(r.paths, " ")
	if got != "/uuid/ /uuid/ /uuid/fail /uuid/fail /uuid/" {
		t.Fatal(got)
	}
}

func TestOffAndErrors(t *testing.T) {
	if New("", 0, t.Logf).Cycle(context.Background(), nil) {
		t.Fatal()
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/secret-uuid"
	srv.Close()
	s, _ := newSw(url, time.Minute, t)
	err := s.Ping(context.Background(), url)
	if err == nil || strings.Contains(err.Error(), "secret-uuid") {
		t.Fatal(err)
	}
	r := &rec{status: 500}
	hs := r.srv()
	defer hs.Close()
	s, _ = newSw(hs.URL, time.Minute, t)
	if err := s.Ping(context.Background(), hs.URL); err == nil {
		t.Fatal("want HTTP error")
	}
}

func TestEveryHasHardFloor(t *testing.T) {
	for _, in := range []time.Duration{0, time.Second, 59 * time.Second} {
		if got := New("http://x", in, t.Logf).Every; got != MinEvery {
			t.Errorf("Every(%s) = %s", in, got)
		}
	}
	if New("http://x", 5*time.Minute, t.Logf).Every != 5*time.Minute {
		t.Fatal("longer value changed")
	}
}

func TestPingsNoMoreOftenThanEvery(t *testing.T) {
	r := &rec{}
	srv := r.srv()
	defer srv.Close()
	s, fk := newSw(srv.URL, 2*time.Minute, t)
	ctx := context.Background()
	for i := 0; i < 6; i++ { // INTERVAL 60s: a cycle every minute
		s.Cycle(ctx, nil)
		fk.Advance(time.Minute)
	}
	if len(r.paths) != 3 {
		t.Fatalf("%d pings in 6 cycles: %v", len(r.paths), r.paths)
	}
}

func TestBodyMustBeOK(t *testing.T) {
	var logs []string
	for _, tt := range []struct {
		body string
		ok   bool
	}{{"OK", true}, {" OK\n", true}, {"OK (not found)", false}, {"OK (rate limited)", false}, {"", false}} {
		r := &rec{body: tt.body}
		if tt.body == "" {
			r.body = " "
		}
		srv := r.srv()
		s, _ := newSw(srv.URL, time.Minute, t)
		err := s.Ping(context.Background(), srv.URL)
		srv.Close()
		if (err == nil) != tt.ok {
			t.Errorf("body %q: err %v", tt.body, err)
		}
	}
	// a wrong UUID is logged loudly once, not every ping
	r := &rec{body: "OK (not found)"}
	srv := r.srv()
	defer srv.Close()
	s := New(srv.URL, time.Minute, func(f string, a ...any) { logs = append(logs, f) })
	fk := clock.NewFake()
	s.Clk = fk.Clock()
	for i := 0; i < 4; i++ {
		s.Cycle(context.Background(), nil)
		fk.Advance(2 * time.Minute)
	}
	if len(logs) != 1 {
		t.Fatalf("logged %d times: %v", len(logs), logs)
	}
}

func TestRetryOnlyOnNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	s, fk := newSw(url, time.Minute, t)
	if err := s.Ping(context.Background(), url); err == nil {
		t.Fatal("want error")
	}
	if got := fk.Sleeps(); len(got) != 2 || got[0] != 5*time.Second || got[1] != 10*time.Second {
		t.Fatalf("sleeps %v", got)
	}
	for _, code := range []int{500, 404, 429} {
		r := &rec{status: code}
		hs := r.srv()
		s, fk := newSw(hs.URL, time.Minute, t)
		s.Ping(context.Background(), hs.URL)
		hs.Close()
		if len(r.paths) != 1 || len(fk.Sleeps()) != 0 {
			t.Errorf("HTTP %d: %d requests, sleeps %v", code, len(r.paths), fk.Sleeps())
		}
	}
	// body error is an answer too: no retry
	r := &rec{body: "OK (not found)"}
	hs := r.srv()
	defer hs.Close()
	s, fk = newSw(hs.URL, time.Minute, t)
	s.Ping(context.Background(), hs.URL)
	if len(r.paths) != 1 || len(fk.Sleeps()) != 0 {
		t.Fatalf("%d requests", len(r.paths))
	}
}

func TestFailReplacesSuccessPing(t *testing.T) {
	r := &rec{}
	srv := r.srv()
	defer srv.Close()
	s, fk := newSw(srv.URL+"/u", 2*time.Minute, t)
	for i := 0; i < Threshold; i++ {
		s.NoteSend(false)
	}
	s.Cycle(context.Background(), func(context.Context) error { return errors.New("down") })
	fk.Advance(time.Minute)
	if strings.Join(r.paths, " ") != "/u/fail" {
		t.Fatalf("%v", r.paths)
	}
}

func TestProbeAtMostOncePerEvery(t *testing.T) {
	r := &rec{}
	srv := r.srv()
	defer srv.Close()
	s, fk := newSw(srv.URL, 2*time.Minute, t)
	for i := 0; i < Threshold; i++ {
		s.NoteSend(false)
	}
	var probes int
	p := func(context.Context) error { probes++; return errors.New("down") }
	for i := 0; i < 6; i++ {
		s.Cycle(context.Background(), p)
		fk.Advance(time.Minute)
	}
	if probes != 3 {
		t.Fatalf("%d probes in 6 minutes", probes)
	}
}

func TestKeepAliveAndMethod(t *testing.T) {
	var conns atomic.Int32
	r := &rec{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		r.methods = append(r.methods, q.Method)
		r.mu.Unlock()
		w.Write([]byte("OK"))
	}))
	srv.Config.ConnState = connCounter(&conns)
	srv.Start()
	defer srv.Close()
	s, fk := newSw(srv.URL, 2*time.Minute, t)
	for i := 0; i < 4; i++ {
		if err := s.Ping(context.Background(), srv.URL); err != nil {
			t.Fatal(err)
		}
		fk.Advance(2 * time.Minute)
	}
	if conns.Load() != 1 {
		t.Fatalf("%d TCP connections for 4 pings", conns.Load())
	}
	for _, m := range r.methods {
		if m != http.MethodGet {
			t.Fatalf("method %s", m)
		}
	}
}

func TestRedirectIsAFailureAndNotFollowed(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.Write([]byte("OK")) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer srv.Close()
	s := New(srv.URL+"/uuid", 0, func(string, ...any) {})
	if err := s.Ping(context.Background(), s.URL); err == nil {
		t.Fatal("3xx counted as success")
	}
	if hits.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestOnBadCalledOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("OK (not found)")) }))
	defer srv.Close()
	s := New(srv.URL, 0, func(string, ...any) {})
	var n int
	s.OnBad = func(error) { n++ }
	for i := 0; i < 3; i++ {
		s.Cycle(context.Background(), nil)
		s.last = s.last.Add(-s.Every)
	}
	if n != 1 {
		t.Fatalf("OnBad called %d times", n)
	}
}

func TestFailURLKeepsTheQuery(t *testing.T) {
	for in, want := range map[string]string{
		"https://hc.example.org/ping/abc":          "https://hc.example.org/ping/abc/fail",
		"https://hc.example.org/ping/abc/":         "https://hc.example.org/ping/abc/fail",
		"https://hc.example.org/ping/abc?rid=7":    "https://hc.example.org/ping/abc/fail?rid=7",
		"https://hc.example.org/ping/abc/?a=1&b=2": "https://hc.example.org/ping/abc/fail?a=1&b=2",
	} {
		if got := failURL(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
