// Package hostedlink connects a local Toget account to an official account
// host. It owns the small amount of orchestration that needs both packages:
// creating an account, adopting one on a new device, changing the official
// password, and rotating the official recovery file.
package hostedlink

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/vault"
)

// Service coordinates official-host operations with the local account store.
type Service struct {
	Accounts *account.Service
	Sessions *hosted.SessionCache
}

// New creates a link service. A nil session cache creates a private one.
func New(accounts *account.Service, sessions *hosted.SessionCache) *Service {
	if sessions == nil {
		sessions = hosted.NewSessionCache()
	}
	return &Service{Accounts: accounts, Sessions: sessions}
}

// RegisterSpec describes a new official-hosted account.
type RegisterSpec struct {
	HostURL    string
	Name       string
	Email      string
	Password   string
	DeviceName string
	NodeURL    string
}

// Register creates the account on the host and imports it locally. The caller
// must present the recovery credential to the user before discarding it.
func (s *Service) Register(ctx context.Context, spec RegisterSpec) (*account.Snapshot, *hosted.RegistrationResult, error) {
	if s == nil || s.Accounts == nil {
		return nil, nil, errors.New("account service is unavailable")
	}
	hostURL := strings.TrimSpace(spec.HostURL)
	name := strings.TrimSpace(spec.Name)
	if hostURL == "" {
		return nil, nil, errors.New("an official account host is required")
	}
	if name == "" || len(name) > 64 {
		return nil, nil, account.ErrInvalidName
	}
	if err := account.ValidatePassword(spec.Password); err != nil {
		return nil, nil, err
	}
	if !account.ValidNodeURL(spec.NodeURL) {
		return nil, nil, fmt.Errorf("%w: node address is invalid", account.ErrInvalidImport)
	}
	client, err := hosted.NewClient(hostURL)
	if err != nil {
		return nil, nil, err
	}
	result, err := client.Register(ctx, hosted.RegisterOptions{
		Name: name, Email: strings.TrimSpace(spec.Email), Password: spec.Password,
		DeviceName: spec.DeviceName, NodeURL: spec.NodeURL,
	})
	if err != nil {
		return nil, nil, err
	}
	passwordHash, err := account.HashPassword(spec.Password)
	if err != nil {
		return nil, nil, err
	}
	deviceName := strings.TrimSpace(result.Device.Name)
	if deviceName == "" {
		deviceName = "This device"
	}
	snapshot, err := s.Accounts.Import(ctx, account.ImportSpec{
		AccountID: result.AccountID, AccountCode: result.AccountCode, Name: result.Name,
		AccountPublicKey:  result.AccountPublicKey,
		AccountPrivateKey: result.AccountPrivateKey,
		PasswordHash:      passwordHash,
		Password:          spec.Password,
		CreatedAt:         result.CreatedAt,
		HostURL:           result.HostURL,
		KeyGeneration:     1,
		Device: account.Device{
			ID: result.Device.ID, Name: deviceName,
			PublicKey: result.Device.PublicKey, PrivateKey: result.Device.PrivateKey,
			EncryptionPublicKey:  result.Device.EncryptionPublicKey,
			EncryptionPrivateKey: result.Device.EncryptionPrivateKey,
			Certificate:          result.Device.Certificate,
			NodeURL:              strings.TrimSpace(spec.NodeURL),
			Role:                 "node", CreatedAt: result.Device.CreatedAt,
		},
	})
	if err != nil {
		return nil, result, fmt.Errorf("account was created on the host but could not be stored locally: %w", err)
	}
	s.cacheSession(result.AccountID, result.SessionToken, result.SessionExpiresAt)
	return snapshot, result, nil
}

// LoginSpec describes adopting an existing official account on this device.
type LoginSpec struct {
	HostURL    string
	AccountID  string
	Password   string
	DeviceName string
	NodeURL    string
}

