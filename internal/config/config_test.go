// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const good = `# comment
TELEGRAM_TOKEN=123:ABC
TELEGRAM_CHAT_ID=-1001
INTERVAL=30s
HTTP_NEXTCLOUD=https://c.example.org/x contains=hello world
TCP_SSH=192.0.2.1:22
HOST_NAS=nas:445
DISK_PATHS=/,/data
SOMETHING_ELSE=1
`

func TestParseGood(t *testing.T) {
	c, err := Parse(strings.NewReader(good), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.ChatID != -1001 || c.Interval != 30*time.Second || len(c.Checks) != 3 || len(c.DiskPaths) != 2 {
		t.Fatalf("%+v", c)
	}
	if c.Checks[0].Contains != "hello world" || c.Checks[0].Name != "nextcloud" || c.Checks[2].Type != "host" {
		t.Fatalf("%+v", c.Checks)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "SOMETHING_ELSE") {
		t.Fatalf("warnings %v", c.Warnings)
	}
	if c.FailAfter != 3 || c.RecoverAfter != 2 || c.CPUWindow != 15*time.Minute {
		t.Fatal("defaults")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"interval low", "INTERVAL=5s", "line 1: INTERVAL"},
		{"interval junk", "INTERVAL=abc", "INTERVAL"},
		{"fail after", "FAIL_AFTER=0", "FAIL_AFTER"},
		{"chat", "TELEGRAM_CHAT_ID=abc", "TELEGRAM_CHAT_ID"},
		{"no equals", "\nfoo", "line 2"},
		{"http url", "HTTP_A=ftp://x", "HTTP_A"},
		{"http extra", "HTTP_A=http://x foo", "contains="},
		{"tcp port", "TCP_A=host:99999", "port"},
		{"tcp form", "TCP_A=host", "host:port"},
		{"dup", "TCP_A=h:1\nHOST_A=h:2", "already used"},
		{"pct", "DISK_MIN_FREE_PCT=101", "DISK_MIN_FREE_PCT"},
		{"disk rel", "DISK_PATHS=rel", "absolute"},
		{"window", "INTERVAL=60s\nCPU_WINDOW=10s", "CPU_WINDOW"},
		{"state rel", "STATE_FILE=x", "absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.in+"\n"), false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v want %q", err, tt.want)
			}
		})
	}
}

func TestRequired(t *testing.T) {
	if _, err := Parse(strings.NewReader("INTERVAL=60s\n"), true); err == nil || !strings.Contains(err.Error(), "TELEGRAM_TOKEN") {
		t.Fatal(err)
	}
	if _, err := Parse(strings.NewReader("TELEGRAM_TOKEN=1:a\n"), true); err == nil || !strings.Contains(err.Error(), "TELEGRAM_CHAT_ID") {
		t.Fatal(err)
	}
	if _, err := Parse(strings.NewReader(""), false); err != nil {
		t.Fatal(err)
	}
}

func TestErrorDoesNotLeakToken(t *testing.T) {
	_, err := Parse(strings.NewReader("TELEGRAM_TOKEN=secret value\n"), false)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestLoadPermWarning(t *testing.T) {
	p := t.TempDir() + "/c.env"
	if err := writeFile(p, "TELEGRAM_TOKEN=1:a\nTELEGRAM_CHAT_ID=5\n", 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, true)
	if err != nil || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "readable by others") {
		t.Fatal(err, c)
	}
}

func TestResolve(t *testing.T) {
	t.Setenv(EnvPath, "")
	if got := Resolve(""); got != DefaultPath {
		t.Fatalf("got %q", got)
	}
	t.Setenv(EnvPath, "/e")
	if got := Resolve(""); got != "/e" {
		t.Fatalf("env: got %q", got)
	}
	if got := Resolve("/x"); got != "/x" {
		t.Fatalf("flag must win, got %q", got)
	}
}

func TestHostCheckSwitches(t *testing.T) {
	d, err := Parse(strings.NewReader(""), false)
	if err != nil || d.CPUMaxPct != 90 || d.MemMinAvail != 5 || d.TempMaxC != 85 || d.DiskMinFree != 10 || len(d.DiskPaths) != 1 {
		t.Fatalf("defaults must stay on: %+v %v", d, err)
	}
	for _, c := range []struct {
		in   string
		zero func(*Config) bool
	}{
		{"CPU_MAX_PCT=0", func(c *Config) bool { return c.CPUMaxPct == 0 }},
		{"MEM_MIN_AVAIL_PCT=0", func(c *Config) bool { return c.MemMinAvail == 0 }},
		{"TEMP_MAX_C=0", func(c *Config) bool { return c.TempMaxC == 0 }},
		{"DISK_MIN_FREE_PCT=0", func(c *Config) bool { return c.DiskMinFree == 0 }},
		{"DISK_PATHS=", func(c *Config) bool { return len(c.DiskPaths) == 0 }},
	} {
		got, err := Parse(strings.NewReader(c.in+"\n"), false)
		if err != nil || !c.zero(got) {
			t.Errorf("%s: %+v %v", c.in, got, err)
		}
	}
	if _, err := Parse(strings.NewReader("CPU_MAX_PCT=101\n"), false); err == nil {
		t.Error("out of range accepted")
	}
}

func TestErrorsNeverEchoInput(t *testing.T) {
	lines := []string{
		"123456:SECRETXYZ", "TOKEN 123:SECRETXYZ=1", "lower:SECRETXYZ=1",
		"INTERVAL=SECRETXYZ", "TIMEOUT=SECRETXYZ", "CPU_WINDOW=SECRETXYZ", "FAIL_AFTER=SECRETXYZ",
		"RECOVER_AFTER=SECRETXYZ", "TELEGRAM_CHAT_ID=SECRETXYZ", "DISK_PATHS=SECRETXYZ",
		"DISK_MIN_FREE_PCT=SECRETXYZ", "MEM_MIN_AVAIL_PCT=SECRETXYZ", "TEMP_MAX_C=SECRETXYZ",
		"CPU_MAX_PCT=SECRETXYZ", "TCP_A=SECRETXYZ", "TCP_A=h:SECRETXYZ", "HOST_A=a b:1/SECRETXYZ",
		"HTTP_A=http://x SECRETXYZ", "TELEGRAM_API_URL=SECRETXYZ", "DEADMAN_URL=SECRETXYZ",
		"TEMP_SENSOR=SECRETXYZ", "TELEGRAM_TOKEN=SECRETXYZ",
	}
	for _, l := range lines {
		_, err := Parse(strings.NewReader("\n"+l+"\n"), false)
		if err == nil {
			t.Errorf("%q accepted", l)
			continue
		}
		if strings.Contains(err.Error(), "SECRETXYZ") || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("%q -> %v", l, err)
		}
	}
}

func TestTempSensorOnlyUnderSys(t *testing.T) {
	if _, err := Parse(strings.NewReader("TEMP_SENSOR=/etc/shadow\n"), false); err == nil {
		t.Fatal("accepted /etc/shadow")
	}
	if _, err := Parse(strings.NewReader("TEMP_SENSOR=/sys/class/thermal/thermal_zone0/temp\n"), false); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateKeyWarns(t *testing.T) {
	c, err := Parse(strings.NewReader("INTERVAL=30s\nINTERVAL=40s\n"), false)
	if err != nil || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "duplicate key INTERVAL") {
		t.Fatal(err, c)
	}
}

func TestDeadmanEvery(t *testing.T) {
	base := "TELEGRAM_TOKEN=1:A\nTELEGRAM_CHAT_ID=1\n"
	c, err := Parse(strings.NewReader(base), false)
	if err != nil || c.DeadmanEvery != 2*time.Minute {
		t.Fatalf("default: %v %v", c, err)
	}
	for _, v := range []string{"59s", "10s", "0", "-2m", "soon", ""} {
		if _, err := Parse(strings.NewReader(base+"DEADMAN_EVERY="+v+"\n"), false); err == nil {
			t.Errorf("DEADMAN_EVERY=%q accepted", v)
		}
	}
	for v, want := range map[string]time.Duration{"60s": time.Minute, "5m": 5 * time.Minute} {
		c, err := Parse(strings.NewReader(base+"DEADMAN_EVERY="+v+"\n"), false)
		if err != nil || c.DeadmanEvery != want {
			t.Errorf("%s: %v %v", v, c, err)
		}
	}
}

func TestSecretURLsNeedHTTPSExceptLoopback(t *testing.T) {
	for _, tt := range []struct {
		line string
		ok   bool
	}{
		{"DEADMAN_URL=https://hc.example.org/ping/x", true},
		{"DEADMAN_URL=http://hc.example.org/ping/x", false},
		{"DEADMAN_URL=http://127.0.0.1:8000/p", true},
		{"DEADMAN_URL=http://localhost/p", true},
		{"DEADMAN_URL=http://[::1]:8/p", true},
		{"TELEGRAM_API_URL=http://api.example.org", false},
		{"TELEGRAM_API_URL=http://127.0.0.1:9", true},
		{"HTTP_A=http://lan.example/", true}, // checks carry no secret
	} {
		_, err := Parse(strings.NewReader(tt.line+"\n"), false)
		if (err == nil) != tt.ok {
			t.Errorf("%s: %v", tt.line, err)
		}
	}
}

func TestTempSensorPathRestricted(t *testing.T) {
	for p, ok := range map[string]bool{
		"/sys/class/thermal/thermal_zone0/temp": true, "/sys/class/hwmon/hwmon1/temp1_input": true,
		"/sys/devices/virtual/thermal/thermal_zone0/temp": true,
		"/sys/kernel/debug/x":                             false, "/sys/../etc/shadow": false, "/sys/class/thermal/../../../etc/shadow": false,
	} {
		_, err := Parse(strings.NewReader("TEMP_SENSOR="+p+"\n"), false)
		if (err == nil) != ok {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestStaleAfterGrowsWithTimeoutAndChecks(t *testing.T) {
	small := Defaults()
	if got := small.StaleAfter(); got != 2*time.Minute+small.RoundMax()+30*time.Second || got < 150*time.Second {
		t.Fatal(got)
	}
	big := Defaults()
	big.Timeout = time.Minute
	for i := 0; i < 100; i++ {
		big.Checks = append(big.Checks, CheckSpec{Name: "c", Type: "tcp"})
	}
	// 5 waves of (1 min + 0.5 s) must fit into what counts as "alive"
	if big.StaleAfter() < 5*time.Minute+2*time.Minute {
		t.Fatal(big.StaleAfter())
	}
}

func TestDiskPathsAreDeduplicated(t *testing.T) {
	c, err := Parse(strings.NewReader("DISK_PATHS=/data,/data/,/,/data/../data\n"), false)
	if err != nil || len(c.DiskPaths) != 2 || c.DiskPaths[0] != "/data" || c.DiskPaths[1] != "/" {
		t.Fatal(c.DiskPaths, err)
	}
}

func TestCommentInsideAValueGetsAHint(t *testing.T) {
	_, err := Parse(strings.NewReader("DEADMAN_EVERY=2m   # min 60s\n"), false)
	if err == nil || !strings.Contains(err.Error(), "comments are only allowed at the start of a line") {
		t.Fatal(err)
	}
}

func TestLenientStateFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.env")
	os.WriteFile(p, []byte("TCP_X=zz\nSTATE_FILE=/srv/s.json\n"), 0o600)
	if got := LenientStateFile(p, "/def"); got != "/srv/s.json" {
		t.Fatal(got)
	}
	os.WriteFile(p, []byte("STATE_FILE=relative\n"), 0o600)
	if got := LenientStateFile(p, "/def"); got != "/def" {
		t.Fatal(got)
	}
	if got := LenientStateFile(p+"x", "/def"); got != "/def" {
		t.Fatal(got)
	}
}

func TestCheckOptions(t *testing.T) {
	tests := []struct {
		name, in, target, contains, label, mode string
		warns                                   int
	}{
		{"v0.4.0 line", `HTTP_A=http://h:2283 name="Immich Fotos" mode=lenient`, "http://h:2283", "", "Immich Fotos", "lenient", 0},
		{"contains legacy", "HTTP_A=http://h/x contains=hello world", "http://h/x", "hello world", "", "", 0},
		{"contains takes the rest", "HTTP_A=http://h/x contains=ok mode=lenient", "http://h/x", "ok mode=lenient", "", "", 0},
		{"options before contains", `HTTP_A=http://h/x name="N" contains=a b`, "http://h/x", "a b", "N", "", 0},
		{"contains quoted", `HTTP_A=http://h/x contains="a b" mode=strict`, "http://h/x", "a b", "", "strict", 0},
		{"quote escape", `HTTP_A=http://h/x contains="say \"hi\" \\o/"`, "http://h/x", `say "hi" \o/`, "", "", 0},
		{"bad quote falls back to 0.3.0", `HTTP_A=http://h/x contains="a" b`, "http://h/x", `"a" b`, "", "", 0},
		{"open quote falls back to 0.3.0", `HTTP_A=http://h/x contains="a b`, "http://h/x", `"a b`, "", "", 0},
		{"quotes inside after a name", `HTTP_A=http://h/x name="N" contains="status":"ok"`, "http://h/x", `"status":"ok"`, "N", "", 0},
		{"open quote after a name", `HTTP_A=http://h/x name="N" contains="a b`, "http://h/x", `"a b`, "N", "", 0},
		{"quotes inside, first", `HTTP_A=http://h/x contains="status":"ok"`, "http://h/x", `"status":"ok"`, "", "", 0},
		{"escaped quotes after a name", `HTTP_A=http://h/x name="N" contains="\"status\":\"ok\""`, "http://h/x", `"status":"ok"`, "N", "", 0},
		{"tabs", "HTTP_A=http://h/x\tmode=lenient", "http://h/x", "", "", "lenient", 0},
		{"value with =", "HTTP_A=http://h/x opt=a=b", "http://h/x", "", "", "", 1},
		{"unknown mode", "HTTP_A=http://h/x mode=fuzzy", "http://h/x", "", "", "", 1},
		{"tcp options", `TCP_A=192.0.2.5:22 name="SSH box" x=1`, "192.0.2.5:22", "", "SSH box", "", 1},
		{"host options", `HOST_A=nas:445 name="NAS"`, "nas:445", "", "NAS", "", 0},
		{"tcp contains unknown", "TCP_A=h:1 contains=x", "h:1", "", "", "", 1},
		{"tcp mode unknown", "TCP_A=h:1 mode=lenient", "h:1", "", "", "", 1},
		{"ping", `PING_NAS=192.0.2.5 name="Die Kiste ä"`, "192.0.2.5", "", "Die Kiste ä", "", 0},
		{"ping name", "PING_NAS=nas.lan", "nas.lan", "", "", "", 0},
		{"ping v6", "PING_NAS=fe80::1", "fe80::1", "", "", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse(strings.NewReader(tt.in+"\n"), false)
			if err != nil {
				t.Fatal(err)
			}
			s := c.Checks[0]
			if s.Target != tt.target || s.Contains != tt.contains || s.Label != tt.label || s.Mode != tt.mode || len(c.Warnings) != tt.warns {
				t.Fatalf("got %q %q %q %q %q", s.Target, s.Contains, s.Label, s.Mode, c.Warnings)
			}
			for _, w := range c.Warnings {
				if !strings.Contains(w, "line 1: ") || !strings.Contains(w, "unknown") {
					t.Fatalf("warning %q", w)
				}
			}
		})
	}
}

