// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package testfake is a tiny fake Telegram Bot API server for tests and for
// tools/fake-telegram. It is not used by the daemon.
package testfake

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

type Telegram struct {
	Token string

	mu       sync.Mutex
	Sent     []string // texts of sendMessage
	Fail     bool     // sendMessage answers 500
	Fail409  bool     // getUpdates answers 409
	Desc409  string   // its description (default: another poller)
	FailCode int      // sendMessage answers this status (overrides Fail)
	// RejectContains makes sendMessage answer 400 for texts containing it.
	RejectContains string
	SendDelay      time.Duration // sendMessage waits this long before answering
	Webhook        string
	Calls          map[string]int
	updates        []map[string]any
	nextID         int64
	SendCount      int
	PollTimeouts   []int // timeout parameter of each getUpdates
	// PollHold is how long an empty getUpdates is held open (default 200ms;
	// the real API holds up to the requested timeout).
	PollHold time.Duration
}

// Handler serves /bot<token>/<method>.
func (f *Telegram) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.Calls == nil {
			f.Calls = map[string]int{}
		}
		pre := "/bot" + f.Token + "/"
		if !strings.HasPrefix(r.URL.Path, pre) {
			reply(w, 401, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
			return
		}
		method := strings.TrimPrefix(r.URL.Path, pre)
		f.Calls[method]++
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		switch method {
		case "getMe":
			reply(w, 200, ok(map[string]any{"id": 1, "username": "fakebot", "first_name": "Fake"}))
		case "sendMessage":
			f.SendCount++
			if d := f.SendDelay; d > 0 {
				f.mu.Unlock()
				select {
				case <-time.After(d):
				case <-r.Context().Done():
				}
				f.mu.Lock()
			}
			text, _ := p["text"].(string)
			if f.FailCode != 0 || (f.RejectContains != "" && strings.Contains(text, f.RejectContains)) {
				code, desc := f.FailCode, "rejected"
				if code == 0 { // a message the bot cannot send as written
					code, desc = 400, "Bad Request: message is too long"
				}
				reply(w, code, map[string]any{"ok": false, "error_code": code, "description": desc})
				return
			}
			if f.Fail {
				reply(w, 500, map[string]any{"ok": false, "error_code": 500, "description": "boom"})
				return
			}
			f.Sent = append(f.Sent, text)
			reply(w, 200, ok(map[string]any{"message_id": 1}))
		case "getWebhookInfo":
			reply(w, 200, ok(map[string]any{"url": f.Webhook}))
		case "deleteWebhook":
			f.Webhook = ""
			reply(w, 200, ok(true))
		case "getUpdates":
			if f.Fail409 {
				desc := f.Desc409
				if desc == "" {
					desc = "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"
				}
				reply(w, 409, map[string]any{"ok": false, "error_code": 409, "description": desc})
				return
			}
			off, _ := p["offset"].(float64)
			wt, _ := p["timeout"].(float64)
			f.PollTimeouts = append(f.PollTimeouts, int(wt))
			var out []map[string]any
			if off < 0 {
				if n := len(f.updates); n > 0 {
					out = f.updates[n-1:]
				}
			} else {
				for _, u := range f.updates {
					if int64(u["update_id"].(int64)) >= int64(off) {
						out = append(out, u)
					}
				}
			}
			if out == nil {
				out = []map[string]any{}
			}
			f.mu.Unlock()
			if len(out) == 0 {
				hold := f.PollHold
				if hold == 0 {
					hold = 200 * time.Millisecond
				}
				wait, _ := p["timeout"].(float64)
				hold = min(hold, time.Duration(wait*float64(time.Second)))
				select {
				case <-time.After(hold):
				case <-r.Context().Done():
				}
			}
			f.mu.Lock()
			reply(w, 200, ok(out))
		default:
			reply(w, 404, map[string]any{"ok": false, "error_code": 404, "description": "unknown"})
		}
	})
}

func ok(v any) map[string]any { return map[string]any{"ok": true, "result": v} }

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// AddMessage queues an incoming text message from chat.
func (f *Telegram) AddMessage(chat int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.updates = append(f.updates, map[string]any{
		"update_id": f.nextID,
		"message":   map[string]any{"text": text, "chat": map[string]any{"id": chat}},
	})
}

func (f *Telegram) Texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Sent...)
}

func (f *Telegram) Set(fn func(*Telegram)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// Start runs the fake on an httptest server.
func Start(token string) (*Telegram, *httptest.Server) {
	f := &Telegram{Token: token}
	return f, httptest.NewServer(f.Handler())
}
