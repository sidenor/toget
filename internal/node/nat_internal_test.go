package node

import (
	"strings"
	"testing"
)

func TestNewClientForURLAlternates(t *testing.T) {
	fingerprint := strings.Repeat("ab", 32)
	raw := "https://192.168.1.5:7717#fp=" + fingerprint +
		"&alt=203.0.113.9%3A7717" +
		"&alt=192.168.1.5%3A7717" +
		"&alt=203.0.113.9%3A7717" +
		"&alt=not-a-host-port"
	client, err := NewClientForURL(raw, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if len(client.directs) != 2 {
		t.Fatalf("got %d direct endpoints, want primary plus one alternate", len(client.directs))
	}
	if client.directs[0].base != "https://192.168.1.5:7717" {
		t.Fatalf("primary = %q", client.directs[0].base)
	}
	if client.directs[1].base != "https://203.0.113.9:7717" {
		t.Fatalf("alternate = %q", client.directs[1].base)
	}
	for _, direct := range client.directs {
		if direct.quic == nil {
			t.Fatalf("endpoint %q has no transport", direct.base)
		}
	}
}

func TestManifestExternalAlternate(t *testing.T) {
	fingerprint := strings.Repeat("cd", 32)
	manifest := &Manifest{TLSFingerprint: fingerprint, ExternalEndpoint: "203.0.113.9:7717"}
	endpoint, url := externalAlternate(manifest)
	if endpoint != "203.0.113.9:7717" {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if !strings.Contains(url, "https://203.0.113.9:7717#fp="+fingerprint) {
		t.Fatalf("url = %q", url)
	}
	for _, invalid := range []string{"", "https://203.0.113.9:7717", "203.0.113.9", "203.0.113.9:0", "203.0.113.9:99999"} {
		manifest.ExternalEndpoint = invalid
		if endpoint, url := externalAlternate(manifest); endpoint != "" || url != "" {
			t.Fatalf("%q was accepted as an alternate", invalid)
		}
	}
}
