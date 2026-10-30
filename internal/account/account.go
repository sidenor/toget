// Package account creates and loads the local account, its server identity,
// and its first device.
package account

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
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
	ID           string    `json:"id"`
	ServerID     string    `json:"server_id"`
	Code         string    `json:"code"`
	Name         string    `json:"name"`
	PublicKey    []byte    `json:"-"`
	PrivateKey   []byte    `json:"-"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// Device is the machine running this node.
type Device struct {
	ID                   string     `json:"id"`
	AccountID            string     `json:"account_id"`
	Name                 string     `json:"name"`
	PublicKey            []byte     `json:"-"`
	PrivateKey           []byte     `json:"-"`
	EncryptionPublicKey  []byte     `json:"-"`
	EncryptionPrivateKey []byte     `json:"-"`
	Certificate          string     `json:"certificate"`
	Role                 string     `json:"role"`
	RevokedAt            *time.Time `json:"revoked_at,omitempty"`
	LastSeenAt           *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
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
	AddDevice(ctx context.Context, device *Device) error
	UpdateDevice(ctx context.Context, device *Device) error
	AccountCodeExists(ctx context.Context, serverID, code string) (bool, error)
}

// Service owns account creation and local password checks.
type Service struct {
	store Store
	vault *vault.Vault
	now   func() time.Time
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
		ID:           accountID,
		ServerID:     server.ID,
		Code:         accountCode,
		Name:         name,
		PublicKey:    accountPub,
		PrivateKey:   []byte(sealedAccountKey),
		PasswordHash: passwordHash,
		CreatedAt:    now,
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
	}

	if err := s.store.CreateAccount(ctx, server, account, device); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return nil, ErrAlreadyExists
		}
		return nil, err
	}
	return &Snapshot{Account: account, Server: server, Device: device}, nil
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

// Login verifies the account password and returns the local snapshot.
func (s *Service) Login(ctx context.Context, password string) (*Snapshot, error) {
	snapshot, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(snapshot.Account.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}
	return snapshot, nil
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
