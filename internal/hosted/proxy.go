//go:build !js

package hosted

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// cloudflareProxyCIDRs is a snapshot of Cloudflare's published edge ranges
// (https://www.cloudflare.com/ips/). Cloudflare can add or retire ranges;
// operators should refresh this list or pass --host-trusted-proxies explicitly
// when the origin is reachable from an outdated edge address.
var cloudflareProxyCIDRs = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
	"2400:cb00::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2405:b500::/32",
	"2405:8100::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
}

// CloudflareProxyPrefixes returns the built-in Cloudflare edge ranges. Callers
// that need current data should fetch Cloudflare's published list and pass it
// to ParseTrustedProxies instead.
func CloudflareProxyPrefixes() []netip.Prefix {
	prefixes, err := ParseTrustedProxies(cloudflareProxyCIDRs)
	if err != nil {
		// The list is a compile-time constant; an invalid entry is a bug.
		panic("hosted: invalid built-in Cloudflare proxy range: " + err.Error())
	}
	return prefixes
}

// ParseTrustedProxies parses CIDRs or bare IP addresses, accepting repeated
// flags and comma-separated values. Prefixes are masked and deduplicated.
func ParseTrustedProxies(values []string) ([]netip.Prefix, error) {
	seen := make(map[netip.Prefix]bool)
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			prefix, err := parseProxyPrefix(part)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", part, err)
			}
			if seen[prefix] {
				continue
			}
			seen[prefix] = true
			out = append(out, prefix)
		}
	}
	return out, nil
}

func parseProxyPrefix(raw string) (netip.Prefix, error) {
	if strings.Contains(raw, "/") {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return netip.Prefix{}, err
		}
		return prefix.Masked(), nil
	}
	address, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Prefix{}, err
	}
	address = address.Unmap()
	return netip.PrefixFrom(address, address.BitLen()), nil
}

// clientIP returns the address used for rate-limit buckets. Forwarded headers
// are honored only when the direct peer is in a configured trusted range, so
// a direct client cannot choose its own bucket by spoofing a header.
func (s *Service) clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	remote := remoteProxyIP(r.RemoteAddr)
	if s == nil || len(s.trustedProxies) == 0 || !ipInPrefixes(remote, s.trustedProxies) {
		return remote
	}
	// Cloudflare overwrites CF-Connecting-IP. Require its accompanying CF-Ray
	// as a cheap signal that the header came from Cloudflare rather than a
	// generic proxy that copied a client-supplied value.
	if r.Header.Get("CF-Ray") != "" {
		if address := singleProxyIP(r.Header.Get("CF-Connecting-IP")); address != "" {
			return address
		}
	}
	if address := forwardedProxyIP(r.Header.Values("X-Forwarded-For")); address != "" {
		return address
	}
	if address := singleProxyIP(r.Header.Get("X-Real-IP")); address != "" {
		return address
	}
	return remote
}

func remoteProxyIP(remote string) string {
	remote = strings.TrimSpace(remote)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		if address := normalizeProxyIP(host); address != "" {
			return address
		}
		return host
	}
	if address := normalizeProxyIP(remote); address != "" {
		return address
	}
	return remote
}

func ipInPrefixes(raw string, prefixes []netip.Prefix) bool {
	address, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	address = address.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func singleProxyIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, ",") {
		return ""
	}
	return normalizeProxyIP(raw)
}

// forwardedProxyIP returns the rightmost valid address in X-Forwarded-For.
// Proxies conventionally append the peer that connected to them, so the
// rightmost entry is the one the trusted proxy actually saw.
func forwardedProxyIP(values []string) string {
	for i := len(values) - 1; i >= 0; i-- {
		parts := strings.Split(values[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			part := strings.TrimSpace(parts[j])
			if part == "" {
				continue
			}
			if address := normalizeProxyIP(part); address != "" {
				return address
			}
		}
	}
	return ""
}

func normalizeProxyIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(raw, "[]")
	address, err := netip.ParseAddr(raw)
	if err != nil || address.Zone() != "" {
		return ""
	}
	return address.Unmap().String()
}
