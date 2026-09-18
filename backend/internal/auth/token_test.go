package auth

import (
	"testing"
	"time"
)

func TestAllocationTokenAndExpiry(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	now := time.Now()
	for _, tc := range []struct {
		claims  EdgeClaims
		allowed bool
	}{
		{EdgeClaims{StreamID: "s", Action: "read", ExpiresAt: now.Add(time.Minute).Unix()}, true},
		{EdgeClaims{StreamID: "s", Action: "read", ExpiresAt: now.Add(-time.Minute).Unix()}, false},
		{EdgeClaims{StreamID: "s", Action: "read"}, false},
		{EdgeClaims{StreamID: "s", Action: "read", AllocationID: "a", Generation: 1}, true},
		{EdgeClaims{StreamID: "s", Action: "read", AllocationID: "a"}, false},
	} {
		_, err := VerifyEdgeToken(SignEdgeToken(tc.claims, key), key, now)
		if (err == nil) != tc.allowed {
			t.Fatalf("%+v: %v", tc.claims, err)
		}
	}
}
