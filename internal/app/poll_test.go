// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestStatusAnsweredOncePer10s(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	ctx, cancel := context.WithCancel(context.Background())
	a.Cycle(ctx)
	done := make(chan struct{})
	go func() { a.Poll(ctx); close(done) }()
	time.Sleep(300 * time.Millisecond)
	for i := 0; i < 4; i++ {
		f.AddMessage(42, "/status")
	}
	time.Sleep(800 * time.Millisecond)
	cancel()
	<-done
	if got := f.Texts(); len(got) != 1 {
		t.Fatalf("%d replies: %q", len(got), got)
	}
}

func TestPollStepsAsideWhilePairing(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	a.Path = filepath.Join(t.TempDir(), "c.env")
	release, err := config.MarkPairing(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	a.Now = time.Now
	ctx, cancel := context.WithCancel(context.Background())
	fk.OnWait = func(time.Duration) {
		if len(fk.Sleeps()) >= 3 {
			cancel()
		}
	}
	a.Poll(ctx)
	f.Set(func(f *testfake.Telegram) {
		if len(f.PollTimeouts) != 0 {
			t.Errorf("polled %v while the setup runs", f.PollTimeouts)
		}
	})
	release()
	if config.PairingActive(a.Path, time.Now()) {
		t.Fatal("marker stays")
	}
}

func TestStatusForAnotherBotIsNotAnswered(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var buf bytes.Buffer
	a, _ := newApp(t, srv.URL, "", &buf)
	ctx, cancel := context.WithCancel(context.Background())
	a.Cycle(ctx)
	done := make(chan struct{})
	go func() { a.Poll(ctx); close(done) }()
	time.Sleep(300 * time.Millisecond)
	f.AddMessage(42, "/status@some_other_bot")
	time.Sleep(600 * time.Millisecond)
	if got := f.Texts(); len(got) != 0 {
		t.Fatalf("answered a command meant for another bot: %q", got)
	}
	f.AddMessage(42, "/status@FakeBot") // our own name, any case
	waitFor(t, func() bool { return len(f.Texts()) == 1 })
	cancel()
	<-done
}
