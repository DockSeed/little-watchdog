// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/deadman"
	"github.com/DockSeed/little-watchdog/internal/setup"
	"github.com/DockSeed/little-watchdog/internal/state"
)

type App struct {
	Path       string
	APIURL     string
	PollWindow time.Duration
	Setup      func() (restore func(), err error) // raw mode on, restorer
	Size       func() (w, h int)
	Winch      <-chan os.Signal
	Tick       time.Duration
	Getenv     func(string) string // default os.Getenv
}

// screen is what is drawn behind the dialogs.
type screen int

const (
	scMenu screen = iota
	scStatus
	scRemove
	scBlank // empty frame named by flow, behind add / telegram / dead-man dialogs
)

// dlg is the dialog on top of the screen, if any.
type dlg int

const (
	dNone dlg = iota
	dDetails
	dForm
	dTelegram
	dResult
)

// mode says what this user may do with the config.
type mode int

const (
	modeFull     mode = iota
	modeReadOnly      // config readable, directory not writable
	modeExplore       // config missing or not readable
)

var menuItems = []string{"Status", "Add check", "Remove check", "Telegram", "Dead-man switch", "Quit"}

func locked(item string, md mode) bool { return md != modeFull && item != "Status" && item != "Quit" }

type kindSpec struct {
	name, typ string
	labels    []string
	hints     []string
}

// form is a dialog with an optional kind choice, text fields and buttons.
type form struct {
	title  string
	kinds  []kindSpec
	kind   int
	vals   []string
	secret bool
	note   string
	idx    int // focus: kind choice (if several), fields, then buttons
	btns   []string
	err    string
	submit func(f *form) string // error text, or "" when handled
}

func (f *form) radio() int {
	if len(f.kinds) > 1 {
		return 1
	}
	return 0
}
func (f *form) nf() int { return len(f.kinds[f.kind].labels) }
func (f *form) n() int  { return f.radio() + f.nf() + len(f.btns) }

// at classifies the focus: -1 kind choice, 0 field (fi), 1 button (fi).
func (f *form) at() (class, fi int) {
	switch i := f.idx - f.radio(); {
	case i < 0:
		return -1, 0
	case i < f.nf():
		return 0, i
	default:
		return 1, i - f.nf()
	}
}

type confirm struct {
	title  string
	lines  []string
	btns   [2]string
	idx    int
	answer func(bool)
}

// tgState is the Telegram setup progress shown as a step list.
type tgState struct {
	done  [4]bool
	note  [4]string
	code  string
	bot   string
	until time.Time
}

type model struct {
	a          *App
	ctx        context.Context
	th         theme
	host       string
	file       *config.File
	mode       mode
	scr        screen
	flow       string // title of the empty frame
	dk         dlg
	cur        int // menu cursor
	scur       int // status cursor
	lcur       int // remove list cursor
	help       bool
	cached     bool // snap holds a parse of cfgText
	cfgText    string
	cfgExplore bool
	stSig      stateSig
	last       string // the frame on the terminal
	form       *form
	det        state.Entry
	items      []checkRow // remove list
	lines      []string   // result dialog
	cf         *confirm
	tg         *tgState
	snap       snapshot
	cancel     context.CancelFunc // set while background work runs
	quit       bool
	ev         chan func(*model)
	done       chan struct{}
}

var nameRe = regexp.MustCompile(`^[A-Z0-9_]+$`)

// dirWritable: can this user create files where path lives (nearest existing parent)?
func dirWritable(path string) bool {
	d := filepath.Dir(path)
	for {
		if _, err := os.Stat(d); err == nil || filepath.Dir(d) == d {
			return syscall.Access(d, 3) == nil // W_OK|X_OK
		}
		d = filepath.Dir(d)
	}
}

func (a *App) getenv() func(string) string {
	if a.Getenv != nil {
		return a.Getenv
	}
	return os.Getenv
}

