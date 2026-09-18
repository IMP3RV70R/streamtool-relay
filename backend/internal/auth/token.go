package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type EdgeClaims struct {
	StreamID, Action string
	ExpiresAt        int64
	AllocationID     string `json:",omitempty"`
	Generation       uint64 `json:",omitempty"`
}

func SignEdgeToken(claims EdgeClaims, key []byte) string {
	payload, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func VerifyEdgeToken(token string, key []byte, now time.Time) (EdgeClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return EdgeClaims{}, errors.New("invalid token")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return EdgeClaims{}, errors.New("invalid token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	var claims EdgeClaims
	if err != nil || json.Unmarshal(payload, &claims) != nil || (claims.ExpiresAt <= now.Unix() && !(claims.ExpiresAt == 0 && claims.AllocationID != "" && claims.Generation > 0)) {
		return EdgeClaims{}, errors.New("expired token")
	}
	return claims, nil
}