func TestCheckOptionErrors(t *testing.T) {
	for _, in := range []string{
		"HTTP_A=http://x foo",
		"HTTP_A=http://x contains=",
		`HTTP_A=http://x contains=""`,
		`HTTP_A=http://x contains="a" contains="b"`,
		"HTTP_A=http://x Name=a",
		"HTTP_A=http://x =a",
		`TCP_A=h:1 name="open`,
		"TCP_A=h:1 junk",
		`HOST_A=h:1 name="a"b`,
		`HTTP_A=http://x name=""`,
		"PING_A=192.0.2.5:22",
		"PING_A=http://x",
		"PING_A=a b",
		"PING_A=",
		"PING_=h",
	} {
		if _, err := Parse(strings.NewReader(in+"\n"), false); err == nil {
			t.Errorf("%s: no error", in)
		}
	}
}

func TestQuote(t *testing.T) {
	for _, v := range []string{"a b", `x"y`, `back\slash`, `"`, "Immich Fotos"} {
		c, err := Parse(strings.NewReader("HTTP_A=http://h contains="+Quote(v)+" mode=lenient\n"), false)
		if err != nil || c.Checks[0].Contains != v {
			t.Errorf("%q: %v %+v", v, err, c)
		}
	}
}

func TestStateInRAM(t *testing.T) {
	t.Setenv("RUNTIME_DIRECTORY", "")
	c, err := Parse(strings.NewReader("STATE_IN_RAM=yes\n"), false)
	if err != nil || !c.StateInRAM || c.StateFile != "/run/little-watchdog/state.json" || len(c.Warnings) != 0 {
		t.Fatalf("%+v %v", c, err)
	}
	for _, v := range []string{"no", "off", ""} {
		if c, err = Parse(strings.NewReader("STATE_IN_RAM="+v+"\n"), false); err != nil || c.StateInRAM || c.StateFile != Defaults().StateFile {
			t.Fatalf("%q: %+v %v", v, c, err)
		}
	}
	if _, err = Parse(strings.NewReader("STATE_IN_RAM=maybe\n"), false); err == nil || !strings.Contains(err.Error(), "STATE_IN_RAM") {
		t.Fatalf("%v", err)
	}
	// the disk path stays the one STATE_FILE says; RAM mode names its own and warns about STATE_FILE
	c, _ = Parse(strings.NewReader("STATE_FILE=/srv/s.json\n"), false)
	if c.StateFile != "/srv/s.json" || c.StateInRAM {
		t.Fatalf("%+v", c)
	}
	c, _ = Parse(strings.NewReader("STATE_FILE=/srv/s.json\nSTATE_IN_RAM=yes\n"), false)
	if c.StateFile != "/run/little-watchdog/state.json" || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "STATE_FILE is ignored") {
		t.Fatalf("%+v", c)
	}
	t.Setenv("RUNTIME_DIRECTORY", "/run/x/")
	c, _ = Parse(strings.NewReader("STATE_IN_RAM=yes\n"), false)
	if c.StateFile != "/run/x/state.json" {
		t.Fatal(c.StateFile)
	}
	p := filepath.Join(t.TempDir(), "c")
	os.WriteFile(p, []byte("STATE_FILE=/srv/s.json\nSTATE_IN_RAM=yes\nINTERVAL=1s\n"), 0o600) // invalid elsewhere
	if got := LenientStateFile(p, "/def"); got != "/run/x/state.json" {
		t.Fatal(got)
	}
}
