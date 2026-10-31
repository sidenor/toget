package update

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// ReasonAvailable means a newer release is available for this device.
	ReasonAvailable = "available"
	// ReasonUpToDate means the channel release is already installed.
	ReasonUpToDate = "up_to_date"
	// ReasonNewerInstalled means the installed release is newer than the
	// channel pointer.
	ReasonNewerInstalled = "newer_installed"
	// ReasonRollout means the release is not yet enabled for this device's
	// rollout bucket.
	ReasonRollout = "rollout"
	// ReasonNoArtifact means the release has no archive for this platform.
	ReasonNoArtifact = "no_artifact"
	// ReasonSchemaDowngrade means the release uses an older database schema
	// than the installed binary; updating would make the database unusable.
	ReasonSchemaDowngrade = "schema_downgrade"
	// ReasonProtocolDowngrade means the release uses an older protocol than
	// the installed binary.
	ReasonProtocolDowngrade = "protocol_downgrade"
	// ReasonAppTooOld means the installed app is older than the release's
	// minimum supported app version; an intermediate release is required.
	ReasonAppTooOld = "app_too_old"
	// ReasonProtocolTooOld means the installed protocol is older than the
	// release's minimum supported protocol version.
	ReasonProtocolTooOld = "protocol_too_old"
)

// Current describes the installed binary.
type Current struct {
	App            string `json:"app"`
	Protocol       int    `json:"protocol"`
	Schema         int    `json:"schema"`
	Release        string `json:"release,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
}

// Client fetches and verifies signed releases.
type Client struct {
	HTTP       *http.Client
	Verifier   *Verifier
	ChannelURL string
	Channel    string
	OS         string
	Arch       string
	UserAgent  string
	Now        func() time.Time
}

// CheckOptions modifies update selection.
type CheckOptions struct {
	// Force ignores staged rollout.
	Force bool
	// AllowDowngrade permits a release older than the installed one. It does
	// not bypass signature verification.
	AllowDowngrade bool
}

// CheckResult is the verified outcome of one update check.
type CheckResult struct {
	ChannelURL     string    `json:"channel_url"`
	ManifestURL    string    `json:"manifest_url"`
	Channel        *Channel  `json:"channel"`
	Manifest       *Manifest `json:"manifest"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	Artifact       *Artifact `json:"artifact,omitempty"`
	SBOM           *FileRef  `json:"sbom,omitempty"`
	Current        Current   `json:"current"`
	Available      bool      `json:"available"`
	Reason         string    `json:"reason"`
	RolloutPercent int       `json:"rollout_percent,omitempty"`
	RolloutBucket  int       `json:"rollout_bucket,omitempty"`
	Downgrade      bool      `json:"downgrade,omitempty"`
	Warning        string    `json:"warning,omitempty"`
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Client) osName() string {
	if c.OS != "" {
		return c.OS
	}
	return runtime.GOOS
}

func (c *Client) archName() string {
	if c.Arch != "" {
		return c.Arch
	}
	return runtime.GOARCH
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout:       60 * time.Second,
		CheckRedirect: safeRedirect,
	}
}

// downloadClient has no total timeout: artifacts can be hundreds of
// megabytes on slow links. The caller's context still cancels the download.
func (c *Client) downloadClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{CheckRedirect: safeRedirect}
	}
	transport := base.Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{
		Transport:     transport,
		CheckRedirect: safeRedirect,
	}
}

func safeRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" && !(req.URL.Scheme == "http" && isLoopbackHost(req.URL.Hostname())) {
		return errors.New("update redirect must use HTTPS")
	}
	if req.URL.User != nil {
		return errors.New("update redirect must not contain user information")
	}
	return nil
}

