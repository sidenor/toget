package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testHostServer(t *testing.T) *Server {
	t.Helper()
	server, err := NewServer([]Endpoint{{Transport: "https", Addr: "host.example:443"}})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestHostManifestRoundTrip(t *testing.T) {
	server := testHostServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	data, err := SignHostManifest(server, "Test Host", "https://host.example/", "", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := VerifyHostManifest(data, server.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ServerID != server.ID || manifest.ServerCode != server.Code {
		t.Fatalf("manifest server = %s/%s, want %s/%s", manifest.ServerID, manifest.ServerCode, server.ID, server.Code)
	}
	if manifest.URL != "https://host.example" || manifest.Name != "Test Host" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if !manifest.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expiry = %s", manifest.ExpiresAt)
	}
}

func TestHostManifestRejectsTampering(t *testing.T) {
	server := testHostServer(t)
	now := time.Now().UTC()
	data, err := SignHostManifest(server, "Test Host", "https://host.example", "", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["url"] = "https://attacker.example"
	tampered, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHostManifest(tampered, server.PublicKey); err == nil {
		t.Fatal("tampered host manifest verified")
	}

	other, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHostManifest(data, other.PublicKey); err == nil {
		t.Fatal("host manifest verified under another server key")
	}
	if _, err := VerifyHostManifest(data, ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))); err == nil {
		t.Fatal("host manifest verified under an invalid key")
	}
}

func TestHostManifestExpiryAndInputs(t *testing.T) {
	server := testHostServer(t)
	if _, err := SignHostManifest(server, "Test", "http://host.example", "", time.Now(), time.Hour); err == nil {
		t.Fatal("plain HTTP host manifest URL was accepted")
	}
	if _, err := SignHostManifest(server, "Test", "https://host.example/path", "", time.Now(), time.Hour); err == nil {
		t.Fatal("host manifest with a path was accepted")
	}
	if _, err := SignHostManifest(server, "Test", "https://host.example", "abcd", time.Now(), time.Hour); err == nil {
		t.Fatal("invalid TLS fingerprint was accepted")
	}
	if _, err := SignHostManifest(server, "Test", "https://host.example", "", time.Now(), 0); err == nil {
		t.Fatal("zero host manifest lifetime was accepted")
	}

	// A correctly signed but expired statement must still fail freshness.
	now := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	data, err := SignHostManifest(server, "Test", "https://host.example", "", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHostManifest(data, server.PublicKey); err == nil {
		t.Fatal("expired host manifest verified")
	}
}

func TestHostManifestRejectsWrongNonce(t *testing.T) {
	server := testHostServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	data, err := SignHostManifest(server, "Test", "https://host.example", "", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var manifest HostManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	otherNonce := make([]byte, 16)
	otherNonce[0] = 1
	manifest.ServerNonce = base64.RawStdEncoding.EncodeToString(otherNonce)
	// Re-sign the payload so only the self-certifying nonce check can fail.
	payload, err := hostManifestBytes(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(server.PrivateKey, payload))
	tampered, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHostManifest(tampered, server.PublicKey); err == nil ||
		!strings.Contains(err.Error(), "code") {
		t.Fatalf("wrong nonce error = %v", err)
	}
}
