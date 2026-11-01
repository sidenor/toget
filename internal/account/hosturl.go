package account

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/sidenor/toget/internal/identity"
)

// HostAddress is an official account host address split into its HTTPS origin
// and exactly one trust anchor. Fingerprint is set for a direct certificate
// pin (#fp= or #sha256=); HostID is set for a self-certifying host identity
// pin (#id=) used behind a TLS-terminating proxy.
type HostAddress struct {
	Origin      string
	Fingerprint string
	HostID      string
}

// ParseHostURL parses an official account host address. The address must use
// HTTPS, have no path, query, or user info, and carry exactly one trust
// anchor: either a SHA-256 certificate fingerprint or a canonical
// self-certifying host ID. Unknown fragment parameters are ignored.
func ParseHostURL(raw string) (HostAddress, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxNodeURLLength {
		return HostAddress{}, ErrInvalidHostURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery ||
		(parsed.Path != "" && parsed.Path != "/") {
		return HostAddress{}, ErrInvalidHostURL
	}
	values, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		return HostAddress{}, ErrInvalidHostURL
	}
	if values.Get("relay") != "" || values.Get("target") != "" {
		return HostAddress{}, ErrInvalidHostURL
	}
	if len(values["id"]) > 1 || len(values["fp"]) > 1 || len(values["sha256"]) > 1 {
		return HostAddress{}, ErrInvalidHostURL
	}
	hasID := values.Has("id")
	hasFingerprint := values.Has("fp") || values.Has("sha256")
	if hasID == hasFingerprint {
		// Neither anchor, or both kinds: ambiguous.
		return HostAddress{}, ErrInvalidHostURL
	}

	address := HostAddress{}
	if hasID {
		id := strings.TrimSpace(values.Get("id"))
		if !identity.ValidServerID(id) {
			return HostAddress{}, ErrInvalidHostURL
		}
		address.HostID = id
	} else {
		rawFingerprint := values.Get("fp")
		if rawFingerprint == "" {
			rawFingerprint = values.Get("sha256")
		}
		fingerprint, err := parseHostFingerprint(rawFingerprint)
		if err != nil {
			return HostAddress{}, ErrInvalidHostURL
		}
		if values.Has("sha256") {
			alias, err := parseHostFingerprint(values.Get("sha256"))
			if err != nil || alias != fingerprint {
				return HostAddress{}, ErrInvalidHostURL
			}
		}
		address.Fingerprint = fingerprint
	}

	origin := *parsed
	origin.Fragment = ""
	origin.RawFragment = ""
	origin.ForceQuery = false
	if origin.Path == "/" {
		origin.Path = ""
		origin.RawPath = ""
	}
	origin.RawQuery = ""
	address.Origin = strings.TrimSuffix(origin.String(), "/")
	if address.Origin == "" {
		return HostAddress{}, ErrInvalidHostURL
	}
	return address, nil
}

func parseHostFingerprint(raw string) (string, error) {
	raw = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "sha256:")
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("%w: invalid TLS certificate fingerprint", ErrInvalidHostURL)
	}
	return hex.EncodeToString(decoded), nil
}
