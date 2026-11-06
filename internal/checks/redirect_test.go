// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/config"
)

// fakeDNS: public.test is a public address, lan.test a private one; the rest does not resolve.
func fakeDNS(t *testing.T) {
	old := lookupIP
	forgetClasses()
	t.Cleanup(func() { lookupIP = old; forgetClasses() })
	lookupIP = func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "public.test":
			return []net.IP{net.ParseIP("203.0.113.9")}, nil
		case "lan.test", "lan2.test":
			return []net.IP{net.ParseIP("192.168.1.5")}, nil
		}
		return nil, errors.New("no such host")
	}
}

// counter is a server that only counts its hits.
func counter(t *testing.T, tls bool) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1) })
	srv := httptest.NewServer(h)
	if tls {
		srv = httptest.NewTLSServer(h)
	}
	t.Cleanup(srv.Close)
	return srv, &n
}

// redirector answers every request with a redirect to loc.
func redirector(t *testing.T, loc string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loc, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// via makes the check dial srv whatever name its URL has.
func via(h *HTTP, srv *httptest.Server) *HTTP {
	u, _ := url.Parse(srv.URL)
	h.client.Transport.(*http.Transport).DialContext = func(ctx context.Context, n, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, n, u.Host)
	}
	return h
}

func TestRedirectFromAPublicServerToAnInternalAddressIsRefused(t *testing.T) {
	fakeDNS(t)
	target, hits := counter(t, false)
	for _, loc := range []string{target.URL + "/x", "http://lan.test/x", "http://[::1]:1/x", "http://169.254.169.254/"} {
		pub := redirector(t, loc)
		h := via(NewHTTP("x", "http://public.test/", "", 2*time.Second).SetMode("", config.ModeStrict), pub)
		r := h.Run(context.Background())
		if r.Status != FAIL || r.Detail != errInternalRedirect.Error() {
			t.Fatalf("%s: %+v", loc, r)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("the internal target was asked %d times", hits.Load())
	}
}

func TestRedirectWithinTheLANStaysAllowed(t *testing.T) {
	fakeDNS(t)
	target, hits := counter(t, false)
	lan := redirector(t, target.URL+"/x") // both on loopback: the origin is internal itself
	r := NewHTTP("x", lan.URL, "", 2*time.Second).SetMode("", config.ModeStrict).Run(context.Background())
	if r.Status != OK || hits.Load() != 1 {
		t.Fatalf("%+v, %d hits", r, hits.Load())
	}
	// by name: a private origin to a private target
	lan2 := redirector(t, "http://lan2.test:"+srvPort(target)+"/x")
	h := via(NewHTTP("x", "http://lan.test/", "", 2*time.Second).SetMode("", config.ModeStrict), lan2)
	h.client.Transport.(*http.Transport).DialContext = func(ctx context.Context, n, a string) (net.Conn, error) {
		if _, p, _ := net.SplitHostPort(a); p == srvPort(target) {
			return (&net.Dialer{}).DialContext(ctx, n, target.Listener.Addr().String())
		}
		return (&net.Dialer{}).DialContext(ctx, n, lan2.Listener.Addr().String())
	}
	if r := h.Run(context.Background()); r.Status != OK || hits.Load() != 2 {
		t.Fatalf("%+v, %d hits", r, hits.Load())
	}
}

func srvPort(s *httptest.Server) string {
	_, p, _ := net.SplitHostPort(s.Listener.Addr().String())
	return p
}

// The client without certificate checks stops at a redirect to another host: the 3xx answers.
func TestInsecureClientDoesNotFollowToAnotherHost(t *testing.T) {
	other, hits := counter(t, true)
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://localhost:"+srvPort(other)+"/x", http.StatusFound)
	}))
	t.Cleanup(tlsSrv.Close)
	// http:// on a TLS port: lenient asks again over https without a certificate check
	r := NewHTTP("x", "http://127.0.0.1:"+srvPort(tlsSrv), "", 2*time.Second).SetMode("", config.ModeLenient).Run(context.Background())
	if r.Status != OK {
		t.Fatalf("%+v", r)
	}
	if hits.Load() != 0 {
		t.Fatalf("the other host was asked %d times with the certificate check off", hits.Load())
	}
}

// forgetClasses empties the memo of internalHost, so a test sees its own fake DNS.
func forgetClasses() {
	classes.Lock()
	clear(classes.m)
	classes.Unlock()
}

// publicLocalhost makes the origin check see localhost as a public name; the dialer still
// resolves it the normal way, to loopback. allow lists the local servers a test may reach.
func publicLocalhost(t *testing.T, allow ...*httptest.Server) {
	oldL, oldR := lookupIP, refused
	forgetClasses()
	t.Cleanup(func() { lookupIP, refused = oldL, oldR; forgetClasses() })
	lookupIP = func(_ context.Context, h string) ([]net.IP, error) {
		if h == "localhost" {
			return []net.IP{net.ParseIP("203.0.113.9")}, nil
		}
		return nil, errors.New("no such host")
	}
	refused = func(a netip.AddrPort) bool {
		for _, s := range allow {
			if _, p, _ := net.SplitHostPort(s.Listener.Addr().String()); fmt.Sprint(a.Port()) == p {
				return false
			}
		}
		return internalAddr(a.Addr())
	}
}

