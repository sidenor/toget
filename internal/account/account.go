// Package account creates and loads the local account, its server identity,
// and its first device.
package account

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/vault"
)

var (
	// ErrNotFound means this node has no account yet.
	ErrNotFound = errors.New("account not found")
	// ErrAlreadyExists means this node already has an account.
	ErrAlreadyExists = errors.New("account already exists")
	// ErrInvalidName means the display name is not usable.
	ErrInvalidName = errors.New("invalid account name")
	// ErrInvalidPassword means the password is too short or too long.
	ErrInvalidPassword = errors.New("invalid password")
	// ErrInvalidCredentials means password verification failed.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrCurrentDevice means the current device cannot revoke itself.
	ErrCurrentDevice = errors.New("cannot revoke the current device")
	// ErrInvalidImport means recovered account material is malformed or does
	// not match its own keys and certificates.
	ErrInvalidImport = errors.New("invalid account import")
	// ErrInvalidHostURL means an official account host address is not a
	// pinned HTTPS origin.
	ErrInvalidHostURL = errors.New("invalid account host address")
	// ErrHostImmutable means an account that already has an official host
	// cannot be silently moved to another one.
	ErrHostImmutable = errors.New("account host cannot be changed")
)

const (
	aadServerKey           = "toget:server-key:v1"
	aadAccountKey          = "toget:account-key:v1"
	aadDeviceKey           = "toget:device-key:v1"
	aadDeviceEncryptionKey = "toget:device-encryption-key:v1"

	minPasswordLength = 8
	maxPasswordLength = 1024
)

// Argon2id parameters. Tests may lower these for speed.
var (
	argonMemory      uint32 = 64 * 1024
	argonIterations  uint32 = 3
	argonParallelism uint8  = 2
	argonSaltLength         = 16
	argonKeyLength   uint32 = 32
)

// Server is the local account host.
type Server struct {
	ID         string    `json:"id"`
	Code       string    `json:"code"`
	PublicKey  []byte    `json:"-"`
	PrivateKey []byte    `json:"-"`
	Nonce      []byte    `json:"-"`
	Manifest   string    `json:"manifest"`
	CreatedAt  time.Time `json:"created_at"`
}

// Account is the person this node belongs to.
type Account struct {
	ID string `json:"id"`
	// ServerID is the local server identity. It is the home for rooms created
	// on this machine, including rooms created after joining through pairing.
	ServerID string `json:"server_id"`
	// OriginServerID is the server identity that created the account. It never
	// changes and is the discovery anchor for account-record sync.
	OriginServerID string `json:"origin_server_id"`
	// HostURL is the pinned official account host for an official-hosted
	// account. It is empty for an account hosted on this machine. When set it
	// is authoritative for account-record sync; the host is not discovered
	// from the account ID prefix, because one official host serves many
	// accounts.
	HostURL string `json:"host_url,omitempty"`
	// KeyGeneration starts at 1 and increases with every account-key rotation.
	// KeyRotations is the signed chain from generation 1 to the current key.
	KeyGeneration int       `json:"key_generation"`
	KeyRotations  []string  `json:"-"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	PublicKey     []byte    `json:"-"`
	PrivateKey    []byte    `json:"-"`
	PasswordHash  string    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
}

// Device is the machine running this node.
type Device struct {
	ID                   string `json:"id"`
	AccountID            string `json:"account_id"`
	Name                 string `json:"name"`
	PublicKey            []byte `json:"-"`
	PrivateKey           []byte `json:"-"`
	EncryptionPublicKey  []byte `json:"-"`
	EncryptionPrivateKey []byte `json:"-"`
	Certificate          string `json:"certificate"`
	// NodeURL is the device's last known pinned node address, including its
	// TLS fingerprint and optional relay route. It is only used to reach
	// sibling devices for account-record sync; it grants no trust by itself.
	NodeURL    string     `json:"node_url,omitempty"`
	Role       string     `json:"role"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	// Local marks the device running this node. A paired device stores its
	// siblings' public certificates but never their private keys.
	Local bool `json:"local,omitempty"`
}

// Snapshot is the local account state.
type Snapshot struct {
	Account *Account `json:"account"`
	Server  *Server  `json:"server"`
	Device  *Device  `json:"device"`
}