// Login authenticates with the account password, creates a fresh device
// identity, imports the account locally, and publishes the new device. The
// host password is never stored; the local vault uses the same password.
func (s *Service) Login(ctx context.Context, spec LoginSpec) (*account.Snapshot, *hosted.LoginResult, []string, error) {
	if s == nil || s.Accounts == nil {
		return nil, nil, nil, errors.New("account service is unavailable")
	}
	hostURL := strings.TrimSpace(spec.HostURL)
	accountID := strings.TrimSpace(spec.AccountID)
	if hostURL == "" {
		return nil, nil, nil, errors.New("an official account host is required")
	}
	if !account.ValidNodeURL(spec.NodeURL) {
		return nil, nil, nil, fmt.Errorf("%w: node address is invalid", account.ErrInvalidImport)
	}
	client, err := hosted.NewClient(hostURL)
	if err != nil {
		return nil, nil, nil, err
	}
	login, err := client.Login(ctx, accountID, spec.Password)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(login.AccountPrivateKey) != ed25519.PrivateKeySize ||
		!bytes.Equal(login.AccountPrivateKey.Public().(ed25519.PublicKey), login.PublicKey) {
		return nil, nil, nil, fmt.Errorf("%w: account key material does not match", hosted.ErrInvalidHost)
	}
	passwordHash, err := account.HashPassword(spec.Password)
	if err != nil {
		return nil, nil, nil, err
	}
	deviceName := strings.TrimSpace(spec.DeviceName)
	if deviceName == "" {
		deviceName = "This device"
	}
	if len(deviceName) > 64 {
		deviceName = deviceName[:64]
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	deviceID := identity.NewID()
	devicePub, devicePriv, err := identity.NewDeviceKey()
	if err != nil {
		return nil, nil, nil, err
	}
	encryptionPub, encryptionPriv, err := identity.NewEncryptionKey()
	if err != nil {
		return nil, nil, nil, err
	}
	certificate, err := identity.SignDeviceCertificate(deviceID, accountID, devicePub, encryptionPub,
		login.AccountPrivateKey, now)
	if err != nil {
		return nil, nil, nil, err
	}
	siblings := make([]account.Device, 0, len(login.Devices))
	for i := range login.Devices {
		record := login.Devices[i]
		if record.ID == deviceID || record.RevokedAt != nil || record.Revocation != "" {
			continue
		}
		siblings = append(siblings, account.Device{
			ID: record.ID, AccountID: record.AccountID, Name: record.Name,
			PublicKey: record.PublicKey, EncryptionPublicKey: record.EncryptionPublicKey,
			Certificate: record.Certificate, NodeURL: record.NodeURL,
			Role: record.Role, CreatedAt: record.CreatedAt,
		})
	}
	accountCode := ""
	if len(login.AccountID) >= 6 {
		accountCode = login.AccountID[len(login.AccountID)-6:]
	}
	snapshot, err := s.Accounts.Import(ctx, account.ImportSpec{
		AccountID: login.AccountID, AccountCode: accountCode, Name: login.Name,
		AccountPublicKey:  login.PublicKey,
		AccountPrivateKey: login.AccountPrivateKey,
		PasswordHash:      passwordHash,
		Password:          spec.Password,
		CreatedAt:         login.CreatedAt,
		HostURL:           login.HostURL,
		KeyGeneration:     login.KeyGeneration,
		KeyRotations:      login.KeyRotations,
		Device: account.Device{
			ID: deviceID, Name: deviceName,
			PublicKey: devicePub, PrivateKey: devicePriv,
			EncryptionPublicKey: encryptionPub, EncryptionPrivateKey: encryptionPriv,
			Certificate: string(certificate), NodeURL: strings.TrimSpace(spec.NodeURL),
			Role: "node", CreatedAt: now,
		},
		Siblings: siblings,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	s.cacheSession(login.AccountID, login.SessionToken, login.SessionExpiresAt)
	warnings := s.publishDevice(ctx, client, snapshot, login.SessionToken)
	return snapshot, login, warnings, nil
}

// ChangePassword rotates the official password and keeps this device's local
// vault in step. It authenticates to the host first, so a password changed on
// another device still works here. If the local rewrap fails, it rolls the
// host password back.
func (s *Service) ChangePassword(ctx context.Context, currentPassword, newPassword string) error {
	if s == nil || s.Accounts == nil {
		return errors.New("account service is unavailable")
	}
	if err := account.ValidatePassword(newPassword); err != nil {
		return err
	}
	snapshot, err := s.Accounts.Get(ctx)
	if err != nil {
		return err
	}
	hostURL := strings.TrimSpace(snapshot.Account.HostURL)
	if hostURL == "" {
		_, err := s.Accounts.ChangePassword(ctx, currentPassword, newPassword)
		return err
	}
	client, err := hosted.NewClient(hostURL)
	if err != nil {
		return err
	}
	login, err := client.Login(ctx, snapshot.Account.ID, currentPassword)
	if err != nil {
		return err
	}
	if !account.VerifyPassword(snapshot.Account.PasswordHash, currentPassword) && !s.Accounts.VaultReady() {
		return errors.New("this device's local password is out of sync with the official account; unlock this device first")
	}
	if err := client.ChangePassword(ctx, login.SessionToken, snapshot.Account.ID, newPassword, login.MasterKey); err != nil {
		return err
	}
	s.cacheSession(snapshot.Account.ID, login.SessionToken, login.SessionExpiresAt)
	if err := s.updateLocalPassword(ctx, currentPassword, newPassword); err != nil {
		rollbackErr := s.rollbackHostPassword(ctx, client, snapshot.Account.ID, newPassword, currentPassword)
		if rollbackErr != nil {
			return fmt.Errorf("official password changed to the new value, but this device could not update its local vault: %v (host rollback also failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("could not update this device's local vault; the official password was restored: %w", err)
	}
	return nil
}

func (s *Service) updateLocalPassword(ctx context.Context, currentPassword, newPassword string) error {
	snapshot, err := s.Accounts.Get(ctx)
	if err != nil {
		return err
	}
	if account.VerifyPassword(snapshot.Account.PasswordHash, currentPassword) {
		_, err := s.Accounts.ChangePassword(ctx, currentPassword, newPassword)
		return err
	}
	if s.Accounts.VaultReady() {
		_, err := s.Accounts.ResetPasswordUnlocked(ctx, newPassword)
		return err
	}
	return vault.ErrLocked
}

func (s *Service) rollbackHostPassword(ctx context.Context, client *hosted.Client, accountID, newPassword, currentPassword string) error {
	login, err := client.Login(ctx, accountID, newPassword)
	if err != nil {
		return err
	}
	return client.ChangePassword(ctx, login.SessionToken, accountID, currentPassword, login.MasterKey)
}

// RotateAccountKey rotates the account signing key and publishes the new key
// bundle to the official host. The password is required to unwrap the account
// master key that encrypts the bundle; without it, other devices could not
// obtain the new key after a password login.
func (s *Service) RotateAccountKey(ctx context.Context, password string) (*account.RotateKeyResult, []string, error) {
	if s == nil || s.Accounts == nil {
		return nil, nil, errors.New("account service is unavailable")
	}
	snapshot, err := s.Accounts.Get(ctx)
	if err != nil {
		return nil, nil, err
	}
	hostURL := strings.TrimSpace(snapshot.Account.HostURL)
	if hostURL == "" {
		result, err := s.Accounts.RotateKey(ctx)
		return result, nil, err
	}
	if password == "" {
		return nil, nil, ErrPasswordRequired
	}
	if !s.Accounts.VaultReady() {
		return nil, nil, vault.ErrLocked
	}
	client, err := hosted.NewClient(hostURL)
	if err != nil {
		return nil, nil, err
	}
	login, err := client.Login(ctx, snapshot.Account.ID, password)
	if err != nil {
		return nil, nil, err
	}
	s.cacheSession(snapshot.Account.ID, login.SessionToken, login.SessionExpiresAt)
	localGeneration := snapshot.Account.KeyGeneration
	if localGeneration <= 0 {
		localGeneration = 1
	}
	if login.KeyGeneration > localGeneration {
		// Another device rotated while this one was offline. Adopt that key
		// before rotating again so both sides share one linear history.
		if _, _, err := s.Accounts.InstallAccountKey(ctx, login.KeyGeneration, login.KeyRotations, login.AccountPrivateKey); err != nil {
			return nil, nil, err
		}
	}
	result, err := s.Accounts.RotateKey(ctx)
	if err != nil {
		return nil, nil, err
	}
	warnings, err := s.publishAccountKey(ctx, client, login, snapshot.Account.ID)
	if err != nil {
		return result, []string{
			"the account key rotated locally, but the official host could not be updated: " + describe(err) +
				"; run `toget account rotate-key` again with the account password to publish it",
		}, nil
	}
	return result, warnings, nil
}

// ErrPasswordRequired means an official-host operation needs the account
// password to unlock the account master key.
var ErrPasswordRequired = errors.New("the account password is required")

func (s *Service) publishAccountKey(ctx context.Context, client *hosted.Client, login *hosted.LoginResult, accountID string) ([]string, error) {
	snapshot, err := s.Accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	privateKey, err := s.Accounts.UnsealPrivateKey(snapshot.Account)
	if err != nil {
		return nil, err
	}
	generation := snapshot.Account.KeyGeneration
	if generation <= 0 {
		generation = 1
	}
	bundle, err := hosted.NewKeyBundle(accountID, snapshot.Account.Name, privateKey,
		generation, snapshot.Account.KeyRotations, snapshot.Account.CreatedAt)
	if err != nil {
		return nil, err
	}
	sealed, err := hosted.SealKeyBundle(login.MasterKey, bundle)
	if err != nil {
		return nil, err
	}
	records, err := s.Accounts.ExportDeviceRecords(ctx, "")
	if err != nil {
		return nil, err
	}
	response, err := client.Sync(ctx, login.SessionToken, hosted.SyncRequest{
		KeyGeneration: generation,
		PublicKey:     append([]byte(nil), snapshot.Account.PublicKey...),
		KeyRotations:  append([]string(nil), snapshot.Account.KeyRotations...),
		KeyBundle:     &sealed,
		Devices:       records,
	})
	if err != nil {
		return nil, err
	}
	merged, err := s.Accounts.MergeDeviceRecords(ctx, response.Devices)
	if err != nil {
		return nil, err
	}
	return append(response.Warnings, merged.Warnings...), nil
}

// RotateRecovery issues a new official recovery file. The local per-device
// recovery file is separate and is not changed.
func (s *Service) RotateRecovery(ctx context.Context, password string) (*hosted.RecoveryCredential, []byte, error) {
	if s == nil || s.Accounts == nil {
		return nil, nil, errors.New("account service is unavailable")
	}
	snapshot, err := s.Accounts.Get(ctx)
	if err != nil {
		return nil, nil, err
	}
	hostURL := strings.TrimSpace(snapshot.Account.HostURL)
	if hostURL == "" {
		return nil, nil, errors.New("this account is not hosted by an official server")
	}
	client, err := hosted.NewClient(hostURL)
	if err != nil {
		return nil, nil, err
	}
	login, err := client.Login(ctx, snapshot.Account.ID, password)
	if err != nil {
		return nil, nil, err
	}
	credential, raw, err := client.RotateRecovery(ctx, login.SessionToken, snapshot.Account.ID, login.MasterKey)
	if err != nil {
		return nil, nil, err
	}
	s.cacheSession(snapshot.Account.ID, login.SessionToken, login.SessionExpiresAt)
	return credential, raw, nil
}

// RefreshFromPassword logs in with the account password, installs a newer
// account key if the official host has one, and caches the host session. It is
// best-effort and never blocks local use on a host outage.
func (s *Service) RefreshFromPassword(ctx context.Context, password string) []string {
	if s == nil || s.Accounts == nil {
		return nil
	}
	snapshot, err := s.Accounts.Get(ctx)
	if err != nil || strings.TrimSpace(snapshot.Account.HostURL) == "" {
		return nil
	}
	client, err := hosted.NewClient(snapshot.Account.HostURL)
	if err != nil {
		return []string{"official account host address is invalid: " + describe(err)}
	}
	login, err := client.Login(ctx, snapshot.Account.ID, password)
	if err != nil {
		return []string{"official account sign-in: " + describe(err)}
	}
	s.cacheSession(snapshot.Account.ID, login.SessionToken, login.SessionExpiresAt)
	localGeneration := snapshot.Account.KeyGeneration
	if localGeneration <= 0 {
		localGeneration = 1
	}
	if login.KeyGeneration <= localGeneration {
		return nil
	}
	if _, _, err := s.Accounts.InstallAccountKey(ctx, login.KeyGeneration, login.KeyRotations, login.AccountPrivateKey); err != nil {
		return []string{"could not install the newer official account key: " + err.Error()}
	}
	return nil
}

func (s *Service) publishDevice(ctx context.Context, client *hosted.Client, snapshot *account.Snapshot, token string) []string {
	if token == "" {
		return nil
	}
	records, err := s.Accounts.ExportDeviceRecords(ctx, "")
	if err != nil {
		return []string{"could not prepare the new device record: " + err.Error()}
	}
	response, err := client.Sync(ctx, token, hosted.SyncRequest{Devices: records})
	if err != nil {
		return []string{"the new device will be published on the next account sync: " + describe(err)}
	}
	if _, err := s.Accounts.MergeDeviceRecords(ctx, response.Devices); err != nil {
		return []string{"could not merge official account devices: " + err.Error()}
	}
	return nil
}

func (s *Service) cacheSession(accountID, token string, expires time.Time) {
	if s.Sessions == nil || token == "" || accountID == "" {
		return
	}
	s.Sessions.Set(accountID, token, expires)
}

func describe(err error) string {
	if err == nil {
		return "unknown error"
	}
	if errors.Is(err, hosted.ErrHostUnavailable) {
		return "account host is unreachable"
	}
	if errors.Is(err, hosted.ErrInvalidHost) {
		return "account host identity is invalid"
	}
	var apiError *hosted.APIError
	if errors.As(err, &apiError) && apiError.Message != "" {
		return apiError.Message
	}
	return err.Error()
}
