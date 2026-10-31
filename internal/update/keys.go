package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const clockSkew = 5 * time.Minute

var keyEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// SigningKey is an Ed25519 key used to certify releases or sign manifests.
type SigningKey struct {
	Kind      string
	KeyID     string
	Public    ed25519.PublicKey
	Private   ed25519.PrivateKey
	CreatedAt time.Time
}

// Verifier checks documents against the embedded root trust anchor.
type Verifier struct {
	Root ed25519.PublicKey
	Now  func() time.Time
}

// NewVerifier returns a verifier for the given root public key.
func NewVerifier(root ed25519.PublicKey) *Verifier {
	return &Verifier{Root: append(ed25519.PublicKey(nil), root...), Now: time.Now}
}

func (v *Verifier) now() time.Time {
	if v != nil && v.Now != nil {
		return v.Now().UTC()
	}
	return time.Now().UTC()
}

func keyID(prefix string, public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return prefix + ":" + keyEncoding.EncodeToString(sum[:])
}

func validateKind(kind string) error {
	switch kind {
	case KindRoot, KindRelease:
		return nil
	default:
		return fmt.Errorf("key kind must be %q or %q", KindRoot, KindRelease)
	}
}

// GenerateSigningKey creates a new root or release key.
func GenerateSigningKey(kind string) (*SigningKey, error) {
	if err := validateKind(kind); err != nil {
		return nil, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate %s key: %w", kind, err)
	}
	prefix := ReleaseKeyPrefix
	if kind == KindRoot {
		prefix = RootKeyPrefix
	}
	return &SigningKey{
		Kind:      kind,
		KeyID:     keyID(prefix, public),
		Public:    public,
		Private:   private,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}, nil
}

// KeyFile returns the serializable private key file.
func (k *SigningKey) KeyFile() (KeyFile, error) {
	if k == nil || len(k.Private) != ed25519.PrivateKeySize || len(k.Public) != ed25519.PublicKeySize {
		return KeyFile{}, errors.New("invalid signing key")
	}
	if k.Kind != KindRoot && k.Kind != KindRelease {
		return KeyFile{}, errors.New("invalid signing key kind")
	}
	prefix := ReleaseKeyPrefix
	if k.Kind == KindRoot {
		prefix = RootKeyPrefix
	}
	if want := keyID(prefix, k.Public); k.KeyID != want {
		return KeyFile{}, errors.New("signing key id does not match its public key")
	}
	return KeyFile{
		Format:     FormatKeyFile,
		Version:    CurrentVersion,
		Kind:       k.Kind,
		KeyID:      k.KeyID,
		PublicKey:  encodeBase64(k.Public),
		PrivateKey: encodeBase64(k.Private.Seed()),
		CreatedAt:  formatTime(k.CreatedAt),
	}, nil
}

// Save writes a private key file with mode 0600. It refuses to overwrite an
// existing file unless force is true.
func (k *SigningKey) Save(path string, force bool) error {
	file, err := k.KeyFile()
	if err != nil {
		return err
	}
	data, err := marshalDocument(file)
	if err != nil {
		return fmt.Errorf("encode key file: %w", err)
	}
	data = append(data, '\n')
	if strings.TrimSpace(path) == "" {
		return errors.New("empty key file path")
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return fmt.Errorf("create key file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write key file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync key file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close key file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure key file: %w", err)
	}
	return nil
}

// LoadSigningKey reads a private key file.
func LoadSigningKey(path string) (*SigningKey, error) {
	data, err := readLimitedFile(path, MaxKeyFileBytes)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err == nil && info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("key file %s must be mode 0600", path)
		}
	}
	var file KeyFile
	if err := decodeStrict(data, &file); err != nil {
		return nil, fmt.Errorf("parse key file: %w", err)
	}
	if file.Format != FormatKeyFile || file.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported key file format %q version %d", file.Format, file.Version)
	}
	if err := validateKind(file.Kind); err != nil {
		return nil, err
	}
	if file.PrivateKey == "" {
		return nil, errors.New("key file has no private key")
	}
	seed, err := decodeBase64(file.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("private key must be %d bytes", ed25519.SeedSize)
	}
	public, err := decodeBase64(file.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(public) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be %d bytes", ed25519.PublicKeySize)
	}
	private := ed25519.NewKeyFromSeed(seed)
	if !ed25519.PublicKey(public).Equal(private.Public().(ed25519.PublicKey)) {
		return nil, errors.New("private key does not match its public key")
	}
	prefix := ReleaseKeyPrefix
	if file.Kind == KindRoot {
		prefix = RootKeyPrefix
	}
	wantID := keyID(prefix, public)
	if file.KeyID != wantID {
		return nil, errors.New("key id does not match its public key")
	}
	createdAt := time.Time{}
	if file.CreatedAt != "" {
		createdAt, err = parseTime("created_at", file.CreatedAt)
		if err != nil {
			return nil, err
		}
	}
	return &SigningKey{
		Kind:      file.Kind,
		KeyID:     wantID,
		Public:    ed25519.PublicKey(public),
		Private:   private,
		CreatedAt: createdAt,
	}, nil
}

