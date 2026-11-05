// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Watch is what an address typed into the form becomes: a check type, its target and mode.
type Watch struct {
	Type   string // "ping" or "http"
	Target string // host for ping, URL for http
	Mode   string // http: ModeLenient or ModeStrict
	Name   string // the name the address suggests, for an empty name field
}

// Detect turns an address into a check, without asking what kind it is:
//
//   - an IP address or host name: ping
//   - host:port, or a URL without a path: web, lenient (redirects followed, any answer below 500)
//   - a URL with a path: web, strict (redirects followed, only 2xx)
//
// host:port without a scheme becomes http://host:port.
func Detect(addr string) (Watch, error) {
	a := strings.TrimSpace(addr)
	switch {
	case a == "":
		return Watch{}, errors.New("type an address, for example 192.168.1.10, 192.168.1.10:8080 or https://example.org/health")
	case strings.ContainsAny(a, " \t"):
		return Watch{}, errors.New("an address has no blanks")
	}
	if !strings.Contains(a, "://") && strings.Contains(a, "/") {
		a = "http://" + a // 192.168.1.10:8080/health
	}
	if strings.Contains(a, "://") {
		u, err := url.Parse(a)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Watch{}, errors.New("a web address starts with http:// or https://, for example https://example.org")
		}
		if p := u.Port(); p != "" {
			if err := portOK(p); err != nil {
				return Watch{}, err
			}
		}
		w := Watch{Type: "http", Target: a, Mode: ModeLenient, Name: strings.TrimSuffix(u.Host+u.EscapedPath(), "/")}
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			w.Mode = ModeStrict
		}
		return w, nil
	}
	if h, p, err := net.SplitHostPort(a); err == nil {
		if err := portOK(p); err != nil {
			return Watch{}, err
		}
		if net.ParseIP(h) == nil && !HostName(h) {
			return Watch{}, fmt.Errorf("%q is not an IP address or a host name", h)
		}
		return Watch{Type: "http", Target: "http://" + a, Mode: ModeLenient, Name: a}, nil
	}
	h := strings.TrimSuffix(strings.TrimPrefix(a, "["), "]") // [fe80::1] without a port
	if net.ParseIP(h) == nil && !HostName(h) {
		if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[:i], ":") {
			return Watch{}, portOK(h[i+1:])
		}
		return Watch{}, fmt.Errorf("%q is not an IP address or a host name", h)
	}
	return Watch{Type: "ping", Target: h, Name: h}, nil
}

func portOK(p string) error {
	if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port %q: use a number from 1 to 65535", p)
	}
	return nil
}

var umlaut = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")

// KeyName turns a free name into the part of a config key after the prefix (upper case):
// ä→ae ö→oe ü→ue ß→ss, everything else that is not a-z or 0-9 becomes _, runs of _ are one.
// It may be empty.
func KeyName(name string) string {
	var b strings.Builder
	under := false
	for _, r := range umlaut.Replace(strings.ToLower(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			under = false
		} else if !under {
			b.WriteByte('_')
			under = true
		}
	}
	return strings.ToUpper(strings.Trim(b.String(), "_"))
}

// Prefixes are the key prefixes of the service checks; one name can only be used by one of them.
var Prefixes = []string{"HTTP_", "TCP_", "HOST_", "PING_"}

// FreeKey is the config key for a new check of type typ named name: the key name, with
// _2, _3 ... if the name is taken by any check in f; an empty key name becomes CHECK_N.
func FreeKey(f *File, typ, name string) string {
	taken := func(n string) bool {
		for _, p := range Prefixes {
			if f.Has(p + n) {
				return true
			}
		}
		return false
	}
	base := KeyName(name)
	if base == "" {
		for i := 1; ; i++ {
			if n := "CHECK_" + strconv.Itoa(i); !taken(n) {
				return strings.ToUpper(typ) + "_" + n
			}
		}
	}
	n := base
	for i := 2; taken(n); i++ {
		n = base + "_" + strconv.Itoa(i)
	}
	return strings.ToUpper(typ) + "_" + n
}

// Value is the config value for w shown as name: target, name="..." and the mode.
func (w Watch) Value(name string) string {
	v := w.Target + " name=" + Quote(name)
	if w.Mode != "" {
		v += " mode=" + w.Mode
	}
	return v
}
