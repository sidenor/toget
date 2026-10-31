package directoryproto

import (
	"encoding/json"
	"testing"
)

func TestValidatePublish(t *testing.T) {
	valid := PublishRequest{
		Protocol:         Protocol,
		IdentityManifest: json.RawMessage(`{"server_id":"s"}`),
		NodeManifest:     json.RawMessage(`{"server_id":"s","endpoint":"https://example.test:7717"}`),
	}
	if err := ValidatePublish(&valid); err != nil {
		t.Fatal(err)
	}
	cases := []PublishRequest{
		{Protocol: 0, IdentityManifest: valid.IdentityManifest, NodeManifest: valid.NodeManifest},
		{Protocol: Protocol, NodeManifest: valid.NodeManifest},
		{Protocol: Protocol, IdentityManifest: valid.IdentityManifest},
		{Protocol: Protocol, IdentityManifest: make([]byte, MaxManifest+1), NodeManifest: valid.NodeManifest},
	}
	for i := range cases {
		if err := ValidatePublish(&cases[i]); err == nil {
			t.Fatalf("case %d passed validation", i)
		}
	}
	if err := ValidatePublish(nil); err == nil {
		t.Fatal("nil publish request passed validation")
	}
}
