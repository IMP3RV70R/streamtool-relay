package netpolicy

import (
	"context"
	"net"
	"testing"
)

type resolver struct{ ips []net.IP }

func (r resolver) LookupIP(context.Context, string, string) ([]net.IP, error) { return r.ips, nil }
func TestRejectsSSRFAndCredentials(t *testing.T) {
	p := RTMPPolicy{Resolver: resolver{[]net.IP{net.ParseIP("127.0.0.1")}}}
	if _, err := p.Validate(context.Background(), "rtmp://internal/live"); err == nil {
		t.Fatal("private target accepted")
	}
	if _, err := p.Validate(context.Background(), "rtmp://user:pass@example.com/live"); err == nil {
		t.Fatal("credentials accepted")
	}
}
func TestAllowsPublicTarget(t *testing.T) {
	p := RTMPPolicy{Resolver: resolver{[]net.IP{net.ParseIP("93.184.216.34")}}}
	if _, err := p.Validate(context.Background(), "RTMP://Example.COM/live/"); err != nil {
		t.Fatal(err)
	}
}
