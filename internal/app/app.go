// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package app ties config, checks, state, Telegram and dead-man together.
package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
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
	Host   string // machine name for messages; empty = leave out
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

	stateMu  sync.Mutex   // state writes and the move to another place never overlap
	mu       sync.RWMutex // guards Cfg, Checks, TG, DM and notes against reload
	redact   *RedactWriter
	notes    []note                   // own notices waiting for Telegram
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
	a.Host = checks.Host()
	a.Eng = state.NewEngine(cfg.FailAfter, cfg.RecoverAfter)
	a.Eng.Host = a.Host
	if _, set := config.RuntimeDir(); set {
		a.Eng.FinePath = config.RAMStateFile() + state.FineSuffix
	}
	a.TG = telegram.New(cfg.APIURL, cfg.Token, sendTimeout)
	a.TG.Log = l.Printf
	a.DM = a.newDM(cfg)
	return a
}

func (a *App) newDM(cfg *config.Config) *deadman.Switch {
	dm := deadman.New(cfg.DeadmanURL, cfg.DeadmanEvery, a.Log.Printf)
	dm.OnBad = func(err error) {
		a.notify(telegram.Msg(a.Host, "the dead-man ping was rejected: "+telegram.Esc(err.Error())+". Check the UUID in DEADMAN_URL."))
	}
	url := cfg.DeadmanURL
	dm.OnPing = func(err error) {
		now := a.Now()
		if a.Eng.NoteDeadman(checks.URLFP(url), err, now) {
			a.notify(telegram.Msg(a.Host, fmt.Sprintf("dead-man switch is active, first ping OK at %s. If the pings stop, Healthchecks alarms you.", now.Format("15:04"))))
		}
	}
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
	for _, p := range []string{a.Eng.FinePath, cfg.StateFile + state.FineSuffix} { // RAM, else the disk fallback
		if p != "" && a.Eng.LoadFine(p, valid, a.Now()) {
			break
		}
	}
	if cfg.DeadmanURL == "" {
		a.Eng.ClearDeadman()
	}
}

// fallbackToDisk starts on the disk state file when STATE_IN_RAM=yes but the RAM directory
// cannot be written (the installed unit has no RuntimeDirectory), instead of running blind.
func (a *App) fallbackToDisk() {
	cfg, _, _, _ := a.cur()
	if !cfg.StateInRAM || cfg.DiskState == "" {
		return
	}
	err := ramWritable(cfg.StateFile)
	if err != nil {
		a.Log.Printf("warning: STATE_IN_RAM=yes but %v; update the package: the unit has no RuntimeDirectory. Using %s", err, cfg.DiskState)
		a.mu.Lock()
		a.Cfg.StateFile = cfg.DiskState
		a.mu.Unlock()
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
	if err != nil && ctx.Err() == context.Canceled { // shutting down: not a Telegram problem
		return err
	}
	dm.NoteSend(err == nil)
	if !errors.Is(err, telegram.ErrHeld) { // a flood wait is Telegram answering
		a.Eng.NoteSend(err, a.Now())
	}
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
	a.notify(telegram.Msg(a.Host, fmt.Sprintf("Telegram moved this chat to id %d. The new id is used until restart. Update TELEGRAM_CHAT_ID (little-wd, Telegram).", to)))
	return true
}

// probe is the dead-man's reachability test: a real sendMessage to the chat, one attempt.
func (a *App) probe(ctx context.Context) error {
	_, _, _, dm := a.cur()
	err := a.sendN(ctx, telegram.Msg(a.Host, fmt.Sprintf("Telegram is reachable again after %d failed sends.", dm.Fails())), true)
	if errors.Is(err, telegram.ErrHeld) {
		return nil // flood wait: Telegram answers, it only asks us to slow down
	}
	return err
}

func (a *App) saveState(force bool) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	cfg, _, _, _ := a.cur() // read under stateMu: a move cannot swap the path in between
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
	a.Eng.FixStart()
	a.tgMu.Lock()
	a.lastCycle = a.Clk.Now()
	a.tgMu.Unlock()
	a.stateMu.Lock()
	cfg, _, _, _ := a.cur()
	e := beat(cfg.StateFile, a.Now())
	a.stateMu.Unlock()
	if e != nil && a.debug {
		a.Log.Printf("debug: heartbeat: %v", e)
	}
}

