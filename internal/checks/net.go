// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/DockSeed/little-watchdog/internal/config"
)

const maxBody = 1 << 20

// HTTP checks a URL. Without a mode (0.3 rules) status 200-399 is OK and redirects are
// not followed. With a mode redirects are followed (at most maxRedirects); lenient takes
// any answer below 500, and a port that answers but not as a website; strict takes only 2xx.
type HTTP struct {
	timeout             time.Duration // budget of one run, HEAD and GET fallback together
	name, url, contains string
	label, mode         string
	client              *http.Client
	insecure            *http.Client // lenient http:// on a port that speaks TLS: https without a certificate check
	get                 atomic.Bool  // server refused HEAD (405/501): use GET from now on
	scheme              atomic.Int32 // lenient host:port: which scheme answered last
}

const (
	schemeUnknown int32 = iota
	schemePlain
	schemeTLS
)

const maxRedirects = 5

var errRedirects = fmt.Errorf("more than %d redirects", maxRedirects)

func NewHTTP(name, url, contains string, timeout time.Duration) *HTTP {
	tr := &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 64 << 10,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		DialContext:            (&net.Dialer{Timeout: timeout, ControlContext: guard}).DialContext,
	}
	return &HTTP{name: name, url: url, contains: contains, timeout: timeout, client: &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// SetMode sets the name people see and the mode (config.ModeLenient, config.ModeStrict or "").
func (h *HTTP) SetMode(label, mode string) *HTTP {
	h.label, h.mode = label, mode
	if mode == "" {
		return h
	}
	h.client.CheckRedirect = followRedirect(false)
	if mode == config.ModeLenient && strings.HasPrefix(strings.ToLower(h.url), "http://") {
		follow := h.client.CheckRedirect
		h.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if toTLSOfSameHost(req, via) {
				return errTLSUpgrade
			}
			return follow(req, via)
		}
		tr := h.client.Transport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // only host:port was given: no name a certificate could prove
		h.insecure = &http.Client{Transport: tr, Timeout: h.timeout, CheckRedirect: followRedirect(true)}
	}
	return h
}

// errTLSUpgrade: a lenient http:// address redirects to https:// of the same host. The check asks
// again with the client without certificate checks, as it does for a port that speaks TLS.
var errTLSUpgrade = errors.New("redirect to https on the same host")

func toTLSOfSameHost(req *http.Request, via []*http.Request) bool {
	last := via[len(via)-1].URL
	return req.URL.Scheme == "https" && last.Scheme == "http" && strings.EqualFold(via[0].URL.Hostname(), req.URL.Hostname())
}

// errInternalRedirect: a redirect from a public server to a loopback, private or link-local address.
var errInternalRedirect = errors.New("redirect to an internal address refused")

// lookupIP resolves a host name for the redirect check (replaced in tests).
var lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// internalIP: an address a public server must not send the check to: loopback, private
// (RFC 1918, ULA), shared (CGNAT, Tailscale), link-local, unspecified, "this network" (0/8),
// IETF protocol assignments (192.0.0.0/24), benchmarking (198.18.0.0/15), multicast and
// broadcast, and an IPv4 address carried inside NAT64, 6to4 or Teredo.
func internalIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	return !ok || internalAddr(a)
}

var internalPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("255.255.255.255/32"),
}

func internalAddr(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() {
		return true
	}
	for _, p := range internalPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	if !a.Is6() {
		return false
	}
	b := a.As16()
	var v4 [4]byte
	switch {
	case b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b: // NAT64 64:ff9b::/96 and /48
		copy(v4[:], b[12:16])
	case b[0] == 0x20 && b[1] == 0x02: // 6to4 2002::/16
		copy(v4[:], b[2:6])
	case b[0] == 0x20 && b[1] == 0x01 && b[2] == 0 && b[3] == 0: // Teredo 2001::/32, client inverted
		for i := range v4 {
			v4[i] = b[12+i] ^ 0xff
		}
	default:
		return false
	}
	return internalAddr(netip.AddrFrom4(v4))
}

// classTTL is how long the answer of internalHost is kept per name; maxClasses bounds the memo.
const (
	classTTL   = 5 * time.Minute
	maxClasses = 1024
)

