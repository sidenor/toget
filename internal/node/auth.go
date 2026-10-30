package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// HeaderTimestamp carries the Unix seconds at signing time.
	HeaderTimestamp = "X-Toget-Timestamp"
	// HeaderNonce carries a single-use request nonce.
	HeaderNonce = "X-Toget-Nonce"
	// HeaderSignature carries the Ed25519 request signature.
	HeaderSignature = "X-Toget-Signature"
	// HeaderAccount identifies the calling account.
	HeaderAccount = "X-Toget-Account"
	// HeaderDevice identifies the calling device.
	HeaderDevice = "X-Toget-Device"

	// requestSkew bounds clock difference between nodes.
	requestSkew = 5 * time.Minute
	// nonceTTL is how long a nonce is remembered.
	nonceTTL = 10 * time.Minute
	// maxNonces caps replay-cache memory.
	maxNonces = 200000
)

var (
	// ErrRequestUnauthorized means a node request signature is missing or bad.
	ErrRequestUnauthorized = errors.New("node request is not authorized")
	// ErrRequestReplay means a nonce was already used.
	ErrRequestReplay = errors.New("node request nonce was already used")
)

// SignRequest signs a request body and adds the authentication headers.
func SignRequest(req *http.Request, body []byte, key ed25519.PrivateKey, now time.Time) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("node signing key is invalid")
	}
	nonceRaw := make([]byte, 18)
	if _, err := rand.Read(nonceRaw); err != nil {
		return fmt.Errorf("generate request nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	timestamp := strconv.FormatInt(now.UTC().Unix(), 10)
	canonical := requestCanonical(req.Method, req.URL.RequestURI(), timestamp, nonce, body)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, canonical)))
	return nil
}

func requestCanonical(method, requestURI, timestamp, nonce string, body []byte) []byte {
	sum := sha256.Sum256(body)
	text := "toget-node-request-v1\n" + method + "\n" + requestURI + "\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(sum[:])
	return []byte(text)
}

// NonceCache remembers signed request nonces for a bounded time.
type NonceCache struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	now    func() time.Time
	max    int
	maxAge time.Duration
}

// NewNonceCache creates a replay cache.
func NewNonceCache() *NonceCache {
	return &NonceCache{seen: make(map[string]time.Time), now: time.Now, max: maxNonces, maxAge: nonceTTL}
}

// VerifyRequest checks timestamp, signature, and nonce uniqueness. The cache
// is only updated after the signature verifies.
func VerifyRequest(r *http.Request, body []byte, publicKey ed25519.PublicKey, nonces *NonceCache, now time.Time) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return ErrRequestUnauthorized
	}
	timestampRaw := r.Header.Get(HeaderTimestamp)
	nonce := r.Header.Get(HeaderNonce)
	signatureRaw := r.Header.Get(HeaderSignature)
	if timestampRaw == "" || nonce == "" || signatureRaw == "" {
		return ErrRequestUnauthorized
	}
	if len(nonce) < 16 || len(nonce) > 128 {
		return ErrRequestUnauthorized
	}
	timestamp, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil {
		return ErrRequestUnauthorized
	}
	signedAt := time.Unix(timestamp, 0).UTC()
	now = now.UTC()
	if signedAt.Before(now.Add(-requestSkew)) || signedAt.After(now.Add(requestSkew)) {
		return fmt.Errorf("%w: timestamp is outside the allowed window", ErrRequestUnauthorized)
	}
	signature, err := base64.RawStdEncoding.DecodeString(signatureRaw)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return ErrRequestUnauthorized
	}
	canonical := requestCanonical(r.Method, r.URL.RequestURI(), timestampRaw, nonce, body)
	if !ed25519.Verify(publicKey, canonical, signature) {
		return ErrRequestUnauthorized
	}
	if nonces != nil {
		return nonces.Check(nonce, now)
	}
	return nil
}

// Check records a nonce, returning ErrRequestReplay if it was seen recently.
func (c *NonceCache) Check(nonce string, now time.Time) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now == nil {
		c.now = time.Now
	}
	if c.maxAge <= 0 {
		c.maxAge = nonceTTL
	}
	if c.max <= 0 {
		c.max = maxNonces
	}
	if c.seen == nil {
		c.seen = make(map[string]time.Time)
	}
	// Opportunistic cleanup keeps the map bounded under steady traffic.
	if len(c.seen) >= c.max {
		cutoff := now.Add(-c.maxAge)
		for key, seenAt := range c.seen {
			if seenAt.Before(cutoff) {
				delete(c.seen, key)
			}
		}
		if len(c.seen) >= c.max {
			return errors.New("node replay cache is full")
		}
	}
	if seenAt, ok := c.seen[nonce]; ok && now.Sub(seenAt) < c.maxAge {
		return ErrRequestReplay
	}
	c.seen[nonce] = now
	return nil
}

// ConstantTimeEqual is a small helper for comparing encoded identity values.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
