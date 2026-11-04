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
	Proc       string              // root of /proc for the Status side panel; "" = /proc
}

// screen is what is drawn behind the dialogs.
type screen int

const (
	scMenu screen = iota
	scStatus
	scRemove
	scSys   // limits of the own-host checks
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

var menuItems = []string{"Status", "Add check", "Remove check", "System checks", "Settings", "Telegram", "Dead-man switch", "Quit"}

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
	pos    []int // caret per field, in runes; atEnd or more means the end
	secret bool  // field 0 is masked; Ctrl-R shows it
	reveal bool
	cur    string // fingerprint of the value in use, shown under the fields
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

const atEnd = 1 << 30

// caret is the edit position of field i, kept inside the text.
func (f *form) caret(i int) int { return min(max(f.pos[i], 0), len([]rune(f.vals[i]))) }

// edit changes field i at the caret: del runes before it (back) or after it (fwd), then ins is put there.
func (f *form) edit(i, back, fwd int, ins string) {
	r, p := []rune(f.vals[i]), f.caret(i)
	lo, hi := max(p-back, 0), min(p+fwd, len(r))
	in := []rune(ins)
	f.vals[i] = string(r[:lo]) + string(in) + string(r[hi:])
	f.pos[i] = lo + len(in)
}

type confirm struct {
	title  string
	lines  []string
	btns   [2]string
	idx    int
	answer func(bool)
	noKeys bool // only Enter on a button answers, not y/j/n
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
	ccur       int // system checks cursor
	sysNote    string
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
	sys        sysInfo            // /proc as the side panel last read it
	sysBuf     []byte             // reused for every /proc read
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
	for {
		m.render(out)
		if m.quit {
			return nil
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
	case scSys:
		m.sysKey(k)
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
	case k.Code == Rune && k.R == 'q':
		m.quit = true
	case k.Code == Esc: // Esc is "back" everywhere else, so here it asks first
		bg := "The watchdog keeps running in the background."
		if m.snap.stale {
			bg = "The watchdog is not running (see the hint below)."
		}
		m.cf = &confirm{title: "Quit", btns: [2]string{"Quit", "Cancel"}, noKeys: true,
			lines:  []string{"Quit little-wd?", bg},
			answer: func(yes bool) { m.quit = yes }}
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
		case "System checks":
			m.sysList()
		case "Settings":
			m.settingsForm()
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
	case k.Code == Rune && c.noKeys:
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
	was := f.idx
	defer func() {
		if f.idx != was {
			if c, i := f.at(); c == 0 {
				f.pos[i] = atEnd
			}
		}
	}()
	switch {
	case k.Code == Esc:
		m.dk, m.scr = dNone, scMenu
	case k.Code == CtrlR && f.secret:
		f.reveal = !f.reveal
	case k.Code == Up:
		f.idx = max(f.idx-1, 0)
	case k.Code == Down:
		f.idx = min(f.idx+1, f.n()-1)
	case k.Code == Tab:
		f.idx = (f.idx + 1) % f.n()
	case k.Code == BackTab:
		f.idx = (f.idx + f.n() - 1) % f.n()
	case class == 0 && (k.Code == Left || k.Code == Right || k.Code == Home || k.Code == End):
		f.pos[fi] = map[Code]int{Left: f.caret(fi) - 1, Right: f.caret(fi) + 1, Home: 0, End: atEnd}[k.Code]
	case k.Code == Left || k.Code == Right:
		d := map[bool]int{true: 1, false: -1}[k.Code == Right]
		switch class {
		case -1:
			f.kind = (f.kind + d + len(f.kinds)) % len(f.kinds)
		case 1:
			f.idx = min(max(f.idx+d, f.radio()+f.nf()), f.n()-1)
		}
	case k.Code == Backspace && class == 0:
		f.edit(fi, 1, 0, "")
	case k.Code == Del && class == 0:
		f.edit(fi, 0, 1, "")
	case k.Code == Rune && class == 0:
		f.edit(fi, 0, 0, string(k.R))
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
	n := 0
	for _, k := range kinds {
		n = max(n, len(k.labels))
	}
	f := &form{title: title, kinds: kinds, btns: btns, submit: submit, vals: make([]string, n), pos: make([]int, n)}
	for i := range f.pos {
		f.pos[i] = atEnd
	}
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

// allChecks lists every check the config enables: this machine first, then the services.
func allChecks(c *config.Config) (rs []checkRow) {
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
	for _, s := range c.Checks {
		key := strings.ToUpper(s.Type) + "_" + strings.ToUpper(s.Name)
		rs = append(rs, checkRow{name: s.Name, typ: s.Type, target: s.Target, off: func(f *config.File) { f.Remove(key) }})
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
			tok := strings.TrimSpace(f.vals[0])
			if tok == "" && m.snap.cfg != nil {
				tok = m.snap.cfg.Token // empty keeps the current token
			}
			if tok == "" {
				return "Bot token: enter the token"
			}
			m.tg = &tgState{}
			api := m.a.APIURL // set by tests only; the real URL comes from the config file
			if api == "" && m.snap.cfg != nil {
				api = m.snap.cfg.APIURL
			}
			release, perr := config.MarkPairing(m.file.Path) // the daemon pauses its own polling meanwhile
			if perr != nil {
				m.logf("Note: could not write %s (%v); the daemon may take the /start code before setup does.", config.PairingMarker(m.file.Path), perr)
			}
			o := setup.Options{
				Out: io.Discard, APIURL: api, PollWindow: m.a.PollWindow,
				ReadSecret: func(string) (string, error) { return tok, nil },
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
			cfgPath := m.file.Path // read here: the UI goroutine may replace m.file meanwhile
			m.async(func(ctx context.Context) func(*model) {
				token, chat, err := setup.Pair(ctx, o)
				if err == nil {
					config.MarkLinked(cfgPath) // the test message counts as a sent message
				}
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
	if c := m.snap.cfg; c != nil && c.Token != "" {
		m.form.cur = "current: " + checks.TokenFP(c.Token) + " — leave empty to keep it"
	}
}

func (m *model) deadmanForm() {
	ev := every(config.Defaults().DeadmanEvery)
	if c := m.snap.cfg; c != nil {
		ev = m.raw("DEADMAN_EVERY", every(c.DeadmanEvery))
	}
	f := m.newForm("Dead-man switch", []kindSpec{{labels: []string{"URL", "Every"},
		hints: []string{"Healthchecks ping URL (docs/setup.md).", "Time between pings, for example 2m. At least 60s. Set Period in Healthchecks to the same."}}},
		[]string{"Save", "Cancel"}, func(f *form) string {
			url := strings.TrimSpace(f.vals[0])
			switch {
			case strings.EqualFold(url, "off"):
				url = ""
			case url == "" && m.snap.cfg != nil:
				url = m.snap.cfg.DeadmanURL // empty keeps the current URL
			}
			tmp := m.file.Clone()
			tmp.Set("DEADMAN_URL", url)
			if !putChanged(tmp, "DEADMAN_EVERY", f.vals[1], ev) {
				return "Every: enter a time, for example 2m"
			}
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
	f.secret = true
	f.vals[1] = ev
	f.note = "Pinged once now, then at the time set under Every."
	if c := m.snap.cfg; c != nil && c.DeadmanURL != "" {
		f.kinds[0].hints[0] = "Healthchecks ping URL (docs/setup.md). Empty keeps the current URL, type off to switch it off."
		f.cur = "current: " + checks.URLFP(c.DeadmanURL) + " — leave empty to keep it, type off to switch it off"
	}
}

// settingsForm edits how often checks run and when alarms and all-clears go out.
func (m *model) settingsForm() {
	if m.snap.cfg == nil {
		m.flow, m.scr, m.dk, m.lines = "Settings", scBlank, dResult, []string{"Config is invalid: " + m.snap.cfgErr}
		return
	}
	c := m.snap.cfg
	keys := []string{"INTERVAL", "FAIL_AFTER", "RECOVER_AFTER", "TIMEOUT"}
	vals := []string{m.raw("INTERVAL", every(c.Interval)), m.raw("FAIL_AFTER", fmt.Sprint(c.FailAfter)),
		m.raw("RECOVER_AFTER", fmt.Sprint(c.RecoverAfter)), m.raw("TIMEOUT", every(c.Timeout))}
	spec := kindSpec{labels: []string{"Check every", "Alarm after", "All-clear after", "Timeout per check"}, hints: []string{
		"How often every check runs, for example 60s or 5m. At least 10s, at most the cpu window (" + every(c.CPUWindow) + ", System checks).",
		"Failed rounds in a row, then a Telegram alarm. 3 = three failed rounds in a row.",
		"Good rounds in a row after an alarm, then the all-clear. 2 = two good rounds in a row.",
		"A check that takes longer counts as failed, for example 10s. At most 10m.",
	}}
	f := m.newForm("Settings", []kindSpec{spec}, []string{"Save", "Cancel"}, func(f *form) string {
		tmp := m.file.Clone()
		for i, k := range keys {
			if !putChanged(tmp, k, f.vals[i], vals[i]) {
				return spec.labels[i] + ": enter a value"
			}
		}
		if _, err := config.Parse(strings.NewReader(tmp.Text()), false); err != nil {
			return err.Error()
		}
		m.commit(tmp, "Settings saved to "+tmp.Path+".")
		return ""
	})
	copy(f.vals, vals)
	f.note = "New values apply from the next round."
}

// putChanged writes key only if v (trimmed) differs from what the form showed; false if v is empty.
func putChanged(f *config.File, key, v, cur string) bool {
	v = strings.TrimSpace(v)
	if v != cur {
		f.Put(key, v)
	}
	return v != ""
}

// raw is key as written in the config file, else def: the form shows what the file says.
func (m *model) raw(key, def string) string {
	if v, ok := m.file.Get(key); ok && v != "" {
		return v
	}
	return def
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
