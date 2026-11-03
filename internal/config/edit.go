// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File is an env file held as lines, so comments and unrelated lines survive edits.
type File struct {
	Path    string
	lines   []string
	existed bool
	backed  bool
	base    string // file text when it was read or last saved; Save refuses if the disk differs
}

// OpenFile reads path; a missing file is an empty one.
func OpenFile(path string) (*File, error) {
	f := &File{Path: path}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	f.existed = true
	f.base = string(b)
	if s := strings.TrimSuffix(string(b), "\n"); s != "" {
		f.lines = strings.Split(s, "\n")
	}
	return f, nil
}

func (f *File) Clone() *File {
	c := *f
	c.lines = append([]string(nil), f.lines...)
	return &c
}

func (f *File) Exists() bool { return f.existed }

func (f *File) Text() string {
	if len(f.lines) == 0 {
		return ""
	}
	return strings.Join(f.lines, "\n") + "\n"
}

func (f *File) find(key string) int {
	for i, l := range f.lines {
		k, _, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && !strings.HasPrefix(k, "#") && strings.TrimSpace(k) == key {
			return i
		}
	}
	return -1
}

// Set replaces the line for key or appends one; an empty value removes the key.
// A duplicated key keeps only its last line, the one the daemon uses.
func (f *File) Set(key, val string) {
	if val == "" {
		f.Remove(key)
	} else {
		f.Put(key, val)
	}
}

// Put is Set that keeps an empty value ("KEY=").
func (f *File) Put(key, val string) {
	var at []int
	for i, l := range f.lines {
		if k, _, ok := strings.Cut(strings.TrimSpace(l), "="); ok && !strings.HasPrefix(k, "#") && strings.TrimSpace(k) == key {
			at = append(at, i)
		}
	}
	if len(at) == 0 {
		f.lines = append(f.lines, key+"="+val)
		return
	}
	f.lines[at[len(at)-1]] = key + "=" + val
	for j := len(at) - 2; j >= 0; j-- {
		f.lines = append(f.lines[:at[j]], f.lines[at[j]+1:]...)
	}
}

func (f *File) Remove(key string) {
	for i := f.find(key); i >= 0; i = f.find(key) {
		f.lines = append(f.lines[:i], f.lines[i+1:]...)
	}
}

// writeAtomic replaces path via a private temp file in the same directory (never follows a link at path).
func writeAtomic(path string, b []byte) error {
	t, err := os.CreateTemp(filepath.Dir(path), ".lw-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(t.Name()) // no-op after the rename
	if err := t.Chmod(0o660); err != nil {
		t.Close()
		return err
	}
	if _, err := t.Write(b); err != nil {
		t.Close()
		return err
	}
	if err := t.Sync(); err != nil {
		t.Close()
		return err
	}
	if err := t.Close(); err != nil {
		return err
	}
	return os.Rename(t.Name(), path)
}

// Save writes atomically with mode 0660 (group from the setgid directory); the first save keeps the old file as .bak.
func (f *File) Save() error {
	if _, err := os.Stat(filepath.Dir(f.Path)); err != nil {
		return fmt.Errorf("config directory %s is missing (install the package, or run install.sh from the tar.gz): %w", filepath.Dir(f.Path), err)
	}
	if f.existed {
		old, err := os.ReadFile(f.Path)
		if err != nil {
			return err
		}
		if string(old) != f.base {
			return ErrChanged
		}
	}
	if f.existed && !f.backed {
		old, _ := os.ReadFile(f.Path)
		if err := writeAtomic(f.Path+".bak", old); err != nil {
			return err
		}
		f.backed = true
	}
	if err := writeAtomic(f.Path, []byte(f.Text())); err != nil {
		return err
	}
	f.existed, f.base = true, f.Text()
	return nil
}

// ErrChanged: the file on disk is not the one that was read; saving would overwrite someone else's edit.
var ErrChanged = errors.New("the config file changed on disk since it was opened; nothing was saved, leave and reopen little-wd")

func (f *File) Has(key string) bool { return f.find(key) >= 0 }
