package hosted

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/sidenor/toget/internal/account"
)

const (
	// MasterKeySize is the account master key size.
	MasterKeySize = 32
	// RecoveryKeySize is the raw recovery credential key size.
	RecoveryKeySize = 32
	// BlobVersion is the current sealed blob format.
	BlobVersion = 1
	// BlobAlgorithmXChaCha20Poly1305 names the only blob suite in v1.
	BlobAlgorithmXChaCha20Poly1305 = "xchacha20poly1305"

	// RecoveryCredentialVersion is the current official recovery file format.
	RecoveryCredentialVersion = 1
	// RecoveryCredentialKind identifies an official account recovery file.
	RecoveryCredentialKind = "toget-account-recovery"
	// MaxRecoveryCredentialBytes bounds a recovery file.
	MaxRecoveryCredentialBytes = 16 << 10

	keyBundleVersion = 1
)

var (
	// ErrInvalidBlob means a sealed blob is malformed or fails authentication.
	ErrInvalidBlob = errors.New("invalid encrypted blob")
	// ErrInvalidRecoveryCredential means a recovery file is malformed or does
	// not match the account or host.
	ErrInvalidRecoveryCredential = errors.New("invalid recovery credential")
)

func exportWrapAAD(accountID string) string {
	return "toget:hosted-export-wrap:v1:" + accountID
}

func recoveryWrapAAD(accountID string) string {
	return "toget:hosted-recovery-wrap:v1:" + accountID
}

func keyBundleAAD(accountID string, generation int) string {
	return fmt.Sprintf("toget:hosted-key-bundle:v1:%s:%d", accountID, generation)
}

// KeyBundle is the account signing material encrypted under the account
// master key. It never leaves a device in plaintext.
type KeyBundle struct {
	Version           int       `json:"version"`
	AccountID         string    `json:"account_id"`
	AccountPrivateKey []byte    `json:"account_private_key"`
	KeyGeneration     int       `json:"key_generation"`
	KeyRotations      []string  `json:"key_rotations,omitempty"`
	Name              string    `json:"name"`
	CreatedAt         time.Time `json:"created_at"`
}

// NewKeyBundle builds a bundle from an account private key.
func NewKeyBundle(accountID, name string, privateKey ed25519.PrivateKey, generation int, rotations []string, createdAt time.Time) (*KeyBundle, error) {
	if !digits(accountID, 14) || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid account key bundle")
	}
	if generation <= 0 {
		generation = 1
	}
	return &KeyBundle{
		Version: keyBundleVersion, AccountID: accountID,
		AccountPrivateKey: append([]byte(nil), privateKey...),
		KeyGeneration:     generation,
		KeyRotations:      append([]string(nil), rotations...),
		Name:              strings.TrimSpace(name),
		CreatedAt:         createdAt.UTC(),
	}, nil
}

func (b *KeyBundle) validate(accountID string) error {
	if b == nil || b.Version != keyBundleVersion || b.AccountID != accountID ||
		len(b.AccountPrivateKey) != ed25519.PrivateKeySize || b.KeyGeneration < 1 {
		return errors.New("invalid account key bundle")
	}
	return nil
}

