package hosted

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sidenor/toget/internal/identity"
)

const accountLookupPurpose = "toget:host-account-lookup:v1"

type accountLookupPayload struct {
	Purpose   string    `json:"purpose"`
	Protocol  int       `json:"protocol"`
	AccountID string    `json:"account_id"`
	Name      string    `json:"name"`
	HostID    string    `json:"host_id"`
	HostCode  string    `json:"host_code"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func accountLookupBytes(lookup AccountLookup) ([]byte, error) {
	return json.Marshal(accountLookupPayload{
		Purpose: accountLookupPurpose, Protocol: lookup.Protocol,
		AccountID: lookup.AccountID, Name: lookup.Name,
		HostID: lookup.HostID, HostCode: lookup.HostCode,
		IssuedAt: lookup.IssuedAt, ExpiresAt: lookup.ExpiresAt,
	})
}

// SignAccountLookup signs a public account statement with the host root key.
func SignAccountLookup(lookup AccountLookup, hostKey ed25519.PrivateKey) (AccountLookup, error) {
	if len(hostKey) != ed25519.PrivateKeySize {
		return AccountLookup{}, errors.New("host signing key is invalid")
	}
	payload, err := accountLookupBytes(lookup)
	if err != nil {
		return AccountLookup{}, err
	}
	lookup.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(hostKey, payload))
	return lookup, nil
}

// VerifyAccountLookup verifies a host-signed public account statement. The
// caller must supply the root key from the host's verified identity manifest.
func VerifyAccountLookup(lookup AccountLookup, hostKey ed25519.PublicKey) error {
	if len(hostKey) != ed25519.PublicKeySize {
		return errors.New("host root key is invalid")
	}
	if lookup.Protocol != Protocol || lookup.AccountID == "" || lookup.HostID == "" ||
		lookup.HostCode == "" || lookup.IssuedAt.IsZero() || lookup.ExpiresAt.IsZero() {
		return errors.New("account lookup is incomplete")
	}
	now := time.Now().UTC()
	if lookup.IssuedAt.After(now.Add(5*time.Minute)) || !now.Before(lookup.ExpiresAt) {
		return errors.New("account lookup is not fresh")
	}
	signature, err := base64.RawStdEncoding.DecodeString(lookup.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("account lookup signature is malformed")
	}
	payload, err := accountLookupBytes(lookup)
	if err != nil {
		return err
	}
	if !ed25519.Verify(hostKey, payload, signature) {
		return errors.New("account lookup signature is invalid")
	}
	return nil
}

// LookupAccount fetches a host-signed public account statement. The client
// must already have verified the host identity with Info.
func (c *Client) LookupAccount(ctx context.Context, accountID string) (*AccountLookup, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}
	manifest, err := identity.VerifyManifestJSON(info.Manifest)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHost, err)
	}
	hostKey, err := identity.DecodePublicKey(manifest.RootPubKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHost, err)
	}
	var lookup AccountLookup
	if err := c.do(ctx, http.MethodGet, AccountLookupPrefix+accountID, "", nil, &lookup); err != nil {
		return nil, err
	}
	if err := VerifyAccountLookup(lookup, hostKey); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHost, err)
	}
	if lookup.HostID != info.HostID || lookup.HostCode != info.HostCode ||
		lookup.AccountID != accountID {
		return nil, fmt.Errorf("%w: account lookup does not match the host", ErrInvalidHost)
	}
	return &lookup, nil
}
