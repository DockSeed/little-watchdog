// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package deadman

import (
	"net"
	"net/http"
	"sync/atomic"
)

func connCounter(n *atomic.Int32) func(net.Conn, http.ConnState) {
	return func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			n.Add(1)
		}
	}
}
