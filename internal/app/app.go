// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package app ties config, checks, state, Telegram and dead-man together.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/clock"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/deadman"
	"github.com/DockSeed/little-watchdog/internal/state"
	"github.com/DockSeed/little-watchdog/internal/telegram"
)

// sendTimeout limits one Telegram request, independent of TIMEOUT.
// statusEvery is the shortest gap between two /status replies.
const statusEvery = 10 * time.Second

const sendTimeout = 15 * time.Second

// RedactWriter replaces every known secret (old tokens too) in everything written through it.
type RedactWriter struct {
	W       io.Writer
	mu      sync.Mutex
	secrets []string
}

func (r *RedactWriter) Add(secret string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if secret != "" {
		r.secrets = append(r.secrets, secret)
	}
}

func (r *RedactWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	q := string(p)
	for _, sec := range r.secrets {
		q = strings.ReplaceAll(q, sec, "<token>")
	}
	r.mu.Unlock()
	if q == string(p) {
		return r.W.Write(p)
	}
	if _, err := io.WriteString(r.W, q); err != nil {
		return 0, err
	}
	return len(p), nil
}

type App struct {
	Cfg    *config.Config
	Log    *log.Logger
	Checks []checks.Check
	Eng    *state.Engine
	TG     *telegram.Client
	DM     *deadman.Switch
	Now    func() time.Time
	Clk    clock.Clock // pacing, retries and waits of Telegram and dead-man

	Path string  // config file watched for changes; empty = no reload
	sig  FileSig // stat of Path when it was last read

	mu       sync.RWMutex // guards Cfg, Checks, TG, DM and notes against reload
	redact   *RedactWriter
	notes    []string                 // own notices (config problems) waiting for Telegram
	bad      bool                     // an invalid config was already announced
	badReads int                      // failed reads of the changed config in a row
	moved    struct{ from, to int64 } // Telegram moved the group to a supergroup

	kick  chan struct{}
	hup   chan struct{}
	debug bool

	tgMu      sync.Mutex
	tgBadAt   time.Time // last time a rejected token/chat was logged; zero = Telegram fine
	saveBadAt time.Time // same for failed state saves
	saveBad   bool
	lastCycle time.Time // end of the last finished round (Clk time)
}

// New builds the app from a config; checks may be replaced for tests.
func New(cfg *config.Config, out io.Writer) *App {
	rw := &RedactWriter{W: out}
	rw.Add(cfg.Token)
	l := log.New(rw, "", log.LstdFlags)
	a := &App{Cfg: cfg, Log: l, redact: rw, Now: time.Now, Clk: clock.Real(), kick: make(chan struct{}, 1), hup: make(chan struct{}, 1), debug: os.Getenv("LITTLE_WATCHDOG_DEBUG") != ""}
	a.Checks = checks.Build(cfg, l.Printf)
	a.Eng = state.NewEngine(cfg.FailAfter, cfg.RecoverAfter)
	a.TG = telegram.New(cfg.APIURL, cfg.Token, sendTimeout)
	a.DM = a.newDM(cfg)
	return a
}

func (a *App) newDM(cfg *config.Config) *deadman.Switch {
	dm := deadman.New(cfg.DeadmanURL, cfg.DeadmanEvery, a.Log.Printf)
	dm.OnBad = func(err error) { a.notify("dead-man ping rejected: " + err.Error() + " (wrong UUID in DEADMAN_URL?)") }
	return dm
}

// Reload makes the next cycle re-read the config at once (SIGHUP).
func (a *App) Reload() {
	select {
	case a.hup <- struct{}{}:
	default:
	}
}

// SetClock replaces the clock of the app and its current clients.
func (a *App) SetClock(c clock.Clock) {
	a.Clk, a.TG.Clk, a.DM.Clk = c, c, c
}

// LoadState restores the state file for the current check set.
func (a *App) LoadState() {
	cfg, cs, _, _ := a.cur()
	valid := map[string]bool{}
	for _, c := range cs {
		valid[c.ID()] = true
	}
	if w := a.Eng.Load(cfg.StateFile, valid); w != "" {
		a.Log.Printf("warning: %s", w)
	}
}

