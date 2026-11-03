// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestDeadmanProbeIsSendMessageAnd4xxCounts(t *testing.T) {
	f, tsrv := testfake.Start(tok)
	defer tsrv.Close()
	var mu sync.Mutex
	var paths []string
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
	}))
	defer dm.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, tsrv.URL, dm.URL+"/u", &buf)
	last := func() string { mu.Lock(); defer mu.Unlock(); return paths[len(paths)-1] }
	for i := 0; i < 3; i++ {
		a.DM.NoteSend(false)
	}
	// getMe works, sendMessage is rejected with 403: still /fail
	f.Set(func(f *testfake.Telegram) { f.FailCode = 403 })
	fk.Advance(2 * time.Minute)
	a.Cycle(context.Background())
	if last() != "/u/fail" {
		t.Fatalf("want /fail, got %s", last())
	}
	// sendMessage works again: counter reset by that send
	f.Set(func(f *testfake.Telegram) { f.FailCode = 0 })
	fk.Advance(2 * time.Minute)
	a.Cycle(context.Background())
	if last() != "/u" || a.DM.Fails() != 0 {
		t.Fatalf("path %s fails %d", last(), a.DM.Fails())
	}
}

func TestDeadmanPingsWhileTelegramIsHeld(t *testing.T) {
	var paths []string
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { paths = append(paths, r.URL.Path); w.Write([]byte("OK")) }))
	defer dm.Close()
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":86400}}`))
	}))
	defer tg.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, tg.URL, dm.URL+"/u", &buf)
	ctx := context.Background()
	a.send(ctx, "x") // 429: the client now holds every sender for a day
	for i := 0; i < 3; i++ {
		a.DM.NoteSend(false)
	}
	fk.Advance(3 * time.Minute)
	before := len(fk.Sleeps())
	a.PingDeadman(ctx)
	for _, d := range fk.Sleeps()[before:] {
		if d > 10*time.Second {
			t.Fatalf("dead-man waited %v behind Telegram", d)
		}
	}
	if len(paths) != 1 || paths[0] != "/u" {
		t.Fatalf("held Telegram is reachable, want a normal ping: %v", paths)
	}
}

func TestDeadmanBadUUIDTellsTelegram(t *testing.T) {
	f, tsrv := testfake.Start(tok)
	defer tsrv.Close()
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("OK (not found)")) }))
	defer dm.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, tsrv.URL, dm.URL+"/u", &buf)
	fk.Advance(3 * time.Minute)
	a.Cycle(context.Background())
	a.Cycle(context.Background())
	var n int
	for _, s := range f.Texts() {
		if strings.Contains(s, "dead-man ping rejected") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%q", f.Texts())
	}
}

func TestDeadmanStaysSilentWhenNoRoundFinishes(t *testing.T) {
	var paths []string
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { paths = append(paths, r.URL.Path); w.Write([]byte("OK")) }))
	defer dm.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, "http://127.0.0.1:1", dm.URL+"/u", &buf)
	ctx := context.Background()
	fk.Advance(3 * time.Minute)
	a.Cycle(ctx) // a finished round: pings
	if len(paths) != 1 {
		t.Fatalf("%v", paths)
	}
	fk.Advance(time.Hour) // the loop stands still (hung save, stuck check): the switch must say nothing
	a.PingDeadman(ctx)
	if len(paths) != 1 || !strings.Contains(buf.String(), "not pinging the dead-man switch") {
		t.Fatalf("%v\n%s", paths, buf.String())
	}
	a.Check(ctx) // the loop lives again
	a.PingDeadman(ctx)
	if len(paths) != 2 {
		t.Fatalf("%v", paths)
	}
}
