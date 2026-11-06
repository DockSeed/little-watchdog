// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPlainMapsCommonErrors(t *testing.T) {
	op := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", err)}
	}
	for _, c := range []struct {
		name   string
		err    error
		target string
		want   string
		took   time.Duration
	}{
		{"timeout", context.DeadlineExceeded, "http://nas:8000/", "no answer within 10 s", 0},
		{"refused", op(syscall.ECONNREFUSED), "http://nas:8000/x", "connection refused, nothing listens on port 8000", 0},
		{"refused default port", op(syscall.ECONNREFUSED), "https://nas/", "connection refused, nothing listens on port 443", 0},
		{"refused tcp", op(syscall.ECONNREFUSED), "192.0.2.5:22", "connection refused, nothing listens on port 22", 0},
		{"dns", &net.DNSError{Err: "no such host", Name: "nas.lan", IsNotFound: true}, "http://nas.lan/", "name nas.lan not found", 0},
		{"no route after ARP", op(syscall.EHOSTUNREACH), "192.0.2.5:22", "host not reachable", 3 * time.Second},
		{"firewall reject", op(syscall.EHOSTUNREACH), "http://nas/", "port 80 blocked by a firewall, or no route to the host", 2 * time.Millisecond},
		{"firewall reject tcp", op(syscall.EHOSTUNREACH), "192.0.2.5:22", "port 22 blocked by a firewall, or no route to the host", 0},
		{"net unreachable", op(syscall.ENETUNREACH), "192.0.2.5:22", "host not reachable", 0},
		{"host down", op(syscall.EHOSTDOWN), "192.0.2.5:22", "host not reachable", 0},
		{"other", errors.New("boom\x1b[2J"), "x:1", "boom [2J", 0},
	} {
		if got := Plain(c.err, 10*time.Second, c.target, c.took); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHTTPCheckSaysWhatHappened(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	r := NewHTTP("x", "http://"+addr+"/secret-path", "", time.Second).Run(context.Background())
	_, port, _ := net.SplitHostPort(addr)
	if want := "connection refused, nothing listens on port " + port; r.Detail != want || strings.Contains(r.Detail, "secret") {
		t.Fatalf("%+v", r)
	}
	r = NewHTTP("x", "http://nonexistent.invalid/", "", time.Second).Run(context.Background())
	if r.Detail != "name nonexistent.invalid not found" {
		t.Fatalf("%+v", r)
	}
	tls := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tls.Close()
	r = NewHTTP("x", tls.URL, "", time.Second).Run(context.Background())
	if !strings.HasPrefix(r.Detail, "TLS certificate problem: ") {
		t.Fatalf("%+v", r)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { time.Sleep(2 * time.Second) }))
	defer slow.Close()
	if r = NewHTTP("x", slow.URL, "", time.Second).Run(context.Background()); r.Detail != "no answer within 1 s" {
		t.Fatalf("%+v", r)
	}
}

// Plain is PlainKind without the kind. It turns a network error into a sentence with the facts. timeout is the wait the
// check was given, target its URL or host:port and took how long the attempt ran.
// URLs never appear in the text.
func Plain(err error, timeout time.Duration, target string, took time.Duration) string {
	s, _ := PlainKind(err, timeout, target, took)
	return s
}
