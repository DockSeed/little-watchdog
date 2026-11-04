// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileEdit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d", "lw.env")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("# top\nTELEGRAM_TOKEN=1:a\n# HTTP_OLD=commented\nHTTP_A=http://a\n\nTCP_B=h:1\n"), 0o644)
	f, err := OpenFile(p)
	if err != nil {
		t.Fatal(err)
	}
	f.Set("HTTP_NEW", "http://n contains=x")
	f.Set("HTTP_A", "http://a2")
	f.Remove("TCP_B")
	f.Set("DEADMAN_URL", "")
	want := "# top\nTELEGRAM_TOKEN=1:a\n# HTTP_OLD=commented\nHTTP_A=http://a2\n\nHTTP_NEW=http://n contains=x\n"
	if f.Text() != want {
		t.Fatalf("%q", f.Text())
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	f.Set("X", "1")
	f.Save()
	if b, _ := os.ReadFile(p + ".bak"); string(b) != "# top\nTELEGRAM_TOKEN=1:a\n# HTTP_OLD=commented\nHTTP_A=http://a\n\nTCP_B=h:1\n" {
		t.Fatalf("bak %q", b)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o660 {
		t.Fatal(st.Mode())
	}
	if _, err := os.Stat(p + ".new"); err == nil {
		t.Fatal("tmp left")
	}
}

func TestFileNew(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lw.env")
	f, _ := OpenFile(p)
	f.Set("A", "1")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Fatal("bak for new file")
	}
	g, _ := OpenFile(p)
	if g.Text() != "A=1\n" || !g.Exists() {
		t.Fatal(g.Text())
	}
}

func TestSaveDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	os.WriteFile(victim, []byte("keep"), 0o644)
	p := filepath.Join(dir, "lw.env")
	os.WriteFile(p, []byte("A=1\n"), 0o644)
	os.Symlink(victim, p+".bak")
	os.Symlink(victim, p+".new")
	f, _ := OpenFile(p)
	f.Set("B", "2")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("victim overwritten: %q", b)
	}
	for _, q := range []string{p, p + ".bak"} {
		st, err := os.Lstat(q)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o660 {
			t.Fatalf("%s: %v %v", q, st, err)
		}
	}
	if b, _ := os.ReadFile(p + ".bak"); string(b) != "A=1\n" {
		t.Fatalf("bak %q", b)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestSaveBackupOncePerSession(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lw.env")
	os.WriteFile(p, []byte("A=1\n"), 0o600)
	f, _ := OpenFile(p)
	f.Set("B", "2")
	f.Save()
	g := f.Clone()
	g.Set("C", "3")
	g.Save()
	if b, _ := os.ReadFile(p + ".bak"); string(b) != "A=1\n" {
		t.Fatalf("bak rewritten: %q", b)
	}
}

func TestSetUsesLastDuplicateAndDropsTheRest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.env")
	os.WriteFile(p, []byte("INTERVAL=30s\nTCP_A=h:1\nINTERVAL=40s\n"), 0o600)
	f, _ := OpenFile(p)
	f.Set("INTERVAL", "50s")
	if got := f.Text(); got != "TCP_A=h:1\nINTERVAL=50s\n" {
		t.Fatalf("%q", got)
	}
	c, err := Parse(strings.NewReader(f.Text()), false)
	if err != nil || c.Interval != 50*time.Second || len(c.Warnings) != 0 {
		t.Fatal(c, err)
	}
}

func TestSaveDoesNotCreateTheDirectory(t *testing.T) {
	f, _ := OpenFile(filepath.Join(t.TempDir(), "missing", "c.env"))
	f.Set("A", "1")
	err := f.Save()
	if err == nil || !strings.Contains(err.Error(), "install.sh") {
		t.Fatalf("%v", err)
	}
}

func TestSaveRefusesAFileChangedMeanwhile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.env")
	os.WriteFile(p, []byte("A=1\n"), 0o600)
	f, _ := OpenFile(p)
	os.WriteFile(p, []byte("A=1\nB=2\n"), 0o600) // someone else edits
	f.Set("C", "3")
	if err := f.Save(); !errors.Is(err, ErrChanged) {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "A=1\nB=2\n" {
		t.Fatalf("overwritten: %q", b)
	}
	f2, _ := OpenFile(p)
	f2.Set("C", "3")
	if err := f2.Save(); err != nil {
		t.Fatal(err)
	}
	f2.Set("D", "4")
	if err := f2.Save(); err != nil { // its own earlier save is not a foreign change
		t.Fatal(err)
	}
}

// The keys little-wd's Settings, Dead-man and temperature forms write survive a save and parse back.
func TestSettingsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lw.env")
	os.WriteFile(p, []byte("# keep\nINTERVAL=60s\nTEMP_SENSOR=/sys/class/thermal/thermal_zone0/temp\n"), 0o600)
	f, err := OpenFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"INTERVAL": "2m", "FAIL_AFTER": "5", "RECOVER_AFTER": "4", "TIMEOUT": "30s", "DEADMAN_EVERY": "5m"} {
		f.Put(k, v)
	}
	f.Set("TEMP_SENSOR", "")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	g, err := OpenFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if g.Text() != f.Text() || !strings.HasPrefix(g.Text(), "# keep\nINTERVAL=2m\n") || g.Has("TEMP_SENSOR") {
		t.Fatalf("%q", g.Text())
	}
	c, err := Parse(strings.NewReader(g.Text()), false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Interval != 2*time.Minute || c.FailAfter != 5 || c.RecoverAfter != 4 || c.Timeout != 30*time.Second || c.DeadmanEvery != 5*time.Minute || c.TempSensor != "" {
		t.Fatalf("%+v", c)
	}
}
