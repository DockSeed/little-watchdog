// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const maxBody = 1 << 20

// HTTP checks a URL: status 200-399 is OK, redirects are not followed.
type HTTP struct {
	timeout             time.Duration // budget of one run, HEAD and GET fallback together
	name, url, contains string
	client              *http.Client
	get                 atomic.Bool // server refused HEAD (405/501): use GET from now on
}

func NewHTTP(name, url, contains string, timeout time.Duration) *HTTP {
	tr := &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 64 << 10,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		DialContext:            (&net.Dialer{Timeout: timeout}).DialContext,
	}
	return &HTTP{name: name, url: url, contains: contains, timeout: timeout, client: &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (h *HTTP) ID() string   { return "http:" + h.name }
func (h *HTTP) Name() string { return h.name }
func (h *HTTP) Type() string { return "http" }

// errBadRequest: the check's URL could not even be turned into a request.
var errBadRequest = errors.New("bad request")

func (h *HTTP) Run(ctx context.Context) Result {
	// one budget for HEAD and the GET fallback together, not one each
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
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
			return Result{Status: FAIL, Detail: "bad request"}
		}
		return Result{Status: FAIL, Detail: short(err), Value: short(err)}
	}
	defer resp.Body.Close()
	val := fmt.Sprintf("HTTP %d in %dms", resp.StatusCode, time.Since(start).Milliseconds())
	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		return Result{Status: FAIL, Detail: fmt.Sprintf("HTTP %d", resp.StatusCode), Value: val}
	}
	if h.contains != "" {
		found, err := containsStream(resp.Body, h.contains)
		if err != nil {
			return Result{Status: FAIL, Detail: "read body: " + short(err), Value: val}
		}
		if !found {
			return Result{Status: FAIL, Detail: "HTTP " + fmt.Sprint(resp.StatusCode) + ", text not found", Value: val}
		}
	}
	return Result{Status: OK, Value: val}
}

func (h *HTTP) do(ctx context.Context, method string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, h.url, nil)
	if err != nil {
		return nil, errBadRequest
	}
	req.Header.Set("User-Agent", "little-watchdog")
	return h.client.Do(req)
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
	timeout    time.Duration
	host       bool
}

func (t *TCP) ID() string {
	if t.host {
		return "host:" + t.name
	}
	return "tcp:" + t.name
}
func (t *TCP) Name() string { return t.name }
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
		return Result{Status: OK, Value: fmt.Sprintf("open in %dms", time.Since(start).Milliseconds())}
	}
	if t.host && errors.Is(err, syscall.ECONNREFUSED) {
		return Result{Status: OK, Value: "refused (alive)"}
	}
	if t.host {
		return Result{Status: FAIL, NoAnswer: true, Value: short(err)}
	}
	return Result{Status: FAIL, Detail: "port " + t.addr[strings.LastIndex(t.addr, ":")+1:] + " not reachable (" + short(err) + ")", Value: short(err)}
}
