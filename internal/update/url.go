package update

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// validateReference checks a signed relative or absolute reference. Absolute
// references must use HTTPS. Relative references may not escape their base.
func validateReference(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("reference is empty")
	}
	if len(raw) > 2048 {
		return errors.New("reference is too long")
	}
	if strings.ContainsAny(raw, "\x00\\ \t\r\n") {
		return errors.New("reference contains an unsafe character")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return errors.New("reference contains a control character")
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid reference: %w", err)
	}
	if parsed.Fragment != "" {
		return errors.New("reference must not contain a fragment")
	}
	if parsed.User != nil {
		return errors.New("reference must not contain user information")
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
			return errors.New("absolute reference must use HTTPS")
		}
		if parsed.Host == "" {
			return errors.New("absolute reference has no host")
		}
		return validatePath(parsed.Path)
	}
	if strings.HasPrefix(raw, "/") {
		return errors.New("absolute-path reference is not allowed")
	}
	return validatePath(parsed.Path)
}

func validatePath(value string) error {
	if value == "" {
		return errors.New("reference has an empty path")
	}
	path := strings.ReplaceAll(value, "\\", "/")
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == ".." {
			return errors.New("reference may not contain ..")
		}
		if part == "." {
			return errors.New("reference may not contain a dot segment")
		}
		if part == "" {
			if i == 0 && strings.HasPrefix(path, "/") && len(parts) > 1 {
				continue
			}
			return errors.New("reference contains an empty path segment")
		}
	}
	return nil
}

// resolveReference resolves a signed reference against the document that
// contained it and enforces the transport rules.
func resolveReference(base *url.URL, raw string) (*url.URL, error) {
	if err := validateReference(raw); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid reference: %w", err)
	}
	var resolved *url.URL
	if parsed.IsAbs() {
		resolved = parsed
	} else {
		if base == nil {
			return nil, errors.New("relative reference has no base URL")
		}
		resolved = base.ResolveReference(parsed)
	}
	if resolved.Scheme != "https" && !(resolved.Scheme == "http" && isLoopbackHost(resolved.Hostname())) {
		return nil, fmt.Errorf("update URL must use HTTPS (got %s)", resolved.Scheme)
	}
	if resolved.Host == "" {
		return nil, errors.New("update URL has no host")
	}
	if resolved.User != nil {
		return nil, errors.New("update URL must not contain user information")
	}
	if len(resolved.String()) > 4096 {
		return nil, errors.New("update URL is too long")
	}
	if err := validatePath(resolved.Path); err != nil {
		return nil, err
	}
	return resolved, nil
}

func isLoopbackHost(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}
