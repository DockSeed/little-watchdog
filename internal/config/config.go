// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

// Package config parses the little-watchdog env file (KEY=value, # comments).
// The file is never handed to a shell.
package config

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultPath = "/etc/little-watchdog/little-watchdog.env"
	EnvPath     = "LITTLE_WATCHDOG_CONFIG"
)

// CheckSpec is one HTTP_/TCP_/HOST_/PING_ entry.
type CheckSpec struct {
	Name     string // lower-case check name from the key; with Type the identity of the check
	Type     string // "http", "tcp", "host", "ping"
	Target   string // URL, host:port, or host (ping)
	Contains string // http only
	Label    string // name="..." option: the name shown to people; empty: Name
	Mode     string // http only: "" (0.3 rules: 200-399, no redirects), ModeLenient or ModeStrict
}

// HTTP modes (option mode=): lenient follows redirects and takes any answer below 500,
// strict follows redirects and takes only 2xx. Without mode= the 0.3 rules apply.
const (
	ModeLenient = "lenient"
	ModeStrict  = "strict"
)

// ID is the check's key in the state file, the same for every name= it gets.
func (s CheckSpec) ID() string { return s.Type + ":" + s.Name }

// Shown is the name people see: name= if set, else the name from the key.
func (s CheckSpec) Shown() string { return cmp.Or(s.Label, s.Name) }

type Config struct {
	Token        string
	ChatID       int64
	APIURL       string
	DeadmanURL   string
	DeadmanEvery time.Duration
	Interval     time.Duration
	FailAfter    int
	RecoverAfter int
	Timeout      time.Duration
	StateFile    string // where the daemon keeps its state: the disk path, or RAMStateFile() when StateInRAM
	StateInRAM   bool   // STATE_IN_RAM=yes: the state lives under /run, no disk writes
	DiskState    string // the state file on disk (STATE_FILE), also while StateInRAM overrides StateFile
	Checks       []CheckSpec
	DiskPaths    []string
	DiskMinFree  float64
	MemMinAvail  float64
	TempMaxC     float64
	TempSensor   string
	CPUMaxPct    float64
	CPUWindow    time.Duration

	Warnings []string // non-fatal findings, to be logged by the caller
}

// Error is a fatal config problem with location.
type Error struct {
	Line int
	Key  string
	Msg  string
}

func (e *Error) Error() string {
	if e.Key == "" && e.Line > 0 {
		return fmt.Sprintf("config line %d: %s", e.Line, e.Msg)
	}
	if e.Line > 0 {
		return fmt.Sprintf("config line %d: %s: %s", e.Line, e.Key, e.Msg)
	}
	return fmt.Sprintf("config: %s: %s", e.Key, e.Msg)
}

func Defaults() *Config {
	return &Config{
		APIURL:       "https://api.telegram.org",
		DeadmanEvery: 2 * time.Minute,
		Interval:     60 * time.Second,
		FailAfter:    3,
		RecoverAfter: 2,
		Timeout:      10 * time.Second,
		StateFile:    "/var/lib/little-watchdog/state.json",
		DiskPaths:    []string{"/"},
		DiskMinFree:  10,
		MemMinAvail:  5,
		TempMaxC:     85,
		CPUMaxPct:    90,
		CPUWindow:    15 * time.Minute,
	}
}

var nameRe = regexp.MustCompile(`^[A-Z0-9_]+$`)

// Resolve picks the config path: flag value, then env, then default.
func Resolve(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv(EnvPath); v != "" {
		return v
	}
	return DefaultPath
}

// Load reads and parses path and warns about loose permissions.
func Load(path string, requireTelegram bool) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, err := Parse(f, requireTelegram)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil {
		if w := PermWarning(path, st); w != "" {
			cfg.Warnings = append(cfg.Warnings, w)
		}
	}
	return cfg, nil
}

// Group may edit the config; variable so tests need no such group.
var Group = "little-wd"

