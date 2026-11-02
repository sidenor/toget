package hosted

import (
	"context"
	"errors"
	"fmt"
)

// ResolveAccount asks this host to resolve a 14-digit account ID. The request
// is signed by the caller's device, and the host performs any cross-server
// directory lookup server-to-server.
func (c *Client) ResolveAccount(ctx context.Context, signer RoomSigner, accountID string) (*ResolvedAccount, error) {
	if !signer.valid() {
		return nil, errors.New("room signer is incomplete")
	}
	var response ResolvedAccount
	if err := c.doRoom(ctx, ResolveAccountPath, signer, ResolveAccountRequest{
		Protocol: Protocol, AccountID: accountID,
	}, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol || response.AccountID != accountID || response.HostID == "" {
		return nil, fmt.Errorf("%w: malformed account resolution", ErrInvalidHost)
	}
	return &response, nil
}
