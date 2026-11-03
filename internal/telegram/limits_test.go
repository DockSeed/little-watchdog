// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/clock"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func fakeClient(url string) (*Client, *clock.Fake) {
	fk := clock.NewFake()
	c := New(url, tok, time.Second)
	c.Clk = fk.Clock()
	return c, fk
}

func TestSendSpacing(t *testing.T) {
	for _, tt := range []struct {
		chat int64
		gap  time.Duration
	}{{42, time.Second}, {-42, 3 * time.Second}} {
		_, srv := testfake.Start(tok)
		c, fk := fakeClient(srv.URL)
		for i := 0; i < 3; i++ {
			if err := c.SendMessage(context.Background(), tt.chat, "x"); err != nil {
				t.Fatal(err)
			}
		}
		srv.Close()
		got := fk.Sleeps()
		if len(got) != 2 || got[0] != tt.gap || got[1] != tt.gap {
			t.Errorf("chat %d: sleeps %v, want 2x %s", tt.chat, got, tt.gap)
		}
	}
}

func TestNoWaitWhenGapAlreadyPassed(t *testing.T) {
	_, srv := testfake.Start(tok)
	defer srv.Close()
	c, fk := fakeClient(srv.URL)
	c.SendMessage(context.Background(), 1, "a")
	fk.Advance(5 * time.Second)
	c.SendMessage(context.Background(), 1, "b")
	if len(fk.Sleeps()) != 0 {
		t.Fatalf("slept %v", fk.Sleeps())
	}
}

func TestRetryAfterObeyed(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(429)
			w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":47}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	c, fk := fakeClient(srv.URL)
	if err := c.SendMessage(context.Background(), 1, "x"); err != nil {
		t.Fatal(err)
	}
	if got := fk.Sleeps(); len(got) != 1 || got[0] != 47*time.Second {
		t.Fatalf("sleeps %v, want one of 47s (no 30s cap)", got)
	}
}

func TestFloodWaitHoldsEverySender(t *testing.T) {
	_, srv := testfake.Start(tok)
	defer srv.Close()
	c, fk := fakeClient(srv.URL)
	c.holdFor(30 * time.Second)
	if err := c.SendMessage(context.Background(), 1, "x"); err != nil {
		t.Fatal(err)
	}
	if got := fk.Sleeps(); len(got) != 1 || got[0] != 30*time.Second {
		t.Fatalf("sleeps %v", got)
	}
}

func TestPollTimeoutBounds(t *testing.T) {
	var got []int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		got = append(got, int(p["timeout"].(float64)))
		mu.Unlock()
		w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()
	c, _ := fakeClient(srv.URL)
	ctx := context.Background()
	c.GetUpdates(ctx, 5, 0)  // short polling is never sent
	c.GetUpdates(ctx, 5, 10) // below the floor
	c.GetUpdates(ctx, 5, 50) // normal
	c.GetUpdates(ctx, -1, 0) // one-shot "latest update" lookup
	want := []int{25, 25, 50, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("timeouts %v, want %v", got, want)
		}
	}
}

func TestMigrateToChatID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1001234567890}}`))
	}))
	defer srv.Close()
	c, _ := fakeClient(srv.URL)
	err := c.SendMessage(context.Background(), -5, "x")
	var ae *APIError
	if !errors.As(err, &ae) || ae.MigrateTo != -1001234567890 {
		t.Fatalf("%v", err)
	}
}

func TestKeepAlive(t *testing.T) {
	var conns atomic.Int32
	f := &testfake.Telegram{Token: tok}
	srv := httptest.NewUnstartedServer(f.Handler())
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	c, _ := fakeClient(srv.URL)
	for i := 0; i < 4; i++ {
		if err := c.SendMessage(context.Background(), 1, "x"); err != nil {
			t.Fatal(err)
		}
	}
	if conns.Load() != 1 {
		t.Fatalf("%d TCP connections for 4 sends", conns.Load())
	}
}

func TestSendOnceDoesNotWaitOutAFloodWait(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"ok":true,"result":{}}`)) }))
	defer srv.Close()
	c, fk := fakeClient(srv.URL)
	c.holdFor(24 * time.Hour)
	if err := c.SendOnce(context.Background(), 1, "x"); !errors.Is(err, ErrHeld) {
		t.Fatalf("err %v", err)
	}
	if len(fk.Sleeps()) != 0 {
		t.Fatalf("slept %v", fk.Sleeps())
	}
}

func TestPaceSleepsOutsideTheLock(t *testing.T) { // a sleeping sender must not block the others' bookkeeping
	c, fk := fakeClient("http://127.0.0.1:1")
	entered, release := make(chan struct{}), make(chan struct{})
	fk.OnWait = func(time.Duration) { entered <- struct{}{}; <-release }
	c.holdFor(time.Hour)
	done := make(chan error, 1)
	go func() { done <- c.pace(context.Background(), 1, time.Duration(1<<62)) }()
	<-entered
	got := make(chan error, 1)
	go func() { got <- c.pace(context.Background(), 1, time.Millisecond) }() // would hang on a held mutex
	select {
	case err := <-got:
		if !errors.Is(err, ErrHeld) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pace blocked behind a sleeping sender")
	}
	close(release)
	<-done
}