type classEntry struct {
	internal bool
	until    time.Time
}

// classes remembers internalHost per name, so one check does not ask the resolver again in every
// round and at every redirect. It only decides whether the guard applies; the guard itself looks
// at the address each connect is about to use, so a stale entry cannot let a connection through.
var classes = struct {
	sync.Mutex
	m map[string]classEntry
}{m: map[string]classEntry{}}

// internalHost: host is, or resolves to, an internal address (any one of them counts).
func internalHost(ctx context.Context, host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return internalIP(ip)
	}
	key := strings.ToLower(host)
	classes.Lock()
	e, ok := classes.m[key]
	classes.Unlock()
	if ok && time.Now().Before(e.until) {
		return e.internal
	}
	ips, err := lookupIP(ctx, host)
	if err != nil {
		return false
	}
	internal := false
	for _, ip := range ips {
		internal = internal || internalIP(ip)
	}
	classes.Lock()
	if len(classes.m) >= maxClasses {
		clear(classes.m)
	}
	classes.m[key] = classEntry{internal, time.Now().Add(classTTL)}
	classes.Unlock()
	return internal
}

// errInternalAddr: a check that started at a public address was about to connect to an internal
// one (a redirect, or a name whose DNS answer changed to an internal address).
var errInternalAddr = errors.New("a public address led to an internal one, refused")

// publicOrigin marks a run whose target is a public address (context key).
type publicOrigin struct{}

// refused reports whether a connection to addr must not happen (replaced in tests).
var refused = func(addr netip.AddrPort) bool { return internalAddr(addr.Addr()) }

// guard runs before every connect of the dialer, on the address it is about to connect to.
// In a run with a public origin an internal address is refused there, after DNS and after
// any redirect, so a DNS answer that changes (rebinding) cannot reach the LAN.
func guard(ctx context.Context, _, address string, _ syscall.RawConn) error {
	if ctx.Value(publicOrigin{}) == nil {
		return nil
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil || refused(ap) {
		return errInternalAddr
	}
	return nil
}

// followRedirect follows at most maxRedirects. A target that is not internal itself may not
// send the check to an internal address (a redirect from a public server must not steer GETs
// into the LAN); a LAN target may redirect within the LAN. The client without certificate
// checks (insecure) does not follow a redirect to another host: the 3xx is the answer.
func followRedirect(insecure bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errRedirects
		}
		from, to := via[0].URL.Hostname(), req.URL.Hostname()
		if strings.EqualFold(from, to) {
			return nil
		}
		if insecure {
			return http.ErrUseLastResponse
		}
		if ctx := req.Context(); !internalHost(ctx, from) && internalHost(ctx, to) {
			return errInternalRedirect
		}
		return nil
	}
}

func (h *HTTP) ID() string { return "http:" + h.name }
func (h *HTTP) Name() string {
	if h.label != "" {
		return h.label
	}
	return h.name
}
func (h *HTTP) Type() string { return "http" }

// errBadRequest: the check's URL could not even be turned into a request.
var errBadRequest = errors.New("bad request")

func (h *HTTP) Run(ctx context.Context) Result {
	// one budget for HEAD and the GET fallback together, not one each
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	ctx = h.markOrigin(ctx)
	if h.mode != "" {
		return h.runMode(ctx)
	}
	method := http.MethodGet
	if h.contains == "" && !h.get.Load() {
		method = http.MethodHead
	}
	start := time.Now()
	resp, err := h.do(ctx, method)
	if err == nil && method == http.MethodHead && (resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented) {
		resp.Body.Close()
		h.get.Store(true)
		resp, err = h.do(ctx, http.MethodGet)
	}
	if err != nil {
		if errors.Is(err, errBadRequest) {
			return Result{Status: FAIL, Detail: "the request could not be built"}
		}
		if r, ok := refusedResult(err); ok {
			return r
		}
		return netFail(err, h.timeout, h.url, time.Since(start))
	}
	defer resp.Body.Close()
	took := ms(time.Since(start))
	val := fmt.Sprintf("web %d, %s", resp.StatusCode, took)
	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		return Result{Status: FAIL, Detail: fmt.Sprintf("answered with HTTP %d", resp.StatusCode), Value: val, Help: Help(httpKind(resp.StatusCode))}
	}
	if h.contains != "" {
		found, err := containsStream(resp.Body, h.contains)
		if err != nil {
			return Result{Status: FAIL, Detail: "could not read the answer: " + short(err), Value: val}
		}
		if !found {
			return Result{Status: FAIL, Detail: "HTTP " + fmt.Sprint(resp.StatusCode) + ", but the expected text was not found", Value: val, Help: Help(KindNoText)}
		}
	}
	return Result{Status: OK, Value: val}
}

