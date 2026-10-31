package update

import (
	"net/url"
	"testing"
)

func TestValidateReference(t *testing.T) {
	valid := []string{
		"manifests/v1.2.3.json",
		"daemon/linux-amd64/v1.2.3/toget.tar.gz",
		"sbom/toget.spdx.json",
		"https://releases.toget.dev/manifests/v1.2.3.json",
		"http://127.0.0.1:8080/channels/stable.json",
		"http://localhost:8080/channels/stable.json",
	}
	for _, raw := range valid {
		if err := validateReference(raw); err != nil {
			t.Errorf("validateReference(%q): %v", raw, err)
		}
	}
	invalid := []string{
		"", " ", "../evil", "a/../../evil", "/absolute", "//example.com/x",
		"http://example.com/x", "ftp://example.com/x", "https://user@example.com/x",
		"https://example.com/x#fragment", "https://example.com/../x",
		`daemon\linux\v1\toget.tar.gz`, "daemon//toget.tar.gz", "https://",
	}
	for _, raw := range invalid {
		if err := validateReference(raw); err == nil {
			t.Errorf("validateReference(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestResolveReference(t *testing.T) {
	base, _ := url.Parse("https://releases.toget.dev/channels/stable.json")
	resolved, err := resolveReference(base, "manifests/v1.2.3.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolved.String(), "https://releases.toget.dev/channels/manifests/v1.2.3.json"; got != want {
		t.Fatalf("resolved = %q, want %q", got, want)
	}
	resolved, err = resolveReference(base, "https://example.com/x.json")
	if err != nil || resolved.String() != "https://example.com/x.json" {
		t.Fatalf("absolute resolve = %v, %v", resolved, err)
	}
	loopback, _ := url.Parse("http://127.0.0.1:9000/channels/stable.json")
	resolved, err = resolveReference(loopback, "manifest.json")
	if err != nil || resolved.String() != "http://127.0.0.1:9000/channels/manifest.json" {
		t.Fatalf("loopback resolve = %v, %v", resolved, err)
	}
	external, _ := url.Parse("http://example.com/channels/stable.json")
	if _, err := resolveReference(external, "manifest.json"); err == nil {
		t.Fatal("non-loopback HTTP base was accepted")
	}
	if _, err := resolveReference(base, "http://example.com/x"); err == nil {
		t.Fatal("external HTTP artifact URL was accepted")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"localhost", "LOCALHOST", "127.0.0.1", "127.0.0.2", "::1"} {
		if !isLoopbackHost(host) {
			t.Errorf("%q should be loopback", host)
		}
	}
	for _, host := range []string{"", "example.com", "localhost.example.com", "10.0.0.1", "::2"} {
		if isLoopbackHost(host) {
			t.Errorf("%q should not be loopback", host)
		}
	}
}
