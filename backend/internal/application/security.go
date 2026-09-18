package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Prefixes are deployment-owned; forwarded headers from other peers are ignored.
func ParsePrefixes(raw string) ([]netip.Prefix, error) {
	var result []netip.Prefix
	for _, item := range strings.Split(raw, ",") {
		if strings.TrimSpace(item) == "" {
			continue
		}
		p, err := netip.ParsePrefix(strings.TrimSpace(item))
		if err != nil {
			return nil, err
		}
		result = append(result, p.Masked())
	}
	return result, nil
}
func inPrefixes(ip netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}
func peerIP(r *http.Request) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	ip, err := netip.ParseAddr(host)
	return ip.Unmap(), err
}
func (a *API) clientIP(r *http.Request) (string, error) {
	ip, err := peerIP(r)
	if err != nil {
		return "", err
	}
	if !inPrefixes(ip, a.TrustedProxies) {
		return ip.String(), nil
	}
	raw := r.Header.Get("X-Forwarded-For")
	if raw == "" {
		return "", errors.New("trusted proxy did not provide client IP")
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 16 {
		return "", errors.New("forwarded chain too long")
	}
	for i := len(parts) - 1; i >= 0; i-- {
		ip, err = netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return "", err
		}
		ip = ip.Unmap()
		if !inPrefixes(ip, a.TrustedProxies) {
			return ip.String(), nil
		}
	}
	return "", errors.New("no untrusted client in forwarded chain")
}

// IPv6 privacy addresses in one /64 share a budget; IPv4 is counted per address.
func authNetwork(ip string) string {
	addr := netip.MustParseAddr(ip).Unmap()
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}
func (a *API) authAdmission(w http.ResponseWriter, r *http.Request) bool {
	ip, err := a.clientIP(r)
	if err != nil {
		problem(w, 400, "invalid client address")
		return false
	}
	for _, limit := range []struct {
		key          string
		max, seconds int
	}{
		{"auth:global", 60, 60}, {"auth:ip:" + authNetwork(ip), 10, 900}, {"auth:owner", 10, 900},
	} {
		allowed, err := a.allow(r.Context(), limit.key, limit.max, limit.seconds)
		if err != nil {
			problem(w, 503, "authentication unavailable")
			return false
		}
		if !allowed {
			w.Header().Set("Retry-After", fmt.Sprint(limit.seconds))
			problem(w, 429, "too many attempts")
			return false
		}
	}
	// Random emails/IPs cannot leave expired rows growing indefinitely.
	if _, err := a.Store.Pool.Exec(r.Context(), `DELETE FROM auth_attempts WHERE key IN (SELECT key FROM auth_attempts WHERE expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') LIMIT 128)`); err != nil {
		problem(w, 503, "authentication unavailable")
		return false
	}
	return true
}
func (a *API) allow(ctx context.Context, key string, maximum, seconds int) (bool, error) {
	digest := sha256.Sum256([]byte(key))
	var count int
	err := a.Store.Pool.QueryRow(ctx, `INSERT INTO auth_attempts(key,count,expires_at) VALUES(?1,1,strftime('%Y-%m-%dT%H:%M:%fZ','now','+'||?2||' seconds')) ON CONFLICT(key) DO UPDATE SET count=CASE WHEN auth_attempts.expires_at<strftime('%Y-%m-%dT%H:%M:%fZ','now') THEN 1 ELSE min(auth_attempts.count+1,?3+1) END,expires_at=CASE WHEN auth_attempts.expires_at<strftime('%Y-%m-%dT%H:%M:%fZ','now') THEN strftime('%Y-%m-%dT%H:%M:%fZ','now','+'||?2||' seconds') ELSE auth_attempts.expires_at END RETURNING count`, hex.EncodeToString(digest[:]), seconds, maximum).Scan(&count)
	return count <= maximum, err
}
func acquire(w http.ResponseWriter, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "1")
		problem(w, 429, "server busy")
		return false
	}
}

// EdgeHandler is served only on the dedicated private listener, never the web API.
func (a *API) EdgeHandler() http.Handler {
	slots := make(chan struct{}, 4)
	return recoverJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, err := peerIP(r)
		if err != nil || !inPrefixes(ip, a.EdgePeers) {
			problem(w, 403, "untrusted edge")
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v1/edge/auth" {
			problem(w, 404, "not found")
			return
		}
		if !acquire(w, slots) {
			return
		}
		defer func() { <-slots }()
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		a.edgeAuth(w, r, ip)
	}))
}
