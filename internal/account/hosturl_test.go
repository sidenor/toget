package account_test

import (
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
)

func TestParseHostURLDirectAndIdentity(t *testing.T) {
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("ab", 32)
	cases := []struct {
		raw         string
		origin      string
		fingerprint string
		hostID      string
	}{
		{
			raw:    "https://accounts.example.com#fp=" + fingerprint,
			origin: "https://accounts.example.com", fingerprint: fingerprint,
		},
		{
			raw:    "https://accounts.example.com:8443/#sha256=" + strings.ToUpper(fingerprint),
			origin: "https://accounts.example.com:8443", fingerprint: fingerprint,
		},
		{
			raw:    "https://accounts.example.com#fp=sha256:" + fingerprint + "&sha256=" + fingerprint,
			origin: "https://accounts.example.com", fingerprint: fingerprint,
		},
		{
			raw:    "https://accounts.example.com#id=" + server.ID,
			origin: "https://accounts.example.com", hostID: server.ID,
		},
	}
	for _, test := range cases {
		address, err := account.ParseHostURL(test.raw)
		if err != nil {
			t.Fatalf("ParseHostURL(%q): %v", test.raw, err)
		}
		if address.Origin != test.origin || address.Fingerprint != test.fingerprint ||
			address.HostID != test.hostID {
			t.Fatalf("ParseHostURL(%q) = %+v", test.raw, address)
		}
		if !account.ValidHostURL(test.raw) {
			t.Fatalf("ValidHostURL(%q) = false", test.raw)
		}
	}
}

func TestParseHostURLRejectsAmbiguousOrUnsafeAddresses(t *testing.T) {
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("ab", 32)
	cases := []string{
		"",
		"http://accounts.example.com#fp=" + fingerprint,
		"https://accounts.example.com",
		"https://accounts.example.com#fp=",
		"https://accounts.example.com#id=",
		"https://accounts.example.com#id=not-a-host-id",
		"https://accounts.example.com#fp=" + fingerprint + "&id=" + server.ID,
		"https://accounts.example.com#fp=" + fingerprint + "&sha256=" + strings.Repeat("cd", 32),
		"https://accounts.example.com#fp=" + fingerprint + "&fp=" + fingerprint,
		"https://accounts.example.com#id=" + server.ID + "&id=" + server.ID,
		"https://user:pass@accounts.example.com#fp=" + fingerprint,
		"https://accounts.example.com/path#fp=" + fingerprint,
		"https://accounts.example.com?x=1#fp=" + fingerprint,
		"https://accounts.example.com?#fp=" + fingerprint,
		"https://accounts.example.com#relay=x&fp=" + fingerprint,
		"https://accounts.example.com#target=x&fp=" + fingerprint,
		"https://accounts.example.com#fp=" + strings.Repeat("a", 63),
		"https://accounts.example.com#fp=" + fingerprint + "&id=",
	}
	for _, raw := range cases {
		if _, err := account.ParseHostURL(raw); err == nil {
			t.Fatalf("ParseHostURL(%q) succeeded", raw)
		}
		if raw != "" && account.ValidHostURL(raw) {
			t.Fatalf("ValidHostURL(%q) = true", raw)
		}
	}
	if !account.ValidHostURL("") {
		t.Fatal("ValidHostURL(empty) should allow self-hosted accounts")
	}
	if !account.ValidHostURL("   ") {
		t.Fatal("ValidHostURL(whitespace) should trim to empty")
	}
}