// Check fetches the channel and manifest, verifies both, and decides whether
// the release is available for this device.
func (c *Client) Check(ctx context.Context, current Current, rolloutID string, opts CheckOptions) (*CheckResult, error) {
	if c.Verifier == nil || len(c.Verifier.Root) == 0 {
		return nil, errors.New("release root key is not configured")
	}
	if current.App == "" {
		return nil, errors.New("current app version is required")
	}
	currentVersion, err := ParseSemVer(current.App)
	if err != nil {
		return nil, fmt.Errorf("current app version: %w", err)
	}
	base, err := parseUpdateURL(c.ChannelURL)
	if err != nil {
		return nil, err
	}
	channel, _, err := c.fetchChannel(ctx, base)
	if err != nil {
		return nil, err
	}
	if c.Channel != "" && channel.Channel != c.Channel {
		return nil, fmt.Errorf("channel document is for %q, not %q", channel.Channel, c.Channel)
	}
	manifestURL, err := resolveReference(base, channel.Manifest)
	if err != nil {
		return nil, fmt.Errorf("channel manifest: %w", err)
	}
	manifest, manifestRaw, err := c.fetchManifest(ctx, manifestURL, channel.Release)
	if err != nil {
		return nil, err
	}
	manifestHash := ManifestSHA256(manifestRaw)

	release, err := canonicalRelease(manifest.Release)
	if err != nil {
		return nil, err
	}
	result := &CheckResult{
		ChannelURL:     base.String(),
		ManifestURL:    manifestURL.String(),
		Channel:        channel,
		Manifest:       manifest,
		ManifestSHA256: manifestHash,
		Current:        current,
		RolloutPercent: channel.Rollout,
	}
	if manifest.Schema > current.Schema {
		result.Warning = "this release upgrades the database schema; automatic rollback restores the previous binary but not the database; back up before applying"
	}

	var artifact *Artifact
	for i := range manifest.Artifacts {
		if manifest.Artifacts[i].OS == c.osName() && manifest.Artifacts[i].Arch == c.archName() {
			artifact = &manifest.Artifacts[i]
			break
		}
	}
	result.Artifact = artifact
	result.SBOM = manifest.SBOM

	if release.Compare(currentVersion) == 0 {
		if current.ManifestSHA256 != "" && current.ManifestSHA256 != manifestHash {
			return nil, fmt.Errorf("release %s was republished with different contents", manifest.Release)
		}
		result.Reason = ReasonUpToDate
		result.Available = false
		return result, nil
	}
	if manifest.MinApp != "" {
		minimum, err := canonicalRelease(manifest.MinApp)
		if err != nil {
			return nil, fmt.Errorf("manifest minimum app version: %w", err)
		}
		if currentVersion.Compare(minimum) < 0 {
			result.Reason = ReasonAppTooOld
			result.Available = false
			return result, nil
		}
	}
	if manifest.MinProtocol > 0 && current.Protocol < manifest.MinProtocol {
		result.Reason = ReasonProtocolTooOld
		result.Available = false
		return result, nil
	}
	if release.Compare(currentVersion) < 0 {
		if !opts.AllowDowngrade {
			result.Reason = ReasonNewerInstalled
			result.Available = false
			return result, nil
		}
		result.Downgrade = true
	}
	if manifest.Schema < current.Schema {
		result.Reason = ReasonSchemaDowngrade
		result.Available = false
		return result, nil
	}
	if manifest.Protocol < current.Protocol {
		result.Reason = ReasonProtocolDowngrade
		result.Available = false
		return result, nil
	}
	if artifact == nil {
		result.Reason = ReasonNoArtifact
		result.Available = false
		return result, nil
	}
	if manifest.Level == LevelRecommended && !opts.Force && channel.Rollout < 100 {
		if rolloutID == "" {
			return nil, errors.New("rollout identity is required for a staged release")
		}
		bucket := rolloutBucket(rolloutID, channel.Channel, manifest.Release)
		result.RolloutBucket = bucket
		if bucket >= channel.Rollout {
			result.Reason = ReasonRollout
			result.Available = false
			return result, nil
		}
	}
	result.Available = true
	result.Reason = ReasonAvailable
	if result.Downgrade {
		result.Reason = "downgrade"
	}
	return result, nil
}

func parseUpdateURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("channel URL is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid channel URL: %w", err)
	}
	if parsed.User != nil {
		return nil, errors.New("channel URL must not contain user information")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("channel URL must not contain a fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("channel URL must use HTTPS")
	}
	if parsed.Host == "" {
		return nil, errors.New("channel URL has no host")
	}
	if err := validatePath(parsed.Path); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (c *Client) fetchChannel(ctx context.Context, base *url.URL) (*Channel, []byte, error) {
	data, err := c.getDocument(ctx, base.String(), MaxChannelBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch channel: %w", err)
	}
	channel, err := c.Verifier.VerifyChannel(data, c.Channel)
	if err != nil {
		return nil, nil, err
	}
	return channel, data, nil
}

func (c *Client) fetchManifest(ctx context.Context, manifestURL *url.URL, release string) (*Manifest, []byte, error) {
	data, err := c.getDocument(ctx, manifestURL.String(), MaxManifestBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch manifest: %w", err)
	}
	manifest, err := c.Verifier.VerifyManifest(data, release)
	if err != nil {
		return nil, nil, err
	}
	return manifest, data, nil
}

func (c *Client) getDocument(ctx context.Context, address string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.userAgent())
	response, err := c.httpClient().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("document is too large")
	}
	return data, nil
}

func (c *Client) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return "toget-update/1"
}

// DownloadArtifact downloads and verifies the selected artifact. It writes to
// dest atomically and refuses to leave a partial file behind.
func (c *Client) DownloadArtifact(ctx context.Context, result *CheckResult, dest string) (*Artifact, error) {
	if result == nil || result.Artifact == nil {
		return nil, errors.New("no artifact selected")
	}
	if result.ManifestURL == "" {
		return nil, errors.New("missing manifest URL")
	}
	base, err := url.Parse(result.ManifestURL)
	if err != nil {
		return nil, err
	}
	artifactURL, err := resolveReference(base, result.Artifact.URL)
	if err != nil {
		return nil, fmt.Errorf("artifact URL: %w", err)
	}
	if err := c.download(ctx, artifactURL.String(), result.Artifact.Size, result.Artifact.SHA256, dest, MaxArtifactBytes); err != nil {
		return nil, err
	}
	return result.Artifact, nil
}

// DownloadSBOM verifies the optional SBOM reference when the manifest names
// one.
func (c *Client) DownloadSBOM(ctx context.Context, result *CheckResult, dest string) (*FileRef, error) {
	if result == nil || result.SBOM == nil {
		return nil, nil
	}
	base, err := url.Parse(result.ManifestURL)
	if err != nil {
		return nil, err
	}
	refURL, err := resolveReference(base, result.SBOM.URL)
	if err != nil {
		return nil, fmt.Errorf("SBOM URL: %w", err)
	}
	if err := c.download(ctx, refURL.String(), result.SBOM.Size, result.SBOM.SHA256, dest, MaxSBOMBytes); err != nil {
		return nil, err
	}
	return result.SBOM, nil
}

func (c *Client) download(ctx context.Context, address string, size int64, digest, dest string, max int64) error {
	if dest == "" {
		return errors.New("download destination is empty")
	}
	if size <= 0 || size > max {
		return errors.New("download size is out of range")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", c.userAgent())
	response, err := c.downloadClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s", response.Status)
	}
	if response.ContentLength >= 0 && response.ContentLength != size {
		return fmt.Errorf("server content length %d does not match manifest size %d", response.ContentLength, size)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".toget-download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(response.Body, max+1))
	if err != nil {
		return err
	}
	if written != size {
		return fmt.Errorf("downloaded %d bytes, expected %d", written, size)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != digest {
		return fmt.Errorf("sha256 mismatch: got %s, expected %s", got, digest)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func rolloutBucket(rolloutID, channel, release string) int {
	sum := sha256.Sum256([]byte("toget-update-rollout:v1\n" + rolloutID + "\n" + channel + "\n" + release))
	return int(binary.BigEndian.Uint32(sum[:4]) % 100)
}
