package update

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

var channelNamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)

// VerifyChannel verifies a signed channel pointer and returns it. If
// expectedChannel is non-empty, the channel name must match.
func (v *Verifier) VerifyChannel(data []byte, expectedChannel string) (*Channel, error) {
	if len(data) == 0 || len(data) > MaxChannelBytes {
		return nil, fmt.Errorf("channel document must be between 1 and %d bytes", MaxChannelBytes)
	}
	var channel Channel
	if err := decodeStrict(data, &channel); err != nil {
		return nil, fmt.Errorf("parse channel: %w", err)
	}
	if channel.Format != FormatChannel || channel.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported channel format %q version %d", channel.Format, channel.Version)
	}
	if !channelNamePattern.MatchString(channel.Channel) {
		return nil, fmt.Errorf("invalid channel name %q", channel.Channel)
	}
	if expectedChannel != "" && channel.Channel != expectedChannel {
		return nil, fmt.Errorf("channel document is for %q, not %q", channel.Channel, expectedChannel)
	}
	if _, err := canonicalRelease(channel.Release); err != nil {
		return nil, fmt.Errorf("channel release: %w", err)
	}
	if err := validateReference(channel.Manifest); err != nil {
		return nil, fmt.Errorf("channel manifest: %w", err)
	}
	if channel.Rollout < 1 || channel.Rollout > 100 {
		return nil, fmt.Errorf("channel rollout must be between 1 and 100")
	}
	publishedAt, err := parseTime("channel published_at", channel.PublishedAt)
	if err != nil {
		return nil, err
	}
	if publishedAt.After(v.now().Add(clockSkew)) {
		return nil, errors.New("channel was published in the future")
	}
	public, err := v.VerifyReleaseKey(channel.ReleaseKey)
	if err != nil {
		return nil, err
	}
	if err := verifyDocumentSignature(public, "toget-release-channel:v1", channel.signingView(), channel.Signature); err != nil {
		return nil, fmt.Errorf("channel signature: %w", err)
	}
	return &channel, nil
}

// VerifyManifest verifies a signed release manifest and returns it. If
// expectedRelease is non-empty, the manifest release must match.
func (v *Verifier) VerifyManifest(data []byte, expectedRelease string) (*Manifest, error) {
	if len(data) == 0 || len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("manifest document must be between 1 and %d bytes", MaxManifestBytes)
	}
	var manifest Manifest
	if err := decodeStrict(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.Format != FormatManifest || manifest.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported manifest format %q version %d", manifest.Format, manifest.Version)
	}
	release, err := canonicalRelease(manifest.Release)
	if err != nil {
		return nil, fmt.Errorf("manifest release: %w", err)
	}
	if expectedRelease != "" && manifest.Release != expectedRelease {
		return nil, fmt.Errorf("manifest is for release %s, not %s", manifest.Release, expectedRelease)
	}
	if manifest.Protocol < 1 || manifest.Protocol > 1000 {
		return nil, fmt.Errorf("manifest protocol %d is invalid", manifest.Protocol)
	}
	if manifest.Schema < 1 || manifest.Schema > 100000 {
		return nil, fmt.Errorf("manifest schema %d is invalid", manifest.Schema)
	}
	if manifest.MinProtocol < 0 || manifest.MinProtocol > manifest.Protocol {
		return nil, fmt.Errorf("manifest min_protocol %d is invalid", manifest.MinProtocol)
	}
	if manifest.MinApp != "" {
		minApp, err := canonicalRelease(manifest.MinApp)
		if err != nil {
			return nil, fmt.Errorf("manifest min_app: %w", err)
		}
		if minApp.Compare(release) > 0 {
			return nil, errors.New("manifest min_app is newer than the release")
		}
	}
	switch manifest.Level {
	case LevelRecommended, LevelSecurity, LevelForced:
	default:
		return nil, fmt.Errorf("manifest level %q is invalid", manifest.Level)
	}
	if len(manifest.Changelog) > 8192 {
		return nil, errors.New("manifest changelog is too long")
	}
	publishedAt, err := parseTime("manifest published_at", manifest.PublishedAt)
	if err != nil {
		return nil, err
	}
	if publishedAt.After(v.now().Add(clockSkew)) {
		return nil, errors.New("manifest was published in the future")
	}
	if len(manifest.Artifacts) == 0 || len(manifest.Artifacts) > 64 {
		return nil, errors.New("manifest must list between 1 and 64 artifacts")
	}
	seen := make(map[string]bool, len(manifest.Artifacts))
	for i, artifact := range manifest.Artifacts {
		if err := validateArtifact(artifact); err != nil {
			return nil, fmt.Errorf("manifest artifact %d: %w", i, err)
		}
		key := artifact.OS + "/" + artifact.Arch
		if seen[key] {
			return nil, fmt.Errorf("manifest has duplicate artifact %s", key)
		}
		seen[key] = true
	}
	if manifest.SBOM != nil {
		if err := validateFileRef(*manifest.SBOM, MaxSBOMBytes); err != nil {
			return nil, fmt.Errorf("manifest SBOM: %w", err)
		}
	}
	public, err := v.VerifyReleaseKeyAt(manifest.ReleaseKey, publishedAt)
	if err != nil {
		return nil, err
	}
	if err := verifyDocumentSignature(public, "toget-release-manifest:v1", manifest.signingView(), manifest.Signature); err != nil {
		return nil, fmt.Errorf("manifest signature: %w", err)
	}
	return &manifest, nil
}

