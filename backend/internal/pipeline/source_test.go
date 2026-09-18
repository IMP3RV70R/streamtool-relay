package pipeline

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestResolveSourcePreservesCredentialsAndRefreshesDNS(t *testing.T) {
	location := "srt://edge:8890?streamid=read%3Astream%3Aworker%3Asecret&latency=120"
	address := "192.0.2.1"
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "edge" {
			t.Fatalf("host: %s", host)
		}
		return []net.IPAddr{{IP: net.ParseIP(address)}}, nil
	}
	for _, want := range []string{"192.0.2.1", "192.0.2.2"} {
		address = want
		got, err := resolveSource(context.Background(), location, lookup)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(got)
		if u.Hostname() != want || u.Port() != "8890" || u.Query().Get("streamid") != "read:stream:worker:secret" || u.Query().Get("latency") != "120" {
			t.Fatal("source location was not preserved")
		}
	}
}
func TestResolveSourceDoesNotLeakFailedLocation(t *testing.T) {
	_, err := resolveSource(context.Background(), "srt://edge:8890?streamid=secret", func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("secret") })
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
	location := "srt://[::1]:8890?streamid=secret"
	got, err := resolveSource(context.Background(), location, func(context.Context, string) ([]net.IPAddr, error) { t.Fatal("IP must bypass DNS"); return nil, nil })
	if err != nil || got != location {
		t.Fatal("literal IP changed")
	}
}

func TestResolveSourcePreservesDefaultPort(t *testing.T) {
	got, err := resolveSource(context.Background(), "srt://edge?latency=120", func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}}, nil
	})
	if err != nil || got != "srt://192.0.2.1?latency=120" {
		t.Fatalf("default port: %s %v", got, err)
	}
}

func TestSupportedInputBoundsDoNotRequireOutputMatch(t *testing.T) {
	for _, p := range [][4]int{{640, 360, 30, 1}, {1920, 1080, 60, 1}, {1080, 1920, 60000, 1001}, {1280, 720, 0, 1}} {
		if !supportedInputVideo(p[0], p[1], p[2], p[3], true) {
			t.Fatal("valid input rejected", p)
		}
	}
	for _, p := range [][4]int{{3840, 2160, 30, 1}, {1920, 1920, 30, 1}, {1280, 720, 120, 1}, {0, 720, 30, 1}, {1280, 720, 30, 0}} {
		if supportedInputVideo(p[0], p[1], p[2], p[3], true) {
			t.Fatal("unbounded input accepted", p)
		}
	}
}
