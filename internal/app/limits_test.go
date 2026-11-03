// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestDeadmanFollowsItsOwnClock(t *testing.T) {
	_, tsrv := testfake.Start(tok)
	defer tsrv.Close()
	var mu sync.Mutex
	var n int
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		w.Write([]byte("OK"))
	}))
	defer dm.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, tsrv.URL, dm.URL+"/u", &buf)
	for i := 0; i < 5; i++ { // INTERVAL 60s, DEADMAN_EVERY 2m
		a.Cycle(context.Background())
		fk.Advance(time.Minute)
	}
	if n != 3 {
		t.Fatalf("%d pings in 5 cycles", n)
	}
}

func TestMigrateToChatID(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		chat, _ := p["chat_id"].(float64)
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			if int64(chat) == 42 {
				w.WriteHeader(400)
				w.Write([]byte(`{"ok":false,"error_code":400,"description":"upgraded","parameters":{"migrate_to_chat_id":-100777}}`))
				return
			}
			mu.Lock()
			sent = append(sent, strings.TrimSpace(strings.Join([]string{jsonNum(chat), p["text"].(string)}, "|")))
			mu.Unlock()
		}
		w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()
	var buf bytes.Buffer
	a, s := newApp(t, srv.URL, "", &buf)
	a.Cycle(context.Background())
	s.fail("down")
	for i := 0; i < 4; i++ {
		a.Cycle(context.Background())
	}
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(sent, "\n")
	if strings.Count(all, "-100777|") != len(sent) || len(sent) != 2 {
		t.Fatalf("all sends must go to the new chat, exactly alarm + one note: %q", sent)
	}
	if !strings.Contains(all, "<b>http:web</b> on <b>testhost</b> is down since ") || !strings.Contains(all, ": down.") || strings.Count(all, "Telegram moved this chat to id -100777. The new id is used until restart. Update TELEGRAM_CHAT_ID") != 1 {
		t.Fatalf("%q", sent)
	}
	if a.Cfg.ChatID != 42 {
		t.Fatal("config value must stay untouched")
	}
}

func jsonNum(f float64) string { b, _ := json.Marshal(int64(f)); return string(b) }

func TestPollUsesLongTimeoutAndWaitsAfterFastEmptyAnswer(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	a.Poll(ctx)
	f.Set(func(f *testfake.Telegram) {
		for i, to := range f.PollTimeouts {
			if i > 0 && to != 50 {
				t.Errorf("getUpdates timeout %d", to)
			}
		}
		if len(f.PollTimeouts) < 2 {
			t.Errorf("polls: %v", f.PollTimeouts)
		}
	})
	var one int
	for _, d := range fk.Sleeps() {
		if d == time.Second {
			one++
		}
	}
	if one == 0 {
		t.Fatalf("no 1 s wait after an empty answer: %v", fk.Sleeps())
	}
}

func TestPollBacksOffAfterErrors(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.Fail409 = true })
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	ctx, cancel := context.WithCancel(context.Background())
	fk.OnWait = func(time.Duration) {
		if len(fk.Sleeps()) >= 4 {
			cancel()
		}
	}
	a.Poll(ctx)
	got := fk.Sleeps()
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pauses %v, want %v", got, want)
		}
	}
}