func validateArtifact(artifact Artifact) error {
	if !validPlatformToken(artifact.OS) {
		return fmt.Errorf("invalid os %q", artifact.OS)
	}
	if !validPlatformToken(artifact.Arch) {
		return fmt.Errorf("invalid arch %q", artifact.Arch)
	}
	if err := validateReference(artifact.URL); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if err := validateSHA256(artifact.SHA256); err != nil {
		return err
	}
	if artifact.Size <= 0 || artifact.Size > MaxArtifactBytes {
		return fmt.Errorf("size %d is out of range", artifact.Size)
	}
	switch artifact.Format {
	case FormatTarGz, FormatZip:
	default:
		return fmt.Errorf("format %q is not supported", artifact.Format)
	}
	if err := validateExecutablePath(artifact.Executable); err != nil {
		return err
	}
	return nil
}

func validateFileRef(ref FileRef, limit int64) error {
	if err := validateReference(ref.URL); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if err := validateSHA256(ref.SHA256); err != nil {
		return err
	}
	if ref.Size <= 0 || ref.Size > limit {
		return fmt.Errorf("size %d is out of range", ref.Size)
	}
	return nil
}

func validPlatformToken(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func validateSHA256(value string) error {
	if len(value) != 64 {
		return errors.New("sha256 must be 64 lowercase hex characters")
	}
	if strings.ToLower(value) != value {
		return errors.New("sha256 must be lowercase")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return errors.New("sha256 is not valid hex")
	}
	return nil
}

func validateExecutablePath(value string) error {
	if value == "" || len(value) > 256 {
		return errors.New("executable path is empty or too long")
	}
	if strings.ContainsAny(value, "\x00\\") || strings.HasPrefix(value, "/") {
		return errors.New("executable path is unsafe")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned != value {
		return errors.New("executable path is not clean")
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == ".." || part == "" {
			return errors.New("executable path is unsafe")
		}
	}
	return nil
}

func verifyDocumentSignature(public ed25519.PublicKey, domain string, value any, signature string) error {
	raw, err := decodeBase64(signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	message, err := signingMessageBytes(domain, value)
	if err != nil {
		return err
	}
	if !ed25519.Verify(public, message, raw) {
		return errors.New("signature is invalid")
	}
	return nil
}

func (channel Channel) signingView() Channel {
	channel.Signature = ""
	return channel
}

func (manifest Manifest) signingView() Manifest {
	manifest.Signature = ""
	return manifest
}

// ManifestSHA256 returns the digest of the exact manifest bytes.
func ManifestSHA256(data []byte) string {
	return hashHex(data)
}

// PublishedAt returns the parsed publication time.
func (c *Channel) PublishedAtTime() (time.Time, error) {
	return parseTime("published_at", c.PublishedAt)
}

// PublishedAt returns the parsed publication time.
func (m *Manifest) PublishedAtTime() (time.Time, error) {
	return parseTime("published_at", m.PublishedAt)
}
