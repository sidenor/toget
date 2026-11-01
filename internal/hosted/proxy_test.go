package hosted

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestParseTrustedProxies(t *testing.T) {
	prefixes, err := ParseTrustedProxies([]string{
		"10.0.0.0/8", "127.0.0.1", "192.0.2.9/24, 2001:db8::/32",
		"10.0.0.0/8", "", "  10.1.2.3  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) != 5 {
		t.Fatalf("prefixes = %v", prefixes)
	}
	if !prefixes[0].Contains(netip.MustParseAddr("10.9.8.7")) {
		t.Fatalf("10/8 missing: %v", prefixes)
	}
	if !prefixes[1].Contains(netip.MustParseAddr("127.0.0.1")) {
		t.Fatalf("bare IPv4 not converted: %v", prefixes)
	}
	if prefixes[2].String() != "192.0.2.0/24" {
		t.Fatalf("prefix not masked: %v", prefixes[2])
	}
	if !prefixes[3].Contains(netip.MustParseAddr("2001:db8::1")) {
		t.Fatalf("IPv6 prefix missing: %v", prefixes)
	}
	for _, invalid := range []string{"not-an-ip", "10.0.0.0/33", "2001:db8::/129", "1.2.3.4/"} {
		if _, err := ParseTrustedProxies([]string{invalid}); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestCloudflareProxyPrefixes(t *testing.T) {
	prefixes := CloudflareProxyPrefixes()
	if len(prefixes) < 20 {
		t.Fatalf("Cloudflare prefixes = %d", len(prefixes))
	}
	has4, has6 := false, false
	for _, prefix := range prefixes {
		if !prefix.IsValid() {
			t.Fatalf("invalid prefix %v", prefix)
		}
		if prefix.Addr().Is4() {
			has4 = true
		} else {
			has6 = true
		}
	}
	if !has4 || !has6 {
		t.Fatalf("Cloudflare prefixes lack a family: %v", prefixes)
	}
	if !ipInPrefixes("104.16.1.2", prefixes) {
		t.Fatal("known Cloudflare IPv4 is missing")
	}
	if !ipInPrefixes("2606:4700::1", prefixes) {
		t.Fatal("known Cloudflare IPv6 is missing")
	}
}

func TestClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	trusted := &Service{trustedProxies: mustPrefixes(t, "10.0.0.0/8", "2001:db8::/32")}
	direct := &Service{}
	request := func(remote string, headers map[string]string, xff ...string) *http.Request {
		r := &http.Request{RemoteAddr: remote, Header: make(http.Header)}
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		for _, value := range xff {
			r.Header.Add("X-Forwarded-For", value)
		}
		return r
	}

	cases := []struct {
		name    string
		service *Service
		request *http.Request
		want    string
	}{
		{
			name: "direct peer", service: direct,
			request: request("198.51.100.5:1234", map[string]string{
				"CF-Connecting-IP": "203.0.113.7", "CF-Ray": "ray",
				"X-Forwarded-For": "203.0.113.8",
			}),
			want: "198.51.100.5",
		},
		{
			name: "untrusted peer spoofs headers", service: trusted,
			request: request("198.51.100.5:1234", map[string]string{
				"CF-Connecting-IP": "203.0.113.7", "CF-Ray": "ray",
			}),
			want: "198.51.100.5",
		},
		{
			name: "trusted Cloudflare peer", service: trusted,
			request: request("10.1.2.3:443", map[string]string{
				"CF-Connecting-IP": "203.0.113.7", "CF-Ray": "ray",
			}),
			want: "203.0.113.7",
		},
		{
			name: "Cloudflare header without ray falls back to XFF", service: trusted,
			request: request("10.1.2.3:443", map[string]string{
				"CF-Connecting-IP": "203.0.113.7",
			}, "192.0.2.1, 198.51.100.9"),
			want: "198.51.100.9",
		},
		{
			name: "rightmost XFF wins across header values", service: trusted,
			request: request("10.1.2.3:443", map[string]string{
				"X-Real-IP": "198.51.100.20",
			}, "192.0.2.1", "198.51.100.10, 203.0.113.9"),
			want: "203.0.113.9",
		},
		{
			name: "mapped IPv4 is normalized", service: trusted,
			request: request("10.1.2.3:443", map[string]string{
				"CF-Connecting-IP": "::ffff:203.0.113.7", "CF-Ray": "ray",
			}),
			want: "203.0.113.7",
		},
		{
			name: "IPv6 trusted peer", service: trusted,
			request: request("[2001:db8::1]:443", map[string]string{
				"CF-Connecting-IP": "2001:db8:ffff::7", "CF-Ray": "ray",
			}),
			want: "2001:db8:ffff::7",
		},
		{
			name: "X-Real-IP fallback", service: trusted,
			request: request("10.1.2.3:443", map[string]string{
				"X-Real-IP": "198.51.100.20",
			}),
			want: "198.51.100.20",
		},
		{
			name: "invalid forwarded values fall back to peer", service: trusted,
			request: request("10.1.2.3:443", map[string]string{
				"CF-Connecting-IP": "unknown", "CF-Ray": "ray",
				"X-Forwarded-For": "unknown",
			}),
			want: "10.1.2.3",
		},
		{
			name: "XFF with port is normalized", service: trusted,
			request: request("10.1.2.3:443", nil,
				"198.51.100.9:443"),
			want: "198.51.100.9",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := test.service.clientIP(test.request); got != test.want {
				t.Fatalf("clientIP = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLimitedKeysOnForwardedClientIP(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	service := &Service{
		rateLimited: true, trustedProxies: mustPrefixes(t, "10.0.0.0/8"),
		limiter: newRateLimiter(), now: func() time.Time { return now },
	}
	request := func(address string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/host/v1/register/begin", nil)
		r.RemoteAddr = "10.1.2.3:443"
		r.Header.Set("CF-Connecting-IP", address)
		r.Header.Set("CF-Ray", "ray")
		return r
	}
	alice := request("203.0.113.7")
	if service.limited(httptest.NewRecorder(), alice, "register", 1) {
		t.Fatal("first request was limited")
	}
	if !service.limited(httptest.NewRecorder(), alice, "register", 1) {
		t.Fatal("second request from the same client was not limited")
	}
	if service.limited(httptest.NewRecorder(), request("203.0.113.8"), "register", 1) {
		t.Fatal("a different forwarded client shared the bucket")
	}
}

func mustPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()
	prefixes, err := ParseTrustedProxies(values)
	if err != nil {
		t.Fatal(err)
	}
	return prefixes
}