// cur returns a consistent view of what a reload may replace.
func (a *App) cur() (*config.Config, []checks.Check, *telegram.Client, *deadman.Switch) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.Cfg, a.Checks, a.TG, a.DM
}

// chat is the target chat: the configured one, or where Telegram moved it.
func (a *App) chat() int64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.moved.from == a.Cfg.ChatID && a.moved.to != 0 {
		return a.moved.to
	}
	return a.Cfg.ChatID
}

func (a *App) send(ctx context.Context, text string) error { return a.sendN(ctx, text, false) }

// sendN sends text to the chat (once = a single attempt) and tells the dead-man the outcome.
func (a *App) sendN(ctx context.Context, text string, once bool) error {
	_, _, tg, dm := a.cur()
	do := tg.SendMessage
	if once {
		do = tg.SendOnce
	}
	err := do(ctx, a.chat(), text)
	var ae *telegram.APIError
	if errors.As(err, &ae) && ae.MigrateTo != 0 && a.migrate(ae.MigrateTo) {
		err = do(ctx, a.chat(), text)
	}
	dm.NoteSend(err == nil)
	a.noteTelegram(err)
	return err
}

// noteTelegram logs a rejected token/chat loudly (again every 30 min) and the recovery once.
func (a *App) noteTelegram(err error) {
	a.tgMu.Lock()
	defer a.tgMu.Unlock()
	switch {
	case err == nil && !a.tgBadAt.IsZero():
		a.tgBadAt = time.Time{}
		a.Log.Printf("Telegram accepts messages again")
	case telegram.BadSetup(err):
		if now := a.Now(); a.tgBadAt.IsZero() || now.Sub(a.tgBadAt) >= 30*time.Minute {
			a.tgBadAt = now
			a.Log.Printf("ERROR: Telegram rejects the token or chat (%v): alerts are kept and retried every cycle, fix TELEGRAM_TOKEN / TELEGRAM_CHAT_ID", err)
		}
	}
}

// migrate adopts the new chat id in memory (the daemon may not write /etc) and reports it once.
func (a *App) migrate(to int64) bool {
	a.mu.Lock()
	if a.moved.to == to && a.moved.from == a.Cfg.ChatID {
		a.mu.Unlock()
		return false
	}
	a.moved.from, a.moved.to = a.Cfg.ChatID, to
	a.mu.Unlock()
	a.Log.Printf("warning: Telegram moved the chat to %d; using it until restart, update TELEGRAM_CHAT_ID", to)
	a.notify(fmt.Sprintf("chat moved to %d, update TELEGRAM_CHAT_ID (little-wd → Telegram)", to))
	return true
}

// probe is the dead-man's reachability test: a real sendMessage to the chat, one attempt.
func (a *App) probe(ctx context.Context) error {
	_, _, _, dm := a.cur()
	err := a.sendN(ctx, fmt.Sprintf("little-watchdog: Telegram reachable again after %d failed sends", dm.Fails()), true)
	if errors.Is(err, telegram.ErrHeld) {
		return nil // flood wait: Telegram answers, it only asks us to slow down
	}
	return err
}

func (a *App) saveState(force bool) {
	cfg, _, _, _ := a.cur()
	var err error
	if force {
		err = a.Eng.Save(cfg.StateFile)
	} else {
		err = a.Eng.SaveIfDue(cfg.StateFile, a.Now())
	}
	a.saveResult(err)
}

// endCycle marks a finished round: the heartbeat (the loop runs, whether or not
// the state could be saved) and the time the dead-man switch looks at.
func (a *App) endCycle() {
	cfg, _, _, _ := a.cur()
	a.tgMu.Lock()
	a.lastCycle = a.Clk.Now()
	a.tgMu.Unlock()
	if e := state.Beat(cfg.StateFile, a.Now()); e != nil && a.debug {
		a.Log.Printf("debug: heartbeat: %v", e)
	}
}

// cycleAge is the time since the last finished round; ok is false before the first one.
func (a *App) cycleAge() (age time.Duration, ok bool) {
	a.tgMu.Lock()
	defer a.tgMu.Unlock()
	if a.lastCycle.IsZero() {
		return 0, false
	}
	return a.Clk.Now().Sub(a.lastCycle), true
}

