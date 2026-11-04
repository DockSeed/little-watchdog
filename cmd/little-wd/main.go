// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// little-wd: small homelab watchdog. See README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/DockSeed/little-watchdog/internal/app"
	"github.com/DockSeed/little-watchdog/internal/checks"
	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/setup"
	"github.com/DockSeed/little-watchdog/internal/state"
	"github.com/DockSeed/little-watchdog/internal/telegram"
	"github.com/DockSeed/little-watchdog/internal/tui"
)

var version = "0.0.0~dev" // set by scripts/build.sh

const usage = `usage: little-wd [command] [-config PATH]

  (none)   full-screen setup and status (needs a terminal)
  run      run the daemon
  status   show the state file as a table
  check    run all checks once, print results, send nothing
  version  print version

Default config: ` + config.DefaultPath + ` (or $LITTLE_WATCHDOG_CONFIG)
`

func main() {
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-config") || strings.HasPrefix(os.Args[1], "--config") {
		fs := flag.NewFlagSet("little-wd", flag.ExitOnError)
		cfgFlag := fs.String("config", "", "config file")
		fs.Parse(os.Args[1:])
		if !setup.IsTerminal(os.Stdin.Fd()) || !setup.IsTerminal(os.Stdout.Fd()) {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		if err := cmdTUI(config.Resolve(*cfgFlag)); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgFlag := fs.String("config", "", "config file")
	fs.Parse(os.Args[2:])
	path := config.Resolve(*cfgFlag)

	var err error
	switch cmd {
	case "run":
		err = cmdRun(path)
	case "status":
		err = cmdStatus(path)
	case "check":
		err = cmdCheck(path)
	case "version", "-version", "--version":
		fmt.Println("little-wd", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, explain(path, err))
		os.Exit(1)
	}
}

// explain turns a command's error into what the user sees: a missing config is
// a normal state of a fresh install, a permission problem names the fix.
func explain(path string, err error) string {
	switch {
	case isMissing(path, err):
		return "no config yet at " + path + ": run little-wd to set it up"
	case os.IsPermission(err):
		return "error: " + err.Error() + "\nhint: " + config.NeedGroupText()
	}
	return "error: " + err.Error()
}

// isMissing: the error is the config file itself not existing.
func isMissing(path string, err error) bool {
	var pe *fs.PathError
	return errors.As(err, &pe) && pe.Path == path && errors.Is(err, os.ErrNotExist)
}

func warn(cfg *config.Config) {
	for _, w := range cfg.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
}

// softLimit is the Go memory soft limit of the daemon; the unit hard-caps it at MemoryMax=64M.
const softLimit = 24 << 20

// tuneRuntime trims the daemon's RAM unless the operator set GOMAXPROCS / GOMEMLIMIT.
func tuneRuntime(getenv func(string) string, setProcs func(int) int, setLimit func(int64) int64) {
	if getenv("GOMAXPROCS") == "" {
		setProcs(1)
	}
	if getenv("GOMEMLIMIT") == "" {
		setLimit(softLimit)
	}
}

func cmdRun(path string) error {
	tuneRuntime(os.Getenv, runtime.GOMAXPROCS, debug.SetMemoryLimit)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP) // systemctl reload: re-read the config at once
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg, sig, err := app.WaitConfig(ctx, path, config.Defaults().StateFile, log.New(os.Stderr, "", log.LstdFlags), 10*time.Second, tellOnce)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	a := app.New(cfg, os.Stderr)
	a.Watch(path, sig)
	go func() {
		for range hup {
			a.Reload()
		}
	}()
	for _, w := range cfg.Warnings {
		a.Log.Println("warning:", w)
	}
	return a.Run(ctx)
}

// tellOnce sends one notice to Telegram with a short timeout (the daemon has no valid config yet).
func tellOnce(token string, chat int64, api, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := telegram.New(api, token, 15*time.Second).SendOnce(ctx, chat, text); err != nil {
		fmt.Fprintln(os.Stderr, "notice not sent:", err)
	}
}

func cmdTUI(path string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	a := &tui.App{
		Path: path, Winch: winch, Tick: 2 * time.Second,
		Setup: func() (func(), error) { return setup.MakeRaw(os.Stdin.Fd()) },
		Size:  func() (int, int) { return setup.Size(os.Stdout.Fd()) },
	}
	return a.Loop(ctx, os.Stdin, os.Stdout)
}

func cmdStatus(path string) error {
	cfg, err := config.Load(path, false)
	if err != nil {
		return err
	}
	warn(cfg)
	st, err := state.ReadAll(cfg.StateFile)
	if err != nil {
		return err
	}
	if len(st.Entries) == 0 {
		fmt.Println("state file is empty")
		return nil
	}
	var started time.Time
	if st.Foot != nil {
		started = st.Foot.Started
	}
	fmt.Print(state.Table(st.Entries, started, time.Now()))
	if age, stale := state.Stale(cfg.StateFile, cfg.StaleAfter(), time.Now()); stale {
		fmt.Printf("\nSTALE: the daemon has not reported for %s (is little-watchdog running?)\n", age.Round(time.Second))
	}
	return nil
}

func cmdCheck(path string) error {
	cfg, err := config.Load(path, false)
	if err != nil {
		return err
	}
	warn(cfg)
	cs := checks.Build(cfg, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// CPU needs two samples; prime it and take the second one a second later.
	for _, c := range cs {
		if c.Type() == "cpu" {
			c.Run(ctx)
			time.Sleep(time.Second)
		}
	}
	bad := 0
	for i, r := range checks.RunAll(ctx, cs, cfg.Timeout) {
		if r.Status == checks.FAIL {
			bad++
		}
		fmt.Println(checkLine(cs[i], r))
	}
	if bad > 0 {
		return fmt.Errorf("%d check(s) failing", bad)
	}
	return nil
}

// checkLine renders one result; foreign text is cleaned before it reaches the terminal.
func checkLine(c checks.Check, r checks.Result) string {
	detail := r.Value
	if r.Status == checks.FAIL {
		if r.Detail != "" {
			detail = r.Detail
		} else if r.NoAnswer {
			detail = "no answer (" + r.Value + ")"
		}
	}
	return fmt.Sprintf("%-8s %-24s %-8s %s", c.Type(), checks.Sanitize(c.Name()), r.Status, checks.Sanitize(detail))
}