func (a *App) Loop(ctx context.Context, in io.Reader, out io.Writer) (err error) {
	f, err := config.OpenFile(a.Path)
	md := modeFull
	switch {
	case os.IsPermission(err):
		f, md = &config.File{Path: a.Path}, modeExplore
	case err != nil:
		return err
	case !f.Exists() && !dirWritable(a.Path):
		md = modeExplore
	case !dirWritable(a.Path):
		md = modeReadOnly
	}
	restore, err := a.Setup()
	if err != nil {
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	m := &model{a: a, ctx: ctx, th: newTheme(a.getenv()), file: f, mode: md, ev: make(chan func(*model)), done: make(chan struct{})}
	m.host, _ = os.Hostname()
	defer func() {
		stop()
		close(m.done)
		io.WriteString(out, "\x1b[0m\x1b[?25h\x1b[?1049l")
		restore()
	}()
	io.WriteString(out, "\x1b[?1049h\x1b[?25l")
	m.refresh()
	if !f.Exists() && md == modeFull {
		m.telegramForm()
	}
	keys := make(chan Key)
	go readKeys(in, keys, m.done)
	slow := max(a.Tick, 10*time.Millisecond)
	tick := time.NewTicker(slow)
	defer tick.Stop()
	period := slow
	for {
		m.render(out)
		if m.quit {
			return nil
		}
		if want := m.tickEvery(slow); want != period { // the Telegram countdown wants seconds
			period = want
			tick.Reset(period)
		}
		select {
		case <-ctx.Done():
			return nil
		case k, ok := <-keys:
			if !ok {
				return nil
			}
			m.key(k)
		case fn := <-m.ev:
			fn(m)
		case <-tick.C:
			m.refresh()
		case <-a.Winch:
			m.last = ""
		}
	}
}

func (m *model) tickEvery(slow time.Duration) time.Duration {
	if m.dk == dTelegram && m.tg != nil && m.tg.code != "" {
		return min(slow, time.Second)
	}
	return slow
}

// post runs fn on the UI goroutine.
func (m *model) post(fn func(*model)) {
	select {
	case m.ev <- fn:
	case <-m.done:
	}
}

// async runs work in the background; its result runs on the UI goroutine. Esc cancels.
func (m *model) async(work func(ctx context.Context) func(*model)) {
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel, m.lines = cancel, nil
	if m.dk != dTelegram {
		m.dk = dResult
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.post(func(*model) { panic(r) }) // re-panic on the UI goroutine so the terminal is restored
			}
		}()
		fn := work(ctx)
		m.post(func(m *model) {
			aborted := ctx.Err() != nil
			m.cancel = nil
			cancel()
			m.dk = dResult
			if aborted { // Esc: no result is acted on, nothing is saved or restarted
				m.logf("Cancelled.")
				return
			}
			fn(m)
		})
	}()
}

func (m *model) logf(format string, a ...any) {
	m.lines = append(m.lines, strings.Split(strings.TrimRight(fmt.Sprintf(format, a...), "\n"), "\n")...)
}

func (m *model) key(k Key) {
	if k.Code == CtrlC {
		m.quit = true
		return
	}
	switch {
	case m.help:
		if k.Code == Esc || k.Code == Enter || (k.Code == Rune && (k.R == 'q' || k.R == '?')) {
			m.help = false
		}
		return
	case m.cf != nil:
		m.confirmKey(k)
		return
	}
	switch m.dk {
	case dForm:
		m.formKey(k)
		return
	case dDetails:
		if k.Code == Esc || k.Code == Enter || (k.Code == Rune && k.R == 'q') {
			m.dk = dNone
		}
		return
	case dTelegram, dResult:
		switch {
		case m.cancel != nil:
			if k.Code == Esc || (m.dk == dTelegram && k.Code == Enter) {
				m.cancel()
			}
		default:
			m.dk, m.scr = dNone, scMenu
			m.refresh()
		}
		return
	}
	if k.Code == Rune && k.R == '?' {
		m.help = true
		return
	}
	switch m.scr {
	case scMenu:
		m.menuKey(k)
	case scStatus:
		m.statusKey(k)
	case scRemove:
		m.listKey(k)
	}
}

// move applies up/down/j/k to cursor c within n items.
func move(k Key, c, n int) int {
	switch {
	case k.Code == Up || (k.Code == Rune && k.R == 'k'):
		return max(c-1, 0)
	case k.Code == Down || (k.Code == Rune && k.R == 'j'):
		return min(c+1, max(n-1, 0))
	}
	return c
}

func isBack(k Key) bool { return k.Code == Esc || (k.Code == Rune && k.R == 'q') }

