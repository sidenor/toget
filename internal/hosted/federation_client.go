//go:build !js

package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/requestauth"
)

// doFederation wraps request in a signed peer envelope. The manifest travels
// inside the signed bytes, so the receiving host authenticated by this
// envelope is exactly the peer that signed the request.
func (c *Client) doFederation(ctx context.Context, caller federationCaller, path string, request, response any) error {
	if err := c.ensureVerified(ctx); err != nil {
		return err
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return err
	}
	body, err := json.Marshal(FederationEnvelope{
		Protocol: Protocol, Manifest: caller.manifest, Request: requestJSON,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if err := requestauth.Sign(req, body, caller.key, c.now().UTC()); err != nil {
		return err
	}
	return c.execute(req, response)
}

// LookupFederatedAccount fetches a peer-signed account statement with active
// devices. The caller must already trust the peer transport; the response is
// additionally bound to the peer's self-certifying host identity.
func (c *Client) LookupFederatedAccount(ctx context.Context, caller federationCaller, accountID string) (*FederatedAccount, error) {
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
	var record FederatedAccount
	if err := c.doFederation(ctx, caller, FederationAccountPath, FederationAccountRequest{
		Protocol: Protocol, AccountID: accountID,
	}, &record); err != nil {
		return nil, err
	}
	if err := VerifyFederatedAccount(record, hostKey); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHost, err)
	}
	if record.AccountID != accountID || record.HostID != info.HostID ||
		record.HostCode != info.HostCode {
		return nil, fmt.Errorf("%w: federated account does not match the host", ErrInvalidHost)
	}
	keys, err := FederatedAccountKeys(record)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHost, err)
	}
	seen := make(map[string]bool, len(record.Devices))
	for _, device := range record.Devices {
		if seen[device.ID] {
			return nil, fmt.Errorf("%w: federated account repeats a device", ErrInvalidHost)
		}
		seen[device.ID] = true
		if _, err := VerifyFederatedDeviceCertificate(device, keys, c.now().UTC()); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidHost, err)
		}
	}
	return &record, nil
}

// PushFederatedFriendRequest delivers one sealed request to the recipient
// host. Delivery is synchronous: the sender only keeps the outgoing row after
// the recipient's host has stored it.
func (c *Client) PushFederatedFriendRequest(ctx context.Context, caller federationCaller, request FederatedFriendRequest) (*FederatedFriendRequestAck, error) {
	var ack FederatedFriendRequestAck
	if err := c.doFederation(ctx, caller, FederationFriendRequestPath, request, &ack); err != nil {
		return nil, err
	}
	if ack.Protocol != Protocol || ack.ID != request.ID || ack.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("%w: friend request acknowledgement is inconsistent", ErrInvalidHost)
	}
	return &ack, nil
}

// CancelFederatedFriendRequest withdraws a request the recipient host may
// already hold. The host reports whether it removed anything.
func (c *Client) CancelFederatedFriendRequest(ctx context.Context, caller federationCaller, request FederatedFriendRequestCancel) (*FederatedFriendRequestCancelResponse, error) {
	var response FederatedFriendRequestCancelResponse
	if err := c.doFederation(ctx, caller, FederationFriendRequestCancelPath, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: cancel response is inconsistent", ErrInvalidHost)
	}
	return &response, nil
}

// DeclineFederatedFriendRequest tells the sender's host that its request was
// declined, so the sender's outgoing list can drop it immediately.
func (c *Client) DeclineFederatedFriendRequest(ctx context.Context, caller federationCaller, request FederatedFriendRequestDecline) (*FederatedFriendRequestCancelResponse, error) {
	var response FederatedFriendRequestCancelResponse
	if err := c.doFederation(ctx, caller, FederationFriendRequestDeclinePath, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: decline response is inconsistent", ErrInvalidHost)
	}
	return &response, nil
}