// PermWarning reports a mode that lets others read the token: any "other" bit,
// or group bits while the file's group is not Group.
func PermWarning(path string, st os.FileInfo) string {
	m := st.Mode().Perm()
	if m&0o007 != 0 {
		return fmt.Sprintf("%s has mode %04o, readable by others (it contains the bot token)", path, m)
	}
	if m&0o070 == 0 {
		return ""
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		if g, err := user.LookupGroup(Group); err == nil && strconv.Itoa(int(sys.Gid)) == g.Gid {
			return ""
		}
	}
	return fmt.Sprintf("%s has mode %04o, readable by a group other than %s (it contains the bot token)", path, m, Group)
}

func parseURL(v string) error {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid URL %s (expected http:// or https:// URL)", redactURL(v))
	}
	return nil
}

// parseSecureURL is parseURL for URLs that carry a secret: https, or http only to this machine.
func parseSecureURL(v string) error {
	if err := parseURL(v); err != nil {
		return err
	}
	u, _ := url.Parse(v)
	if u.Scheme == "https" || isLoopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("URL %s must be https:// (http only to localhost): it carries a secret", redactURL(v))
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// redactURL hides anything after the scheme://host part (may hold secrets).
func redactURL(v string) string {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return "<unparsable>"
	}
	return u.Scheme + "://" + u.Host + "/..."
}