// saveResult logs a failing state save at most every 10 min and its recovery once.
func (a *App) saveResult(err error) {
	a.tgMu.Lock()
	defer a.tgMu.Unlock()
	switch {
	case err == nil && a.saveBad:
		a.saveBad = false
		a.Log.Printf("state file saving works again")
	case err != nil:
		if now := a.Now(); !a.saveBad || now.Sub(a.saveBadAt) >= 10*time.Minute {
			a.saveBad, a.saveBadAt = true, now
			a.Log.Printf("warning: saving state failed (kept in memory, retried every cycle): %v", err)
		}
	}
}

// Check runs every check once and updates the state. Results of an aborted cycle are discarded.
func (a *App) Check(ctx context.Context) {
	a.reload()
	cfg, cs, _, _ := a.cur()
	rs := checks.RunAll(ctx, cs, cfg.Timeout)
	if ctx.Err() != nil {
		a.saveState(true)
		return
	}
	now := a.Now()
	for i, c := range cs {
		a.Eng.Observe(c, rs[i], now)
	}
	a.saveState(false)
	a.endCycle()
}

// Deliver sends pending messages, saves the state and pings the dead-man switch.
func (a *App) Deliver(ctx context.Context) {
	a.flushNotes(ctx)
	a.flush(ctx)
	a.saveState(false)
}

// PingDeadman pings the switch when it is due; it never waits for Telegram's pacing.
func (a *App) PingDeadman(ctx context.Context) {
	cfg, _, _, dm := a.cur()
	// A switch that keeps pinging while the checks stand still would hide exactly
	// what it is for. No finished round within the longest a round may take (plus
	// two intervals): stay silent, the service alarms after its grace time.
	if age, ok := a.cycleAge(); ok && age > 2*cfg.Interval+cfg.RoundMax()+30*time.Second {
		a.Log.Printf("warning: no finished round for %s, not pinging the dead-man switch", age.Round(time.Second))
		return
	}
	if dm.Cycle(ctx, a.probe) {
		a.Log.Printf("warning: %d Telegram sends failed in a row, dead-man switch told /fail", deadman.Threshold)
	}
}

// Cycle is Check, Deliver and PingDeadman in turn.
func (a *App) Cycle(ctx context.Context) {
	a.Check(ctx)
	a.Deliver(ctx)
	a.PingDeadman(ctx)
}

// chunk returns how many leading messages fit into one Telegram message.
func chunk(q []state.Queued) int {
	n, size := 0, 0
	for _, m := range q {
		l := telegram.Len16(m.Text) + 1
		if n > 0 && size+l > telegram.MaxText {
			break
		}
		n, size = n+1, size+l
	}
	return n
}

func joinText(q []state.Queued) string {
	t := make([]string, len(q))
	for i, m := range q {
		t[i] = m.Text
	}
	return strings.Join(t, "\n")
}

// flush delivers pending messages bundled; a transient failure stops it until the next round.
func (a *App) flush(ctx context.Context) {
	q := a.Eng.Queue()
	for len(q) > 0 {
		n := chunk(q)
		if !a.sendChunk(ctx, q[:n]) {
			return
		}
		q = q[n:]
	}
}

func (a *App) sendChunk(ctx context.Context, q []state.Queued) bool {
	err := a.send(ctx, joinText(q))
	if err == nil {
		for _, m := range q {
			a.Eng.Delivered(m.ID, m.Text, a.Now())
		}
		return true
	}
	if !telegram.Permanent(err) || telegram.BadSetup(err) { // token/chat problems keep the queue
		a.Log.Printf("warning: telegram send failed: %v", err)
		return false
	}
	// rejected for good: find the offender one by one so the others still get through
	for _, m := range q {
		err := a.send(ctx, m.Text)
		switch {
		case err == nil:
			a.Eng.Delivered(m.ID, m.Text, a.Now())
		case telegram.BadSetup(err):
			return false
		case telegram.Permanent(err):
			a.Eng.Drop(m.ID, m.Text)
			a.Log.Printf("warning: message for %s dropped, Telegram rejects it: %v", m.ID, err)
		default:
			a.Log.Printf("warning: telegram send failed: %v", err)
			return false
		}
	}
	return true
}