// SealBlob encrypts plaintext with a 32-byte key.
func SealBlob(key, plaintext []byte, aad string) (SealedBlob, error) {
	if len(key) != chacha20poly1305.KeySize {
		return SealedBlob{}, errors.New("invalid blob key")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return SealedBlob{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return SealedBlob{}, fmt.Errorf("generate blob nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, []byte(aad))
	return SealedBlob{
		Version: BlobVersion, Algorithm: BlobAlgorithmXChaCha20Poly1305,
		Nonce: nonce, Ciphertext: ciphertext,
	}, nil
}

// OpenBlob decrypts a sealed blob with a 32-byte key.
func OpenBlob(key []byte, blob SealedBlob, aad string) ([]byte, error) {
	if len(key) != chacha20poly1305.KeySize || blob.Version != BlobVersion ||
		blob.Algorithm != BlobAlgorithmXChaCha20Poly1305 ||
		len(blob.Nonce) != chacha20poly1305.NonceSizeX || len(blob.Ciphertext) > MaxBlobLength {
		return nil, ErrInvalidBlob
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, ErrInvalidBlob
	}
	plaintext, err := aead.Open(nil, blob.Nonce, blob.Ciphertext, []byte(aad))
	if err != nil {
		return nil, ErrInvalidBlob
	}
	return plaintext, nil
}

func deriveKey(secret []byte, accountID, purpose string) ([]byte, error) {
	if len(secret) == 0 {
		return nil, errors.New("empty key material")
	}
	info := []byte(purpose)
	reader := hkdf.New(sha256.New, secret, []byte(accountID), info)
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("derive blob key: %w", err)
	}
	return key, nil
}

// WrapExportKey encrypts MK under the OPAQUE login export key.
func WrapExportKey(exportKey, masterKey []byte, accountID string) (SealedBlob, error) {
	if len(masterKey) != MasterKeySize {
		return SealedBlob{}, errors.New("invalid master key")
	}
	key, err := deriveKey(exportKey, accountID, "toget:hosted-export-key:v1")
	if err != nil {
		return SealedBlob{}, err
	}
	defer wipe(key)
	return SealBlob(key, masterKey, exportWrapAAD(accountID))
}

// OpenExportWrap decrypts MK with the OPAQUE login export key.
func OpenExportWrap(exportKey []byte, blob SealedBlob, accountID string) ([]byte, error) {
	key, err := deriveKey(exportKey, accountID, "toget:hosted-export-key:v1")
	if err != nil {
		return nil, err
	}
	defer wipe(key)
	masterKey, err := OpenBlob(key, blob, exportWrapAAD(accountID))
	if err != nil || len(masterKey) != MasterKeySize {
		return nil, ErrInvalidBlob
	}
	return masterKey, nil
}

// WrapRecoveryKey encrypts MK under a high-entropy recovery key.
func WrapRecoveryKey(recoveryKey, masterKey []byte, accountID string) (SealedBlob, error) {
	if len(recoveryKey) != RecoveryKeySize || len(masterKey) != MasterKeySize {
		return SealedBlob{}, errors.New("invalid recovery key material")
	}
	key, err := deriveKey(recoveryKey, accountID, "toget:hosted-recovery-key:v1")
	if err != nil {
		return SealedBlob{}, err
	}
	defer wipe(key)
	return SealBlob(key, masterKey, recoveryWrapAAD(accountID))
}

// OpenRecoveryWrap decrypts MK with a recovery key.
func OpenRecoveryWrap(recoveryKey []byte, blob SealedBlob, accountID string) ([]byte, error) {
	if len(recoveryKey) != RecoveryKeySize {
		return nil, ErrInvalidRecoveryCredential
	}
	key, err := deriveKey(recoveryKey, accountID, "toget:hosted-recovery-key:v1")
	if err != nil {
		return nil, err
	}
	defer wipe(key)
	masterKey, err := OpenBlob(key, blob, recoveryWrapAAD(accountID))
	if err != nil || len(masterKey) != MasterKeySize {
		return nil, ErrInvalidRecoveryCredential
	}
	return masterKey, nil
}

// SealKeyBundle encrypts the account signing bundle under MK.
func SealKeyBundle(masterKey []byte, bundle *KeyBundle) (SealedBlob, error) {
	if len(masterKey) != MasterKeySize || bundle == nil {
		return SealedBlob{}, errors.New("invalid key bundle material")
	}
	if err := bundle.validate(bundle.AccountID); err != nil {
		return SealedBlob{}, err
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return SealedBlob{}, err
	}
	defer wipe(data)
	key, err := deriveKey(masterKey, bundle.AccountID, "toget:hosted-key-bundle-key:v1")
	if err != nil {
		return SealedBlob{}, err
	}
	defer wipe(key)
	return SealBlob(key, data, keyBundleAAD(bundle.AccountID, bundle.KeyGeneration))
}

// OpenKeyBundle decrypts the account signing bundle with MK. generation is
// the account key generation advertised by the host and is bound into the AAD.
func OpenKeyBundle(masterKey []byte, blob SealedBlob, accountID string, generation int) (*KeyBundle, error) {
	if len(masterKey) != MasterKeySize || generation < 1 {
		return nil, ErrInvalidBlob
	}
	key, err := deriveKey(masterKey, accountID, "toget:hosted-key-bundle-key:v1")
	if err != nil {
		return nil, err
	}
	defer wipe(key)
	plaintext, err := OpenBlob(key, blob, keyBundleAAD(accountID, generation))
	if err != nil {
		return nil, ErrInvalidBlob
	}
	var bundle KeyBundle
	dec := json.NewDecoder(bytes.NewReader(plaintext))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&bundle); err != nil {
		return nil, ErrInvalidBlob
	}
	if err := bundle.validate(accountID); err != nil || bundle.KeyGeneration != generation {
		return nil, ErrInvalidBlob
	}
	return &bundle, nil
}

// NewMasterKey generates an account master key.
func NewMasterKey() ([]byte, error) {
	key := make([]byte, MasterKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate account master key: %w", err)
	}
	return key, nil
}

// NewRecoveryKey generates a recovery credential key.
func NewRecoveryKey() ([]byte, error) {
	key := make([]byte, RecoveryKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate recovery key: %w", err)
	}
	return key, nil
}

// RecoveryCredential is the offline bearer file for an official-hosted
// account. It unwraps MK after a forgotten password, but only together with
// the host's recovery-wrap ciphertext.
type RecoveryCredential struct {
	Version   int       `json:"version"`
	Kind      string    `json:"kind"`
	AccountID string    `json:"account_id"`
	HostURL   string    `json:"host_url"`
	CreatedAt time.Time `json:"created_at"`
	Key       string    `json:"key"`
	Checksum  string    `json:"checksum"`
}

// Filename returns the suggested download name.
func (c *RecoveryCredential) Filename() string {
	return "toget-account-recovery-" + c.AccountID + ".togetkey"
}

// Encode returns the file content.
func (c *RecoveryCredential) Encode() ([]byte, error) {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode recovery credential: %w", err)
	}
	return append(data, '\n'), nil
}

