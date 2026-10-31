// Package update implements the signed release channel, verified downloads,
// atomic installation, and rollback for Toget.
package update

const (
	// FormatReleaseKey identifies a release key certificate.
	FormatReleaseKey = "toget-release-key"
	// FormatChannel identifies a signed channel pointer.
	FormatChannel = "toget-release-channel"
	// FormatManifest identifies a signed release manifest.
	FormatManifest = "toget-release-manifest"
	// FormatKeyFile identifies a private signing key file.
	FormatKeyFile = "toget-release-key-file"
	// FormatState identifies local update state.
	FormatState = "toget-update-state"
	// FormatInstalled identifies a release installed in a managed root.
	FormatInstalled = "toget-installed-release"

	// CurrentVersion is the current version of all signed release documents
	// and of local update state.
	CurrentVersion = 1

	// RootKeyPrefix and ReleaseKeyPrefix namespace the two key IDs.
	RootKeyPrefix    = "toget-release-root"
	ReleaseKeyPrefix = "toget-release-key"

	// KindRoot and KindRelease identify signing key files.
	KindRoot    = "root"
	KindRelease = "release"

	// Levels describe how strongly a release should be adopted. Rollout is
	// applied only to recommended releases.
	LevelRecommended = "recommended"
	LevelSecurity    = "security"
	LevelForced      = "forced"

	// Archive formats.
	FormatTarGz = "tar.gz"
	FormatZip   = "zip"

	// Bounds keep signed documents and downloads small enough to verify
	// without unbounded memory or disk use.
	MaxChannelBytes  = 64 << 10
	MaxManifestBytes = 1 << 20
	MaxArtifactBytes = 512 << 20
	MaxSBOMBytes     = 8 << 20
	MaxKeyFileBytes  = 16 << 10
)

// ReleaseKey is a release signing key certificate. The root key signs the
// certificate; the release key signs channel pointers and manifests.
type ReleaseKey struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Root      string `json:"root"`
	NotBefore string `json:"not_before"`
	NotAfter  string `json:"not_after"`
	Signature string `json:"signature,omitempty"`
}

// Channel is the signed pointer a client fetches first. It selects a release
// for an update channel and controls staged rollout.
type Channel struct {
	Format      string     `json:"format"`
	Version     int        `json:"version"`
	Channel     string     `json:"channel"`
	Release     string     `json:"release"`
	Manifest    string     `json:"manifest"`
	Rollout     int        `json:"rollout"`
	PublishedAt string     `json:"published_at"`
	ReleaseKey  ReleaseKey `json:"release_key"`
	Signature   string     `json:"signature,omitempty"`
}

// Artifact describes one platform archive in a release manifest.
type Artifact struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	URL        string `json:"url"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Format     string `json:"format"`
	Executable string `json:"executable"`
}

// FileRef describes an auxiliary file such as an SBOM.
type FileRef struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is a signed description of one immutable release.
type Manifest struct {
	Format      string     `json:"format"`
	Version     int        `json:"version"`
	Release     string     `json:"release"`
	Protocol    int        `json:"protocol"`
	Schema      int        `json:"schema"`
	MinApp      string     `json:"min_app,omitempty"`
	MinProtocol int        `json:"min_protocol,omitempty"`
	Level       string     `json:"level"`
	Changelog   string     `json:"changelog,omitempty"`
	PublishedAt string     `json:"published_at"`
	Artifacts   []Artifact `json:"artifacts"`
	SBOM        *FileRef   `json:"sbom,omitempty"`
	ReleaseKey  ReleaseKey `json:"release_key"`
	Signature   string     `json:"signature,omitempty"`
}

// KeyFile is the on-disk private signing key format. Private key material is
// a 32-byte Ed25519 seed in base64url form and must stay offline.
type KeyFile struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	Kind       string `json:"kind"`
	KeyID      string `json:"key_id"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key,omitempty"`
	CreatedAt  string `json:"created_at"`
}
