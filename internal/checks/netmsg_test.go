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
	}{
		{"timeout", context.DeadlineExceeded, "http://nas:8000/", "no answer within 10 s"},
		{"refused", op(syscall.ECONNREFUSED), "http://nas:8000/x", "connection refused, nothing listens on port 8000"},
		{"refused default port", op(syscall.ECONNREFUSED), "https://nas/", "connection refused, nothing listens on port 443"},
		{"refused tcp", op(syscall.ECONNREFUSED), "192.0.2.5:22", "connection refused, nothing listens on port 22"},
		{"dns", &net.DNSError{Err: "no such host", Name: "nas.lan", IsNotFound: true}, "http://nas.lan/", "name nas.lan not found"},
		{"no route", op(syscall.EHOSTUNREACH), "192.0.2.5:22", "host not reachable"},
		{"net unreachable", op(syscall.ENETUNREACH), "192.0.2.5:22", "host not reachable"},
		{"other", errors.New("boom\x1b[2J"), "x:1", "boom [2J"},
	} {
		if got := Plain(c.err, 10*time.Second, c.target); got != c.want {
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
