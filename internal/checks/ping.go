// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package checks

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"time"
)

// Ping sends ICMP echo requests over an unprivileged datagram socket (no raw socket, no
// capability). Linux allows it to the groups in net.ipv4.ping_group_range.
type Ping struct {
	name, label, host string
	timeout           time.Duration
	seq               atomic.Uint32
}

func NewPing(name, label, host string, timeout time.Duration) *Ping {
	return &Ping{name: name, label: label, host: host, timeout: timeout}
}

func (p *Ping) ID() string   { return "ping:" + p.name }
func (p *Ping) Name() string { return p.label }
func (p *Ping) Type() string { return "ping" }

// PingNotAllowed is the result text when the system does not let this program ping.
const PingNotAllowed = "this system does not allow little-wd to ping (net.ipv4.ping_group_range leaves out its group)"

// PingBlocked: the socket was refused for another reason, such as a sandbox (RestrictAddressFamilies).
const PingBlocked = "ping is blocked for little-wd on this system (it may not open an ICMP socket)"

// pingEvery is the wait for one echo reply before the next request goes out.
const pingEvery = time.Second

func (p *Ping) Run(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	start := time.Now()
	ip, err := resolve(ctx, p.host)
	if err != nil {
		return netFail(err, p.timeout, p.host, time.Since(start))
	}
	rtt, err := p.echo(ctx, ip)
	switch {
	case err == nil:
		return Result{Status: OK, Value: "ping answers, " + ms(rtt)}
	case errors.Is(err, syscall.EACCES): // the kernel's answer when the group is outside ping_group_range
		return Result{Status: FAIL, Detail: PingNotAllowed, Value: "ping not allowed", Help: Help(KindPingBlocked)}
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EPROTONOSUPPORT), errors.Is(err, syscall.EAFNOSUPPORT):
		return Result{Status: FAIL, Detail: PingBlocked, Value: "ping blocked", Help: Help(KindPingBlocked)}
	case isTimeout(err):
		return Result{Status: FAIL, NoAnswer: true, Detail: "no answer to ping within " + Dur(p.timeout), Value: "no ping answer", Help: Help(KindNoPing)}
	}
	return netFail(err, p.timeout, p.host, time.Since(start))
}

// resolve returns the address to ping, IPv4 first.
func resolve(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	as, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		if a.IP.To4() != nil {
			return a.IP, nil
		}
	}
	if len(as) == 0 {
		return nil, &net.DNSError{Err: "no address", Name: host, IsNotFound: true}
	}
	return as[0].IP, nil
}

// echo sends a request every pingEvery until a reply comes or ctx ends; it returns the round trip time.
func (p *Ping) echo(ctx context.Context, ip net.IP) (time.Duration, error) {
	fam, proto, req, rep := syscall.AF_INET, syscall.IPPROTO_ICMP, byte(8), byte(0)
	if ip.To4() == nil {
		fam, proto, req, rep = syscall.AF_INET6, syscall.IPPROTO_ICMPV6, 128, 129
	}
	fd, err := syscall.Socket(fam, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, proto)
	if err != nil {
		return 0, err
	}
	f := os.NewFile(uintptr(fd), "ping")
	c, err := net.FilePacketConn(f) // a dup in the poller; the deadlines then work
	f.Close()
	if err != nil {
		return 0, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.SetDeadline(time.Now()) })
	defer stop()
	dst := &net.UDPAddr{IP: ip}
	buf := make([]byte, 1500)
	for ctx.Err() == nil {
		seq := uint16(p.seq.Add(1))
		msg := []byte{req, 0, 0, 0, 0, 0, byte(seq >> 8), byte(seq), 'l', 'w'} // the kernel sets the id
		if req == 8 {
			binary.BigEndian.PutUint16(msg[2:], icmpSum(msg))
		}
		sent := time.Now()
		if _, err := c.WriteTo(msg, dst); err != nil {
			return 0, err
		}
		dl := sent.Add(pingEvery)
		if d, ok := ctx.Deadline(); ok && d.Before(dl) {
			dl = d
		}
		c.SetReadDeadline(dl)
		for {
			n, _, err := c.ReadFrom(buf)
			if err != nil {
				if isTimeout(err) && ctx.Err() == nil {
					break // send the next one
				}
				if ctx.Err() != nil {
					return 0, context.DeadlineExceeded
				}
				return 0, err
			}
			if n >= 8 && buf[0] == rep && binary.BigEndian.Uint16(buf[6:]) == seq {
				return time.Since(sent), nil
			}
		}
	}
	return 0, context.DeadlineExceeded
}

func icmpSum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

// ms writes a duration as people read it: "0.4 ms", "11 ms".
func ms(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}
