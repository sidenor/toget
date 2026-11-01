package main

import "testing"

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
