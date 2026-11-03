// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

// ownGroup makes Group the test user's primary group and returns its name.
func ownGroup(t *testing.T) string {
	g, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Skip(err)
	}
	old := Group
	Group = g.Name
	t.Cleanup(func() { Group = old })
	return g.Name
}

func TestPermWarning(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.env")
	os.WriteFile(p, []byte("INTERVAL=60s\n"), 0o600)
	warns := func(m os.FileMode) int {
		os.Chmod(p, m)
		c, err := Load(p, false)
		if err != nil {
			t.Fatal(err)
		}
		return len(c.Warnings)
	}
	ownGroup(t)
	for m, want := range map[os.FileMode]int{0o600: 0, 0o640: 0, 0o660: 0, 0o604: 1, 0o601: 1, 0o666: 1} {
		if got := warns(m); got != want {
			t.Errorf("own group is Group, mode %04o: %d warnings, want %d", m, got, want)
		}
	}
	Group = "no-such-group-lwtest"
	for m, want := range map[os.FileMode]int{0o600: 0, 0o640: 1, 0o660: 1, 0o440: 1, 0o604: 1} {
		if got := warns(m); got != want {
			t.Errorf("file group is not Group, mode %04o: %d warnings, want %d", m, got, want)
		}
	}
	if r, err := user.LookupGroup("root"); err == nil && strconv.Itoa(os.Getgid()) != r.Gid {
		Group = "root"
		if warns(0o660) != 1 {
			t.Error("another existing group must warn")
		}
	}
}