// ramWritable checks the place of the RAM state file. Without RUNTIME_DIRECTORY (not started by
// the unit) the daemon writes nowhere below /run/little-watchdog: it belongs to the service.
func ramWritable(stateFile string) error {
	if err := needRuntimeDir(); err != nil {
		return err
	}
	return state.Writable(stateFile)
}

func needRuntimeDir() error {
	if _, set := config.RuntimeDir(); !set {
		return errors.New("RUNTIME_DIRECTORY is not set (not started by the unit)")
	}
	return nil
}

// beat marks the daemon alive in the runtime directory, which is RAM. Without one (or when it
// cannot be written) the heartbeat sits next to the state file, as before 0.6.1.
func beat(stateFile string, now time.Time) error {
	if _, set := config.RuntimeDir(); set && state.Beat(config.RAMStateFile(), now) == nil {
		return nil
	}
	return state.Beat(stateFile, now)
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
	a.Eng.SamplePSI(now)
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

// lateAfter is how long after its event a message may arrive before it says so.
const lateAfter = 2 * time.Minute

// late appends the event time to a message that reaches Telegram long after it happened.
func (a *App) late(text string, at time.Time) string {
	now := a.Now()
	if at.IsZero() || now.Sub(at) <= lateAfter {
		return text
	}
	return text + " (sent late, event at " + state.When(at, now) + ")"
}

// texts renders the queued messages as they are sent.
func (a *App) texts(q []state.Queued) []string {
	t := make([]string, len(q))
	for i, m := range q {
		t[i] = a.late(m.Text, m.At)
	}
	return t
}

// chunk returns how many leading texts fit into one Telegram message.
func chunk(t []string) int {
	n, size := 0, 0
	for _, x := range t {
		l := telegram.Len16(x) + 1
		if n > 0 && size+l > telegram.MaxText {
			break
		}
		n, size = n+1, size+l
	}
	return n
}

// logSendFail logs a failed send, but not the one caused by shutting down.
func (a *App) logSendFail(ctx context.Context, err error) {
	if ctx.Err() == nil {
		a.Log.Printf("warning: telegram send failed: %v", err)
	}
}

// splitLines cuts a long text at line breaks into parts that each fit one message.
func splitLines(text string) []string {
	ls := strings.Split(text, "\n")
	var out []string
	for len(ls) > 0 {
		n := chunk(ls)
		out = append(out, strings.Join(ls[:n], "\n"))
		ls = ls[n:]
	}
	return out
}

// flush delivers pending messages bundled; a transient failure stops it until the next round.
func (a *App) flush(ctx context.Context) {
	q := a.Eng.Queue()
	txt := a.texts(q) // rendered once: the sizes measured are the texts sent (the late suffix cannot change in between)
	for len(q) > 0 {
		n := chunk(txt)
		if !a.sendChunk(ctx, q[:n], txt[:n]) {
			return
		}
		q, txt = q[n:], txt[n:]
	}
}

// sendChunk sends q as one message; txt are the rendered texts of q.
func (a *App) sendChunk(ctx context.Context, q []state.Queued, txt []string) bool {
	err := a.send(ctx, strings.Join(txt, "\n"))
	if err == nil {
		for _, m := range q {
			a.Eng.Delivered(m.ID, m.Text, a.Now())
		}
		return true
	}
	if !telegram.Permanent(err) || telegram.BadSetup(err) { // token/chat problems keep the queue
		a.logSendFail(ctx, err)
		return false
	}
	// rejected for good: find the offender one by one so the others still get through
	for i, m := range q {
		err := a.send(ctx, txt[i])
		switch {
		case err == nil:
			a.Eng.Delivered(m.ID, m.Text, a.Now())
		case telegram.BadSetup(err):
			return false
		case telegram.Permanent(err):
			a.Eng.Drop(m.ID, m.Text)
			a.Log.Printf("warning: message for %s dropped, Telegram rejects it: %v", m.ID, err)
		default:
			a.logSendFail(ctx, err)
			return false
		}
	}
	return true
}

// Run is the daemon main loop; it returns when ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	a.fallbackToDisk()
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
			a.saveState(true) // SIGTERM: keep counters and flap history
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

// StartText is the message sent when the daemon starts; a long list of names is cut.
// The names come in /status order: this machine first, then the services.
func StartText(host string, cs []checks.Check) string {
	cs = slices.Clone(cs)
	order := append(slices.Clone(state.MachineTypes), state.ServiceTypes...)
	slices.SortFunc(cs, func(a, b checks.Check) int { // as Snapshot + Brief: group rank, type, name
		return cmp.Or(state.Rank(order, a.Type())-state.Rank(order, b.Type()), strings.Compare(a.Type(), b.Type()), strings.Compare(a.Name(), b.Name()))
	})
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = telegram.B(checks.Sanitize(c.Name()))
	}
	n := len(names)
	if n > 6 {
		n = 5
	}
	for {
		list := strings.Join(names[:n], ", ")
		if n < len(names) {
			list += fmt.Sprintf(" and %d more", len(names)-n)
		}
		t := "watching " + checks.Count(len(names))
		if len(names) > 0 {
			t += " (" + list + ")"
		}
		t = telegram.Msg(host, t+". /status shows the details.")
		if n <= 1 || len([]rune(telegram.Plain(t))) <= 200 {
			return t
		}
		n--
	}
}

