// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package tui

import (
	"net"
	"net/netip"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/DockSeed/little-watchdog/internal/config"
	"github.com/DockSeed/little-watchdog/internal/state"
)

// hostOf is the host in a check target (URL, host:port or a bare host), lower case,
// without scheme, port, path or IPv6 brackets; "" if there is none.
func hostOf(target string) string {
	t := strings.TrimSpace(target)
	if strings.Contains(t, "://") {
		u, err := url.Parse(t)
		if err != nil {
			return ""
		}
		t = u.Hostname()
	} else if h, _, err := net.SplitHostPort(t); err == nil {
		t = h
	} else {
		t = strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
	}
	return strings.TrimSuffix(strings.ToLower(t), ".")
}

// addrOf is a host as the start of an address: IPv6 in brackets, so ":port" can follow.
func addrOf(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// hostLess orders hosts: IPv4 by number, then IPv6, then names alphabetically.
func hostLess(a, b string) bool {
	ia, ea := netip.ParseAddr(a)
	ib, eb := netip.ParseAddr(b)
	switch {
	case ea == nil && eb == nil:
		return ia.Less(ib)
	case ea == nil || eb == nil:
		return ea == nil
	}
	return a < b
}

// hostsOf maps entry IDs to the host of their check in cfg.
func hostsOf(cfg *config.Config) map[string]string {
	hs := map[string]string{}
	if cfg != nil {
		for _, s := range cfg.Checks {
			if h := hostOf(s.Target); h != "" {
				hs[s.ID()] = h
			}
		}
	}
	return hs
}

// serviceRows lays out the services: checks alone on their host (or without one) first,
// in the usual order, then each host with two or more checks under its name.
// Nothing here depends on a result, so no row moves when a state changes.
func serviceRows(part []state.Entry, hosts map[string]string, rows []srow, ents []state.Entry) ([]srow, []state.Entry) {
	n := map[string]int{}
	for _, e := range part {
		if h := hosts[e.ID]; h != "" {
			n[h]++
		}
	}
	var multi []string
	for _, e := range part {
		if h := hosts[e.ID]; n[h] < 2 {
			rows = append(rows, srow{ent: len(ents)})
			ents = append(ents, e)
		} else if !slices.Contains(multi, h) {
			multi = append(multi, h)
		}
	}
	sort.SliceStable(multi, func(i, j int) bool { return hostLess(multi[i], multi[j]) })
	for _, h := range multi {
		rows = append(rows, srow{hdr: h, host: true})
		for _, e := range part {
			if hosts[e.ID] == h {
				rows = append(rows, srow{ent: len(ents)})
				ents = append(ents, e)
			}
		}
	}
	return rows, ents
}
