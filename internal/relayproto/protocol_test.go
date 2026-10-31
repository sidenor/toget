package relayproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestAddressRoundTripAndRelayFallback(t *testing.T) {
	fingerprint := strings.Repeat("ab", 32)
	direct := "https://192.168.1.5:7717#fp=" + fingerprint
	route, err := NodeRouteURL("https://relay.example:7719", fingerprint, "tog1:s:ABC")
	if err != nil {
		t.Fatal(err)
	}
	address := JoinAddress(direct, route)
	gotDirect, gotRelay := SplitAddress(address)
	if gotDirect != address || gotRelay != route {
		t.Fatalf("split = %q, %q", gotDirect, gotRelay)
	}
	// Parsing the direct part still yields the LAN endpoint and pin even
	// with the relay parameter present.
	if !strings.HasPrefix(gotDirect, direct+"&") {
		t.Fatalf("direct part = %q", gotDirect)
	}
	base, fp, target, err := ParseNodeRoute(gotRelay)
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://relay.example:7719" || fp != fingerprint || target != "tog1:s:ABC" {
		t.Fatalf("route = %q %q %q", base, fp, target)
	}
	if _, err := ParseFingerprint("sha256:" + strings.ToUpper(fingerprint)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"http://relay.example:7719#fp=" + fingerprint,
		"https://relay.example:7719/other#fp=" + fingerprint,
		"https://relay.example:7719#fp=short",
		"https://relay.example:7719/relay/v1/node/a/b#fp=" + fingerprint,
	} {
		if _, _, _, err := ParseNodeRoute(bad); err == nil {
			t.Fatalf("ParseNodeRoute(%q) succeeded", bad)
		}
	}
}

func TestEnvelopeValidation(t *testing.T) {
	good := Envelope{
		Protocol: Protocol, Target: "tog1:s:ABC", Method: "POST",
		Path: "/toget/v1/rooms/sync", Body: EncodeBody([]byte("{}")),
		Headers: map[string]string{"Content-Type": "application/json"},
	}
	if err := ValidateEnvelope(&good); err != nil {
		t.Fatal(err)
	}
	cases := []Envelope{
		{Target: "", Method: "GET", Path: "/toget/v1/health"},
		{Target: "x", Method: "DELETE", Path: "/toget/v1/health"},
		{Target: "x", Method: "GET", Path: "/api/v1/health"},
		{Target: "x", Method: "GET", Path: "/toget/v1/health", Headers: map[string]string{"X-Forwarded-For": "1.2.3.4"}},
		{Target: "x", Method: "GET", Path: "/toget/v1/health", Hops: []string{"a", "a"}},
		{Target: "x", Method: "GET", Path: "/toget/v1/health", Hops: make([]string, MaxHops+1)},
	}
	for i := range cases {
		if err := ValidateEnvelope(&cases[i]); err == nil {
			t.Fatalf("case %d passed validation", i)
		}
	}
}

func TestRegistrationSignatureAndRelayID(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	req := RegisterRequest{
		Kind: "relay", PublicKey: base64.RawStdEncoding.EncodeToString(pub),
	}
	if err := SignRegistration(&req, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRegistration(req, pub, time.Now(), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(RelayID(pub), RelayIDPrefix) {
		t.Fatalf("relay id = %q", RelayID(pub))
	}
	tampered := req
	tampered.PublicKey = base64.RawStdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	if err := VerifyRegistration(tampered, pub, time.Now(), 5*time.Minute); err == nil {
		t.Fatal("tampered registration verified")
	}
	if err := VerifyRegistration(req, pub, time.Now().Add(10*time.Minute), 5*time.Minute); err == nil {
		t.Fatal("stale registration verified")
	}
}

func TestBodyLimits(t *testing.T) {
	if _, err := DecodeBody("not base64"); err == nil {
		t.Fatal("invalid base64 accepted")
	}
	tooLarge := strings.Repeat("A", base64.StdEncoding.EncodedLen(MaxInnerBody)+1)
	if _, err := DecodeBody(tooLarge); err == nil {
		t.Fatal("oversized body accepted")
	}
	payload := []byte("hello relay")
	if got, err := DecodeBody(EncodeBody(payload)); err != nil || string(got) != string(payload) {
		t.Fatalf("round trip = %q %v", got, err)
	}
}
