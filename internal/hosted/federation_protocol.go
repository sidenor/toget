package hosted

import (
	"encoding/json"
	"time"
)

const (
	// FederationAccountPath lets one account host ask another for the signed
	// public record of a local account, including its active devices. The
	// caller is authenticated as a server, not as an account.
	FederationAccountPath = "/host/v1/federation/accounts/lookup"
	// FederationFriendRequestPath receives a sealed friend request from the
	// sender's account host.
	FederationFriendRequestPath = "/host/v1/federation/friends/requests"
	// FederationFriendRequestCancelPath withdraws a previously delivered
	// request. It is best effort; an unreachable recipient can let the
	// request expire instead.
	FederationFriendRequestCancelPath = "/host/v1/federation/friends/requests/cancel"
	// FederationFriendRequestDeclinePath tells the sender's host that the
	// recipient declined, so its outgoing list does not show the request until
	// it expires.
	FederationFriendRequestDeclinePath = "/host/v1/federation/friends/requests/decline"
)

// FederationEnvelope carries a self-certifying server manifest and the
// request body. The whole envelope is signed with the server root key, so the
// manifest and the request cannot be separated in transit.
type FederationEnvelope struct {
	Protocol int             `json:"protocol"`
	Manifest json.RawMessage `json:"manifest"`
	Request  json.RawMessage `json:"request"`
}

// FederationAccountRequest asks one host for an account it owns.
type FederationAccountRequest struct {
	Protocol  int    `json:"protocol"`
	AccountID string `json:"account_id"`
}

// FederatedDevice is one active device in a host-signed federated account
// record. Certificate is the account-signed device certificate.
type FederatedDevice struct {
	ID                  string `json:"id"`
	SigningPublicKey    []byte `json:"signing_public_key"`
	EncryptionPublicKey []byte `json:"encryption_public_key"`
	Certificate         []byte `json:"device_certificate"`
}

// FederatedAccount is a host-signed statement about one local account. It is
// only returned to another authenticated server; it is never a public lookup.
type FederatedAccount struct {
	Protocol      int               `json:"protocol"`
	AccountID     string            `json:"account_id"`
	Name          string            `json:"name"`
	HostID        string            `json:"host_id"`
	HostCode      string            `json:"host_code"`
	HostURL       string            `json:"host_url"`
	HostPublicKey []byte            `json:"host_public_key"`
	PublicKey     []byte            `json:"public_key"`
	KeyGeneration int               `json:"key_generation"`
	KeyRotations  []string          `json:"key_rotations,omitempty"`
	Devices       []FederatedDevice `json:"devices"`
	IssuedAt      time.Time         `json:"issued_at"`
	ExpiresAt     time.Time         `json:"expires_at"`
	Signature     string            `json:"signature,omitempty"`
}

// FederatedFriendRequest is one friend request delivered from the sender's
// host. The room lives on the sender's host; the recipient host only stores
// the sealed invite in its own inbox.
type FederatedFriendRequest struct {
	Protocol      int                 `json:"protocol"`
	ID            string              `json:"id"`
	FromAccountID string              `json:"from_account_id"`
	FromName      string              `json:"from_name"`
	FromHostID    string              `json:"from_host_id"`
	FromHostURL   string              `json:"from_host_url"`
	ToAccountID   string              `json:"to_account_id"`
	RoomID        string              `json:"room_id"`
	RoomHostID    string              `json:"room_host_id"`
	RoomHostURL   string              `json:"room_host_url"`
	Seals         []FriendRequestSeal `json:"seals"`
	CreatedAt     time.Time           `json:"created_at"`
	ExpiresAt     time.Time           `json:"expires_at"`
}

// FederatedFriendRequestAck confirms that the recipient host stored a
// request in its inbox.
type FederatedFriendRequestAck struct {
	Protocol  int       `json:"protocol"`
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// FederatedFriendRequestCancel withdraws a request by its stable identity.
type FederatedFriendRequestCancel struct {
	Protocol      int    `json:"protocol"`
	ID            string `json:"id"`
	FromAccountID string `json:"from_account_id"`
	ToAccountID   string `json:"to_account_id"`
}

// FederatedFriendRequestCancelResponse reports whether the recipient host
// removed the inbox row.
type FederatedFriendRequestCancelResponse struct {
	Protocol int  `json:"protocol"`
	Deleted  bool `json:"deleted"`
}

// FederatedFriendRequestDecline tells the sender's host that the recipient
// declined a delivered request.
type FederatedFriendRequestDecline struct {
	Protocol      int    `json:"protocol"`
	ID            string `json:"id"`
	FromAccountID string `json:"from_account_id"`
	ToAccountID   string `json:"to_account_id"`
}