// Parse reads the env syntax from r.
func Parse(r io.Reader, requireTelegram bool) (*Config, error) {
	c := Defaults()
	lines := map[string]int{}
	seen := map[string]string{} // check name -> type
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if n == 1 {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf")
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if !ok || !nameRe.MatchString(key) {
			return nil, &Error{Line: n, Msg: "expected KEY=value"}
		}
		if prev, dup := lines[key]; dup {
			c.Warnings = append(c.Warnings, fmt.Sprintf("line %d: duplicate key %s (line %d), the last one wins", n, key, prev))
		}
		lines[key] = n
		bad := func(format string, a ...any) error {
			msg := fmt.Sprintf(format, a...)
			if strings.Contains(val, "#") {
				msg += " (comments are only allowed at the start of a line)"
			}
			return &Error{n, key, msg}
		}
		var err error
		switch {
		case key == "TELEGRAM_TOKEN":
			if !strings.Contains(val, ":") || strings.ContainsAny(val, " \t/?#") {
				return nil, bad("invalid token (expected 123456:ABC...)")
			}
			c.Token = val
		case key == "TELEGRAM_CHAT_ID":
			c.ChatID, err = strconv.ParseInt(val, 10, 64)
			if err != nil || c.ChatID == 0 {
				return nil, bad("invalid chat id (expected integer, negative for groups)")
			}
		case key == "TELEGRAM_API_URL":
			if err = parseSecureURL(val); err != nil {
				return nil, bad("%v", err)
			}
			c.APIURL = strings.TrimRight(val, "/")
		case key == "DEADMAN_URL":
			if val != "" {
				if err = parseSecureURL(val); err != nil {
					return nil, bad("%v", err)
				}
			}
			c.DeadmanURL = val
		case key == "DEADMAN_EVERY":
			c.DeadmanEvery, err = time.ParseDuration(val)
			if err != nil || c.DeadmanEvery < time.Minute {
				return nil, bad("invalid value (expected duration like 2m, min 60s)")
			}
		case key == "INTERVAL":
			c.Interval, err = time.ParseDuration(val)
			if err != nil || c.Interval < 10*time.Second {
				return nil, bad("invalid value (expected duration like 60s, min 10s)")
			}
		case key == "TIMEOUT":
			c.Timeout, err = time.ParseDuration(val)
			if err != nil || c.Timeout <= 0 || c.Timeout > 10*time.Minute {
				return nil, bad("invalid value (expected duration like 10s, max 10m)")
			}
		case key == "CPU_WINDOW":
			c.CPUWindow, err = time.ParseDuration(val)
			if err != nil || c.CPUWindow <= 0 {
				return nil, bad("invalid value (expected duration like 15m)")
			}
		case key == "FAIL_AFTER":
			c.FailAfter, err = strconv.Atoi(val)
			if err != nil || c.FailAfter < 1 {
				return nil, bad("invalid value (expected integer >= 1)")
			}
		case key == "RECOVER_AFTER":
			c.RecoverAfter, err = strconv.Atoi(val)
			if err != nil || c.RecoverAfter < 1 {
				return nil, bad("invalid value (expected integer >= 1)")
			}
		case key == "STATE_FILE":
			if !strings.HasPrefix(val, "/") {
				return nil, bad("expected absolute path")
			}
			c.StateFile = val
		case key == "STATE_IN_RAM":
			switch strings.ToLower(val) {
			case "yes", "true", "on", "1":
				c.StateInRAM = true
			case "no", "false", "off", "0", "":
				c.StateInRAM = false
			default:
				return nil, bad("invalid value (expected yes or no)")
			}
		case key == "DISK_PATHS":
			c.DiskPaths = nil
			for _, p := range strings.Split(val, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if !strings.HasPrefix(p, "/") {
					return nil, bad("path is not absolute (expected /a,/b)")
				}
				if p = filepath.Clean(p); !slices.Contains(c.DiskPaths, p) { // "/data" and "/data/" are one disk
					c.DiskPaths = append(c.DiskPaths, p)
				}
			}
		case key == "DISK_MIN_FREE_PCT":
			c.DiskMinFree, err = pct(val, 0, 100)
			if err != nil {
				return nil, bad("%v", err)
			}
		case key == "MEM_MIN_AVAIL_PCT":
			c.MemMinAvail, err = pct(val, 0, 100)
			if err != nil {
				return nil, bad("%v", err)
			}
		case key == "TEMP_MAX_C":
			c.TempMaxC, err = pct(val, 0, 200)
			if err != nil {
				return nil, bad("%v", err)
			}
		case key == "TEMP_SENSOR":
			if val != "" {
				val = filepath.Clean(val)
				if !tempSensorOK(val) {
					return nil, bad("expected a path under /sys/class/thermal/, /sys/class/hwmon/ or /sys/devices/ (a .../temp file)")
				}
			}
			c.TempSensor = val
		case key == "CPU_MAX_PCT":
			c.CPUMaxPct, err = pct(val, 0, 100)
			if err != nil {
				return nil, bad("%v", err)
			}
		case strings.HasPrefix(key, "HTTP_"), strings.HasPrefix(key, "TCP_"), strings.HasPrefix(key, "HOST_"), strings.HasPrefix(key, "PING_"):
			typ, _, _ := strings.Cut(key, "_")
			typ = strings.ToLower(typ)
			name := strings.ToLower(key[len(typ)+1:])
			if name == "" {
				return nil, bad("missing check name after prefix")
			}
			if prev, dup := seen[name]; dup {
				return nil, bad("check name %q already used by a %s check", name, prev)
			}
			spec := CheckSpec{Name: name, Type: typ}
			target, rest := val, ""
			if i := strings.IndexAny(val, " \t"); i >= 0 {
				target, rest = val[:i], val[i:]
			}
			restKey := ""
			if typ == "http" {
				restKey = "contains"
			}
			opts, oerr := parseOpts(rest, restKey)
			if typ == "http" {
				if err = parseURL(target); err != nil {
					return nil, bad("%v (expected `url` or `url contains=TEXT`)", err)
				}
				if oerr != nil {
					// 0.3.0 took anything after contains= as the text; keep that working.
					t, ok := strings.CutPrefix(strings.TrimSpace(rest), "contains=")
					if !ok || t == "" {
						return nil, bad("%v (expected `url` or `url contains=TEXT`)", oerr)
					}
					opts = []option{{"contains", t}}
				}
			} else if typ == "ping" {
				if err = pingHost(target); err != nil {
					return nil, bad("%v", err)
				}
				if oerr != nil {
					return nil, bad("%v (expected `host`)", oerr)
				}
			} else {
				if err = hostPort(target); err != nil {
					return nil, bad("%v", err)
				}
				if oerr != nil {
					return nil, bad("%v (expected `host:port`)", oerr)
				}
			}
			spec.Target = target
			for _, o := range opts {
				switch {
				case typ == "http" && o.key == "contains":
					if o.val == "" || spec.Contains != "" {
						return nil, bad("contains= needs one non-empty text")
					}
					spec.Contains = o.val
				case o.key == "name":
					if strings.TrimSpace(o.val) == "" {
						return nil, bad("name= needs a non-empty text")
					}
					spec.Label = strings.TrimSpace(o.val)
				case typ == "http" && o.key == "mode":
					if o.val != ModeLenient && o.val != ModeStrict {
						c.Warnings = append(c.Warnings, fmt.Sprintf("line %d: %s: unknown mode %s ignored, the default rules apply", n, key, o.val))
						continue
					}
					spec.Mode = o.val
				default:
					c.Warnings = append(c.Warnings, fmt.Sprintf("line %d: %s: unknown option %s ignored", n, key, o.key))
				}
			}
			seen[name] = typ
			c.Checks = append(c.Checks, spec)
		default:
			c.Warnings = append(c.Warnings, fmt.Sprintf("line %d: unknown key %s ignored", n, key))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if requireTelegram {
		if c.Token == "" {
			return nil, &Error{Key: "TELEGRAM_TOKEN", Msg: "required"}
		}
		if c.ChatID == 0 {
			return nil, &Error{Key: "TELEGRAM_CHAT_ID", Msg: "required"}
		}
	}
	if c.StateInRAM {
		if _, set := lines["STATE_FILE"]; set {
			c.Warnings = append(c.Warnings, "STATE_FILE is ignored while STATE_IN_RAM=yes")
		}
		c.DiskState, c.StateFile = c.StateFile, RAMStateFile()
	}
	if c.CPUWindow < c.Interval {
		return nil, &Error{Line: lines["CPU_WINDOW"], Key: "CPU_WINDOW", Msg: "must be at least INTERVAL"}
	}
	return c, nil
}

type option struct{ key, val string }

var optKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// parseOpts reads the options after a check's target: `key=value` or
// `key="value with blanks"`, separated by blanks. Inside quotes a backslash
// takes the next character as it is. A bare value of restKey runs to the end
// of the line (contains=TEXT as in 0.3.0).
func parseOpts(s, restKey string) ([]option, error) {
	var out []option
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return out, nil
		}
		eq := strings.IndexByte(s, '=')
		if eq <= 0 || !optKeyRe.MatchString(s[:eq]) {
			return nil, errors.New("unexpected text after the address (options are key=value)")
		}
		k, v := s[:eq], ""
		s = s[eq+1:]
		switch {
		case strings.HasPrefix(s, `"`):
			var b strings.Builder
			i := 1
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, fmt.Errorf("option %s: missing closing quote", k)
			}
			v, s = b.String(), s[i+1:]
			if s != "" && s[0] != ' ' && s[0] != '\t' {
				return nil, fmt.Errorf("option %s: expected a blank after the closing quote", k)
			}
		case k == restKey:
			v, s = s, ""
		default:
			v, s = s, ""
			if i := strings.IndexAny(v, " \t"); i >= 0 {
				v, s = v[:i], v[i:]
			}
		}
		out = append(out, option{k, v})
	}
}

