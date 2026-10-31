package update

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// BuildReleaseIndexOptions describes a release catalog.
type BuildReleaseIndexOptions struct {
	Entries     []IndexEntry
	PublishedAt time.Time
	Key         *SigningKey
	Cert        ReleaseKey
}

// BuildReleaseIndex validates entries, sorts them newest first, and signs the
// catalog. The latest field is always derived from the entries.
func BuildReleaseIndex(opts BuildReleaseIndexOptions) (*ReleaseIndex, error) {
	if len(opts.Entries) == 0 {
		return nil, errors.New("release index is empty")
	}
	if len(opts.Entries) > MaxIndexEntries {
		return nil, fmt.Errorf("release index has more than %d entries", MaxIndexEntries)
	}
	entries := make([]IndexEntry, len(opts.Entries))
	seen := make(map[string]bool, len(opts.Entries))
	for i, entry := range opts.Entries {
		if err := validateIndexEntry(entry); err != nil {
			return nil, fmt.Errorf("release index entry %d: %w", i, err)
		}
		if seen[entry.Release] {
			return nil, fmt.Errorf("release index lists %s more than once", entry.Release)
		}
		seen[entry.Release] = true
		entries[i] = entry
	}
	sort.Slice(entries, func(i, j int) bool {
		a, _ := canonicalRelease(entries[i].Release)
		b, _ := canonicalRelease(entries[j].Release)
		return a.Compare(b) > 0
	})
	if opts.PublishedAt.IsZero() {
		opts.PublishedAt = time.Now().UTC()
	}
	if err := certificateCovers(opts.Cert, opts.PublishedAt); err != nil {
		return nil, err
	}
	index := &ReleaseIndex{
		Format:      FormatIndex,
		Version:     CurrentVersion,
		Releases:    entries,
		Latest:      latestRelease(entries),
		PublishedAt: formatTime(opts.PublishedAt),
		ReleaseKey:  opts.Cert,
	}
	if err := SignIndex(opts.Key, index); err != nil {
		return nil, err
	}
	return index, nil
}

func validateIndexEntry(entry IndexEntry) error {
	release, err := canonicalRelease(entry.Release)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if entry.Manifest == "" {
		return errors.New("manifest reference is empty")
	}
	if err := validateReference(entry.Manifest); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if err := validateSHA256(entry.ManifestSHA256); err != nil {
		return fmt.Errorf("manifest hash: %w", err)
	}
	if !channelLevelValid(entry.Level) {
		return fmt.Errorf("invalid level %q", entry.Level)
	}
	if _, err := parseTime("published_at", entry.PublishedAt); err != nil {
		return err
	}
	if entry.Yanked {
		if entry.YankedAt != "" {
			if _, err := parseTime("yanked_at", entry.YankedAt); err != nil {
				return err
			}
		}
		if len(entry.YankReason) > MaxYankReasonBytes {
			return fmt.Errorf("yank reason is longer than %d bytes", MaxYankReasonBytes)
		}
	} else if entry.YankedAt != "" || entry.YankReason != "" {
		return fmt.Errorf("release %s has yank metadata but is not yanked", release)
	}
	return nil
}

func latestRelease(entries []IndexEntry) string {
	for _, entry := range entries {
		if !entry.Yanked {
			return entry.Release
		}
	}
	return ""
}

// SignIndex signs a release index with a release key.
func SignIndex(key *SigningKey, index *ReleaseIndex) error {
	if key == nil || key.Kind != KindRelease || len(key.Private) != ed25519.PrivateKeySize {
		return errors.New("a release signing key is required")
	}
	if index == nil {
		return errors.New("release index is required")
	}
	if err := checkReleaseKeyMatches(key, index.ReleaseKey); err != nil {
		return err
	}
	signature, err := signDocument("toget-release-index:v1", index.signingView(), key.Private)
	if err != nil {
		return err
	}
	index.Signature = encodeBase64(signature)
	return nil
}

