package netpolicy

import (
	"context"
	"io"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

type rebindResolver struct{ calls atomic.Int32 }

func (r *rebindResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	if r.calls.Add(1) == 1 {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	return []net.IP{net.ParseIP("127.0.0.1")}, nil
}
func TestRelayRejectsRebindingBeforeDial(t *testing.T) {
	dns := &rebindResolver{}
	relay := &Relay{Policy: RTMPPolicy{Resolver: dns, AllowHosts: map[string]bool{"example.com": true}}}
	defer relay.Close()
	location, err := relay.Location("output:1", "rtmp://example.com/live")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(location)
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [1]byte
	if _, err = conn.Read(b[:]); err == nil {
		t.Fatal("rebound target accepted")
	}
	if dns.calls.Load() != 2 {
		t.Fatal("exact dial addresses were not checked")
	}
}
func TestRelayTransportsAndClosesRemovedOutput(t *testing.T) {
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn)
	}()
	policy := RTMPPolicy{Resolver: resolver{[]net.IP{net.ParseIP("127.0.0.1")}}, Development: true, AllowHosts: map[string]bool{"localhost": true}}
	relay := &Relay{Policy: policy}
	defer relay.Close()
	_, port, _ := net.SplitHostPort(upstream.Addr().String())
	location, err := relay.Location("output:1", "rtmp://localhost:"+port+"/live")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(location)
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write([]byte("test"))
	b := make([]byte, 4)
	if _, err = io.ReadFull(conn, b); err != nil || string(b) != "test" {
		t.Fatal("relay failed", err)
	}
	relay.Retain(nil)
	if _, err = conn.Read(b); err == nil {
		t.Fatal("removed output remained connected")
	}
}
func TestRejectsReservedAndAllowlistBypass(t *testing.T) {
	for _, ip := range []string{"100.64.0.1", "169.254.169.254", "::ffff:127.0.0.1", "2001:db8::1", "2002:7f00:1::1", "240.0.0.1", "3fff::1"} {
		p := RTMPPolicy{Resolver: resolver{[]net.IP{net.ParseIP(ip)}}, AllowHosts: map[string]bool{"trusted": true}}
		if _, err := p.Validate(context.Background(), "rtmp://trusted/live"); err == nil {
			t.Fatal("unsafe address accepted", ip)
		}
	}
}
