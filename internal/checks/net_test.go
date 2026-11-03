// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestContainsStreaming(t *testing.T) {
	big := strings.Repeat("a", 1<<20)
	bigB := []byte(big)
	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) { w.Write(bigB) })
	mux.HandleFunc("/edge", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("a", 32*1024-3)+"NEEDLE"+strings.Repeat("a", 100))
	})
	mux.HandleFunc("/late", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, big+"NEEDLE") })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	run := func(path string) Result {
		return NewHTTP("x", srv.URL+path, "NEEDLE", 5*time.Second).Run(context.Background())
	}
	if r := run("/edge"); r.Status != OK {
		t.Fatalf("needle across a chunk edge: %+v", r)
	}
	if r := run("/late"); r.Status != FAIL {
		t.Fatalf("needle beyond 1 MiB must not count: %+v", r)
	}
	h := NewHTTP("x", srv.URL+"/big", "NEEDLE", 5*time.Second)
	h.Run(context.Background()) // warm up
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	h.Run(context.Background())
	runtime.ReadMemStats(&m1)
	if d := m1.TotalAlloc - m0.TotalAlloc; d > 512<<10 {
		t.Fatalf("allocated %d bytes for a 1 MiB body", d)
	}
}

func TestResponseHeaderLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Big", strings.Repeat("h", 100<<10))
	}))
	defer srv.Close()
	if r := NewHTTP("x", srv.URL, "", 2*time.Second).Run(context.Background()); r.Status != FAIL {
		t.Fatalf("%+v", r)
	}
}

func TestContainsAcrossTheBufferBoundary(t *testing.T) {
	needle := strings.Repeat("xyz", 40)
	for _, at := range []int{0, 8191 - 50, 8192 - 1, 8192, 20000, 100000} {
		body := strings.Repeat("a", at) + needle + strings.Repeat("b", 5000)
		got, err := containsStream(strings.NewReader(body), needle)
		if err != nil || !got {
			t.Errorf("needle at %d not found (%v)", at, err)
		}
	}
	if got, _ := containsStream(strings.NewReader(strings.Repeat("a", 30000)), needle); got {
		t.Fatal("false hit")
	}
	var big bytes.Buffer
	big.WriteString(strings.Repeat("a", 5000))
	if got, _ := containsStream(&big, "needle"); got {
		t.Fatal("false hit")
	}
}
