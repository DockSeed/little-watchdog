// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// portOf is the port of a URL or host:port address ("" if there is none).
func portOf(target string) string {
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		if p := u.Port(); p != "" {
			return p
		}
		switch u.Scheme {
		case "http":
			return "80"
		case "https":
			return "443"
		}
		return ""
	}
	if _, p, err := net.SplitHostPort(target); err == nil {
		return p
	}
	return ""
}

// fastReject: an EHOSTUNREACH sooner than this is an ICMP reply (a firewall REJECT
// answers at once); a host that is off fails only after ARP gives up (3 probes, 1 s apart).
const fastReject = time.Second

// netFail is the FAIL result of a network error, with the hint for its kind.
func netFail(err error, timeout time.Duration, target string, took time.Duration) Result {
	p, k := PlainKind(err, timeout, target, took)
	return Result{Status: FAIL, Detail: p, Value: p, Help: Help(k)}
}

// PlainKind turns a network error into a sentence with the facts, plus the kind of the
// failure ("" when it is none of the known ones). timeout is the wait the check was given,
// target its URL or host:port and took how long the attempt ran. URLs never appear in the text.
func PlainKind(err error, timeout time.Duration, target string, took time.Duration) (string, Kind) {
	var dns *net.DNSError
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	var cv *tls.CertificateVerificationError
	switch {
	case errors.As(err, &dns) && !dns.IsTimeout:
		return "name " + Clean(dns.Name) + " not found", KindDNS
	case isTimeout(err):
		if timeout < time.Second {
			return "no answer within " + timeout.String(), KindTimeout
		}
		return "no answer within " + Dur(timeout), KindTimeout
	case errors.Is(err, syscall.ECONNREFUSED):
		if p := portOf(target); p != "" {
			return "connection refused, nothing listens on port " + p, KindRefused
		}
		return "connection refused", KindRefused
	case errors.Is(err, syscall.EHOSTUNREACH) && took < fastReject:
		if p := portOf(target); p != "" {
			return "port " + p + " blocked by a firewall, or no route to the host", KindBlocked
		}
		return "blocked by a firewall, or no route to the host", KindBlocked
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.EHOSTDOWN):
		return "host not reachable", KindUnreachable
	case errors.As(err, &cv), errors.As(err, &ua), errors.As(err, &hn), errors.As(err, &ci):
		msg := err.Error()
		if cv != nil && cv.Err != nil {
			msg = cv.Err.Error()
		}
		return Sanitize("TLS certificate problem: " + strings.TrimPrefix(msg, "tls: failed to verify certificate: ")), KindTLS
	}
	return Sanitize(short(err)), ""
}

func isTimeout(err error) bool {
	var to interface{ Timeout() bool }
	return errors.As(err, &to) && to.Timeout()
}
