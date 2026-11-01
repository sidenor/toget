// Package node implements the LAN transport between Toget nodes: pinned TLS,
// signed requests, invitation redemption, and home-node ordered sync.
package node

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
)

const (
	// InvitePrefix identifies a portable invite code.
	InvitePrefix = "toget-invite:v1:"
	// Protocol is the wire protocol version.
	Protocol = 1
	// MaxInviteCodeLength bounds pasted invite input.
	MaxInviteCodeLength = 64 * 1024
)

var (
	// ErrNotInviteCode means the input is a bare invite secret, not a code.
	ErrNotInviteCode = errors.New("not a Toget invite code")
	// ErrInvalidInviteCode means a code is malformed or unsupported.
	ErrInvalidInviteCode = errors.New("invalid Toget invite code")
)

// Invite kinds. An empty Kind is the historical node-hosted invite.
const (
	InviteKindNode     = "node"
	InviteKindOfficial = "official"
)

// InviteCode carries a single-use secret and everything a peer needs to reach
// the room home. The URL fragment contains the home's trust anchor: a TLS
// certificate fingerprint for a node, or either a certificate fingerprint or
// a self-certifying host ID for an official host. It is never sent over the
// wire as part of an HTTP request.
type InviteCode struct {
	Protocol int    `json:"protocol"`
	Secret   string `json:"secret"`
	URL      string `json:"url"`
	ServerID string `json:"server_id"`
	RoomID   string `json:"room_id"`
	RoomName string `json:"room_name,omitempty"`
	// HomeKind is "node" or "official". Older codes omit it.
	HomeKind  string `json:"home_kind,omitempty"`
	RoomTopic string `json:"room_topic,omitempty"`
}

// EncodeInvite serializes an invite code for messaging.
func EncodeInvite(code InviteCode) (string, error) {
	if code.Protocol == 0 {
		code.Protocol = Protocol
	}
	if code.Protocol != Protocol {
		return "", fmt.Errorf("%w: unsupported protocol", ErrInvalidInviteCode)
	}
	if code.HomeKind == "" {
		code.HomeKind = InviteKindNode
	}
	if code.HomeKind != InviteKindNode && code.HomeKind != InviteKindOfficial {
		return "", fmt.Errorf("%w: unsupported home kind", ErrInvalidInviteCode)
	}
	if len(code.Secret) < 16 {
		return "", fmt.Errorf("%w: secret is too short", ErrInvalidInviteCode)
	}
	if code.ServerID == "" || code.RoomID == "" {
		return "", fmt.Errorf("%w: server or room is missing", ErrInvalidInviteCode)
	}
	parsed, err := url.Parse(code.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("%w: home URL is not HTTPS", ErrInvalidInviteCode)
	}
	if err := validateInviteHome(code.HomeKind, code.URL, code.ServerID); err != nil {
		return "", err
	}
	data, err := json.Marshal(code)
	if err != nil {
		return "", fmt.Errorf("encode invite: %w", err)
	}
	return InvitePrefix + base64.RawURLEncoding.EncodeToString(data), nil
}

// DecodeInvite parses a portable invite code.
func DecodeInvite(raw string) (*InviteCode, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrInvalidInviteCode
	}
	if !strings.HasPrefix(raw, InvitePrefix) {
		return nil, ErrNotInviteCode
	}
	if len(raw) > MaxInviteCodeLength {
		return nil, fmt.Errorf("%w: code is too large", ErrInvalidInviteCode)
	}
	data, err := base64.RawURLEncoding.DecodeString(raw[len(InvitePrefix):])
	if err != nil {
		return nil, fmt.Errorf("%w: bad encoding", ErrInvalidInviteCode)
	}
	var code InviteCode
	if err := json.Unmarshal(data, &code); err != nil {
		return nil, fmt.Errorf("%w: bad payload", ErrInvalidInviteCode)
	}
	if code.Protocol != Protocol {
		return nil, fmt.Errorf("%w: unsupported protocol", ErrInvalidInviteCode)
	}
	if code.HomeKind == "" {
		code.HomeKind = InviteKindNode
	}
	if code.HomeKind != InviteKindNode && code.HomeKind != InviteKindOfficial {
		return nil, fmt.Errorf("%w: unsupported home kind", ErrInvalidInviteCode)
	}
	if len(code.Secret) < 16 || code.ServerID == "" || code.RoomID == "" {
		return nil, fmt.Errorf("%w: missing fields", ErrInvalidInviteCode)
	}
	if err := validateInviteHome(code.HomeKind, code.URL, code.ServerID); err != nil {
		return nil, err
	}
	return &code, nil
}

// validateInviteHome checks that an invite points at a home with a usable
// trust anchor. Node homes always use a certificate pin. Official homes may
// use a certificate pin or a host identity pin, and an identity pin must match
// the server ID carried in the signed invite.
func validateInviteHome(kind, rawURL, serverID string) error {
	if kind == InviteKindOfficial {
		if strings.TrimSpace(rawURL) == "" || !account.ValidHostURL(rawURL) {
			return fmt.Errorf("%w: official home URL is invalid", ErrInvalidInviteCode)
		}
		if !identity.ValidServerID(serverID) {
			return fmt.Errorf("%w: official server id is invalid", ErrInvalidInviteCode)
		}
		address, err := account.ParseHostURL(rawURL)
		if err != nil {
			return fmt.Errorf("%w: official home URL is invalid", ErrInvalidInviteCode)
		}
		if address.HostID != "" && address.HostID != serverID {
			return fmt.Errorf("%w: official host id does not match the invite", ErrInvalidInviteCode)
		}
		return nil
	}
	if _, _, err := ParsePinnedURL(rawURL); err != nil {
		return err
	}
	return nil
}

// IsInviteCode reports whether raw looks like an encoded invite.
func IsInviteCode(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), InvitePrefix)
}