func recoveryChecksum(c *RecoveryCredential, key []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\n%s\n%s\n%s\n%s\n",
		c.Version, c.Kind, c.AccountID, c.HostURL, c.CreatedAt.UTC().Format(time.RFC3339Nano))
	h.Write(key)
	return hex.EncodeToString(h.Sum(nil))
}

// NewRecoveryCredential creates a fresh recovery key and file. The caller must
// wrap MK with the returned key before completing registration.
func NewRecoveryCredential(accountID, hostURL string, now time.Time) (*RecoveryCredential, []byte, error) {
	if !digits(accountID, 14) || !account.ValidHostURL(hostURL) || strings.TrimSpace(hostURL) == "" {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	key, err := NewRecoveryKey()
	if err != nil {
		return nil, nil, err
	}
	credential := &RecoveryCredential{
		Version: RecoveryCredentialVersion, Kind: RecoveryCredentialKind,
		AccountID: accountID, HostURL: strings.TrimSpace(hostURL),
		CreatedAt: now.UTC().Truncate(time.Second),
		Key:       base64.RawURLEncoding.EncodeToString(key),
	}
	credential.Checksum = recoveryChecksum(credential, key)
	return credential, key, nil
}

// ParseRecoveryCredential validates a recovery file and returns its key.
func ParseRecoveryCredential(data []byte) (*RecoveryCredential, []byte, error) {
	if len(data) == 0 || len(data) > MaxRecoveryCredentialBytes {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c RecoveryCredential
	if err := dec.Decode(&c); err != nil {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	if c.Version != RecoveryCredentialVersion || c.Kind != RecoveryCredentialKind ||
		!digits(c.AccountID, 14) || !account.ValidHostURL(c.HostURL) || strings.TrimSpace(c.HostURL) == "" ||
		c.CreatedAt.IsZero() {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	key, err := base64.RawURLEncoding.DecodeString(c.Key)
	if err != nil || len(key) != RecoveryKeySize {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	want, err := hex.DecodeString(c.Checksum)
	if err != nil || len(want) != sha256.Size {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	got := recoveryChecksum(&c, key)
	if subtle.ConstantTimeCompare([]byte(c.Checksum), []byte(got)) != 1 {
		return nil, nil, ErrInvalidRecoveryCredential
	}
	return &c, key, nil
}

func digits(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func wipe(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
