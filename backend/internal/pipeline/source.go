package pipeline

import (
	"context"
	"errors"
	"net"
	"net/url"
)

// Resolve outside GStreamer: repeated cancellation of GLib's async DNS resolver
// during SRT reconnects can abort the process. Re-resolve on every attempt so an
// edge container replacement can change addresses without restarting the output.
func resolveSource(ctx context.Context, location string, lookup func(context.Context, string) ([]net.IPAddr, error)) (string, error) {
	u, err := url.Parse(location)
	if err != nil || u.Scheme != "srt" || u.Hostname() == "" {
		return "", errors.New("invalid SRT source")
	}
	if net.ParseIP(u.Hostname()) != nil {
		return location, nil
	}
	addresses, err := lookup(ctx, u.Hostname())
	if err != nil || len(addresses) == 0 {
		return "", errors.New("SRT source DNS unavailable")
	}
	selected := addresses[0].IP
	for _, address := range addresses {
		if address.IP.To4() != nil {
			selected = address.IP
			break
		}
	}
	if u.Port() != "" {
		u.Host = net.JoinHostPort(selected.String(), u.Port())
	} else if selected.To4() != nil {
		u.Host = selected.String()
	} else {
		u.Host = "[" + selected.String() + "]"
	}
	return u.String(), nil
}

// Unknown frame rate is accepted until the parser reports it. Resource admission
// and namespace traffic policing bound work independently of missing metadata.
func supportedInputVideo(width, height, numerator, denominator int, rateKnown bool) bool {
	if width < 1 || height < 1 || width > 1920 || height > 1920 || width*height > 1920*1080 {
		return false
	}
	return !rateKnown || numerator == 0 || (denominator > 0 && numerator > 0 && numerator <= 60*denominator)
}
