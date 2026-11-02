// Package directoryproto defines the public directory wire format. A
// directory is an optional convenience: it maps a short server code to the
// server's self-certifying identity and latest signed node manifest. Clients
// verify both signatures themselves, so a directory cannot impersonate a
// server or redirect a pinned endpoint without detection.
package directoryproto

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	// Protocol is the directory wire protocol version.
	Protocol = 1
	// RecordKindNode describes a Toget node manifest. It is the default for
	// older publishers that do not send a kind.
	RecordKindNode = "node"
	// RecordKindHost describes an account host manifest.
	RecordKindHost = "host"
	// PublishPath accepts a signed server record.
	PublishPath = "/directory/v1/publish"
	// LookupPrefix resolves a short server code.
	LookupPrefix = "/directory/v1/servers/"
	// RelayPath returns the directory's default relay.
	RelayPath = "/directory/v1/relay"
	// HealthPath is the unauthenticated health check.
	HealthPath = "/directory/v1/health"

	// MaxManifest bounds either embedded manifest.
	MaxManifest = 256 << 10
	// MaxPublishBody bounds the publish request.
	MaxPublishBody = 2 * MaxManifest
	// DefaultRecordTTL is how long a published record stays fresh.
	DefaultRecordTTL = 24 * time.Hour
)

// PublishRequest carries the two signed statements that make a directory
// entry self-certifying. The identity manifest binds the root key to the
// server ID and code; the node manifest is signed by that root key and
// carries the current endpoint, TLS pin, and optional relay route.
type PublishRequest struct {
	Protocol         int             `json:"protocol"`
	Kind             string          `json:"kind,omitempty"`
	IdentityManifest json.RawMessage `json:"identity_manifest"`
	NodeManifest     json.RawMessage `json:"node_manifest,omitempty"`
	HostManifest     json.RawMessage `json:"host_manifest,omitempty"`
}

// PublishResponse acknowledges a record.
type PublishResponse struct {
	Protocol   int       `json:"protocol"`
	Kind       string    `json:"kind"`
	ServerID   string    `json:"server_id"`
	ServerCode string    `json:"server_code"`
	URL        string    `json:"url,omitempty"`
	RelayURL   string    `json:"relay_url,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// LookupResponse returns a verified record. Clients still verify both
// manifests; the directory only helps find them.
type LookupResponse struct {
	Protocol         int             `json:"protocol"`
	Kind             string          `json:"kind"`
	IdentityManifest json.RawMessage `json:"identity_manifest"`
	NodeManifest     json.RawMessage `json:"node_manifest,omitempty"`
	HostManifest     json.RawMessage `json:"host_manifest,omitempty"`
	RelayURL         string          `json:"relay_url,omitempty"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// RelayResponse advertises the default relay used when a node is configured
// with relay "auto". RelayURL is a pinned URL including #fp=.
type RelayResponse struct {
	Protocol  int       `json:"protocol"`
	RelayURL  string    `json:"relay_url"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// ErrorBody is the directory's JSON error shape.
type ErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ValidatePublish checks the outer shape; signature verification is the
// caller's job because it owns the identity parser.
func ValidatePublish(req *PublishRequest) error {
	if req == nil {
		return errors.New("publish request is empty")
	}
	if req.Protocol != Protocol {
		return errors.New("unsupported directory protocol")
	}
	switch req.Kind {
	case "", RecordKindNode:
		req.Kind = RecordKindNode
		if len(req.IdentityManifest) == 0 || len(req.NodeManifest) == 0 {
			return errors.New("publish request is missing a manifest")
		}
		if len(req.HostManifest) != 0 {
			return errors.New("node publish request carries a host manifest")
		}
		if len(req.IdentityManifest) > MaxManifest || len(req.NodeManifest) > MaxManifest {
			return errors.New("publish manifest is too large")
		}
	case RecordKindHost:
		if len(req.IdentityManifest) == 0 || len(req.HostManifest) == 0 {
			return errors.New("publish request is missing a manifest")
		}
		if len(req.NodeManifest) != 0 {
			return errors.New("host publish request carries a node manifest")
		}
		if len(req.IdentityManifest) > MaxManifest || len(req.HostManifest) > MaxManifest {
			return errors.New("publish manifest is too large")
		}
	default:
		return errors.New("unsupported directory record kind")
	}
	return nil
}
