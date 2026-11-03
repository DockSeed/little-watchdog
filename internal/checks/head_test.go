// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type methods struct {
	mu sync.Mutex
	m  []string
}

func (s *methods) add(x string) { s.mu.Lock(); s.m = append(s.m, x); s.mu.Unlock() }

func (s *methods) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.m, " ")
}

func TestHTTPUsesHEADWithoutContains(t *testing.T) {
	var ms methods
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ms.add(r.Method); w.Write([]byte("hello")) }))
	defer srv.Close()
	if r := NewHTTP("a", srv.URL, "", time.Second).Run(context.Background()); r.Status != OK {
		t.Fatal(r)
	}
	if r := NewHTTP("b", srv.URL, "hello", time.Second).Run(context.Background()); r.Status != OK {
		t.Fatal(r)
	}
	if ms.String() != "HEAD GET" {
		t.Fatal(ms.String())
	}
}

func TestHTTPFallsBackToGETOnce(t *testing.T) {
	for _, code := range []int{405, 501} {
		var ms methods
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ms.add(r.Method)
			if r.Method == http.MethodHead {
				w.WriteHeader(code)
			}
		}))
		h := NewHTTP("a", srv.URL, "", time.Second)
		for i := 0; i < 3; i++ {
			if r := h.Run(context.Background()); r.Status != OK {
				t.Fatalf("%d: %+v", code, r)
			}
		}
		srv.Close()
		if ms.String() != "HEAD GET GET GET" { // HEAD asked once, then remembered
			t.Fatalf("%d: %s", code, ms.String())
		}
	}
}

func TestHTTPNoFallbackOnOtherErrors(t *testing.T) {
	var ms methods
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ms.add(r.Method); w.WriteHeader(502) }))
	defer srv.Close()
	if r := NewHTTP("a", srv.URL, "", time.Second).Run(context.Background()); r.Status != FAIL || r.Detail != "answered with HTTP 502" {
		t.Fatal(r)
	}
	if ms.String() != "HEAD" {
		t.Fatal(ms.String())
	}
}

func TestHeadAndGetFallbackShareOneBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			time.Sleep(300 * time.Millisecond)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		select { // the GET hangs
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	start := time.Now()
	r := NewHTTP("a", srv.URL, "", 500*time.Millisecond).Run(context.Background())
	if el := time.Since(start); el > 800*time.Millisecond {
		t.Fatalf("HEAD and GET took %v for a budget of 500ms", el)
	}
	if r.Status != FAIL {
		t.Fatal(r)
	}
}