func (h *HTTP) do(ctx context.Context, method string) (*http.Response, error) {
	return h.send(ctx, h.client, method, h.url, nil)
}

func (h *HTTP) send(ctx context.Context, c *http.Client, method, url string, connected *bool) (*http.Response, error) {
	if connected != nil {
		// Only the first hop counts: after a redirect the target was named by the
		// server, and a dead target is a failure, not "port open, no website".
		hops := 0
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			GetConn: func(string) {
				hops++
				if hops > 1 {
					*connected = false
				}
			},
			ConnectDone: func(_, _ string, err error) {
				if err == nil && hops == 1 {
					*connected = true
				}
			},
		})
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, errBadRequest
	}
	req.Header.Set("User-Agent", "little-watchdog")
	return c.Do(req)
}

// refusedResult is the FAIL for a connect the guard refused.
func refusedResult(err error) (Result, bool) {
	if !errors.Is(err, errInternalAddr) {
		return Result{}, false
	}
	return Result{Status: FAIL, Detail: errInternalAddr.Error(), Value: "internal address refused"}, true
}

// markOrigin tells the dialer's guard that this run started at a public address (every mode).
func (h *HTTP) markOrigin(ctx context.Context) context.Context {
	if u, err := url.Parse(h.url); err == nil && !internalHost(ctx, u.Hostname()) {
		return context.WithValue(ctx, publicOrigin{}, true)
	}
	return ctx
}

// runMode is Run for lenient and strict: redirects followed, and the status rule of the mode.
func (h *HTTP) runMode(ctx context.Context) Result {
	method := http.MethodGet
	if h.contains == "" && !h.get.Load() {
		method = http.MethodHead
	}
	start := time.Now()
	ok := func(code int) bool {
		if h.mode == config.ModeStrict {
			return code >= 200 && code <= 299
		}
		return code < 500
	}
	try := func(c *http.Client, url string) (*http.Response, bool, error) {
		connected := false
		resp, err := h.send(ctx, c, method, url, &connected)
		if err == nil && method == http.MethodHead && !ok(resp.StatusCode) {
			// 405/501, or a page that answers HEAD differently: ask with GET
			if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
				h.get.Store(true)
			}
			resp.Body.Close()
			method = http.MethodGet
			resp, err = h.send(ctx, c, method, url, &connected)
		}
		return resp, connected, err
	}
	first, firstURL := h.client, h.url
	var second *http.Client
	secondURL := ""
	if h.insecure != nil { // lenient host:port: the port may speak TLS
		second, secondURL = h.insecure, "https://"+h.url[len("http://"):]
		if h.scheme.Load() == schemeTLS {
			first, firstURL, second, secondURL = second, secondURL, first, firstURL
		}
	}
	resp, connected, err := try(first, firstURL)
	if errors.Is(err, errTLSUpgrade) { // the plain client stopped at the redirect to https
		resp, connected, err = try(h.insecure, firstURL)
	}
	// Plain HTTP to a TLS port gets no HTTP answer, or a 400 (Go, nginx): ask over the
	// other scheme, once while it is unknown, and whenever there is no HTTP answer.
	retry := second != nil && ((err != nil && notWeb(ctx, err, connected)) ||
		(err == nil && h.scheme.Load() == schemeUnknown && resp.StatusCode == http.StatusBadRequest))
	if retry {
		if r2, c2, e2 := try(second, secondURL); e2 == nil {
			if resp != nil {
				resp.Body.Close()
			}
			resp, err = r2, nil
			first = second
		} else {
			connected = connected || c2
		}
	}
	if err == nil && h.insecure != nil {
		h.scheme.Store(map[bool]int32{true: schemeTLS, false: schemePlain}[first == h.insecure])
	}
	if err != nil {
		if errors.Is(err, errBadRequest) {
			return Result{Status: FAIL, Detail: "the request could not be built"}
		}
		if errors.Is(err, errRedirects) {
			return Result{Status: FAIL, Detail: errRedirects.Error(), Value: "too many redirects", Help: Help(KindRedirects)}
		}
		if errors.Is(err, errInternalRedirect) {
			return Result{Status: FAIL, Detail: errInternalRedirect.Error(), Value: "redirect refused"}
		}
		if r, ok := refusedResult(err); ok {
			return r
		}
		if h.mode == config.ModeLenient && notWeb(ctx, err, connected) {
			return Result{Status: OK, Odd: true, Value: "port open, no website"}
		}
		return netFail(err, h.timeout, h.url, time.Since(start))
	}
	defer resp.Body.Close()
	took := ms(time.Since(start))
	if !ok(resp.StatusCode) {
		return Result{Status: FAIL, Detail: fmt.Sprintf("answered with HTTP %d", resp.StatusCode), Value: fmt.Sprintf("web %d, %s", resp.StatusCode, took), Help: Help(httpKind(resp.StatusCode))}
	}
	val := fmt.Sprintf("web %d, %s", resp.StatusCode, took)
	if h.contains != "" {
		found, err := containsStream(resp.Body, h.contains)
		if err != nil {
			return Result{Status: FAIL, Detail: "could not read the answer: " + short(err), Value: val}
		}
		if !found {
			return Result{Status: FAIL, Detail: "HTTP " + fmt.Sprint(resp.StatusCode) + ", but the expected text was not found", Value: val, Help: Help(KindNoText)}
		}
	}
	return Result{Status: OK, Value: val}
}

