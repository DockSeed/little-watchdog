// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/config"
)

func modeServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("fine")) })
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) })
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	mux.HandleFunc("/broken", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	mux.HandleFunc("/headless", func(w http.ResponseWriter, r *http.Request) { // HEAD 404, GET 200
		if r.Method == http.MethodHead {
			w.WriteHeader(404)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPModes(t *testing.T) {
	srv := modeServer(t)
	tests := []struct {
		mode, path string
		want       Status
		value      string
	}{
		{"", "/moved", OK, "web 302, "}, // 0.3 rules: redirect not followed, 302 is OK
		{"", "/missing", FAIL, "web 404, "},
		{config.ModeLenient, "/moved", OK, "web 200, "},
		{config.ModeLenient, "/forbidden", OK, "web 403, "},
		{config.ModeLenient, "/missing", OK, "web 404, "},
		{config.ModeLenient, "/broken", FAIL, "web 500, "},
		{config.ModeLenient, "/loop", FAIL, "too many redirects"},
		{config.ModeStrict, "/moved", OK, "web 200, "},
		{config.ModeStrict, "/forbidden", FAIL, "web 403, "},
		{config.ModeStrict, "/missing", FAIL, "web 404, "},
		{config.ModeStrict, "/broken", FAIL, "web 500, "},
		{config.ModeStrict, "/headless", OK, "web 200, "},
	}
	for _, tt := range tests {
		r := NewHTTP("x", srv.URL+tt.path, "", 2*time.Second).SetMode("", tt.mode).Run(context.Background())
		if r.Status != tt.want || !strings.HasPrefix(r.Value, tt.value) {
			t.Errorf("%q %s: %+v", tt.mode, tt.path, r)
		}
	}
	r := NewHTTP("x", srv.URL+"/broken", "", 2*time.Second).SetMode("", config.ModeStrict).Run(context.Background())
	if r.Detail != "answered with HTTP 500" {
		t.Fatal(r.Detail)
	}
	if r := NewHTTP("x", srv.URL+"/loop", "", 2*time.Second).SetMode("", config.ModeStrict).Run(context.Background()); r.Detail != "more than 5 redirects" {
		t.Fatal(r.Detail)
	}
}

// A port that takes the connection but speaks no HTTP answers, but is no website.
func TestHTTPLenientNotWeb(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("SSH-2.0-OpenSSH_9.2\r\n"))
			c.Close()
		}
	}()
	url := "http://" + ln.Addr().String()
	h := NewHTTP("x", url, "", 2*time.Second).SetMode("", config.ModeLenient)
	if r := h.Run(context.Background()); r.Status != OK || !r.Odd || r.Value != "port open, no website" {
		t.Fatalf("%+v", r)
	}
	if r := NewHTTP("x", url, "", 2*time.Second).SetMode("", config.ModeStrict).Run(context.Background()); r.Status != FAIL {
		t.Fatalf("strict: %+v", r)
	}
	if r := NewHTTP("x", url, "", 2*time.Second).Run(context.Background()); r.Status != FAIL {
		t.Fatalf("0.3 rules: %+v", r)
	}
	ln.Close()
	if r := h.Run(context.Background()); r.Status != FAIL || !strings.Contains(r.Detail, "refused") {
		t.Fatalf("closed: %+v", r)
	}
}

// host:port on a port that speaks TLS: the lenient check finds the website over https.
func TestHTTPLenientFindsTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	h := NewHTTP("x", "http://"+addr, "", 2*time.Second).SetMode("", config.ModeLenient)
	for i := 0; i < 2; i++ {
		if r := h.Run(context.Background()); r.Status != OK || !strings.HasPrefix(r.Value, "web 401, ") {
			t.Fatalf("run %d: %+v", i, r)
		}
	}
	if h.scheme.Load() != schemeTLS {
		t.Fatal("https not remembered")
	}
	// An https:// address keeps its certificate check.
	r := NewHTTP("x", srv.URL, "", 2*time.Second).SetMode("", config.ModeLenient).Run(context.Background())
	if r.Status != FAIL || !strings.Contains(r.Detail, "certificate") {
		t.Fatalf("%+v", r)
	}
}

