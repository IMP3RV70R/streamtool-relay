package application

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/netip"
	"streamtool-relay/internal/auth"
	"strings"
	"testing"
	"time"
)

func TestManagementAndObservationAuthorization(t *testing.T) {
	a := &API{AdminToken: "operator-token", EdgeToken: "edge-token"}
	for _, tc := range []struct{ path, token string }{{"/v1/accounts", ""}, {"/v1/accounts", "edge-token"}, {"/v1/edge/observations", "operator-token"}, {"/v1/streams/anything/status", "wrong"}} {
		r := httptest.NewRequest("POST", tc.path, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("%s returned %d", tc.path, w.Code)
		}
	}
}

func TestEdgeReadScopeAndExpiry(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	a := &API{ReadKey: key, EdgePeers: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}
	for _, tc := range []struct {
		stream string
		expiry int64
		want   int
	}{{"stream-a", time.Now().Add(time.Minute).Unix(), 403}, {"stream-b", time.Now().Add(time.Minute).Unix(), 403}, {"stream-a", time.Now().Add(-time.Minute).Unix(), 403}} {
		token := auth.SignEdgeToken(auth.EdgeClaims{StreamID: tc.stream, Action: "read", ExpiresAt: tc.expiry}, key)
		body, _ := json.Marshal(map[string]string{"action": "read", "protocol": "srt", "path": "stream-a", "password": token})
		w := httptest.NewRecorder()
		a.EdgeHandler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/edge/auth", bytes.NewReader(body)))
		if w.Code != tc.want {
			t.Fatalf("scope result: %d want %d", w.Code, tc.want)
		}
	}
}