// Run is the daemon main loop; it returns when ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	a.LoadState()
	a.tgMu.Lock()
	a.lastCycle = a.Clk.Now() // the first round gets the same allowance as any other
	a.tgMu.Unlock()
	cfg, cs, _, _ := a.cur()
	a.Log.Printf("little-watchdog starting with %d checks, interval %s", len(cs), cfg.Interval)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		a.deadmanLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		a.Poll(ctx)
	}()
	go func() {
		defer wg.Done()
		a.sender(ctx)
	}()
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		a.Check(ctx)
		if c, _, _, _ := a.cur(); c.Interval != cfg.Interval {
			cfg = c
			t.Reset(cfg.Interval)
		}
		select {
		case a.kick <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-t.C:
		case <-a.hup:
			a.sig = FileSig{}
		}
	}
}

// deadmanLoop pings on its own clock so a Telegram flood wait cannot silence it.
func (a *App) deadmanLoop(ctx context.Context) {
	for a.Clk.Sleep(ctx, 15*time.Second) == nil {
		a.PingDeadman(ctx)
	}
}

// sender owns all slow Telegram work so the check loop never waits for it.
func (a *App) sender(ctx context.Context) {
	_, cs, _, _ := a.cur()
	if err := a.send(ctx, fmt.Sprintf("little-watchdog started, %d checks", len(cs))); err != nil {
		a.Log.Printf("warning: start message failed: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.kick:
			a.Deliver(ctx)
		}
	}
}

// Poll long-polls getUpdates and answers /status from the configured chat.
func (a *App) Poll(ctx context.Context) {
	var offset int64
	var tg *telegram.Client
	backoff := 5 * time.Second
	warned := false
	var lastStatus time.Time
	var bot string // our own @name: a command for another bot in the chat is not ours
	for ctx.Err() == nil {
		if a.Path != "" && config.PairingActive(a.Path, a.Now()) { // the setup polls this bot right now
			if a.Clk.Sleep(ctx, 5*time.Second) != nil {
				return
			}
			tg = nil
			continue
		}
		_, _, cur, _ := a.cur()
		if cur != tg { // first round or new token: skip old updates
			tg, offset, bot = cur, 0, ""
			if me, err := tg.GetMe(ctx); err == nil {
				bot = me.Username
			}
			if us, err := tg.GetUpdates(ctx, -1, 0); err == nil && len(us) > 0 {
				offset = us[len(us)-1].UpdateID + 1
			}
		}
		began := a.Clk.Now()
		us, err := tg.GetUpdates(ctx, offset, telegram.PollDefault)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var ae *telegram.APIError
			if errors.As(err, &ae) && ae.Code == 409 {
				if !warned {
					a.Log.Printf("warning: Telegram getUpdates returned 409: a webhook is set or another process polls this bot; /status is unavailable until that is fixed (retrying with backoff up to 5 min)")
					warned = true
				}
			} else {
				a.Log.Printf("warning: getUpdates: %v", err)
			}
			if errors.Is(err, telegram.ErrBadAnswer) {
				tg = nil // do not hang on an update that cannot be read: skip to the latest
			}
			if a.Clk.Sleep(ctx, backoff) != nil {
				return
			}
			backoff = min(backoff*2, 5*time.Minute)
			continue
		}
		backoff, warned = 5*time.Second, false
		if len(us) == 0 && a.Clk.Now().Sub(began) < time.Second && a.Clk.Sleep(ctx, time.Second) != nil {
			return // an empty answer that came at once is no long poll: do not spin
		}
		for _, u := range us {
			offset = u.UpdateID + 1
			if u.Message == nil {
				continue
			}
			if u.Message.Chat.ID != a.chat() {
				if a.debug {
					a.Log.Printf("debug: ignoring message from foreign chat")
				}
				continue
			}
			cmd, _, _ := strings.Cut(strings.TrimSpace(u.Message.Text), " ")
			cmd, who, _ := strings.Cut(cmd, "@")
			if who != "" && bot != "" && !strings.EqualFold(who, bot) {
				continue
			}
			if now := a.Now(); cmd == "/status" && now.Sub(lastStatus) >= statusEvery {
				lastStatus = now
				if err := a.sendN(ctx, state.Brief(a.Eng.Snapshot(), a.Now()), true); err != nil {
					a.Log.Printf("warning: /status reply failed: %v", err)
				}
			}
		}
	}
}
