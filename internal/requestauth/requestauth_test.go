package requestauth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/requestauth"
)

func keyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestSignVerify(t *testing.T) {
	pub, priv := keyPair(t)
	body := []byte(`{"hello":"world"}`)
	req, err := http.NewRequest(http.MethodPost, "https://node.example:7717/toget/v1/account/sync?x=1", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := requestauth.Sign(req, body, priv, now); err != nil {
		t.Fatal(err)
	}
	if err := requestauth.Verify(req, body, pub, requestauth.NewNonceCache(), now); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	pub, priv := keyPair(t)
	now := time.Now()
	cases := []struct {
		name    string
		method  string
		uri     string
		body    []byte
		verify  []byte
		skew    time.Duration
		replay  bool
		wantErr bool
	}{
		{name: "body", method: http.MethodPost, uri: "https://n/x", body: []byte("a"), verify: []byte("b"), wantErr: true},
		{name: "method", method: http.MethodPost, uri: "https://n/x", body: []byte("a"), wantErr: true},
		{name: "uri", method: http.MethodPost, uri: "https://n/x", body: []byte("a"), wantErr: true},
		{name: "stale", method: http.MethodGet, uri: "https://n/x", skew: -10 * time.Minute, wantErr: true},
		{name: "future", method: http.MethodGet, uri: "https://n/x", skew: 10 * time.Minute, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, tc.uri, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := requestauth.Sign(req, tc.body, priv, now.Add(tc.skew)); err != nil {
				t.Fatal(err)
			}
			if tc.name == "method" {
				req.Method = http.MethodPut
			}
			if tc.name == "uri" {
				req.URL.Path = "/y"
			}
			verifyBody := tc.body
			if tc.verify != nil {
				verifyBody = tc.verify
			}
			err = requestauth.Verify(req, verifyBody, pub, requestauth.NewNonceCache(), now)
			if tc.wantErr && err == nil {
				t.Fatal("expected verification failure")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("verify: %v", err)
			}
		})
	}
}

func TestVerifyRejectsReplay(t *testing.T) {
	pub, priv := keyPair(t)
	now := time.Now()
	req, err := http.NewRequest(http.MethodPost, "https://n/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := requestauth.Sign(req, nil, priv, now); err != nil {
		t.Fatal(err)
	}
	cache := requestauth.NewNonceCache()
	if err := requestauth.Verify(req, nil, pub, cache, now); err != nil {
		t.Fatal(err)
	}
	if err := requestauth.Verify(req, nil, pub, cache, now); !errors.Is(err, requestauth.ErrReplay) {
		t.Fatalf("second verify = %v, want replay", err)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	_, priv := keyPair(t)
	otherPub, _ := keyPair(t)
	now := time.Now()
	req, err := http.NewRequest(http.MethodPost, "https://n/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := requestauth.Sign(req, nil, priv, now); err != nil {
		t.Fatal(err)
	}
	if err := requestauth.Verify(req, nil, otherPub, nil, now); !errors.Is(err, requestauth.ErrUnauthorized) {
		t.Fatalf("verify = %v, want unauthorized", err)
	}
}

func TestCanonicalBindsBodyHash(t *testing.T) {
	a := requestauth.Canonical(http.MethodPost, "/x", "1", "nonce-1234567890", []byte("a"))
	b := requestauth.Canonical(http.MethodPost, "/x", "1", "nonce-1234567890", []byte("b"))
	if string(a) == string(b) {
		t.Fatal("canonical form does not bind the body")
	}
	if !strings.Contains(string(a), "toget-node-request-v1") {
		t.Fatal("canonical form is missing its domain separator")
	}
}