// A name that looks public when it is checked and is internal when it is dialled (DNS
// rebinding) never gets a connection, on the first hop or after a redirect.
func TestDNSRebindingIsRefused(t *testing.T) {
	target, hits := counter(t, false)
	publicLocalhost(t)
	for _, mode := range []string{"", config.ModeLenient, config.ModeStrict} { // "" = a 0.3 line without mode=
		h := NewHTTP("x", "http://localhost:"+srvPort(target)+"/", "", 2*time.Second).SetMode("", mode)
		if r := h.Run(context.Background()); r.Status != FAIL || r.Detail != errInternalAddr.Error() {
			t.Fatalf("first hop, mode %q: %+v", mode, r)
		}
	}
	// after a redirect on the same name: CheckRedirect lets it pass, the dialer must not
	pub := redirector(t, "http://localhost:"+srvPort(target)+"/x")
	publicLocalhost(t, pub)
	r := NewHTTP("x", "http://localhost:"+srvPort(pub)+"/", "", 2*time.Second).SetMode("", config.ModeStrict).Run(context.Background())
	if r.Status != FAIL || r.Detail != errInternalAddr.Error() {
		t.Fatalf("after a redirect: %+v", r)
	}
	// lenient: https on the same name is asked without a certificate check, the dialer still refuses
	tlsTarget, tlsHits := counter(t, true)
	pubTLS := redirector(t, "https://localhost:"+srvPort(tlsTarget)+"/x")
	publicLocalhost(t, pubTLS)
	r = NewHTTP("x", "http://localhost:"+srvPort(pubTLS)+"/", "", 2*time.Second).SetMode("", config.ModeLenient).Run(context.Background())
	if r.Status != FAIL || r.Detail != errInternalAddr.Error() || tlsHits.Load() != 0 {
		t.Fatalf("after a redirect to https: %+v, %d hits", r, tlsHits.Load())
	}
	if hits.Load() != 0 {
		t.Fatalf("the internal target was asked %d times", hits.Load())
	}
}

// Names are looked up once per few minutes, not twice per run and again at every redirect, and
// the guard keeps working on the remembered answer: a name that turns internal is still refused.
func TestNameClassIsRemembered(t *testing.T) {
	target, hits := counter(t, false)
	pub := redirector(t, "http://localhost:"+srvPort(target)+"/x")
	publicLocalhost(t, pub)
	var lookups atomic.Int32
	inner := lookupIP
	lookupIP = func(ctx context.Context, h string) ([]net.IP, error) { lookups.Add(1); return inner(ctx, h) }
	h := NewHTTP("x", "http://localhost:"+srvPort(pub)+"/", "", 2*time.Second).SetMode("", config.ModeStrict)
	for i := 0; i < 3; i++ {
		if r := h.Run(context.Background()); r.Status != FAIL || r.Detail != errInternalAddr.Error() {
			t.Fatalf("run %d: %+v", i, r)
		}
	}
	if n := lookups.Load(); n > 1 {
		t.Fatalf("%d lookups for one name in 3 runs", n)
	}
	if hits.Load() != 0 {
		t.Fatalf("the internal target was asked %d times", hits.Load())
	}
}

// The guard does not get in the way of a public target.
func TestPublicTargetStillAnswers(t *testing.T) {
	target, hits := counter(t, false)
	publicLocalhost(t, target)
	r := NewHTTP("x", "http://localhost:"+srvPort(target)+"/", "", 2*time.Second).SetMode("", config.ModeStrict).Run(context.Background())
	if r.Status != OK || hits.Load() != 1 {
		t.Fatalf("%+v, %d hits", r, hits.Load())
	}
}

func TestInternalAddr(t *testing.T) {
	for s, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "::ffff:127.0.0.1": true, "10.1.2.3": true, "192.168.1.1": true,
		"172.16.0.1": true, "fd00::1": true, "fe80::1": true, "169.254.169.254": true, "100.64.0.1": true,
		"100.127.255.254": true, "0.0.0.0": true, "0.1.2.3": true, "::": true, "192.0.0.8": true,
		"198.18.0.1": true, "224.0.0.1": true, "ff02::1": true, "255.255.255.255": true,
		"64:ff9b::7f00:1": true, "64:ff9b::c0a8:101": true, "2002:7f00:1::": true, "2002:c0a8:101::": true,
		"2001:0:4136:e378:8000:63bf:80ff:fffe": true, // Teredo, client 127.0.0.1 inverted
		"203.0.113.9":                          false, "8.8.8.8": false, "100.63.255.255": false, "100.128.0.0": false,
		"2606:4700::1111": false, "64:ff9b::808:808": false, "2002:808:808::": false,
	} {
		if got := internalAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("%s: %v", s, got)
		}
	}
}