// Store persists account data.
type Store interface {
	GetServer(ctx context.Context) (*Server, error)
	GetAccount(ctx context.Context) (*Account, error)
	GetDevice(ctx context.Context, accountID string) (*Device, error)
	GetDeviceByID(ctx context.Context, deviceID string) (*Device, error)
	ListDevices(ctx context.Context, accountID string) ([]Device, error)
	CreateAccount(ctx context.Context, server *Server, account *Account, device *Device) error
	ImportAccount(ctx context.Context, server *Server, account *Account, devices []Device) error
	UpdateAccountKey(ctx context.Context, account *Account, devices []Device) error
	UpdateAccountPasswordHash(ctx context.Context, accountID, passwordHash string) error
	UpdateAccountHost(ctx context.Context, accountID, hostURL string) error
	AddDevice(ctx context.Context, device *Device) error
	UpdateDevice(ctx context.Context, device *Device) error
	AccountCodeExists(ctx context.Context, serverID, code string) (bool, error)
}

// Service owns account creation and local password checks.
type Service struct {
	store Store
	vault *vault.Vault
	now   func() time.Time
	// passwordMu serializes password and recovery mutations so the database
	// verifier and the vault key wrap cannot diverge under concurrent
	// requests.
	passwordMu sync.Mutex
}

// New creates an account service.
func New(store Store, v *vault.Vault) *Service {
	return &Service{store: store, vault: v, now: time.Now}
}

// Create makes the local account, its server identity, and its first device.
func (s *Service) Create(ctx context.Context, name, password, deviceName string) (*Snapshot, error) {
	name = strings.TrimSpace(name)
	deviceName = strings.TrimSpace(deviceName)
	if name == "" || len(name) > 64 {
		return nil, ErrInvalidName
	}
	if len(password) < minPasswordLength || len(password) > maxPasswordLength {
		return nil, ErrInvalidPassword
	}
	if deviceName == "" {
		deviceName = "This device"
	}
	if len(deviceName) > 64 {
		deviceName = deviceName[:64]
	}

	if _, err := s.store.GetAccount(ctx); err == nil {
		return nil, ErrAlreadyExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err := s.vault.Initialize(password); err != nil {
		return nil, err
	}

	now := s.now().UTC().Truncate(time.Millisecond)

	server, err := s.loadOrCreateServer(ctx, now)
	if err != nil {
		return nil, err
	}

	accountCode, err := s.newAccountCode(ctx, server.ID)
	if err != nil {
		return nil, err
	}

	accountPub, accountPriv, err := identity.NewAccountKey()
	if err != nil {
		return nil, err
	}
	devicePub, devicePriv, err := identity.NewDeviceKey()
	if err != nil {
		return nil, err
	}
	encryptionPub, encryptionPriv, err := identity.NewEncryptionKey()
	if err != nil {
		return nil, err
	}
	deviceID := identity.NewID()
	accountID := identity.AccountID(server.Code, accountCode)

	cert, err := identity.SignDeviceCertificate(deviceID, accountID, devicePub, encryptionPub, accountPriv, now)
	if err != nil {
		return nil, err
	}

	passwordHash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}

	sealedAccountKey, err := s.vault.Seal(accountPriv, aadAccountKey)
	if err != nil {
		return nil, fmt.Errorf("seal account key: %w", err)
	}
	sealedDeviceKey, err := s.vault.Seal(devicePriv, aadDeviceKey)
	if err != nil {
		return nil, fmt.Errorf("seal device key: %w", err)
	}
	sealedEncryptionKey, err := s.vault.Seal(encryptionPriv, aadDeviceEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("seal device encryption key: %w", err)
	}

	account := &Account{
		ID:             accountID,
		ServerID:       server.ID,
		OriginServerID: server.ID,
		KeyGeneration:  1,
		Code:           accountCode,
		Name:           name,
		PublicKey:      accountPub,
		PrivateKey:     []byte(sealedAccountKey),
		PasswordHash:   passwordHash,
		CreatedAt:      now,
	}
	device := &Device{
		ID:                   deviceID,
		AccountID:            accountID,
		Name:                 deviceName,
		PublicKey:            devicePub,
		PrivateKey:           []byte(sealedDeviceKey),
		EncryptionPublicKey:  encryptionPub,
		EncryptionPrivateKey: []byte(sealedEncryptionKey),
		Certificate:          string(cert),
		Role:                 "node",
		CreatedAt:            now,
		Local:                true,
	}

	if err := s.store.CreateAccount(ctx, server, account, device); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return nil, ErrAlreadyExists
		}
		return nil, err
	}
	return &Snapshot{Account: account, Server: server, Device: device}, nil
}

