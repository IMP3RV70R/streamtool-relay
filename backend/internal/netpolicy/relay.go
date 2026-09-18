package netpolicy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Relay keeps native RTMP networking on loopback. Every reconnect resolves and
// checks all addresses, then dials an IP literal (no second DNS resolution).
// TLS is verified against the original hostname, not the pinned IP.
type Relay struct {
	Policy    RTMPPolicy
	mu        sync.Mutex
	listeners map[string]*relayTarget
}
type relayTarget struct {
	raw      string
	listener net.Listener
	cancel   context.CancelFunc
	location string
}

func (r *Relay) Location(key, raw string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listeners == nil {
		r.listeners = map[string]*relayTarget{}
	}
	if target := r.listeners[key]; target != nil {
		if target.raw == raw {
			return target.location, nil
		}
		target.cancel()
		target.listener.Close()
		delete(r.listeners, key)
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "rtmp" && u.Scheme != "rtmps") {
		return "", errors.New("invalid relay target")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	local := *u
	local.Scheme = "rtmp"
	local.Host = listener.Addr().String()
	r.listeners[key] = &relayTarget{raw: raw, listener: listener, cancel: cancel, location: local.String()}
	go func() {
		slots := make(chan struct{}, 2)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				go func() { defer func() { <-slots }(); r.forward(ctx, conn, u) }()
			default:
				conn.Close()
			}
		}
	}()
	return local.String(), nil
}
func (r *Relay) forward(parent context.Context, down net.Conn, u *url.URL) {
	defer down.Close()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	port := u.Port()
	if port == "" {
		port = "1935"
		if u.Scheme == "rtmps" {
			port = "443"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return
	}
	if _, err = r.Policy.Validate(ctx, u.String()); err != nil {
		return
	}
	// Resolve once more only to obtain the dial candidates; validate these exact
	// candidates too, so rebinding between the two resolutions cannot bypass it.
	ips, err := r.Policy.Resolver.LookupIP(ctx, "ip", u.Hostname())
	if err != nil || len(ips) == 0 {
		return
	}
	for _, ip := range ips {
		if deniedIP(ip) && !(r.Policy.Development && r.Policy.AllowHosts[u.Hostname()]) {
			return
		}
	}
	var up net.Conn
	for _, ip := range ips {
		up, err = (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			break
		}
	}
	if err != nil || up == nil {
		return
	}
	defer up.Close()
	if u.Scheme == "rtmps" {
		secure := tls.Client(up, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()})
		if secure.HandshakeContext(ctx) != nil {
			return
		}
		up = secure
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-parent.Done():
			down.Close()
			up.Close()
		case <-done:
		}
	}()
	defer close(done)
	finished := make(chan struct{}, 1)
	go func() { io.Copy(up, down); up.Close(); down.Close(); finished <- struct{}{} }()
	io.Copy(down, up)
	down.Close()
	up.Close()
	<-finished
}

// Retain bounds listeners and closes connections for deleted outputs.
func (r *Relay) Retain(keys map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, target := range r.listeners {
		if !keys[key] {
			target.cancel()
			target.listener.Close()
			delete(r.listeners, key)
		}
	}
}
func (r *Relay) Close() { r.Retain(nil) }
