// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestSendTruncatesTo4096(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	c := New(srv.URL, tok, time.Second)
	if err := c.SendMessage(context.Background(), 1, strings.Repeat("ä", 5000)); err != nil {
		t.Fatal(err)
	}
	if got := f.Texts(); len(got) != 1 || len([]rune(got[0])) != MaxText {
		t.Fatalf("sent %d runes", len([]rune(got[0])))
	}
}

func TestPermanent(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want bool
	}{
		{&APIError{Code: 400}, true}, {&APIError{Code: 403}, true}, {&APIError{Code: 429}, false},
		{&APIError{Code: 500}, false}, {errors.New("timeout"), false}, {nil, false},
	} {
		if got := Permanent(tt.err); got != tt.want {
			t.Errorf("%v: %v", tt.err, got)
		}
	}
}

func TestSendOnceNoRetry(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(500)
		w.Write([]byte(`{"ok":false,"error_code":500,"description":"x"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, tok, time.Second)
	c.Backoff = time.Millisecond
	if err := c.SendOnce(context.Background(), 1, "x"); err == nil || n.Load() != 1 {
		t.Fatal(err, n.Load())
	}
}

func TestHeaderLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Big", strings.Repeat("h", 100<<10))
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL, tok, time.Second).GetMe(context.Background()); err == nil {
		t.Fatal("oversized headers accepted")
	}
}

func TestGetUpdatesSendsLimit(t *testing.T) {
	var got []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		got = append(got, p["limit"].(float64))
		w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()
	c, _ := fakeClient(srv.URL)
	c.GetUpdates(context.Background(), 5, 50)
	c.GetUpdates(context.Background(), -1, 0)
	if len(got) != 2 || got[0] != 10 || got[1] != 1 {
		t.Fatalf("limits %v", got)
	}
}

func TestUnreadableAnswerIsErrBadAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":true,"result":[` + string(make([]byte, maxResp+10))))
	}))
	defer srv.Close()
	c, _ := fakeClient(srv.URL)
	if _, err := c.GetUpdates(context.Background(), 1, 25); !errors.Is(err, ErrBadAnswer) {
		t.Fatalf("err %v", err)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) { // token in the URL must not travel on
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer srv.Close()
	c, _ := fakeClient(srv.URL)
	if err := c.SendOnce(context.Background(), 1, "x"); err == nil {
		t.Fatal("redirect accepted as success")
	}
	if hits.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestBadSetup(t *testing.T) {
	for _, tt := range []struct {
		code int
		desc string
		want bool
	}{
		{401, "Unauthorized", true}, {403, "Forbidden: bot was blocked by the user", true},
		{400, "Bad Request: chat not found", true}, {404, "Not Found", true},
		{400, "Bad Request: message is too long", false}, {400, "Bad Request: can't parse entities", false},
		{429, "Too Many Requests", false}, {500, "boom", false},
	} {
		if got := BadSetup(&APIError{Code: tt.code, Desc: tt.desc}); got != tt.want {
			t.Errorf("%d %q: %v", tt.code, tt.desc, got)
		}
	}
}

const tok = "123456:SECRETTOKEN"

func TestSendAndMe(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	c := New(srv.URL, tok, time.Second)
	ctx := context.Background()
	me, err := c.GetMe(ctx)
	if err != nil || me.Username != "fakebot" {
		t.Fatal(me, err)
	}
	if err := c.SendMessage(ctx, -42, "hi"); err != nil {
		t.Fatal(err)
	}
	if got := f.Texts(); len(got) != 1 || got[0] != "hi" {
		t.Fatal(got)
	}
}

func TestRetry(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(500)
			w.Write([]byte(`{"ok":false,"error_code":500,"description":"x"}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	c, _ := fakeClient(srv.URL)
	c.Backoff = time.Millisecond
	if err := c.SendMessage(context.Background(), 1, "x"); err != nil || n.Load() != 3 {
		t.Fatal(err, n.Load())
	}
	// permanent failure: 3 attempts then error
	n.Store(-100)
	if err := c.SendMessage(context.Background(), 1, "x"); err == nil || n.Load() != -97 {
		t.Fatal(err, n.Load())
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(400)
		w.Write([]byte(`{"ok":false,"error_code":400,"description":"chat not found"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, tok, time.Second)
	c.Backoff = time.Millisecond
	err := c.SendMessage(context.Background(), 1, "x")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != 400 || n.Load() != 1 {
		t.Fatal(err, n.Load())
	}
}

func TestTokenNeverInErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connection refused; Go's error would contain the full URL with token
	c, _ := fakeClient(url)
	c.Backoff = time.Millisecond
	for name, err := range map[string]error{
		"send": c.SendMessage(context.Background(), 1, "x"),
		"me":   func() error { _, e := c.GetMe(context.Background()); return e }(),
	} {
		if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// API description echoing the token is scrubbed as well
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"error_code":401,"description":"bad ` + tok + `"}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, tok, time.Second).GetMe(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatal(err)
	}
}

func TestUpdatesAnd409(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	c := New(srv.URL, tok, time.Second)
	f.AddMessage(7, "/status")
	us, err := c.GetUpdates(context.Background(), 0, 0)
	if err != nil || len(us) != 1 || us[0].Message.Chat.ID != 7 || us[0].Message.Text != "/status" {
		t.Fatal(us, err)
	}
	f.Set(func(f *testfake.Telegram) { f.Fail409 = true })
	_, err = c.GetUpdates(context.Background(), 0, 0)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != 409 {
		t.Fatal(err)
	}
}

func TestMessageLengthIsCountedInUTF16Units(t *testing.T) {
	if Len16("abc") != 3 || Len16("❌") != 1 || Len16("𝔘") != 2 {
		t.Fatal(Len16("abc"), Len16("❌"), Len16("𝔘"))
	}
	long := strings.Repeat("𝔘", MaxText) // 2 units each
	if got := Cut16(long, MaxText); Len16(got) != MaxText || !utf8.ValidString(got) {
		t.Fatal(Len16(got))
	}
	if Cut16("short", 10) != "short" {
		t.Fatal()
	}
}

func TestChatWide400IsASetupProblem(t *testing.T) {
	for desc, want := range map[string]bool{
		"Bad Request: have no rights to send a message": true,
		"Bad Request: group chat was deactivated":       true,
		"Bad Request: message is too long":              false,
		"Bad Request: can't parse entities: x":          false,
	} {
		if BadSetup(&APIError{Code: 400, Desc: desc}) != want {
			t.Errorf("%q", desc)
		}
	}
}