// ImportSpec carries account material recovered from a paired device.
type ImportSpec struct {
	AccountID         string
	AccountCode       string
	Name              string
	AccountPublicKey  []byte
	AccountPrivateKey ed25519.PrivateKey
	PasswordHash      string
	CreatedAt         time.Time
	// OriginServerID is the server that created the account. Older pairing
	// bundles do not carry it; those imports fall back to the local server,
	// which disables account-host bootstrap discovery but not account sync.
	OriginServerID string
	// HostURL is the pinned official account host for an official-hosted
	// account. It is empty for a self-hosted account.
	HostURL string
	// KeyGeneration and KeyRotations carry the account's current key history.
	// Older pairing bundles leave them empty; the account starts at generation
	// 1 with the imported key.
	KeyGeneration int
	KeyRotations  []string
	// Password is the account password. A fresh device needs it to derive the
	// local vault key before it can store the account signing key. It is never
	// persisted.
	Password string
	// Device is this machine's new device identity with plaintext keys. The
	// service seals them with the local vault.
	Device Device
	// Siblings are the account's other devices. They carry certificates and
	// public keys only.
	Siblings []Device
}

// Import stores an account recovered through device pairing. It creates a
// fresh local server identity while preserving the account ID, account code,
// password, and account signing key. The local server becomes the home for
// rooms created on this machine; rooms hosted elsewhere keep their own home.
func (s *Service) Import(ctx context.Context, spec ImportSpec) (*Snapshot, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" || len(name) > 64 {
		return nil, ErrInvalidName
	}
	if !isDigits(spec.AccountID, 14) || !isDigits(spec.AccountCode, 6) ||
		!strings.HasSuffix(spec.AccountID, spec.AccountCode) {
		return nil, fmt.Errorf("%w: account id or code is invalid", ErrInvalidImport)
	}
	if len(spec.AccountPublicKey) != ed25519.PublicKeySize ||
		len(spec.AccountPrivateKey) != ed25519.PrivateKeySize ||
		!bytes.Equal(spec.AccountPrivateKey.Public().(ed25519.PublicKey), spec.AccountPublicKey) {
		return nil, fmt.Errorf("%w: account key material does not match", ErrInvalidImport)
	}
	keyGeneration := spec.KeyGeneration
	if keyGeneration <= 0 {
		keyGeneration = 1
	}
	keyRotations := append([]string(nil), spec.KeyRotations...)
	if keyGeneration == 1 {
		if len(keyRotations) != 0 {
			return nil, fmt.Errorf("%w: account key history does not match its generation", ErrInvalidImport)
		}
	} else {
		key, err := identity.AccountPublicKeyAtGeneration(keyRotations, spec.AccountID, keyGeneration)
		if err != nil || !bytes.Equal(key, spec.AccountPublicKey) {
			return nil, fmt.Errorf("%w: account key history is invalid", ErrInvalidImport)
		}
	}
	if _, _, _, _, _, err := parsePasswordHash(spec.PasswordHash); err != nil {
		return nil, fmt.Errorf("%w: password hash is invalid", ErrInvalidImport)
	}
	if spec.CreatedAt.IsZero() {
		spec.CreatedAt = s.now().UTC().Truncate(time.Millisecond)
	}
	if _, err := s.store.GetAccount(ctx); err == nil {
		return nil, ErrAlreadyExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if !s.vault.Ready() {
		// A fresh device has no local vault key yet. The pairing bundle carries
		// the password hash, so the user must enter the account password before
		// the imported account key can be stored under a password-derived KEK.
		if spec.Password == "" {
			return nil, vault.ErrLocked
		}
		if !VerifyPassword(spec.PasswordHash, spec.Password) {
			return nil, ErrInvalidCredentials
		}
		if err := s.vault.Initialize(spec.Password); err != nil {
			return nil, err
		}
	}

	now := s.now().UTC().Truncate(time.Millisecond)
	server, err := s.loadOrCreateServer(ctx, now)
	if err != nil {
		return nil, err
	}
	originServerID := strings.TrimSpace(spec.OriginServerID)
	if originServerID == "" {
		originServerID = server.ID
	}
	if !identity.ValidServerID(originServerID) {
		return nil, fmt.Errorf("%w: origin server id is invalid", ErrInvalidImport)
	}
	hostURL := strings.TrimSpace(spec.HostURL)
	if !ValidHostURL(hostURL) {
		return nil, fmt.Errorf("%w: account host address is invalid", ErrInvalidImport)
	}
	device := spec.Device
	device.AccountID = spec.AccountID
	device.Name = strings.TrimSpace(device.Name)
	if device.Name == "" || len(device.Name) > 64 {
		return nil, ErrInvalidName
	}
	if device.ID == "" || len(device.PublicKey) != ed25519.PublicKeySize ||
		len(device.PrivateKey) != ed25519.PrivateKeySize ||
		!bytes.Equal(ed25519.PrivateKey(device.PrivateKey).Public().(ed25519.PublicKey), device.PublicKey) ||
		len(device.EncryptionPublicKey) != 32 || len(device.EncryptionPrivateKey) != 32 {
		return nil, fmt.Errorf("%w: device key material is invalid", ErrInvalidImport)
	}
	if device.Role == "" {
		device.Role = "node"
	}
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}
	device.Local = true
	if device.Certificate == "" {
		return nil, fmt.Errorf("%w: device certificate is missing", ErrInvalidImport)
	}
	if !ValidNodeURL(device.NodeURL) {
		return nil, fmt.Errorf("%w: device node address is invalid", ErrInvalidImport)
	}
	devices := []Device{device}
	seen := map[string]bool{device.ID: true}
	for i := range spec.Siblings {
		sibling := spec.Siblings[i]
		sibling.AccountID = spec.AccountID
		sibling.Local = false
		// Sibling private keys never travel during pairing.
		sibling.PrivateKey = nil
		sibling.EncryptionPrivateKey = nil
		if sibling.ID == "" || seen[sibling.ID] ||
			len(sibling.PublicKey) != ed25519.PublicKeySize ||
			len(sibling.EncryptionPublicKey) != 32 || sibling.Certificate == "" ||
			!ValidNodeURL(sibling.NodeURL) {
			return nil, fmt.Errorf("%w: sibling device is invalid", ErrInvalidImport)
		}
		if sibling.Role == "" {
			sibling.Role = "node"
		}
		seen[sibling.ID] = true
		devices = append(devices, sibling)
	}
	for i := range devices {
		if err := verifyDeviceCertificate(&devices[i], spec.AccountPublicKey, spec.AccountID); err != nil {
			return nil, err
		}
	}

	sealedAccountKey, err := s.vault.Seal(spec.AccountPrivateKey, aadAccountKey)
	if err != nil {
		return nil, fmt.Errorf("seal account key: %w", err)
	}
	sealedDeviceKey, err := s.vault.Seal(device.PrivateKey, aadDeviceKey)
	if err != nil {
		return nil, fmt.Errorf("seal device key: %w", err)
	}
	sealedEncryptionKey, err := s.vault.Seal(device.EncryptionPrivateKey, aadDeviceEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("seal device encryption key: %w", err)
	}
	account := &Account{
		ID:             spec.AccountID,
		ServerID:       server.ID,
		OriginServerID: originServerID,
		HostURL:        hostURL,
		KeyGeneration:  keyGeneration,
		KeyRotations:   keyRotations,
		Code:           spec.AccountCode,
		Name:           name,
		PublicKey:      append([]byte(nil), spec.AccountPublicKey...),
		PrivateKey:     []byte(sealedAccountKey),
		PasswordHash:   spec.PasswordHash,
		CreatedAt:      spec.CreatedAt.UTC().Truncate(time.Millisecond),
	}
	devices[0].PrivateKey = []byte(sealedDeviceKey)
	devices[0].EncryptionPrivateKey = []byte(sealedEncryptionKey)
	if err := s.store.ImportAccount(ctx, server, account, devices); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return nil, ErrAlreadyExists
		}
		return nil, err
	}
	device = devices[0]
	return &Snapshot{Account: account, Server: server, Device: &device}, nil
}

