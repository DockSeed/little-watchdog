// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/state"
	"github.com/DockSeed/little-watchdog/internal/telegram"
)

// FileSig identifies one version of the config file (mtime, size, inode).
type FileSig struct {
	Mtime, Size int64
	Ino         uint64
}

// StatConfig returns the signature of path; the zero value if it cannot be read.
func StatConfig(path string) FileSig {
	st, err := os.Stat(path)
	if err != nil {
		return FileSig{}
	}
	s := FileSig{Mtime: st.ModTime().UnixNano(), Size: st.Size()}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		s.Ino = sys.Ino
	}
	return s
}

// Watch makes the app re-read path whenever its signature differs from sig.
func (a *App) Watch(path string, sig FileSig) { a.Path, a.sig = path, sig }

// reload applies a changed config. An invalid one keeps the old config; it is announced only
// when two reads in a row failed (an editor may have been halfway through writing).
func (a *App) reload() {
	if a.Path == "" {
		return
	}
	sig := StatConfig(a.Path)
	if sig == a.sig {
		return
	}
	nc, err := config.Load(a.Path, true)
	if err != nil {
		a.badReads++
		a.Log.Printf("warning: config reload failed, keeping previous: %v", err)
		if a.badReads < 2 {
			return // sig stays: the next cycle reads again
		}
		a.sig = sig
		if !a.bad {
			a.bad = true
			a.notify(invalidText(err))
		}
		return
	}
	a.sig, a.badReads = sig, 0
	if a.bad {
		a.bad = false
		a.notify("config OK again")
	}
	a.apply(nc)
}

func invalidText(err error) string { return "config invalid, keeping previous: " + err.Error() }

// apply swaps in nc: unchanged checks and their state stay, a new token or URL gets a new client.
func (a *App) apply(nc *config.Config) {
	for _, w := range nc.Warnings {
		a.Log.Printf("warning: %s", w)
	}
	a.redact.Add(nc.Token)
	old, oldCS, tg, dm := a.cur()
	cs := checks.Build(nc, a.Log.Printf)
	valid := map[string]bool{}
	for i, c := range cs {
		valid[c.ID()] = true
		for _, o := range oldCS {
			if o.ID() == c.ID() && sameSetup(old, nc, c) {
				cs[i] = o
			}
		}
	}
	if nc.APIURL != old.APIURL || nc.Token != old.Token {
		defer tg.Close()
		tg = telegram.New(nc.APIURL, nc.Token, sendTimeout)
		tg.Clk = a.Clk
	}
	if nc.DeadmanURL != old.DeadmanURL || nc.DeadmanEvery != old.DeadmanEvery {
		defer dm.Close()
		dm = a.newDM(nc)
		dm.Clk = a.Clk
	}
	a.Eng.Retain(valid)
	a.Eng.Configure(nc.FailAfter, nc.RecoverAfter)
	a.mu.Lock()
	a.Cfg, a.Checks, a.TG, a.DM = nc, cs, tg, dm
	a.mu.Unlock()
	a.Log.Printf("config reloaded: %d checks, interval %s", len(cs), nc.Interval)
}

// sameSetup reports whether c would behave exactly like its predecessor under nc.
func sameSetup(old, nc *config.Config, c checks.Check) bool {
	switch c.Type() {
	case "http", "tcp", "host":
		return old.Timeout == nc.Timeout && spec(old, c) == spec(nc, c)
	case "disk":
		return old.DiskMinFree == nc.DiskMinFree
	case "mem":
		return old.MemMinAvail == nc.MemMinAvail
	case "temp":
		return old.TempMaxC == nc.TempMaxC && old.TempSensor == nc.TempSensor
	case "cpu":
		return old.CPUMaxPct == nc.CPUMaxPct && old.CPUWindow == nc.CPUWindow
	}
	return false
}

func spec(cfg *config.Config, c checks.Check) config.CheckSpec {
	for _, s := range cfg.Checks {
		if s.Type == c.Type() && s.Name == c.Name() {
			return s
		}
	}
	return config.CheckSpec{}
}

// notify queues an own notice for Telegram, stamped with the local time.
func (a *App) notify(text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.notes = append(a.notes, a.Now().Format("15:04")+" "+text)
	if len(a.notes) > 20 {
		a.notes = a.notes[len(a.notes)-20:]
	}
}

// flushNotes sends queued notices; a transient failure keeps the rest for the next round.
func (a *App) flushNotes(ctx context.Context) {
	a.mu.Lock()
	n := a.notes
	a.notes = nil
	a.mu.Unlock()
	for i, t := range n {
		err := a.send(ctx, t)
		if err != nil && (!telegram.Permanent(err) || telegram.BadSetup(err)) {
			a.Log.Printf("warning: telegram send failed: %v", err)
			a.mu.Lock()
			a.notes = append(n[i:], a.notes...)
			a.mu.Unlock()
			return
		}
		if err != nil {
			a.Log.Printf("warning: notice dropped, Telegram rejects it: %v", err)
		}
	}
}

// WaitConfig returns the config once path holds a valid one. While it does not, it
// logs (once per distinct problem), tells Telegram once (when token and chat are
// readable), keeps a heartbeat at stateFile so the UI sees a live daemon, and
// checks again every poll.
func WaitConfig(ctx context.Context, path, stateFile string, l *log.Logger, poll time.Duration, tell func(token string, chat int64, api, text string)) (*config.Config, FileSig, error) {
	told, last := false, ""
	for {
		state.Beat(config.LenientStateFile(path, stateFile), time.Now())
		sig := StatConfig(path) // before Load, so a change in between is not missed
		cfg, err := config.Load(path, true)
		if err == nil {
			return cfg, sig, nil
		}
		msg := "config not usable, waiting for a valid one: " + err.Error()
		if errors.Is(err, os.ErrNotExist) {
			msg = "no config yet at " + path + ", waiting (run little-wd to set it up)"
		}
		if msg != last {
			last = msg
			l.Printf("%s", msg)
		}
		if !told {
			told = true
			if tok, chat, api := config.TelegramOnly(path); tok != "" && chat != 0 && tell != nil {
				tell(tok, chat, api, "little-watchdog cannot start: "+err.Error()+" (waiting for a fixed config)")
			}
		}
		select {
		case <-ctx.Done():
			return nil, sig, ctx.Err()
		case <-time.After(poll):
		}
	}
}
