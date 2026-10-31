// Package notify sends content-free Web Push notifications. Payloads contain
// only a type and a language; message text, senders, room names, and approval
// descriptions never leave the node.
package notify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"
)

const (
	vapidSetting = "notifications.vapid"
	vapidSubject = "https://toget.dev"
)

// VAPIDKeys is one P-256 application-server key pair, encoded as base64url.
type VAPIDKeys struct {
	Public  string `json:"public"`
	Private string `json:"private"`
}

type settingsStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// LoadOrCreateVAPID reads the node's push key, creating it on first use. The
// private key stays in the local database and is never sent to a browser.
func LoadOrCreateVAPID(ctx context.Context, store settingsStore) (*VAPIDKeys, error) {
	raw, err := store.GetSetting(ctx, vapidSetting)
	if err != nil {
		return nil, err
	}
	if raw != "" {
		var keys VAPIDKeys
		if err := json.Unmarshal([]byte(raw), &keys); err != nil {
			return nil, fmt.Errorf("notify: parse VAPID keys: %w", err)
		}
		if err := keys.validate(); err != nil {
			return nil, fmt.Errorf("notify: invalid VAPID keys: %w", err)
		}
		return &keys, nil
	}
	keys, err := GenerateVAPID()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	if err := store.SetSetting(ctx, vapidSetting, string(data)); err != nil {
		return nil, err
	}
	return keys, nil
}

// GenerateVAPID creates a new P-256 key pair.
func GenerateVAPID() (*VAPIDKeys, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("notify: generate VAPID key: %w", err)
	}
	pub := elliptic.Marshal(elliptic.P256(), private.X, private.Y)
	priv := private.D.FillBytes(make([]byte, 32))
	return &VAPIDKeys{
		Public:  base64.RawURLEncoding.EncodeToString(pub),
		Private: base64.RawURLEncoding.EncodeToString(priv),
	}, nil
}

func (k VAPIDKeys) validate() error {
	pub, err := base64.RawURLEncoding.DecodeString(k.Public)
	if err != nil || len(pub) != 65 || pub[0] != 4 {
		return errors.New("public key must be an uncompressed P-256 point")
	}
	priv, err := base64.RawURLEncoding.DecodeString(k.Private)
	if err != nil || len(priv) != 32 {
		return errors.New("private key must be 32 bytes")
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), pub)
	if x == nil || y == nil {
		return errors.New("public key is not on P-256")
	}
	d := new(big.Int).SetBytes(priv)
	if d.Sign() <= 0 {
		return errors.New("private key is zero")
	}
	expectedX, expectedY := elliptic.P256().ScalarBaseMult(priv)
	if expectedX.Cmp(x) != 0 || expectedY.Cmp(y) != 0 {
		return errors.New("public and private keys do not match")
	}
	return nil
}

func (k VAPIDKeys) privateKey() (*ecdsa.PrivateKey, error) {
	priv, err := base64.RawURLEncoding.DecodeString(k.Private)
	if err != nil {
		return nil, err
	}
	pub, err := base64.RawURLEncoding.DecodeString(k.Public)
	if err != nil {
		return nil, err
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), pub)
	if x == nil {
		return nil, errors.New("invalid VAPID public key")
	}
	return &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: new(big.Int).SetBytes(priv)}, nil
}

// vapidAuthorization builds the Authorization header for one push endpoint.
func vapidAuthorization(keys VAPIDKeys, endpoint string, now time.Time) (string, error) {
	aud, err := endpointAudience(endpoint)
	if err != nil {
		return "", err
	}
	private, err := keys.privateKey()
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": aud,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": vapidSubject,
	})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, private, digest[:])
	if err != nil {
		return "", fmt.Errorf("notify: sign VAPID JWT: %w", err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return "vapid t=" + unsigned + "." + base64.RawURLEncoding.EncodeToString(signature) +
		", k=" + keys.Public, nil
}
