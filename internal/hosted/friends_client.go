package hosted

import (
	"context"
	"fmt"
)

// CreateFriendRequest stores one sealed invite for another account on this
// host.
func (c *Client) CreateFriendRequest(ctx context.Context, signer RoomSigner, request FriendRequestCreate) (*FriendRequestCreated, error) {
	var response FriendRequestCreated
	if err := c.doRoom(ctx, FriendRequestPath, signer, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol || response.ID != request.ID || response.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("%w: malformed friend request response", ErrInvalidHost)
	}
	return &response, nil
}

// ListFriendRequests returns the current device's incoming requests.
func (c *Client) ListFriendRequests(ctx context.Context, signer RoomSigner) ([]FriendRequestSummary, error) {
	var response FriendRequestList
	if err := c.doRoom(ctx, FriendRequestListPath, signer,
		struct {
			Protocol int `json:"protocol"`
		}{Protocol: Protocol}, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: malformed friend request list", ErrInvalidHost)
	}
	return response.Requests, nil
}

// DeleteFriendRequest declines or dismisses one incoming request.
func (c *Client) DeleteFriendRequest(ctx context.Context, signer RoomSigner, id string) (bool, error) {
	var response FriendRequestDeleteResponse
	if err := c.doRoom(ctx, FriendRequestDeletePath, signer, FriendRequestDelete{
		Protocol: Protocol, ID: id,
	}, &response); err != nil {
		return false, err
	}
	if response.Protocol != Protocol {
		return false, fmt.Errorf("%w: malformed friend request deletion", ErrInvalidHost)
	}
	return response.Deleted, nil
}
