package hosted

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// VaultPath is the opaque, account-encrypted browser-state vault. The host
// stores and versions the blob but cannot open it: it is sealed with a key
// derived from the account's password-unlocked master key.
const (
	VaultPath = "/host/v1/account/vault"
	// MaxVaultSize bounds the sealed vault JSON.
	MaxVaultSize = 4 << 20
)

var (
	// ErrVaultConflict means a vault write used a stale base version.
	ErrVaultConflict = errors.New("vault version conflict")
	// ErrVaultNotFound means the account has not stored a vault yet.
	ErrVaultNotFound = errors.New("vault not found")
)

// VaultResponse is the account's current sealed vault, if any.
type VaultResponse struct {
	Protocol  int         `json:"protocol"`
	Blob      *SealedBlob `json:"blob,omitempty"`
	Version   int64       `json:"version"`
	UpdatedAt time.Time   `json:"updated_at,omitempty"`
}

// VaultPutRequest replaces the vault when BaseVersion is current.
type VaultPutRequest struct {
	Protocol    int        `json:"protocol"`
	Blob        SealedBlob `json:"blob"`
	BaseVersion int64      `json:"base_version"`
}

// VaultPutResponse reports the new vault version.
type VaultPutResponse struct {
	Protocol  int       `json:"protocol"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Vault fetches the sealed browser vault. A missing vault is not an error;
// Version is zero and Blob is nil.
func (c *Client) Vault(ctx context.Context, sessionToken string) (*VaultResponse, error) {
	if sessionToken == "" {
		return nil, ErrSessionInvalid
	}
	var response VaultResponse
	if err := c.do(ctx, http.MethodGet, VaultPath, sessionToken, nil, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: vault response is inconsistent", ErrInvalidHost)
	}
	return &response, nil
}

// PutVault conditionally replaces the vault. A stale BaseVersion returns an
// error containing ErrVaultConflict so the caller can merge and retry.
func (c *Client) PutVault(ctx context.Context, sessionToken string, blob SealedBlob, baseVersion int64) (*VaultPutResponse, error) {
	if sessionToken == "" {
		return nil, ErrSessionInvalid
	}
	var response VaultPutResponse
	if err := c.do(ctx, http.MethodPut, VaultPath, sessionToken, VaultPutRequest{
		Protocol: Protocol, Blob: blob, BaseVersion: baseVersion,
	}, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol || response.Version < 1 {
		return nil, fmt.Errorf("%w: vault response is inconsistent", ErrInvalidHost)
	}
	return &response, nil
}

func validVaultBlob(blob SealedBlob) bool {
	return blob.Version == BlobVersion && blob.Algorithm == BlobAlgorithmXChaCha20Poly1305 &&
		len(blob.Nonce) > 0 && len(blob.Nonce) <= 64 && len(blob.Ciphertext) > 0
}