// Quote writes v as an option value that parseOpts reads back unchanged.
func Quote(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

func tempSensorOK(p string) bool {
	for _, pre := range []string{"/sys/class/thermal/", "/sys/class/hwmon/", "/sys/devices/"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

func pct(v string, lo, hi float64) (float64, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < lo || f > hi {
		return 0, fmt.Errorf("invalid value (expected number %g..%g)", lo, hi)
	}
	return f, nil
}

// pingHost accepts an IP address (v4 or v6) or a host name, no port.
func pingHost(v string) error {
	if net.ParseIP(v) != nil || HostName(v) {
		return nil
	}
	return errors.New("invalid host (expected an IP address or a host name, no port)")
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*\.?$`)

// HostName reports whether v is a plain host name (letters, digits, - and dots).
func HostName(v string) bool { return len(v) <= 253 && hostRe.MatchString(v) }

func hostPort(v string) error {
	i := strings.LastIndex(v, ":")
	if i <= 0 || i == len(v)-1 {
		return errors.New("invalid value (expected host:port)")
	}
	p, err := strconv.Atoi(v[i+1:])
	if err != nil || p < 1 || p > 65535 {
		return errors.New("invalid port (expected 1..65535)")
	}
	if strings.ContainsAny(v[:i], " \t/") {
		return errors.New("invalid host (expected host:port)")
	}
	return nil
}

// ParallelChecks is how many checks run at the same time.
const ParallelChecks = 24

// rounds is the number of checks a round runs (services plus the machine's own).
func (c *Config) rounds() int {
	n := len(c.Checks) + 3
	if c.DiskMinFree > 0 {
		n += len(c.DiskPaths)
	}
	return n
}

// RoundMax is the longest a round of checks can take: each wave of parallel
// checks may need TIMEOUT plus the 0.5 s grace.
func (c *Config) RoundMax() time.Duration {
	waves := (c.rounds() + ParallelChecks - 1) / ParallelChecks
	return time.Duration(waves) * (c.Timeout + 500*time.Millisecond)
}

// StaleAfter is how old a heartbeat may get before the daemon counts as dead:
// two intervals plus the longest round, plus a margin.
func (c *Config) StaleAfter() time.Duration {
	return 2*c.Interval + c.RoundMax() + 30*time.Second
}

// Hooks for the group lookup; tests replace them.
var (
	GroupFile     = "/etc/group"
	CurrentUser   = func() string { return currentUser() }
	ProcessGroups = os.Getgroups
)

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if n := os.Getenv("USER"); n != "" {
		return n
	}
	return "YOUR_USER"
}

// groupMembers returns the gid and member list of Group from GroupFile.
func groupMembers() (gid int, members []string, ok bool) {
	b, err := os.ReadFile(GroupFile)
	if err != nil {
		return 0, nil, false
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 4 && f[0] == Group {
			g, err := strconv.Atoi(f[2])
			return g, strings.Split(f[3], ","), err == nil
		}
	}
	return 0, nil, false
}

// OtherStateFile is the state file of the other mode: where the daemon still writes right
// after the switch, until its next cycle moves the state.
func (c *Config) OtherStateFile() string {
	if c.StateInRAM {
		return c.DiskState
	}
	return RAMStateFile()
}

// RuntimeDir is the unit's RuntimeDirectory= (systemd exports it as RUNTIME_DIRECTORY to the
// daemon). set is false without it: readers (the UI) then look in /run/little-watchdog, but the
// daemon writes nowhere below it, because that directory belongs to the installed service.
func RuntimeDir() (dir string, set bool) {
	if v := os.Getenv("RUNTIME_DIRECTORY"); strings.HasPrefix(v, "/") {
		return strings.TrimRight(v, "/"), true
	}
	return "/run/little-watchdog", false
}

// RAMStateFile is the state file in RAM mode, and the name the heartbeat and the fine history
// take their place from (<it>.alive, <it>.fine) in either mode.
func RAMStateFile() string {
	dir, _ := RuntimeDir()
	return dir + "/state.json"
}

// LenientStateFile returns the state file from a config that may be invalid elsewhere
// (STATE_FILE, or RAMStateFile() with STATE_IN_RAM=yes), or def.
func LenientStateFile(path, def string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	ram := false
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "STATE_FILE":
			if strings.HasPrefix(v, "/") {
				def = v
			}
		case "STATE_IN_RAM":
			switch strings.ToLower(v) {
			case "yes", "true", "on", "1":
				ram = true
			case "no", "false", "off", "0", "":
				ram = false
			}
		}
	}
	if ram {
		return RAMStateFile()
	}
	return def
}

// NeedGroupText says what to do to get write access to the config: the exact
// command, or, if the group database already lists the user, to log in again.
func NeedGroupText() string {
	name := CurrentUser()
	if gid, members, ok := groupMembers(); ok {
		active := os.Getgid() == gid
		if gs, err := ProcessGroups(); err == nil {
			for _, g := range gs {
				active = active || g == gid
			}
		}
		for _, m := range members {
			if m == name && !active {
				return "you are in the group " + Group + ", but this login does not know it yet: log out and in again, then run little-wd"
			}
		}
	}
	return "needs group " + Group + ": sudo usermod -aG " + Group + " " + name + ", then log out and in again"
}

// PairingMarker is the file the setup creates while it polls the bot, so the daemon steps aside.
func PairingMarker(path string) string { return path + ".pairing" }

const pairingMaxAge = 15 * time.Minute

// MarkPairing creates the marker; release removes it.
func MarkPairing(path string) (release func(), err error) {
	p := PairingMarker(path)
	if err := touch(p); err != nil {
		return func() {}, err
	}
	return func() { os.Remove(p) }, nil
}

// PairingActive reports a recent marker (an old one is a crashed setup and ignored).
func PairingActive(path string, now time.Time) bool {
	st, err := os.Stat(PairingMarker(path))
	return err == nil && now.Sub(st.ModTime()) < pairingMaxAge
}

// LinkedMarker is touched by the setup after its test message went out, so the TUI
// can show a last message at once (the daemon steps aside meanwhile and cannot).
func LinkedMarker(path string) string { return path + ".linked" }

// MarkLinked records that a test message was sent just now.
func MarkLinked(path string) error { return touch(LinkedMarker(path)) }

var chtimes = os.Chtimes // swapped in tests

// touch creates the marker p (group-writable whatever the umask) or sets the time of an
// existing one to now. It never follows a symlink: the directory may be group-writable.
// A marker we may not touch (another owner) is removed and created anew; never written to.
func touch(p string) error {
	for try := 0; ; try++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o660)
		if err == nil {
			defer f.Close()
			return f.Chmod(0o660)
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if st, e := os.Lstat(p); e != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		now := time.Now()
		if chtimes(p, now, now) == nil {
			return nil
		}
		if try > 0 {
			return err
		}
		if e := os.Remove(p); e != nil {
			return e
		}
	}
}

// LinkedAt is the time of the setup's test message; ok is false if there was none.
func LinkedAt(path string) (t time.Time, ok bool) {
	st, err := os.Stat(LinkedMarker(path))
	if err != nil {
		return time.Time{}, false
	}
	return st.ModTime(), true
}

// TelegramOnly reads just token and chat id, ignoring every other line; for a notice when the rest is invalid.
func TelegramOnly(path string) (token string, chat int64, api string) {
	api = Defaults().APIURL
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, api
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		switch {
		case !ok:
		case strings.TrimSpace(k) == "TELEGRAM_TOKEN":
			token = strings.TrimSpace(v)
		case strings.TrimSpace(k) == "TELEGRAM_API_URL":
			if v = strings.TrimSpace(v); parseSecureURL(v) == nil {
				api = strings.TrimRight(v, "/")
			}
		case strings.TrimSpace(k) == "TELEGRAM_CHAT_ID":
			chat, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	return
}