// ParseTrustAnchor parses a root public key from a key file, a base64url
// string, or a path to either.
func ParseTrustAnchor(value string) (ed25519.PublicKey, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil, errors.New("empty release root key")
	}
	if info, err := os.Stat(raw); err == nil && !info.IsDir() {
		if info.Size() > MaxKeyFileBytes {
			return nil, errors.New("release root key file is too large")
		}
		data, err := os.ReadFile(raw)
		if err != nil {
			return nil, fmt.Errorf("read release root key: %w", err)
		}
		return parseTrustAnchorData(data)
	}
	if strings.HasPrefix(raw, "{") {
		return parseTrustAnchorData([]byte(raw))
	}
	if len(raw) > 4096 {
		return nil, errors.New("release root key is too long")
	}
	return decodePublicKey(raw)
}

func parseTrustAnchorData(data []byte) (ed25519.PublicKey, error) {
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "{") {
		return decodePublicKey(trimmed)
	}
	var file KeyFile
	if err := decodeStrict([]byte(trimmed), &file); err != nil {
		return nil, fmt.Errorf("parse release root key file: %w", err)
	}
	if file.Format != FormatKeyFile || file.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported key file format %q version %d", file.Format, file.Version)
	}
	if file.Kind != KindRoot {
		return nil, fmt.Errorf("key file is a %s key, not a root key", file.Kind)
	}
	public, err := decodeBase64(file.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode root public key: %w", err)
	}
	if len(public) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("root public key must be %d bytes", ed25519.PublicKeySize)
	}
	if want := keyID(RootKeyPrefix, ed25519.PublicKey(public)); file.KeyID != "" && file.KeyID != want {
		return nil, errors.New("root key id does not match its public key")
	}
	return ed25519.PublicKey(public), nil
}

func decodePublicKey(value string) (ed25519.PublicKey, error) {
	public, err := decodeBase64(value)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(public) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be %d bytes", ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(public), nil
}

// CertifyRelease signs a release public key with the root key.
func CertifyRelease(root *SigningKey, releasePublic ed25519.PublicKey, notBefore, notAfter time.Time) (ReleaseKey, error) {
	if root == nil || root.Kind != KindRoot || len(root.Private) != ed25519.PrivateKeySize {
		return ReleaseKey{}, errors.New("a root signing key is required")
	}
	if len(releasePublic) != ed25519.PublicKeySize {
		return ReleaseKey{}, fmt.Errorf("release public key must be %d bytes", ed25519.PublicKeySize)
	}
	if notAfter.IsZero() || !notAfter.After(notBefore) {
		return ReleaseKey{}, errors.New("release key validity must end after it begins")
	}
	cert := ReleaseKey{
		Format:    FormatReleaseKey,
		Version:   CurrentVersion,
		KeyID:     keyID(ReleaseKeyPrefix, releasePublic),
		PublicKey: encodeBase64(releasePublic),
		Root:      root.KeyID,
		NotBefore: formatTime(notBefore),
		NotAfter:  formatTime(notAfter),
	}
	if cert.Root != keyID(RootKeyPrefix, root.Public) {
		return ReleaseKey{}, errors.New("root key id does not match its public key")
	}
	signature, err := signDocument("toget-release-key:v1", cert.signingView(), root.Private)
	if err != nil {
		return ReleaseKey{}, err
	}
	cert.Signature = encodeBase64(signature)
	return cert, nil
}

// VerifyReleaseKey checks a certificate against the root trust anchor and
// returns its public key. The certificate must be valid now.
func (v *Verifier) VerifyReleaseKey(cert ReleaseKey) (ed25519.PublicKey, error) {
	return v.VerifyReleaseKeyAt(cert, v.now())
}

