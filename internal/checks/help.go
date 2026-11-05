// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

// Kind is the kind of a failure; it picks the "what to do" hint. "" has none.
type Kind string

// Failure kinds. Plain and the checks set them; Help turns them into a hint.
const (
	KindRefused     Kind = "refused"     // nothing listens on the port
	KindBlocked     Kind = "blocked"     // fast EHOSTUNREACH: firewall REJECT, or no route
	KindUnreachable Kind = "unreachable" // slow unreachable: the machine is off or gone
	KindTimeout     Kind = "timeout"     // no answer within the timeout
	KindDNS         Kind = "dns"         // name not found
	KindTLS         Kind = "tls"         // certificate problem
	KindHTTP4xx     Kind = "http4xx"     // the server refused the request
	KindHTTP5xx     Kind = "http5xx"     // the app behind the web server failed
	KindHTTPOther   Kind = "http"        // another status outside 200-399
	KindNoText      Kind = "notext"      // the expected text is missing
	KindCPU         Kind = "cpu"         // CPU busy over the limit
	KindMem         Kind = "mem"         // too little memory available
	KindDiskFull    Kind = "diskfull"    // too little free space
	KindDiskMount   Kind = "diskmount"   // path not mounted
	KindDiskStuck   Kind = "diskstuck"   // the last disk query still hangs
	KindDiskRead    Kind = "diskread"    // the path cannot be read
	KindTemp        Kind = "temp"        // too hot
	KindNoPing      Kind = "noping"      // no echo reply within the timeout
	KindPingBlocked Kind = "pingblocked" // this system does not let little-wd ping
	KindRedirects   Kind = "redirects"   // more redirects than allowed
)

// helps holds one short hint per kind. It names possible causes only: the check knows
// the kind of failure, not its reason. Plain words, no values from the config.
var helps = map[Kind]string{
	KindRefused:     "Maybe the service is stopped, or it listens on another port.",
	KindBlocked:     "Maybe a firewall on that machine blocks the port, or no route leads there.",
	KindUnreachable: "Maybe the machine is off or not in the network.",
	KindTimeout:     "Maybe the machine is off, overloaded, or a firewall drops the traffic.",
	KindDNS:         "Maybe the name is misspelled. The IP address works without DNS.",
	KindTLS:         "Maybe the certificate has expired, is self-signed, or is made for another name.",
	KindHTTP4xx:     "Maybe the path is wrong, or the page wants a login.",
	KindHTTP5xx:     "Maybe the app behind the web server has failed. Its log may say why.",
	KindHTTPOther:   "The answer is unusual. A browser shows what the page says.",
	KindNoText:      "Maybe the page shows an error or a login instead. A browser shows what it says.",
	KindCPU:         "Maybe a program is stuck or busy. A process list such as top shows which one.",
	KindMem:         "Maybe a program uses too much memory. A process list such as top shows which one.",
	KindDiskFull:    "Maybe old logs, backups or images fill the disk.",
	KindDiskMount:   "Maybe the disk is unplugged, off, or not mounted yet.",
	KindDiskStuck:   "Maybe the disk or its connection hangs.",
	KindDiskRead:    "Maybe the path does not exist, or its disk is gone.",
	KindTemp:        "Maybe the fan has stopped or the air cannot flow.",
	KindNoPing:      "Maybe the machine is off, or it does not answer ping. Add a port to watch a service instead.",
	KindPingBlocked: "This system does not let little-wd ping. Add a port to watch a service instead.",
	KindRedirects:   "Maybe the page sends visitors in a circle. A browser shows where it leads.",
}

// Kinds lists every failure kind that has a hint, in a fixed order.
func Kinds() []Kind {
	return []Kind{KindRefused, KindBlocked, KindUnreachable, KindTimeout, KindDNS, KindTLS,
		KindHTTP4xx, KindHTTP5xx, KindHTTPOther, KindNoText, KindCPU, KindMem,
		KindDiskFull, KindDiskMount, KindDiskStuck, KindDiskRead, KindTemp,
		KindNoPing, KindPingBlocked, KindRedirects}
}

// Help is the hint for k; an unknown kind has none ("").
func Help(k Kind) string { return helps[k] }

// httpKind is the kind of an HTTP status outside 200-399.
func httpKind(code int) Kind {
	switch {
	case code >= 400 && code < 500:
		return KindHTTP4xx
	case code >= 500 && code < 600:
		return KindHTTP5xx
	}
	return KindHTTPOther
}
