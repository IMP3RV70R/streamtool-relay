package netpolicy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type Resolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
}
type RTMPPolicy struct {
	Resolver    Resolver
	AllowHosts  map[string]bool
	Development bool
}

func (p RTMPPolicy) Validate(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "rtmp" && u.Scheme != "rtmps") || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return "", errors.New("invalid RTMP endpoint")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid RTMP port")
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	host := u.Hostname()
	{
		ips, err := p.Resolver.LookupIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return "", errors.New("destination DNS resolution failed")
		}
		for _, ip := range ips {
			if deniedIP(ip) && !(p.Development && p.AllowHosts[host]) {
				return "", errors.New("destination resolves to a private or reserved address")
			}
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}
func deniedIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	if a.Is6() && !netip.MustParsePrefix("2000::/3").Contains(a) {
		return true
	}
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return true
	}
	for _, p := range reserved {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

var reserved = []netip.Prefix{
	netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

type NetResolver struct{ *net.Resolver }
