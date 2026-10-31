package node

import (
	"crypto/ed25519"
	"net/http"
	"time"

	"github.com/sidenor/toget/internal/requestauth"
)

const (
	// HeaderTimestamp carries the Unix seconds at signing time.
	HeaderTimestamp = requestauth.HeaderTimestamp
	// HeaderNonce carries a single-use request nonce.
	HeaderNonce = requestauth.HeaderNonce
	// HeaderSignature carries the Ed25519 request signature.
	HeaderSignature = requestauth.HeaderSignature
	// HeaderAccount identifies the calling account.
	HeaderAccount = requestauth.HeaderAccount
	// HeaderDevice identifies the calling device.
	HeaderDevice = requestauth.HeaderDevice
)

var (
	// ErrRequestUnauthorized means a node request signature is missing or bad.
	ErrRequestUnauthorized = requestauth.ErrUnauthorized
	// ErrRequestReplay means a nonce was already used.
	ErrRequestReplay = requestauth.ErrReplay
)

// SignRequest signs a request body and adds the authentication headers.
func SignRequest(req *http.Request, body []byte, key ed25519.PrivateKey, now time.Time) error {
	return requestauth.Sign(req, body, key, now)
}

// NonceCache remembers signed request nonces for a bounded time.
type NonceCache = requestauth.NonceCache

// NewNonceCache creates a replay cache.
func NewNonceCache() *NonceCache { return requestauth.NewNonceCache() }

// VerifyRequest checks timestamp, signature, and nonce uniqueness. The cache
// is only updated after the signature verifies.
func VerifyRequest(r *http.Request, body []byte, publicKey ed25519.PublicKey, nonces *NonceCache, now time.Time) error {
	return requestauth.Verify(r, body, publicKey, nonces, now)
}

// ConstantTimeEqual is a small helper for comparing encoded identity values.
func ConstantTimeEqual(a, b string) bool { return requestauth.ConstantTimeEqual(a, b) }