// A plain web server that answers 400 stays on http after one look at https.
func TestHTTPLenientPlain400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(400) }))
	defer srv.Close()
	h := NewHTTP("x", srv.URL, "", 2*time.Second).SetMode("", config.ModeLenient)
	for i := 0; i < 2; i++ {
		if r := h.Run(context.Background()); r.Status != OK || !strings.HasPrefix(r.Value, "web 400, ") {
			t.Fatalf("run %d: %+v", i, r)
		}
	}
	if h.scheme.Load() != schemePlain {
		t.Fatal(h.scheme.Load())
	}
}

func TestPingLoopback(t *testing.T) {
	p := NewPing("lo", "Loopback", "127.0.0.1", 3*time.Second)
	r := p.Run(context.Background())
	if r.Status == FAIL && r.Detail == PingNotAllowed {
		t.Skip("ping not allowed here (net.ipv4.ping_group_range)")
	}
	if r.Status != OK || !strings.HasPrefix(r.Value, "ping answers, ") || !strings.HasSuffix(r.Value, " ms") {
		t.Fatalf("%+v", r)
	}
	if p.ID() != "ping:lo" || p.Name() != "Loopback" || p.Type() != "ping" {
		t.Fatal(p.ID(), p.Name())
	}
	if r := NewPing("lo6", "", "::1", 3*time.Second).Run(context.Background()); r.Status != OK {
		t.Logf("IPv6 loopback: %+v", r) // hosts without IPv6 cannot
	}
	if r := NewPing("x", "", "nonexistent.invalid", time.Second).Run(context.Background()); r.Status != FAIL || !strings.Contains(r.Detail, "not found") {
		t.Fatalf("%+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := NewPing("x", "", "127.0.0.1", time.Second).Run(ctx); r.Status != FAIL {
		t.Fatalf("cancelled: %+v", r)
	}
}

func TestICMPSum(t *testing.T) {
	// echo request, id 0, seq 1, no data: sum of words 0x0800 + 0x0001
	if got := icmpSum([]byte{8, 0, 0, 0, 0, 0, 0, 1}); got != ^uint16(0x0801) {
		t.Fatalf("%#x", got)
	}
}

// The name people see comes from name=, the state key from the config key.
func TestBuildNames(t *testing.T) {
	c, err := config.Parse(strings.NewReader("HTTP_IMMICH=http://h:1 name=\"Immich Fotos\" mode=lenient\nTCP_SSH=h:22\nPING_NAS=nas name=\"Die Kiste\"\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	c.DiskMinFree, c.MemMinAvail, c.TempMaxC, c.CPUMaxPct = 0, 0, 0, 0
	var got []string
	for _, x := range Build(c, t.Logf) {
		got = append(got, x.ID()+"="+x.Name())
	}
	if strings.Join(got, ",") != "http:immich=Immich Fotos,tcp:ssh=ssh,ping:nas=Die Kiste" {
		t.Fatal(got)
	}
}

// A redirect to a dead target is a failure, not "port open, no website": the first hop
// answered as a website, the place it points to is gone.
func TestLenientRedirectToDeadTargetFails(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String() + "/"
	l.Close() // nothing listens there any more
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dead, http.StatusFound)
	}))
	defer srv.Close()
	for _, mode := range []string{config.ModeLenient, config.ModeStrict} {
		r := NewHTTP("x", srv.URL, "", 2*time.Second).SetMode("", mode).Run(context.Background())
		if r.Status != FAIL || r.Odd {
			t.Errorf("%s: %+v", mode, r)
		}
	}
}

func TestLenientSchemeIgnoresCase(t *testing.T) {
	if h := NewHTTP("x", "HTTP://127.0.0.1:1", "", time.Second).SetMode("", config.ModeLenient); h.insecure == nil {
		t.Fatal("no TLS fallback for HTTP://")
	}
}