func (m *model) menuKey(k Key) {
	m.cur = move(k, m.cur, len(menuItems))
	switch {
	case isBack(k):
		m.quit = true
	case k.Code == Enter:
		it := menuItems[m.cur]
		if locked(it, m.mode) {
			m.flow, m.scr, m.dk, m.lines = it, scBlank, dResult, []string{needGroup()}
			return
		}
		switch it {
		case "Status":
			m.scr = scStatus
			m.refresh()
		case "Add check":
			m.addForm()
		case "Remove check":
			m.removeList()
		case "Telegram":
			m.telegramForm()
		case "Dead-man switch":
			m.deadmanForm()
		case "Quit":
			m.quit = true
		}
	}
}

func (m *model) statusKey(k Key) {
	_, ents := m.statusRows()
	m.scur = move(k, m.scur, len(ents))
	switch {
	case isBack(k):
		m.scr = scMenu
	case k.Code == Enter && m.scur < len(ents):
		m.det, m.dk = ents[m.scur], dDetails
	}
}

func (m *model) listKey(k Key) {
	m.lcur = move(k, m.lcur, len(m.items))
	switch {
	case isBack(k):
		m.scr = scMenu
	case k.Code == Enter && m.lcur < len(m.items):
		it := m.items[m.lcur]
		lines := []string{"Remove " + it.name + "?", "It is no longer checked and no longer reported."}
		if it.host {
			lines = append(lines, "To turn it on again, put this line in the config: "+it.back)
		}
		m.cf = &confirm{
			title: "Remove check", btns: [2]string{"Remove", "Cancel"}, lines: lines,
			answer: func(yes bool) {
				if !yes {
					return
				}
				tmp := m.file.Clone()
				it.off(tmp)
				m.flow, m.scr, m.lcur = "Remove check", scBlank, 0
				m.lines = nil
				m.commit(tmp, "Removed "+it.name+".")
			}}
	}
}

func (m *model) confirmKey(k Key) {
	c := m.cf
	answer := func(yes bool) {
		m.cf = nil
		c.answer(yes)
	}
	switch {
	case k.Code == Rune && strings.ContainsRune("yYjJ", k.R):
		answer(true)
	case k.Code == Esc || (k.Code == Rune && (k.R == 'n' || k.R == 'N')):
		answer(false)
	case k.Code == Enter:
		answer(c.idx == 0)
	case k.Code == Tab || k.Code == BackTab || k.Code == Left || k.Code == Right || k.Code == Up || k.Code == Down:
		c.idx = 1 - c.idx
	}
}

func (m *model) formKey(k Key) {
	f := m.form
	class, fi := f.at()
	switch {
	case k.Code == Esc:
		m.dk, m.scr = dNone, scMenu
	case k.Code == Up:
		f.idx = max(f.idx-1, 0)
	case k.Code == Down:
		f.idx = min(f.idx+1, f.n()-1)
	case k.Code == Tab:
		f.idx = (f.idx + 1) % f.n()
	case k.Code == BackTab:
		f.idx = (f.idx + f.n() - 1) % f.n()
	case k.Code == Left || k.Code == Right:
		d := map[bool]int{true: 1, false: -1}[k.Code == Right]
		switch class {
		case -1:
			f.kind = (f.kind + d + len(f.kinds)) % len(f.kinds)
		case 1:
			f.idx = min(max(f.idx+d, f.radio()+f.nf()), f.n()-1)
		}
	case k.Code == Backspace && class == 0:
		if v := []rune(f.vals[fi]); len(v) > 0 {
			f.vals[fi] = string(v[:len(v)-1])
		}
	case k.Code == Rune && class == 0:
		f.vals[fi] += string(k.R)
	case k.Code == Rune && class == -1 && k.R == ' ':
		f.kind = (f.kind + 1) % len(f.kinds)
	case k.Code == Enter && class == 1 && fi == len(f.btns)-1:
		m.dk, m.scr = dNone, scMenu
	case k.Code == Enter && class == 0 && fi < f.nf()-1, k.Code == Enter && class == -1:
		f.idx++
	case k.Code == Enter:
		f.err = f.submit(f)
	}
}

func (m *model) newForm(title string, kinds []kindSpec, btns []string, submit func(*form) string) *form {
	f := &form{title: title, kinds: kinds, btns: btns, submit: submit, vals: make([]string, 3)}
	f.idx = f.radio()
	m.form, m.dk, m.scr, m.flow = f, dForm, scBlank, title
	return f
}