// VerifyReleaseIndex verifies a signed release index and returns it.
func (v *Verifier) VerifyReleaseIndex(data []byte) (*ReleaseIndex, error) {
	if len(data) == 0 || len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("release index must be between 1 and %d bytes", MaxManifestBytes)
	}
	var index ReleaseIndex
	if err := decodeStrict(data, &index); err != nil {
		return nil, fmt.Errorf("parse release index: %w", err)
	}
	if index.Format != FormatIndex || index.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported release index format %q version %d", index.Format, index.Version)
	}
	if len(index.Releases) == 0 || len(index.Releases) > MaxIndexEntries {
		return nil, fmt.Errorf("release index must list between 1 and %d releases", MaxIndexEntries)
	}
	publishedAt, err := parseTime("release index published_at", index.PublishedAt)
	if err != nil {
		return nil, err
	}
	if publishedAt.After(v.now().Add(clockSkew)) {
		return nil, errors.New("release index was published in the future")
	}
	for i, entry := range index.Releases {
		if err := validateIndexEntry(entry); err != nil {
			return nil, fmt.Errorf("release index entry %d: %w", i, err)
		}
		if i > 0 {
			previous, _ := canonicalRelease(index.Releases[i-1].Release)
			current, _ := canonicalRelease(entry.Release)
			if previous.Compare(current) <= 0 {
				return nil, fmt.Errorf("release index entries are not strictly newest-first at %s", entry.Release)
			}
		}
	}
	if want := latestRelease(index.Releases); index.Latest != want {
		return nil, fmt.Errorf("release index latest is %q, want %q", index.Latest, want)
	}
	public, err := v.VerifyReleaseKeyAt(index.ReleaseKey, publishedAt)
	if err != nil {
		return nil, err
	}
	if err := verifyDocumentSignature(public, "toget-release-index:v1", index.signingView(), index.Signature); err != nil {
		return nil, fmt.Errorf("release index signature: %w", err)
	}
	return &index, nil
}

// LatestEntry returns the newest non-yanked release, or nil when every entry
// is yanked.
func (index *ReleaseIndex) LatestEntry() *IndexEntry {
	if index == nil || index.Latest == "" {
		return nil
	}
	for i := range index.Releases {
		if index.Releases[i].Release == index.Latest {
			entry := index.Releases[i]
			return &entry
		}
	}
	return nil
}

func (index ReleaseIndex) signingView() ReleaseIndex {
	index.Signature = ""
	return index
}

// BuildDetachedSignature creates a .sig sidecar for exact document bytes. The
// document path is relative to the release root.
func BuildDetachedSignature(document string, data []byte, key *SigningKey) (*DetachedSignature, error) {
	if key == nil || key.Kind != KindRelease || len(key.Private) != ed25519.PrivateKeySize {
		return nil, errors.New("a release signing key is required")
	}
	if err := validateExecutablePath(document); err != nil {
		return nil, fmt.Errorf("signature document path: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("cannot sign an empty document")
	}
	signature := &DetachedSignature{
		Format:   FormatDetachedSignature,
		Version:  CurrentVersion,
		Document: document,
		SHA256:   hashHex(data),
		KeyID:    key.KeyID,
	}
	signed, err := signDocument("toget-release-detached:v1", signature.signingView(), key.Private)
	if err != nil {
		return nil, err
	}
	signature.Signature = encodeBase64(signed)
	return signature, nil
}

// VerifyDetachedSignature checks a .sig sidecar against the exact document
// bytes and the release key certificate named by the document. It does not
// verify the certificate chain; callers must verify the document first.
func VerifyDetachedSignature(document string, data []byte, cert ReleaseKey, signature DetachedSignature) error {
	if signature.Format != FormatDetachedSignature || signature.Version != CurrentVersion {
		return fmt.Errorf("unsupported detached signature format %q version %d", signature.Format, signature.Version)
	}
	if err := validateExecutablePath(signature.Document); err != nil {
		return fmt.Errorf("detached signature document path: %w", err)
	}
	if document != "" && signature.Document != document {
		return fmt.Errorf("detached signature is for %s, not %s", signature.Document, document)
	}
	if want := hashHex(data); signature.SHA256 != want {
		return errors.New("detached signature document hash does not match")
	}
	if cert.Format != FormatReleaseKey || cert.Version != CurrentVersion {
		return errors.New("release key certificate is missing or has an unsupported format")
	}
	if signature.KeyID != cert.KeyID {
		return errors.New("detached signature key does not match the release certificate")
	}
	public, err := decodeBase64(cert.PublicKey)
	if err != nil {
		return fmt.Errorf("decode release public key: %w", err)
	}
	if len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("release public key must be %d bytes", ed25519.PublicKeySize)
	}
	if want := keyID(ReleaseKeyPrefix, ed25519.PublicKey(public)); cert.KeyID != want {
		return errors.New("release key id does not match its public key")
	}
	if err := verifyDocumentSignature(ed25519.PublicKey(public), "toget-release-detached:v1", signature.signingView(), signature.Signature); err != nil {
		return fmt.Errorf("detached signature: %w", err)
	}
	return nil
}

func (signature DetachedSignature) signingView() DetachedSignature {
	signature.Signature = ""
	return signature
}

// NormalizeYankReason trims a reason and rejects oversized input.
func NormalizeYankReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxYankReasonBytes {
		return "", fmt.Errorf("yank reason is longer than %d bytes", MaxYankReasonBytes)
	}
	return reason, nil
}
