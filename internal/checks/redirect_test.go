// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DockSeed/little-watchdog/internal/config"
)

// fakeDNS: public.test is a public address, lan.test a private one; the rest does not resolve.
func fakeDNS(t *testing.T) {
	old := lookupIP
	t.Cleanup(func() { lookupIP = old })
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