// VerifyReleaseKeyAt checks a certificate against the root trust anchor and
// returns its public key. Manifests and release indexes are checked at their
// publication time so archived artifacts remain verifiable after a release
// certificate expires. Channel pointers are checked at the current time so an
// expired key cannot keep a channel alive.
func (v *Verifier) VerifyReleaseKeyAt(cert ReleaseKey, at time.Time) (ed25519.PublicKey, error) {
	if v == nil || len(v.Root) != ed25519.PublicKeySize {
		return nil, errors.New("release root key is not configured")
	}
	if cert.Format != FormatReleaseKey || cert.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported release key format %q version %d", cert.Format, cert.Version)
	}
	public, err := decodeBase64(cert.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode release public key: %w", err)
	}
	if len(public) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("release public key must be %d bytes", ed25519.PublicKeySize)
	}
	if want := keyID(ReleaseKeyPrefix, ed25519.PublicKey(public)); cert.KeyID != want {
		return nil, errors.New("release key id does not match its public key")
	}
	if cert.Root != keyID(RootKeyPrefix, v.Root) {
		return nil, errors.New("release key is not certified by this root")
	}
	notBefore, err := parseTime("release key not_before", cert.NotBefore)
	if err != nil {
		return nil, err
	}
	notAfter, err := parseTime("release key not_after", cert.NotAfter)
	if err != nil {
		return nil, err
	}
	at = at.UTC()
	if at.Before(notBefore.Add(-clockSkew)) {
		return nil, errors.New("release key is not valid yet")
	}
	if !at.Before(notAfter.Add(clockSkew)) {
		return nil, errors.New("release key has expired")
	}
	signature, err := decodeBase64(cert.Signature)
	if err != nil {
		return nil, fmt.Errorf("decode release key signature: %w", err)
	}
	if !ed25519.Verify(v.Root, signingMessage("toget-release-key:v1", cert.signingView()), signature) {
		return nil, errors.New("release key signature is invalid")
	}
	return ed25519.PublicKey(public), nil
}

// certificateCovers reports whether a document published at the given time
// falls inside the certificate window. Signing tools use it to refuse
// documents that clients would reject.
func certificateCovers(cert ReleaseKey, at time.Time) error {
	notBefore, err := parseTime("release key not_before", cert.NotBefore)
	if err != nil {
		return err
	}
	notAfter, err := parseTime("release key not_after", cert.NotAfter)
	if err != nil {
		return err
	}
	at = at.UTC()
	if at.Before(notBefore) || at.After(notAfter) {
		return fmt.Errorf("release certificate is valid from %s to %s, not at %s",
			notBefore.Format(time.RFC3339), notAfter.Format(time.RFC3339), at.Format(time.RFC3339))
	}
	return nil
}

// SignChannel signs a channel pointer with a release key.
func SignChannel(key *SigningKey, channel *Channel) error {
	if key == nil || key.Kind != KindRelease || len(key.Private) != ed25519.PrivateKeySize {
		return errors.New("a release signing key is required")
	}
	if channel == nil {
		return errors.New("channel is required")
	}
	if err := checkReleaseKeyMatches(key, channel.ReleaseKey); err != nil {
		return err
	}
	signature, err := signDocument("toget-release-channel:v1", channel.signingView(), key.Private)
	if err != nil {
		return err
	}
	channel.Signature = encodeBase64(signature)
	return nil
}

// SignManifest signs a release manifest with a release key.
func SignManifest(key *SigningKey, manifest *Manifest) error {
	if key == nil || key.Kind != KindRelease || len(key.Private) != ed25519.PrivateKeySize {
		return errors.New("a release signing key is required")
	}
	if manifest == nil {
		return errors.New("manifest is required")
	}
	if err := checkReleaseKeyMatches(key, manifest.ReleaseKey); err != nil {
		return err
	}
	signature, err := signDocument("toget-release-manifest:v1", manifest.signingView(), key.Private)
	if err != nil {
		return err
	}
	manifest.Signature = encodeBase64(signature)
	return nil
}

func checkReleaseKeyMatches(key *SigningKey, cert ReleaseKey) error {
	if cert.Format != FormatReleaseKey || cert.Version != CurrentVersion {
		return errors.New("release key certificate is missing or has an unsupported format")
	}
	public, err := decodeBase64(cert.PublicKey)
	if err != nil {
		return fmt.Errorf("decode release public key: %w", err)
	}
	if !ed25519.PublicKey(public).Equal(key.Public) {
		return errors.New("release key does not match the certificate")
	}
	if cert.KeyID != key.KeyID {
		return errors.New("release key id does not match the certificate")
	}
	return nil
}

func (cert ReleaseKey) signingView() ReleaseKey {
	cert.Signature = ""
	return cert
}

func signDocument(domain string, value any, private ed25519.PrivateKey) ([]byte, error) {
	message, err := signingMessageBytes(domain, value)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(private, message), nil
}

func signingMessage(domain string, value any) []byte {
	message, _ := signingMessageBytes(domain, value)
	return message
}

func signingMessageBytes(domain string, value any) ([]byte, error) {
	raw, err := marshalDocument(value)
	if err != nil {
		return nil, fmt.Errorf("encode signed document: %w", err)
	}
	sum := sha256.Sum256(raw)
	message := make([]byte, 0, len(domain)+1+len(sum))
	message = append(message, domain...)
	message = append(message, '\n')
	message = append(message, sum[:]...)
	return message, nil
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", filepath.Clean(path))
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s is too large", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is too large", path)
	}
	return data, nil
}
