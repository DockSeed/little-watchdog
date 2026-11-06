// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// little-wd: small homelab watchdog. See README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

// Help texts. docs/little-wd.1 says the same; TestHelpMatchesTheManpage keeps them from drifting.
const (
	configOpt = "  -config PATH  config file (default " + config.DefaultPath + ",\n                or $LITTLE_WATCHDOG_CONFIG)\n"

	usage = `usage: little-wd [command] [-config PATH]

Commands:
  (none)   full-screen setup and status (needs a terminal)
  run      run the watchdog (the service does this)
  status   print the last results as a table
  check    run all checks once, print results, send nothing
  version  print the version

Options:
` + configOpt + `  -h, --help    show this help; "little-wd COMMAND -h" helps with a command
  --version     print the version

More: man little-wd
`

	tuiHelp = `usage: little-wd [-config PATH]

Open the full-screen program: see the checks and change the config. Needs a
terminal. Quitting does not stop the service. Press ? inside for the keys.

Options:
` + configOpt + `
Exit status: 0 on quit, 1 on error, 2 without a terminal or on a bad option.
`

	runHelp = `usage: little-wd run [-config PATH]

Run the watchdog in the foreground. The systemd service does this; run it by
hand only to try something. Waits for a usable config instead of failing.
SIGHUP re-reads the config, SIGINT and SIGTERM stop it.

Options:
` + configOpt + `
Exit status: 0 after SIGINT or SIGTERM, 1 on error, 2 on a bad option.
`

	statusHelp = `usage: little-wd status [-config PATH]

Print the last results from the state file as a table. Runs no check. Adds a
STALE line when the service has not reported lately.

Options:
` + configOpt + `
Exit status: 0 if the state was read (also when checks fail or STALE),
1 if the config or state cannot be read, 2 on a bad option.
`

	checkHelp = `usage: little-wd check [-config PATH]

Run every check once and print one line per check. Sends nothing to Telegram
and writes no state. Takes about a second longer when CPU is checked.

Options:
` + configOpt + `
Exit status: 0 if all checks are OK, 1 if a check fails or the config cannot
be read, 2 on a bad option.
`

	versionHelp = `usage: little-wd version

Print the version.
`
)

// commands maps a command to its help text; "" is the full-screen program.
var commands = map[string]string{"": tuiHelp, "run": runHelp, "status": statusHelp, "check": checkHelp, "version": versionHelp}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is main without the process: it returns the exit status.
func run(args []string, out, errw io.Writer) int {
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-config") && !strings.HasPrefix(args[0], "--config") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return 0
	case "-version", "--version":
		cmd = "version"
	}
	help, ok := commands[cmd]
	if !ok {
		fmt.Fprintf(errw, "unknown command: %s\n\n%s", cmd, usage)
		return 2
	}
	path, code, done := parseFlags(cmd, help, args, out, errw)
	if done {
		return code
	}
	var err error
	switch cmd {
	case "":
		if !setup.IsTerminal(os.Stdin.Fd()) || !setup.IsTerminal(os.Stdout.Fd()) {
			fmt.Fprint(errw, "little-wd needs a terminal.\n\n"+usage)
			return 2
		}
		err = cmdTUI(path)
	case "run":
		err = cmdRun(path)
	case "status":
		err = cmdStatus(path)
	case "check":
		err = cmdCheck(path)
	case "version":
		fmt.Fprintln(out, "little-wd", version)
	}
	if err != nil {
		fmt.Fprintln(errw, explain(path, err))
		return 1
	}
	return 0
}

// parseFlags reads -config. done is true when the command is already finished: help was
// asked for (code 0) or an option is wrong (code 2, with that command's help).
func parseFlags(cmd, help string, args []string, out, errw io.Writer) (path string, code int, done bool) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgFlag := fs.String("config", "", "config file")
	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(out, help)
		return "", 0, true
	case err != nil:
		fmt.Fprintf(errw, "%s\n\n%s", strings.TrimSpace("little-wd "+cmd+": "+err.Error()), help)
		return "", 2, true
	}
	return config.Resolve(*cfgFlag), 0, false
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
	fmt.Print(state.Table(st.Entries, cfg.RecoverAfter, started, time.Now()))
	if age, stale := state.Stale(cfg.StateFile, cfg.StaleAfter(), time.Now(), config.RAMStateFile()); stale {
		fmt.Printf("\nSTALE: the service has not reported for %s. Is it running? systemctl status little-watchdog\n", age.Round(time.Second))
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
