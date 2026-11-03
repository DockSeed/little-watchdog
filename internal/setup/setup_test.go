// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/testfake"
)

func TestPairSurvivesAnUnreadableUpdateAnswer(t *testing.T) {
	var code atomic.Value
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		switch m := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]; m {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"username":"b","first_name":"B"}}`)
		case "getWebhookInfo":
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case "getUpdates":
			if off, _ := p["offset"].(float64); off < 0 {
				fmt.Fprint(w, `{"ok":true,"result":[]}`)
				return
			}
			if polls.Add(1) == 1 {
				io.WriteString(w, "<html>proxy error</html>")
				return
			}
			c, _ := code.Load().(string)
			fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":5,"message":{"text":"/start %s","chat":{"id":77}}}]}`, c)
		case "sendMessage":
			fmt.Fprint(w, `{"ok":true,"result":{}}`)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, chat, err := Pair(ctx, Options{
		Out: io.Discard, APIURL: srv.URL, PollWindow: time.Minute,
		ReadSecret: func(string) (string, error) { return "1:a", nil },
		Confirm:    func(string) (bool, error) { return true, nil },
		Step: func(s Step) {
			if s.Code != "" {
				code.Store(s.Code)
			}
		},
	})
	if err != nil || chat != 77 {
		t.Fatal(chat, err)
	}
	if polls.Load() < 2 {
		t.Fatal("no second poll after the unreadable answer")
	}
}

const tok = "123456:SECRETTOKEN"

func TestNewCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c, err := NewCode()
		if err != nil || len(c) != 6 {
			t.Fatal(c, err)
		}
		seen[c] = true
	}
	if len(seen) < 45 {
		t.Fatal("codes not random")
	}
}

// pair drives Pair; the "user" sends /start CODE as soon as it is printed.
func pair(t *testing.T, f *testfake.Telegram, apiURL string, confirm bool, send bool, window time.Duration) (string, int64, string, error) {
	var out syncBuf
	o := Options{
		Out:        &out,
		ReadSecret: func(string) (string, error) { return tok, nil },
		Confirm:    func(string) (bool, error) { return confirm, nil },
		APIURL:     apiURL, PollWindow: window,
	}
	if send {
		go func() {
			for i := 0; i < 200; i++ {
				time.Sleep(25 * time.Millisecond)
				if _, rest, ok := strings.Cut(out.String(), "Send `/start "); ok {
					code, _, _ := strings.Cut(rest, "`")
					f.AddMessage(777, "/start WRONG1")
					f.AddMessage(777, "/start "+code)
					return
				}
			}
		}()
	}
	token, chat, err := Pair(context.Background(), o)
	return token, chat, out.String(), err
}

func TestPairHappyPath(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.AddMessage(1, "old stale update")
	token, chat, out, err := pair(t, f, srv.URL, false, true, 3*time.Second)
	if err != nil || token != tok || chat != 777 {
		t.Fatal(token, chat, err, out)
	}
	if !strings.Contains(out, "@fakebot") {
		t.Fatal(out)
	}
	if got := f.Texts(); len(got) != 1 || got[0] != TestText(checks.Host()) {
		t.Fatal(got)
	}
	if strings.Contains(out, tok) {
		t.Fatal("token printed")
	}
}

func TestPairTimeoutAnd409(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	_, _, _, err := pair(t, f, srv.URL, false, false, 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatal(err)
	}
	f.Set(func(f *testfake.Telegram) { f.Fail409 = true })
	_, _, _, err = pair(t, f, srv.URL, false, false, time.Second)
	if err == nil || !strings.Contains(err.Error(), "409") || !strings.Contains(err.Error(), "webhook") {
		t.Fatal(err)
	}
}

func TestPairWebhook(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	f.Set(func(f *testfake.Telegram) { f.Webhook = "https://example.org/hook" })
	_, _, _, err := pair(t, f, srv.URL, false, false, time.Second)
	if err == nil || !strings.Contains(err.Error(), "webhook") {
		t.Fatal(err)
	}
	if _, _, _, err := pair(t, f, srv.URL, true, true, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	f.Set(func(f *testfake.Telegram) {
		if f.Webhook != "" {
			t.Error("webhook not deleted")
		}
	})
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestPairReportsEveryStepInOrder(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	var mu sync.Mutex
	var steps []string
	var out syncBuf
	o := Options{
		Out: &out, APIURL: srv.URL, PollWindow: 3 * time.Second,
		ReadSecret: func(string) (string, error) { return tok, nil },
		Confirm:    func(string) (bool, error) { return true, nil },
		Step: func(s Step) {
			mu.Lock()
			defer mu.Unlock()
			steps = append(steps, fmt.Sprintf("%d:%v", s.N, s.Done))
			if s.Code != "" {
				f.AddMessage(5, "/start "+s.Code)
			}
		},
	}
	if _, chat, err := Pair(context.Background(), o); err != nil || chat != 5 {
		t.Fatal(chat, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(steps, " "); got != "1:true 2:true 3:false 3:true 4:true" {
		t.Fatal(got)
	}
}

func TestPairFailurePaths(t *testing.T) {
	f, srv := testfake.Start(tok)
	defer srv.Close()
	base := func() Options {
		return Options{Out: &syncBuf{}, APIURL: srv.URL, PollWindow: 2 * time.Second,
			ReadSecret: func(string) (string, error) { return tok, nil },
			Confirm:    func(string) (bool, error) { return true, nil }}
	}
	o := base()
	o.ReadSecret = func(string) (string, error) { return "999:wrong", nil }
	if _, _, err := Pair(context.Background(), o); err == nil || !strings.Contains(err.Error(), "getMe failed") {
		t.Fatalf("wrong token: %v", err)
	}
	o = base()
	o.ReadSecret = func(string) (string, error) { return "", errors.New("no tty") }
	if _, _, err := Pair(context.Background(), o); err == nil || err.Error() != "no tty" {
		t.Fatalf("secret error: %v", err)
	}
	f.Set(func(f *testfake.Telegram) { f.Webhook = "https://example.org/h" })
	o = base()
	o.Confirm = func(string) (bool, error) { return false, errors.New("asked and failed") }
	if _, _, err := Pair(context.Background(), o); err == nil || err.Error() != "asked and failed" {
		t.Fatalf("confirm error: %v", err)
	}
	f.Set(func(f *testfake.Telegram) { f.Webhook = ""; f.Fail409 = true })
	if _, _, err := Pair(context.Background(), base()); err == nil || !strings.Contains(err.Error(), "409") || !strings.Contains(err.Error(), "daemon steps aside") {
		t.Fatalf("409: %v", err)
	}
	f.Set(func(f *testfake.Telegram) { f.Fail409 = false; f.FailCode = 500 }) // the test message fails
	o = base()
	o.Step = func(s Step) {
		if s.Code != "" {
			f.AddMessage(5, "/start "+s.Code)
		}
	}
	if _, _, err := Pair(context.Background(), o); err == nil || !strings.Contains(err.Error(), "test message") {
		t.Fatalf("test message: %v", err)
	}
}

func TestTestTextHostAndFallback(t *testing.T) {
	want := "little-watchdog on box: this chat is linked. You get a message here when the status of a check changes. /status lists all checks."
	if got := TestText("box"); got != want {
		t.Fatalf("%q", got)
	}
	if got := TestText(""); !strings.HasPrefix(got, "little-watchdog: this chat is linked.") {
		t.Fatalf("%q", got)
	}
}
