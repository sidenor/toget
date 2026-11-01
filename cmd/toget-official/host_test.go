package main

import "testing"

func TestAccountHostDirectAddress(t *testing.T) {
	cases := []struct {
		listen  string
		direct  string
		want    string
		wantErr bool
	}{
		{listen: "127.0.0.1:7722", want: "127.0.0.1:7722"},
		{listen: "[::1]:7722", want: "[::1]:7722"},
		{listen: "0.0.0.0:7722", direct: "accounts.example.com:443", want: "accounts.example.com:443"},
		{listen: "127.0.0.1:7722", direct: "43.134.184.155:443", want: "43.134.184.155:443"},
		{listen: "127.0.0.1:7722", direct: "[2001:db8::1]:443", want: "[2001:db8::1]:443"},
		{listen: "127.0.0.1:7722", direct: "https://accounts.example.com:443", wantErr: true},
		{listen: "127.0.0.1:7722", direct: "accounts.example.com", wantErr: true},
		{listen: "127.0.0.1:7722", direct: "accounts.example.com:0", wantErr: true},
		{listen: "127.0.0.1:7722", direct: "accounts.example.com:65536", wantErr: true},
		{listen: "127.0.0.1:7722", direct: "accounts.example.com:https", wantErr: true},
		{listen: "127.0.0.1:7722", direct: "accounts.example.com:+443", wantErr: true},
		{listen: "127.0.0.1:7722", direct: "accounts.example.com:0443", want: "accounts.example.com:443"},
		{listen: "127.0.0.1:7722", direct: "0.0.0.0:443", wantErr: true},
		{listen: "127.0.0.1:7722", direct: "[::]:443", wantErr: true},
		{listen: "127.0.0.1:7722", direct: ":443", wantErr: true},
		{listen: "127.0.0.1:7722", direct: "accounts.example.com/path:443", wantErr: true},
		{listen: "127.0.0.1:7722", direct: "user@accounts.example.com:443", wantErr: true},
		{listen: "not-an-address", wantErr: true},
	}
	for _, test := range cases {
		got, err := accountHostDirectAddress(test.listen, test.direct)
		if test.wantErr {
			if err == nil {
				t.Fatalf("accountHostDirectAddress(%q, %q) = %q, want error", test.listen, test.direct, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("accountHostDirectAddress(%q, %q): %v", test.listen, test.direct, err)
		}
		if got != test.want {
			t.Fatalf("accountHostDirectAddress(%q, %q) = %q, want %q", test.listen, test.direct, got, test.want)
		}
	}
}

func TestPublicUnicastIP(t *testing.T) {
	cases := map[string]bool{
		"10.3.0.13":            false,
		"127.0.0.1":            false,
		"169.254.1.1":          false,
		"0.0.0.0":              false,
		"fd00::1":              false,
		"43.134.184.155":       true,
		"2001:4860:4860::8888": true,
		"not-an-ip":            false,
	}
	for raw, want := range cases {
		if got := publicUnicastIP(raw); got != want {
			t.Fatalf("publicUnicastIP(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestAccountHostAddresses(t *testing.T) {
	cases := []struct {
		listen     string
		publicURL  string
		smtpAddr   string
		wantHost   string
		wantOrigin string
		wantErr    bool
	}{
		{
			listen:   "127.0.0.1:7722",
			wantHost: "127.0.0.1:7722",
		},
		{
			listen: "127.0.0.1:7722", publicURL: "accounts.example.com",
			wantHost: "accounts.example.com:443", wantOrigin: "https://accounts.example.com",
		},
		{
			listen: "127.0.0.1:7722", publicURL: "https://accounts.example.com/",
			wantHost: "accounts.example.com:443", wantOrigin: "https://accounts.example.com",
		},
		{
			listen: "127.0.0.1:7722", publicURL: "https://accounts.example.com:8443",
			smtpAddr: "smtp.example.com:587",
			wantHost: "accounts.example.com:8443", wantOrigin: "https://accounts.example.com:8443",
		},
		{
			listen: "127.0.0.1:7722", smtpAddr: "smtp.example.com:587",
			wantErr: true,
		},
		{
			listen: "127.0.0.1:7722", publicURL: "  ", smtpAddr: "smtp.example.com:587",
			wantErr: true,
		},
		{
			listen: "127.0.0.1:7722", publicURL: "https://accounts.example.com/path",
			smtpAddr: "smtp.example.com:587",
			wantErr:  true,
		},
		{
			listen: "127.0.0.1:7722", smtpAddr: "  ",
			wantHost: "127.0.0.1:7722",
		},
	}
	for _, test := range cases {
		host, origin, err := accountHostAddresses(test.listen, test.publicURL, test.smtpAddr)
		if test.wantErr {
			if err == nil {
				t.Fatalf("accountHostAddresses(%q, %q, %q) = (%q, %q), want error",
					test.listen, test.publicURL, test.smtpAddr, host, origin)
			}
			continue
		}
		if err != nil {
			t.Fatalf("accountHostAddresses(%q, %q, %q): %v", test.listen, test.publicURL, test.smtpAddr, err)
		}
		if host != test.wantHost || origin != test.wantOrigin {
			t.Fatalf("accountHostAddresses(%q, %q, %q) = (%q, %q), want (%q, %q)",
				test.listen, test.publicURL, test.smtpAddr, host, origin, test.wantHost, test.wantOrigin)
		}
	}
}

func TestAccountHostAddress(t *testing.T) {
	cases := []struct {
		listen    string
		publicURL string
		want      string
		wantErr   bool
	}{
		{listen: "127.0.0.1:7722", want: "127.0.0.1:7722"},
		{listen: "[::1]:7722", want: "[::1]:7722"},
		{listen: "127.0.0.1:7722", publicURL: "accounts.example.com", want: "accounts.example.com:443"},
		{listen: "127.0.0.1:7722", publicURL: "https://accounts.example.com/", want: "accounts.example.com:443"},
		{listen: "127.0.0.1:7722", publicURL: "accounts.example.com:8443", want: "accounts.example.com:8443"},
		{listen: "127.0.0.1:7722", publicURL: "https://accounts.example.com:8443", want: "accounts.example.com:8443"},
		{listen: "127.0.0.1:7722", publicURL: "http://accounts.example.com", wantErr: true},
		{listen: "127.0.0.1:7722", publicURL: "https://accounts.example.com/path", wantErr: true},
		{listen: "127.0.0.1:7722", publicURL: "https://accounts.example.com?x=1", wantErr: true},
		{listen: "127.0.0.1:7722", publicURL: "https://accounts.example.com#frag", wantErr: true},
		{listen: "127.0.0.1:7722", publicURL: "https://user@accounts.example.com", wantErr: true},
	}
	for _, test := range cases {
		got, err := accountHostAddress(test.listen, test.publicURL)
		if test.wantErr {
			if err == nil {
				t.Fatalf("accountHostAddress(%q, %q) = %q, want error", test.listen, test.publicURL, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("accountHostAddress(%q, %q): %v", test.listen, test.publicURL, err)
		}
		if got != test.want {
			t.Fatalf("accountHostAddress(%q, %q) = %q, want %q", test.listen, test.publicURL, got, test.want)
		}
	}
}