// sender owns all slow Telegram work so the check loop never waits for it.
func (a *App) sender(ctx context.Context) {
	_, cs, _, _ := a.cur()
	a.notify(StartText(a.Host, cs)) // queued like any notice, so a failed send is retried
	a.flushNotes(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.kick:
			a.Deliver(ctx)
		}
	}
}

// conflictKind tells from the API text whether a webhook or another poller causes a getUpdates 409.
func conflictKind(desc string) string {
	d := strings.ToLower(desc)
	switch {
	case strings.Contains(d, "webhook"):
		return state.ConflictWebhook
	case strings.Contains(d, "other getupdates") || strings.Contains(d, "another") || strings.Contains(d, "one bot instance"):
		return state.ConflictPoller
	}
	return state.ConflictUnknown
}

// conflictText is the one notice per 409 streak.
func conflictText(host, kind string) string {
	if kind == state.ConflictWebhook {
		return telegram.Msg(host, "a webhook is set for this bot, so /status does not work here. Remove the webhook (setup in little-wd, Telegram, offers it) or use one bot per machine.")
	}
	return telegram.Msg(host, "this bot is also used elsewhere, so /status does not work here. Use one bot per machine.")
}

// Poll long-polls getUpdates and answers /status from the configured chat.
func (a *App) Poll(ctx context.Context) {
	var offset int64
	var tg *telegram.Client
	backoff := 5 * time.Second
	warned := false
	conflict := a.Eng.Conflict() // a streak that began before a restart is not announced again
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
				kind := conflictKind(ae.Desc)
				if !warned {
					a.Log.Printf("warning: Telegram getUpdates returned 409 (%s): %s; /status is unavailable until that is fixed (retrying with backoff up to 5 min)", kind, ae.Desc)
					warned = true
				}
				a.Eng.NoteUpdates(kind, a.Now())
				if conflict == "" { // one notice per streak, not per retry
					a.notify(conflictText(a.Host, kind))
				}
				conflict = kind
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
		if conflict != "" {
			conflict = ""
			a.Eng.NoteUpdates("", a.Now())
			a.Log.Printf("Telegram getUpdates works again, /status is available")
		}
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
				for _, part := range splitLines(state.Brief(a.Host, a.Eng.Snapshot(), a.Eng.RecoverN(), a.Eng.StartedAt(), a.Now())) {
					if err := a.sendN(ctx, part, true); err != nil {
						if ctx.Err() == nil {
							a.Log.Printf("warning: /status reply failed: %v", err)
						}
						break
					}
				}
			}
		}
	}
}
