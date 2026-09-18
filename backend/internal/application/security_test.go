package application

import (
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPrivateEdgeBoundary(t *testing.T) {
	a := &API{EdgePeers: []netip.Prefix{netip.MustParsePrefix("10.1.1.10/32")}, EdgeToken: "secret"}
	for _, tc := range []struct {
		public bool
		peer   string
		want   int
	}{
		{true, "10.1.1.10:1234", 404}, {false, "10.1.1.11:1234", 403}, {false, "10.1.1.10:1234", 204},
	} {
		r := httptest.NewRequest("POST", "/v1/edge/auth", strings.NewReader(`{"action":"api","user":"controller","password":"secret"}`))
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", "10.1.1.10")
		w := httptest.NewRecorder()
		if tc.public {
			a.Handler().ServeHTTP(w, r)
		} else {
			a.EdgeHandler().ServeHTTP(w, r)
		}
		if w.Code != tc.want {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
	}
}
func TestTrustedProxyChain(t *testing.T) {
	a := &API{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.1.0.0/24")}}
	for _, tc := range []struct {
		peer, forwarded, want string
		rejected              bool
	}{
		{"93.184.216.34:1234", "127.0.0.1", "93.184.216.34", false},
		{"10.1.0.2:1234", "fake, 93.184.216.34, 10.1.0.3", "93.184.216.34", false},
		{"10.1.0.2:1234", "93.184.216.35", "93.184.216.35", false},
		{"10.1.0.2:1234", "", "", true}, {"10.1.0.2:1234", "invalid", "", true},
	} {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		ip, err := a.clientIP(r)
		if (err != nil) != tc.rejected || ip != tc.want {
			t.Fatalf("%+v: %s %v", tc, ip, err)
		}
	}
}
func TestBoundedRequests(t *testing.T) {
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	w := httptest.NewRecorder()
	if acquire(w, slots) || w.Code != 429 {
		t.Fatal("concurrency limit bypassed")
	}
}
