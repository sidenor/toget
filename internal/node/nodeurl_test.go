package node_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/relayproto"
)

func TestValidateNodeURL(t *testing.T) {
	fingerprint := strings.Repeat("ab", 32)
	direct := "https://127.0.0.1:7717#fp=" + fingerprint
	relay := "https://relay.example:8443/relay/v1/node/" +
		url.PathEscape("tog1:s:ABCDEFGHIJKLMNOPQRSTUVWXYZ") + "#fp=" + fingerprint

	valid := []string{
		"",
		direct,
		relayproto.JoinAddress(direct, relay),
	}
	for _, raw := range valid {
		if err := node.ValidateNodeURL(raw); err != nil {
			t.Errorf("ValidateNodeURL(%q) = %v, want nil", raw, err)
		}
	}

	invalid := []string{
		"http://127.0.0.1:7717#fp=" + fingerprint,
		"https://127.0.0.1:7717",
		"https://user:pass@127.0.0.1:7717#fp=" + fingerprint,
		direct + "&relay=not-a-relay",
		relayproto.JoinAddress(direct, "https://relay.example/relay/v1/node/target"),
	}
	for _, raw := range invalid {
		if err := node.ValidateNodeURL(raw); err == nil {
			t.Errorf("ValidateNodeURL(%q) = nil, want an error", raw)
		}
	}
}