// notWeb: the port took the connection, but what came back was no HTTP answer
// (not a timeout, a certificate problem or too many redirects).
func notWeb(ctx context.Context, err error, connected bool) bool {
	var cv *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	switch {
	case !connected, ctx.Err() != nil, isTimeout(err), errors.Is(err, errRedirects), errors.Is(err, errInternalRedirect), errors.Is(err, errInternalAddr), errors.Is(err, errBadRequest):
		return false
	case errors.As(err, &cv), errors.As(err, &ua), errors.As(err, &hn), errors.As(err, &ci):
		return false
	}
	return true
}

// containsStream searches the first maxBody bytes of r with a fixed buffer.
func containsStream(r io.Reader, needle string) (bool, error) {
	nb := []byte(needle)
	lr := io.LimitReader(r, maxBody)
	buf := make([]byte, max(8<<10, 2*len(nb)))
	have := 0
	for {
		m, err := lr.Read(buf[have:])
		have += m
		if bytes.Contains(buf[:have], nb) {
			return true, nil
		}
		if have >= len(nb) { // keep a tail so a match can span two reads
			keep := len(nb) - 1
			copy(buf, buf[have-keep:have])
			have = keep
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// TCP connects to host:port. With host=true a refused connection counts as
// "alive" (something answered); only timeout/unreachable is FAIL.
type TCP struct {
	name, addr string
	label      string // shown name; empty: name
	timeout    time.Duration
	host       bool
}

func (t *TCP) ID() string {
	if t.host {
		return "host:" + t.name
	}
	return "tcp:" + t.name
}
func (t *TCP) Name() string {
	if t.label != "" {
		return t.label
	}
	return t.name
}
func (t *TCP) Type() string {
	if t.host {
		return "host"
	}
	return "tcp"
}

func (t *TCP) Run(ctx context.Context) Result {
	d := net.Dialer{Timeout: t.timeout}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", t.addr)
	if err == nil {
		c.Close()
		return Result{Status: OK, Value: "port open, " + ms(time.Since(start))}
	}
	if t.host && errors.Is(err, syscall.ECONNREFUSED) {
		return Result{Status: OK, Value: "port closed, host up, " + ms(time.Since(start))}
	}
	if t.host {
		r := netFail(err, t.timeout, t.addr, time.Since(start))
		r.NoAnswer, r.Detail = true, ""
		return r
	}
	return netFail(err, t.timeout, t.addr, time.Since(start))
}