func verifyDeviceCertificate(device *Device, accountPublicKey ed25519.PublicKey, accountID string) error {
	if device.AccountID != accountID {
		return fmt.Errorf("%w: device belongs to another account", ErrInvalidImport)
	}
	cert, err := identity.VerifyDeviceCertificate([]byte(device.Certificate), accountPublicKey)
	if err != nil {
		return fmt.Errorf("%w: device certificate: %v", ErrInvalidImport, err)
	}
	if cert.DeviceID != device.ID || cert.AccountID != accountID {
		return fmt.Errorf("%w: device certificate identity does not match", ErrInvalidImport)
	}
	signingKey, err := identity.DecodePublicKey(cert.DevicePubKey)
	if err != nil || !bytes.Equal(signingKey, device.PublicKey) {
		return fmt.Errorf("%w: device signing key does not match its certificate", ErrInvalidImport)
	}
	encryptionKey, err := identity.DecodeEncryptionPublicKey(cert.EncryptionPublicKey)
	if err != nil || !bytes.Equal(encryptionKey, device.EncryptionPublicKey) {
		return fmt.Errorf("%w: device encryption key does not match its certificate", ErrInvalidImport)
	}
	return nil
}

func isDigits(value string, length int) bool {
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

// Get returns the local account snapshot.
func (s *Service) Get(ctx context.Context) (*Snapshot, error) {
	account, err := s.store.GetAccount(ctx)
	if err != nil {
		return nil, err
	}
	server, err := s.store.GetServer(ctx)
	if err != nil {
		return nil, err
	}
	device, err := s.store.GetDevice(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	return &Snapshot{Account: account, Server: server, Device: device}, nil
}

// Login verifies the account password, unlocks the vault, and returns the
// local snapshot. A legacy plaintext vault key is wrapped on the first
// successful login.
func (s *Service) Login(ctx context.Context, password string) (*Snapshot, error) {
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(snapshot.Account.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}
	if err := s.vault.Unlock(password); err != nil {
		if errors.Is(err, vault.ErrNoKey) {
			return nil, fmt.Errorf("vault key is missing: %w", err)
		}
		if errors.Is(err, vault.ErrInvalidPassword) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if s.vault.Legacy() {
		if err := s.vault.WrapWithPassword(password); err != nil {
			return nil, fmt.Errorf("wrap legacy vault key: %w", err)
		}
	}
	return snapshot, nil
}

// LockVault removes the vault master key from memory.
func (s *Service) LockVault() {
	if s != nil && s.vault != nil {
		s.vault.Lock()
	}
}

// VaultReady reports whether secrets can be opened.
func (s *Service) VaultReady() bool {
	return s != nil && s.vault != nil && s.vault.Ready()
}

// ListDevices returns every device on this account.
func (s *Service) ListDevices(ctx context.Context) ([]Device, error) {
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	return s.store.ListDevices(ctx, snapshot.Account.ID)
}

// GetDeviceByID returns one device on the local account.
func (s *Service) GetDeviceByID(ctx context.Context, id string) (*Device, error) {
	return s.store.GetDeviceByID(ctx, id)
}

// AddDevice stores a device whose certificate was signed by the account key.
func (s *Service) AddDevice(ctx context.Context, device *Device) error {
	return s.store.AddDevice(ctx, device)
}

// UpdateDevice persists mutable device fields.
func (s *Service) UpdateDevice(ctx context.Context, device *Device) error {
	return s.store.UpdateDevice(ctx, device)
}

// RefreshCertificates re-signs device certificates that are missing, invalid,
// or within 30 days of expiry. Account keys rarely change, but certificates do
// expire after a year; renewing them keeps paired devices able to register
// with room homes without asking the user to re-pair.
func (s *Service) RefreshCertificates(ctx context.Context) error {
	snapshot, err := s.Get(ctx)
	if err != nil {
		return err
	}
	accountKey, err := s.UnsealPrivateKey(snapshot.Account)
	if err != nil {
		return err
	}
	devices, err := s.store.ListDevices(ctx, snapshot.Account.ID)
	if err != nil {
		return err
	}
	now := s.now().UTC().Truncate(time.Second)
	renewAfter := now.Add(30 * 24 * time.Hour)
	for i := range devices {
		device := &devices[i]
		if device.RevokedAt != nil {
			continue
		}
		var cert identity.DeviceCertificate
		parseErr := json.Unmarshal([]byte(device.Certificate), &cert)
		if parseErr == nil && cert.ExpiresAt.After(renewAfter) && cert.DeviceID == device.ID &&
			cert.AccountID == snapshot.Account.ID {
			continue
		}
		signed, err := identity.SignDeviceCertificate(device.ID, snapshot.Account.ID, device.PublicKey,
			device.EncryptionPublicKey, accountKey, now)
		if err != nil {
			return err
		}
		device.Certificate = string(signed)
		if err := s.store.UpdateDevice(ctx, device); err != nil {
			return err
		}
	}
	return nil
}

// RevokeDevice marks another device on this account as revoked.
func (s *Service) RevokeDevice(ctx context.Context, deviceID string) error {
	snapshot, err := s.Get(ctx)
	if err != nil {
		return err
	}
	if snapshot.Device.ID == deviceID {
		return ErrCurrentDevice
	}
	device, err := s.store.GetDeviceByID(ctx, deviceID)
	if err != nil {
		return err
	}
	if device.AccountID != snapshot.Account.ID {
		return ErrNotFound
	}
	if device.RevokedAt != nil {
		return nil
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	device.RevokedAt = &now
	return s.store.UpdateDevice(ctx, device)
}

// VerifyPassword checks a password against an encoded Argon2id hash.
func VerifyPassword(encoded, password string) bool {
	memory, iterations, parallelism, salt, want, err := parsePasswordHash(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (s *Service) loadOrCreateServer(ctx context.Context, now time.Time) (*Server, error) {
	server, err := s.store.GetServer(ctx)
	if err == nil {
		return server, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	created, err := identity.NewServer(nil)
	if err != nil {
		return nil, err
	}
	sealed, err := s.vault.Seal(created.PrivateKey, aadServerKey)
	if err != nil {
		return nil, fmt.Errorf("seal server key: %w", err)
	}
	return &Server{
		ID:         created.ID,
		Code:       created.Code,
		PublicKey:  created.PublicKey,
		PrivateKey: []byte(sealed),
		Nonce:      created.Nonce,
		Manifest:   string(created.ManifestJSON),
		CreatedAt:  now,
	}, nil
}

func (s *Service) newAccountCode(ctx context.Context, serverID string) (string, error) {
	for range 32 {
		n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return "", fmt.Errorf("generate account code: %w", err)
		}
		code := fmt.Sprintf("%06d", n.Int64())
		exists, err := s.store.AccountCodeExists(ctx, serverID, code)
		if err != nil {
			return "", err
		}
		if !exists {
			return code, nil
		}
	}
	return "", errors.New("could not allocate an account code")
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func parsePasswordHash(encoded string) (uint32, uint32, uint8, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return 0, 0, 0, nil, nil, errors.New("unsupported password hash")
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("parse password parameters: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("decode password salt: %w", err)
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("decode password hash: %w", err)
	}
	if len(salt) == 0 || len(hash) == 0 {
		return 0, 0, 0, nil, nil, errors.New("empty password hash")
	}
	return memory, iterations, parallelism, salt, hash, nil
}

// UnsealServerKey decrypts the node server signing key.
func (s *Service) UnsealServerKey(server *Server) (ed25519.PrivateKey, error) {
	raw, err := s.vault.OpenBytes(server.PrivateKey, aadServerKey)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("server key has the wrong size")
	}
	return ed25519.PrivateKey(raw), nil
}

// UnsealPrivateKey decrypts the account signing key for a trusted local caller.
func (s *Service) UnsealPrivateKey(account *Account) (ed25519.PrivateKey, error) {
	raw, err := s.vault.OpenBytes(account.PrivateKey, aadAccountKey)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("account key has the wrong size")
	}
	return ed25519.PrivateKey(raw), nil
}

// UnsealDeviceKey decrypts a device signing key.
func (s *Service) UnsealDeviceKey(device *Device) (ed25519.PrivateKey, error) {
	raw, err := s.vault.OpenBytes(device.PrivateKey, aadDeviceKey)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("device key has the wrong size")
	}
	return ed25519.PrivateKey(raw), nil
}

// UnsealDeviceEncryptionKey decrypts a device X25519 private key.
func (s *Service) UnsealDeviceEncryptionKey(device *Device) ([]byte, error) {
	raw, err := s.vault.OpenBytes(device.EncryptionPrivateKey, aadDeviceEncryptionKey)
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, errors.New("device encryption key has the wrong size")
	}
	return raw, nil
}