func (m *model) addForm() {
	kinds := []kindSpec{
		{"Website", "http", []string{"Name", "URL", "Text"}, []string{nameHint, "The page address, http:// or https://.", "Optional: OK only if this text is on the page."}},
		{"Port", "tcp", []string{"Name", "host:port"}, []string{nameHint, "A TCP port that should be open, for example 192.0.2.5:22."}},
		{"Host", "host", []string{"Name", "host:port"}, []string{nameHint, "Any port of the host; a refused connection still counts as alive."}},
	}
	f := m.newForm("Add check", kinds, []string{"Check and save", "Cancel"}, func(f *form) string {
		typ, v := f.kinds[f.kind].typ, f.vals
		name := strings.ToUpper(strings.TrimSpace(v[0]))
		val := strings.TrimSpace(v[1])
		if typ == "http" && strings.TrimSpace(v[2]) != "" {
			val += " contains=" + strings.TrimSpace(v[2])
		}
		key := strings.ToUpper(typ) + "_" + name
		if !nameRe.MatchString(name) {
			return "Name: use letters, digits and _ only"
		}
		if m.file.Has(key) {
			return key + " exists, remove it first"
		}
		tmp := m.file.Clone()
		tmp.Set(key, val)
		cfg, err := config.Parse(strings.NewReader(tmp.Text()), false)
		if err != nil {
			return err.Error()
		}
		m.async(func(ctx context.Context) func(*model) {
			ctx, cancel := context.WithTimeout(ctx, cfg.Timeout+time.Second)
			defer cancel()
			res := "no result"
			for _, c := range checks.Build(cfg, func(string, ...any) {}) {
				if c.Type() == typ && c.Name() == strings.ToLower(name) {
					r := c.Run(ctx)
					res = string(r.Status) + " " + r.Value + " " + r.Detail
				}
			}
			return func(m *model) { m.commit(tmp, "Check once: "+strings.TrimSpace(res)) }
		})
		return ""
	})
	f.note = "Checked once before saving."
}

const nameHint = "Name: letters, digits and _ only. Shown in Telegram messages."

type checkRow struct {
	name, typ, target string
	host              bool
	off               func(f *config.File) // removes the check from the config
	back              string               // config line that turns an own-host check on again
}

// allChecks lists every check the config enables: services first, then this host.
func allChecks(c *config.Config) (rs []checkRow) {
	for _, s := range c.Checks {
		key := strings.ToUpper(s.Type) + "_" + strings.ToUpper(s.Name)
		rs = append(rs, checkRow{name: s.Name, typ: s.Type, target: s.Target, off: func(f *config.File) { f.Remove(key) }})
	}
	sw := func(name, typ, target, key string, v float64) {
		rs = append(rs, checkRow{name: name, typ: typ, target: target, host: true, back: fmt.Sprintf("%s=%g", key, v),
			off: func(f *config.File) { f.Put(key, "0") }})
	}
	if c.CPUMaxPct > 0 {
		sw("cpu", "cpu", fmt.Sprintf("alarm above %g%% (%s average)", c.CPUMaxPct, every(c.CPUWindow)), "CPU_MAX_PCT", c.CPUMaxPct)
	}
	if c.MemMinAvail > 0 {
		sw("memory", "mem", fmt.Sprintf("alarm below %g%% available", c.MemMinAvail), "MEM_MIN_AVAIL_PCT", c.MemMinAvail)
	}
	if c.DiskMinFree > 0 {
		for i, p := range c.DiskPaths {
			rest := append(append([]string{}, c.DiskPaths[:i]...), c.DiskPaths[i+1:]...)
			rs = append(rs, checkRow{name: "disk " + p, typ: "disk", target: fmt.Sprintf("alarm below %g%% free", c.DiskMinFree), host: true,
				back: "DISK_PATHS=" + strings.Join(c.DiskPaths, ","),
				off:  func(f *config.File) { f.Put("DISK_PATHS", strings.Join(rest, ",")) }})
		}
	}
	if c.TempMaxC > 0 {
		sw("temperature", "temp", fmt.Sprintf("alarm above %g C", c.TempMaxC), "TEMP_MAX_C", c.TempMaxC)
	}
	return rs
}

func (m *model) removeList() {
	m.items, m.lcur = nil, 0
	if m.snap.cfg == nil {
		m.flow, m.scr, m.dk, m.lines = "Remove check", scBlank, dResult, []string{"Config is invalid: " + m.snap.cfgErr}
		return
	}
	m.items = allChecks(m.snap.cfg)
	m.scr = scRemove
}

