// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	tests := []struct{ in, typ, target, mode, name string }{
		{"192.168.178.12", "ping", "192.168.178.12", "", "192.168.178.12"},
		{" nas.lan ", "ping", "nas.lan", "", "nas.lan"},
		{"nas", "ping", "nas", "", "nas"},
		{"fe80::1", "ping", "fe80::1", "", "fe80::1"},
		{"[2001:db8::5]", "ping", "2001:db8::5", "", "2001:db8::5"},
		{"192.168.178.12:2283", "http", "http://192.168.178.12:2283", "lenient", "192.168.178.12:2283"},
		{"[2001:db8::5]:8080", "http", "http://[2001:db8::5]:8080", "lenient", "[2001:db8::5]:8080"},
		{"nas.lan:22", "http", "http://nas.lan:22", "lenient", "nas.lan:22"},
		{"https://dockseed.de", "http", "https://dockseed.de", "lenient", "dockseed.de"},
		{"https://dockseed.de/", "http", "https://dockseed.de/", "lenient", "dockseed.de"},
		{"http://h:8096", "http", "http://h:8096", "lenient", "h:8096"},
		{"https://h/health", "http", "https://h/health", "strict", "h/health"},
		{"https://h/?x=1", "http", "https://h/?x=1", "strict", "h"},
		{"192.168.1.5:8080/health", "http", "http://192.168.1.5:8080/health", "strict", "192.168.1.5:8080/health"},
	}
	for _, tt := range tests {
		w, err := Detect(tt.in)
		if err != nil || w.Type != tt.typ || w.Target != tt.target || w.Mode != tt.mode || w.Name != tt.name {
			t.Errorf("%q: %+v %v", tt.in, w, err)
		}
	}
	for _, in := range []string{"", "a b", "host:0", "host:99999", "host:x", "ftp://h/", "http://", "bad_host!", "h:"} {
		if w, err := Detect(in); err == nil {
			t.Errorf("%q: no error, %+v", in, w)
		}
	}
}

func TestKeyName(t *testing.T) {
	for in, want := range map[string]string{
		"Immich Fotos":   "IMMICH_FOTOS",
		"Größe über Maß": "GROESSE_UEBER_MASS",
		"ÄÖÜ":            "AEOEUE",
		"dockseed.de":    "DOCKSEED_DE",
		"  --a--b--  ":   "A_B",
		"192.168.1.5:22": "192_168_1_5_22",
		"Café":           "CAF",
		"???":            "",
		"":               "",
	} {
		if got := KeyName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestFreeKey(t *testing.T) {
	f := &File{lines: []string{"HTTP_IMMICH=http://h", "TCP_IMMICH_2=h:1", "PING_CHECK_1=h", "# HTTP_FREE=x"}}
	for _, tt := range []struct{ typ, name, want string }{
		{"http", "Immich", "HTTP_IMMICH_3"},
		{"ping", "immich", "PING_IMMICH_3"},
		{"ping", "Free", "PING_FREE"},
		{"http", "?!", "HTTP_CHECK_2"},
		{"http", "dockseed.de", "HTTP_DOCKSEED_DE"},
	} {
		if got := FreeKey(f, tt.typ, tt.name); got != tt.want {
			t.Errorf("%s %q: %s, want %s", tt.typ, tt.name, got, tt.want)
		}
	}
}

// What the form writes reads back as the same check, name and all.
func TestWatchRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.env")
	f, _ := OpenFile(p)
	for _, in := range []struct{ addr, name string }{
		{"192.168.178.12:2283", `Immich "Fotos" \ ä`},
		{"192.168.178.12", ""},
		{"https://dockseed.de/health", "Health"},
	} {
		w, err := Detect(in.addr)
		if err != nil {
			t.Fatal(err)
		}
		name := in.name
		if name == "" {
			name = w.Name
		}
		f.Set(FreeKey(f, w.Type, name), w.Value(name))
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	f, _ = OpenFile(p)
	c, err := Parse(strings.NewReader(f.Text()), false)
	if err != nil || len(c.Warnings) != 0 {
		t.Fatal(err, c.Warnings)
	}
	want := []CheckSpec{
		{Name: "immich_fotos_ae", Type: "http", Target: "http://192.168.178.12:2283", Label: `Immich "Fotos" \ ä`, Mode: "lenient"},
		{Name: "192_168_178_12", Type: "ping", Target: "192.168.178.12", Label: "192.168.178.12"},
		{Name: "health", Type: "http", Target: "https://dockseed.de/health", Label: "Health", Mode: "strict"},
	}
	for i, s := range c.Checks {
		if s != want[i] {
			t.Errorf("%d: %+v, want %+v", i, s, want[i])
		}
	}
	if !strings.Contains(f.Text(), `HTTP_IMMICH_FOTOS_AE=http://192.168.178.12:2283 name="Immich \"Fotos\" \\ ä" mode=lenient`) {
		t.Fatal(f.Text())
	}
}
