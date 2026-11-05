// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEveryKindHasAShortHint(t *testing.T) {
	seen := map[Kind]bool{}
	for _, k := range Kinds() {
		h := Help(k)
		switch {
		case seen[k]:
			t.Errorf("%s listed twice", k)
		case h == "":
			t.Errorf("%s has no hint", k)
		case len([]rune(h)) > 100:
			t.Errorf("%s: hint too long (%d): %q", k, len([]rune(h)), h)
		case strings.Count(h, ". ") > 1 || !strings.HasSuffix(h, "."):
			t.Errorf("%s: at most two short sentences: %q", k, h)
		case Clean(h) != h || strings.ContainsAny(h, "`/:"):
			t.Errorf("%s: plain words only: %q", k, h)
		}
		seen[k] = true
	}
	if len(seen) != len(helps) {
		t.Fatalf("Kinds lists %d, the table has %d", len(seen), len(helps))
	}
	if Help("") != "" || Help("nope") != "" {
		t.Fatal("an unknown kind has no hint")
	}
}

func TestPlainKindNamesTheKind(t *testing.T) {
	op := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", err)}
	}
	for _, c := range []struct {
		err  error
		took time.Duration
		want Kind
	}{
		{context.DeadlineExceeded, 0, KindTimeout},
		{op(syscall.ECONNREFUSED), 0, KindRefused},
		{&net.DNSError{Err: "no such host", Name: "nas.lan", IsNotFound: true}, 0, KindDNS},
		{op(syscall.EHOSTUNREACH), 2 * time.Millisecond, KindBlocked},
		{op(syscall.EHOSTUNREACH), 3 * time.Second, KindUnreachable},
		{op(syscall.ENETUNREACH), 0, KindUnreachable},
		{x509.UnknownAuthorityError{}, 0, KindTLS},
		{os.ErrClosed, 0, ""},
	} {
		s, k := PlainKind(c.err, 10*time.Second, "http://nas:8000/", c.took)
		if k != c.want || s != Plain(c.err, 10*time.Second, "http://nas:8000/", c.took) {
			t.Errorf("%v: %q %q, want kind %q", c.err, s, k, c.want)
		}
	}
}

func TestChecksCarryTheHint(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if r := NewHTTP("x", "http://"+addr+"/", "", time.Second).Run(context.Background()); r.Help != Help(KindRefused) || r.Detail == "" {
		t.Fatalf("refused: %+v", r)
	}
	if r := (&TCP{name: "x", addr: addr, timeout: time.Second}).Run(context.Background()); r.Help != Help(KindRefused) {
		t.Fatalf("tcp refused: %+v", r)
	}
	code := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		w.Write([]byte("hello"))
	}))
	defer srv.Close()
	for _, c := range []struct {
		code     int
		contains string
		want     Kind
	}{{502, "", KindHTTP5xx}, {404, "", KindHTTP4xx}, {200, "absent", KindNoText}} {
		code = c.code
		r := NewHTTP("x", srv.URL, c.contains, time.Second).Run(context.Background())
		if r.Status != FAIL || r.Help != Help(c.want) {
			t.Errorf("%d: %+v", c.code, r)
		}
	}
	code = 200
	if r := NewHTTP("x", srv.URL, "hello", time.Second).Run(context.Background()); r.Status != OK || r.Help != "" {
		t.Fatalf("OK has no hint: %+v", r)
	}
}