func (m *model) telegramForm() {
	m.newForm("Telegram setup", []kindSpec{{labels: []string{"Bot token"}, hints: []string{"Token from @BotFather (see docs/setup.md). The input is hidden."}}},
		[]string{"Continue", "Cancel"}, func(f *form) string {
			v := f.vals
			m.tg = &tgState{}
			api := m.a.APIURL // set by tests only; the real URL comes from the config file
			if api == "" && m.snap.cfg != nil {
				api = m.snap.cfg.APIURL
			}
			release, _ := config.MarkPairing(m.file.Path) // the daemon pauses its own polling meanwhile
			o := setup.Options{
				Out: io.Discard, APIURL: api, PollWindow: m.a.PollWindow,
				ReadSecret: func(string) (string, error) { return v[0], nil },
				Step: func(s setup.Step) {
					m.post(func(m *model) {
						t := m.tg
						if t == nil || s.N < 1 || s.N > 4 {
							return
						}
						t.done[s.N-1], t.note[s.N-1] = s.Done, s.Note
						if s.Code != "" {
							t.code, t.bot, t.until = s.Code, s.Note, s.Until
						}
					})
				},
				Confirm: func(p string) (bool, error) {
					ch := make(chan bool, 1)
					m.post(func(m *model) {
						m.cf = &confirm{title: "Webhook", btns: [2]string{"Delete webhook", "Cancel"}, answer: func(b bool) { ch <- b },
							lines: []string{"A webhook is set for this bot. Telegram delivers messages either to a webhook or to getUpdates, never both, so the /start code cannot arrive until it is removed. Whatever uses the webhook today will stop receiving messages.", "", p}}
					})
					select {
					case b := <-ch:
						return b, nil
					case <-m.ctx.Done():
						return false, m.ctx.Err()
					}
				},
			}
			m.dk = dTelegram
			m.async(func(ctx context.Context) func(*model) {
				token, chat, err := setup.Pair(ctx, o)
				release()
				return func(m *model) {
					m.tg = nil
					if err != nil {
						m.logf("Failed: %v", err)
						return
					}
					tmp := m.file.Clone()
					tmp.Set("TELEGRAM_TOKEN", token)
					tmp.Set("TELEGRAM_CHAT_ID", fmt.Sprint(chat))
					m.commit(tmp)
				}
			})
			return ""
		})
	m.form.secret = true
}

func (m *model) deadmanForm() {
	f := m.newForm("Dead-man switch", []kindSpec{{labels: []string{"URL"}, hints: []string{"Healthchecks ping URL (docs/setup.md). Empty = off."}}},
		[]string{"Save", "Cancel"}, func(f *form) string {
			url := strings.TrimSpace(f.vals[0])
			tmp := m.file.Clone()
			tmp.Set("DEADMAN_URL", url)
			if _, err := config.Parse(strings.NewReader(tmp.Text()), false); err != nil {
				return err.Error()
			}
			m.async(func(ctx context.Context) func(*model) {
				res := "Dead-man switch off."
				if url != "" {
					res = "Ping OK."
					if err := deadman.New(url, 0, func(string, ...any) {}).Ping(ctx, url); err != nil {
						res = fmt.Sprintf("Ping failed: %v (URL is kept anyway)", err)
					}
				}
				return func(m *model) { m.commit(tmp, res) }
			})
			return ""
		})
	f.note = "Empty = off. Pinged once now, then every DEADMAN_EVERY."
}

func needGroup() string { return config.NeedGroupText() }

// commit saves tmp as the new config and tells the user how the daemon picks it up.
func (m *model) commit(tmp *config.File, msg ...string) {
	m.dk = dResult
	for _, s := range msg {
		m.logf("%s", s)
	}
	if err := tmp.Save(); err != nil {
		m.logf("Cannot write %s: %v", tmp.Path, err)
		if os.IsPermission(err) {
			m.logf("%s", needGroup())
		}
		return
	}
	m.file = tmp
	m.refresh()
	if c := m.snap.cfg; c != nil {
		if _, stale := state.Stale(c.StateFile, c.StaleAfter(), time.Now()); stale {
			m.logf("Saved. The daemon does not seem to run (no recent heartbeat): start it with sudo systemctl start little-watchdog.")
			return
		}
	}
	m.logf("Saved — little-wd applies it within one interval.")
}
