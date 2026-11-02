// Package hosted implements the official, multi-tenant Toget account host.
//
// The host stores only public account metadata, OPAQUE password records, and
// ciphertext. It never sees a password, an account signing key, or the account
// master key. A password login is a PAKE exchange; the resulting export key
// unwraps the account master key locally. Email recovery requires the user's
// recovery credential for the same reason.
package hosted

import (
	"encoding/json"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
)

const (
	// Protocol is the account-host wire protocol version.
	Protocol = 1
	// DefaultPort is the default official account-host port.
	DefaultPort = 7722
	// DefaultListen is the default listen address.
	DefaultListen = "0.0.0.0:7722"

	// HealthPath is a plain liveness endpoint.
	HealthPath = "/host/v1/health"
	// InfoPath returns the signed host manifest and OPAQUE configuration.
	InfoPath = "/host/v1/info"
	// ResolveAccountPath resolves an account ID to its host and display name.
	// It requires a signed-in device on this host.
	ResolveAccountPath = "/host/v1/resolve"
	// AccountLookupPrefix is the public per-account lookup endpoint. The host
	// signs the response, and only public profile fields are returned.
	AccountLookupPrefix = "/host/v1/accounts/"
	// FriendRequestPath creates one sealed friend request.
	FriendRequestPath = "/host/v1/friends/requests"
	// FriendRequestListPath lists requests addressed to the current device.
	FriendRequestListPath = "/host/v1/friends/requests/list"
	// FriendRequestDeletePath declines or dismisses one request.
	FriendRequestDeletePath = "/host/v1/friends/requests/delete"
	// FriendRequestSentPath lists requests the current account sent.
	FriendRequestSentPath = "/host/v1/friends/requests/sent"
	// FriendRequestCancelPath withdraws one request before it is answered.
	FriendRequestCancelPath = "/host/v1/friends/requests/cancel"
	// HeaderDeviceCertificate carries a base64url account-signed device
	// certificate when a device talks to a host that is not its account host.
	// The host uses it to bind a remote device to its account after checking
	// the account's federated device list.
	HeaderDeviceCertificate = "X-Toget-Device-Certificate"
	// RegisterBeginPath reserves an account ID and starts OPAQUE registration.
	RegisterBeginPath = "/host/v1/account/register/begin"
	// RegisterFinishPath completes registration with the client record and blobs.
	RegisterFinishPath = "/host/v1/account/register/finish"
	// LoginBeginPath starts a PAKE login.
	LoginBeginPath = "/host/v1/account/login/begin"
	// LoginFinishPath finishes a PAKE login and releases encrypted account blobs.
	LoginFinishPath = "/host/v1/account/login/finish"
	// DeviceSessionPath exchanges a device signature for a short-lived session.
	// It lets a logged-in device sync and rotate recovery without re-entering
	// the account password or storing one on the host.
	DeviceSessionPath = "/host/v1/account/device/session"
	// SyncPath exchanges signed account device records.
	SyncPath = "/host/v1/account/sync"
	// PasswordBeginPath starts an authenticated password re-registration.
	PasswordBeginPath = "/host/v1/account/password/begin"
	// PasswordFinishPath completes an authenticated password re-registration.
	PasswordFinishPath = "/host/v1/account/password/finish"
	// RecoveryRotatePath replaces the recovery wrap after re-keying the
	// recovery credential on a logged-in device.
	RecoveryRotatePath = "/host/v1/account/recovery"
	// EmailVerifyPath consumes an email verification token.
	EmailVerifyPath = "/host/v1/account/email/verify"
	// EmailResendPath sends a fresh verification token to the stored address.
	EmailResendPath = "/host/v1/account/email/resend"
	// RecoverRequestPath asks for a password-recovery email.
	RecoverRequestPath = "/host/v1/account/recover/request"
	// RecoverContextPath resolves a recovery token to public account metadata.
	RecoverContextPath = "/host/v1/account/recover/context"
	// RecoverMaterialPath releases the ciphertext needed by the reset page.
	RecoverMaterialPath = "/host/v1/account/recover/material"
	// RecoverRegisterBeginPath starts OPAQUE re-registration on the reset page.
	RecoverRegisterBeginPath = "/host/v1/account/recover/register/begin"
	// RecoverFinishPath completes a reset and replaces all password material.
	RecoverFinishPath = "/host/v1/account/recover/finish"

	// MaxBody bounds one JSON request.
	MaxBody = 8 << 20
	// MaxNameLength bounds a display name.
	MaxNameLength = 64
	// MaxEmailLength bounds an email address.
	MaxEmailLength = 254
	// MaxBlobLength bounds one sealed blob.
	MaxBlobLength = 64 << 10
	// MaxOpaqueRequest bounds one OPAQUE message.
	MaxOpaqueRequest = 16 << 10
	// MaxDeviceRecords bounds one account's device set.
	MaxDeviceRecords = account.MaxDeviceRecords

	// PendingTTL is how long a half-finished registration reserves an ID.
	PendingTTL = 15 * time.Minute
	// LoginTTL bounds one in-flight PAKE login.
	LoginTTL = 2 * time.Minute
	// VerifyTokenTTL is the lifetime of an email verification link.
	VerifyTokenTTL = 24 * time.Hour
	// ResetTokenTTL is the lifetime of a password-reset link.
	ResetTokenTTL = 30 * time.Minute
	// FriendRequestTTL is how long a pending friend request stays visible.
	FriendRequestTTL = 7 * 24 * time.Hour
	// MaxFriendRequestsPerAccount bounds pending outgoing requests.
	MaxFriendRequestsPerAccount = 100
	// MaxFriendRequestSeals bounds the devices one request can address.
	MaxFriendRequestSeals = 64
	// MaxFriendRequestSealLength bounds one sealed invite. A 64 KiB invite
	// grows to about 88 KiB when sealbox base64-encodes it.
	MaxFriendRequestSealLength = 96 << 10
)

// Error is the host's structured error body.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Info is the public description of an account host.
type Info struct {
	Protocol int    `json:"protocol"`
	HostID   string `json:"host_id"`
	HostCode string `json:"host_code"`
	Name     string `json:"name"`
	// Manifest is the host's signed identity manifest. It binds the host ID,
	// short code, and public key, and can be verified with identity.VerifyManifestJSON.
	Manifest json.RawMessage `json:"manifest"`
	// OPAQUEConfig is the serialized OPAQUE configuration. Clients must use
	// exactly this configuration or registration and login will not agree.
	OPAQUEConfig []byte `json:"opaque_config"`
	// EmailRecovery reports whether this host can send recovery email.
	EmailRecovery bool `json:"email_recovery"`
	// ResetEnabled reports whether the browser reset page is configured.
	ResetEnabled bool      `json:"reset_enabled"`
	IssuedAt     time.Time `json:"issued_at"`
}

// AccountLookup is a host-signed public profile statement. It proves that the
// account exists on this host without exposing email, devices, or key material.
type AccountLookup struct {
	Protocol  int       `json:"protocol"`
	AccountID string    `json:"account_id"`
	Name      string    `json:"name"`
	HostID    string    `json:"host_id"`
	HostCode  string    `json:"host_code"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Signature string    `json:"signature,omitempty"`
}

// ResolveAccountRequest resolves one 14-digit account ID. The caller must be a
// signed-in device on this host, which keeps the endpoint from becoming an
// anonymous account-enumeration proxy.
type ResolveAccountRequest struct {
	Protocol  int    `json:"protocol"`
	AccountID string `json:"account_id"`
}

// ResolvedDevice is the minimum public material needed to seal a friend
// request to one of the target account's devices. The key is encoded with
// identity.EncodeEncryptionPublicKey.
type ResolvedDevice struct {
	ID                  string `json:"id"`
	EncryptionPublicKey string `json:"encryption_public_key"`
}

// ResolvedAccount is the cross-host account discovery result.
type ResolvedAccount struct {
	Protocol   int    `json:"protocol"`
	AccountID  string `json:"account_id"`
	Name       string `json:"name,omitempty"`
	HostID     string `json:"host_id"`
	HostCode   string `json:"host_code"`
	HostURL    string `json:"host_url"`
	ServerName string `json:"server_name,omitempty"`
	// Kind is "account" for an hosted account or "node" for the owner account
	// published in a self-hosted node manifest.
	Kind string `json:"kind"`
	Self bool   `json:"self"`
	// Devices is populated only when the target is local to this host. A
	// friend request is sealed once per active target device.
	Devices []ResolvedDevice `json:"devices,omitempty"`
}

// FriendRequestSeal is one invite code sealed to one target device. The
// sealed value is a sealbox ciphertext whose context binds the request ID,
// both account IDs, and the room ID.
type FriendRequestSeal struct {
	DeviceID string `json:"device_id"`
	Sealed   string `json:"sealed"`
}

// FriendRequestCreate asks the host to deliver a sealed invite to another
// account on the same host.
type FriendRequestCreate struct {
	Protocol    int                 `json:"protocol"`
	ID          string              `json:"id"`
	ToAccountID string              `json:"to_account_id"`
	RoomID      string              `json:"room_id"`
	Seals       []FriendRequestSeal `json:"seals"`
}

// FriendRequestCreated confirms one stored friend request.
type FriendRequestCreated struct {
	Protocol  int       `json:"protocol"`
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// FriendRequestSummary is one incoming request, with only the ciphertext for
// the requesting device.
type FriendRequestSummary struct {
	ID            string    `json:"id"`
	FromAccountID string    `json:"from_account_id"`
	FromName      string    `json:"from_name,omitempty"`
	RoomID        string    `json:"room_id"`
	Sealed        string    `json:"sealed"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// FriendRequestList is the current device's inbox.
type FriendRequestList struct {
	Protocol int                    `json:"protocol"`
	Requests []FriendRequestSummary `json:"requests"`
}

// FriendRequestDelete declines or dismisses one request. The recipient
// account must own the request; the device does not need to be able to open
// the seal.
type FriendRequestDelete struct {
	Protocol int    `json:"protocol"`
	ID       string `json:"id"`
}

// FriendRequestDeleteResponse reports whether a request was removed.
type FriendRequestDeleteResponse struct {
	Protocol int  `json:"protocol"`
	Deleted  bool `json:"deleted"`
}

// SentFriendRequestSummary is one outgoing request. It carries no ciphertext;
// the sender only needs the recipient and expiry to manage the request.
type SentFriendRequestSummary struct {
	ID          string    `json:"id"`
	ToAccountID string    `json:"to_account_id"`
	ToName      string    `json:"to_name,omitempty"`
	RoomID      string    `json:"room_id"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// SentFriendRequestList is the current account's outgoing requests.
type SentFriendRequestList struct {
	Protocol int                        `json:"protocol"`
	Requests []SentFriendRequestSummary `json:"requests"`
}

// FriendRequestSealContext binds a seal to its request, both accounts, and
// the room. The recipient can reproduce it from public metadata, so the host
// can move a seal but cannot redirect it to another request or room.
func FriendRequestSealContext(requestID, fromAccountID, toAccountID, roomID string) string {
	return "toget:friend-request:v1:" + requestID + ":" + fromAccountID + ":" +
		toAccountID + ":" + roomID
}

// DirectoryRecord is the neutral, already-verified record shape a directory
// lookup returns to the account host.
type DirectoryRecord struct {
	Kind           string
	ServerID       string
	ServerCode     string
	AccountID      string
	Endpoint       string
	TLSFingerprint string
	HostManifest   *identity.HostManifest
}

// SealedBlob is one versioned AEAD ciphertext. The algorithm is named so a
// future host can add a suite without guessing at old data.
type SealedBlob struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// RegisterBeginRequest reserves an account and starts OPAQUE registration.
// The account public key is bound into the reservation; the first device
// certificate is checked at finish, when the client knows the account ID.
type RegisterBeginRequest struct {
	Protocol         int    `json:"protocol"`
	Name             string `json:"name"`
	Email            string `json:"email,omitempty"`
	AccountPublicKey []byte `json:"account_public_key"`
	OPAQUERequest    []byte `json:"opaque_request"`
}

// RegisterBeginResponse returns the assigned account ID and OPAQUE response.
type RegisterBeginResponse struct {
	Protocol       int       `json:"protocol"`
	AccountID      string    `json:"account_id"`
	AccountCode    string    `json:"account_code"`
	HostID         string    `json:"host_id"`
	HostCode       string    `json:"host_code"`
	Name           string    `json:"name"`
	OPAQUEConfig   []byte    `json:"opaque_config"`
	OPAQUEResponse []byte    `json:"opaque_response"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// RegisterFinishRequest carries the proof that the client knows the password
// (the OPAQUE registration record), the first device, and every encrypted
// blob the account needs later.
type RegisterFinishRequest struct {
	Protocol     int    `json:"protocol"`
	AccountID    string `json:"account_id"`
	OPAQUERecord []byte `json:"opaque_record"`
	// ExportWrap is Enc(OPAQUE export key, MK).
	ExportWrap SealedBlob `json:"export_wrap"`
	// RecoveryWrap is Enc(recovery key, MK).
	RecoveryWrap SealedBlob `json:"recovery_wrap"`
	// KeyBundle is Enc(MK, account signing key bundle).
	KeyBundle SealedBlob           `json:"key_bundle"`
	Device    account.DeviceRecord `json:"device"`
}

// RegisterFinishResponse is returned once the account is durable. The session
// token authorizes email verification and the first device sync.
type RegisterFinishResponse struct {
	Protocol         int       `json:"protocol"`
	AccountID        string    `json:"account_id"`
	Name             string    `json:"name"`
	HostID           string    `json:"host_id"`
	SessionToken     string    `json:"session_token,omitempty"`
	SessionExpiresAt time.Time `json:"session_expires_at,omitempty"`
	EmailSent        bool      `json:"email_sent,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// LoginBeginRequest starts a PAKE login. AccountID is public metadata, not a
// password; the OPAQUE request carries the blinded password.
type LoginBeginRequest struct {
	Protocol  int    `json:"protocol"`
	AccountID string `json:"account_id"`
	KE1       []byte `json:"ke1"`
}

// LoginBeginResponse returns the server's PAKE message and a login handle.
type LoginBeginResponse struct {
	Protocol  int       `json:"protocol"`
	LoginID   string    `json:"login_id"`
	KE2       []byte    `json:"ke2"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LoginFinishRequest finishes a PAKE login.
type LoginFinishRequest struct {
	Protocol int    `json:"protocol"`
	LoginID  string `json:"login_id"`
	KE3      []byte `json:"ke3"`
}

// AccountMaterial is everything a logged-in client needs. All secret material
// is encrypted; the host cannot read it.
type AccountMaterial struct {
	Protocol         int                    `json:"protocol"`
	AccountID        string                 `json:"account_id"`
	Name             string                 `json:"name"`
	HostID           string                 `json:"host_id"`
	HostCode         string                 `json:"host_code"`
	PublicKey        []byte                 `json:"public_key"`
	KeyGeneration    int                    `json:"key_generation"`
	KeyRotations     []string               `json:"key_rotations,omitempty"`
	ExportWrap       SealedBlob             `json:"export_wrap"`
	RecoveryWrap     SealedBlob             `json:"recovery_wrap"`
	KeyBundle        SealedBlob             `json:"key_bundle"`
	Devices          []account.DeviceRecord `json:"devices"`
	EmailVerified    bool                   `json:"email_verified"`
	SessionToken     string                 `json:"session_token"`
	SessionExpiresAt time.Time              `json:"session_expires_at"`
	CreatedAt        time.Time              `json:"created_at"`
}

// DeviceSessionRequest authenticates a device with its account-signed
// certificate and device signing key. The same identity headers are also
// signed into the HTTP request.
type DeviceSessionRequest struct {
	Protocol  int    `json:"protocol"`
	AccountID string `json:"account_id"`
	DeviceID  string `json:"device_id"`
}

// DeviceSessionResponse is a short-lived bearer session for a known device.
type DeviceSessionResponse struct {
	Protocol         int       `json:"protocol"`
	SessionToken     string    `json:"session_token"`
	SessionExpiresAt time.Time `json:"session_expires_at"`
}

// SyncRequest exchanges the account's public device set and, after a key
// rotation, the new key material. Authentication is the bearer session.
type SyncRequest struct {
	Protocol      int                    `json:"protocol"`
	KeyGeneration int                    `json:"key_generation,omitempty"`
	PublicKey     []byte                 `json:"public_key,omitempty"`
	KeyRotations  []string               `json:"key_rotations,omitempty"`
	KeyBundle     *SealedBlob            `json:"key_bundle,omitempty"`
	Devices       []account.DeviceRecord `json:"devices"`
	// SessionDeviceID binds the caller's current session to this device.
	// Signing in on a new browser uses it once, right after registering the
	// device, so revoking that browser can end this exact session later.
	SessionDeviceID string `json:"session_device_id,omitempty"`
}

// SyncResponse returns the converged device set.
type SyncResponse struct {
	Protocol      int                    `json:"protocol"`
	Devices       []account.DeviceRecord `json:"devices"`
	KeyGeneration int                    `json:"key_generation"`
	PublicKey     []byte                 `json:"public_key"`
	KeyRotations  []string               `json:"key_rotations,omitempty"`
	Added         int                    `json:"added"`
	Updated       int                    `json:"updated"`
	Revoked       int                    `json:"revoked"`
	Skipped       int                    `json:"skipped"`
	Warnings      []string               `json:"warnings,omitempty"`
	EmailVerified bool                   `json:"email_verified"`
}

// PasswordBeginRequest starts OPAQUE re-registration for a new password.
type PasswordBeginRequest struct {
	Protocol      int    `json:"protocol"`
	OPAQUERequest []byte `json:"opaque_request"`
}

// PasswordBeginResponse returns the server's OPAQUE registration response.
type PasswordBeginResponse struct {
	Protocol       int       `json:"protocol"`
	OPAQUEResponse []byte    `json:"opaque_response"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// PasswordFinishRequest installs the new password record and export wrap.
type PasswordFinishRequest struct {
	Protocol     int        `json:"protocol"`
	OPAQUERecord []byte     `json:"opaque_record"`
	ExportWrap   SealedBlob `json:"export_wrap"`
}

// PasswordFinishResponse confirms a password change. Every other session for
// the account has been revoked.
type PasswordFinishResponse struct {
	Protocol  int       `json:"protocol"`
	ChangedAt time.Time `json:"changed_at"`
}

// RecoveryRotateRequest replaces the recovery wrap with one made from a new
// recovery credential. The caller must already hold the account key.
type RecoveryRotateRequest struct {
	Protocol     int        `json:"protocol"`
	RecoveryWrap SealedBlob `json:"recovery_wrap"`
}

// EmailVerifyRequest consumes an email verification token.
type EmailVerifyRequest struct {
	Protocol int    `json:"protocol"`
	Token    string `json:"token"`
}

// EmailVerifyResponse reports the stored verification state.
type EmailVerifyResponse struct {
	Protocol int    `json:"protocol"`
	Email    string `json:"email"`
	Verified bool   `json:"verified"`
}

// EmailResendRequest asks for another verification email.
type EmailResendRequest struct {
	Protocol int `json:"protocol"`
}

// RecoverRequest starts a password-recovery email. User is an account ID or an
// email address. The response is always accepted so the endpoint cannot be
// used to enumerate accounts.
type RecoverRequest struct {
	Protocol int    `json:"protocol"`
	User     string `json:"user"`
}

// RecoverContextRequest resolves a reset token.
type RecoverContextRequest struct {
	Protocol int    `json:"protocol"`
	Token    string `json:"token"`
}

// RecoverContext is the public metadata the reset page may display.
type RecoverContext struct {
	Protocol      int       `json:"protocol"`
	AccountID     string    `json:"account_id"`
	Name          string    `json:"name"`
	HostID        string    `json:"host_id"`
	EmailHint     string    `json:"email_hint,omitempty"`
	Purpose       string    `json:"purpose"`
	ExpiresAt     time.Time `json:"expires_at"`
	RecoveryReady bool      `json:"recovery_ready"`
}

// RecoverMaterialRequest fetches the ciphertext the reset page needs.
type RecoverMaterialRequest struct {
	Protocol int    `json:"protocol"`
	Token    string `json:"token"`
}

// RecoverMaterial is ciphertext only: the recovery wrap, export wrap, and key
// bundle. The page decrypts the recovery wrap locally with the recovery file.
type RecoverMaterial struct {
	Protocol      int        `json:"protocol"`
	AccountID     string     `json:"account_id"`
	PublicKey     []byte     `json:"public_key"`
	KeyGeneration int        `json:"key_generation"`
	RecoveryWrap  SealedBlob `json:"recovery_wrap"`
	ExportWrap    SealedBlob `json:"export_wrap"`
	KeyBundle     SealedBlob `json:"key_bundle"`
}

// RecoverRegisterBeginRequest starts OPAQUE registration authorized by a
// recovery token.
type RecoverRegisterBeginRequest struct {
	Protocol      int    `json:"protocol"`
	Token         string `json:"token"`
	OPAQUERequest []byte `json:"opaque_request"`
}

// RecoverRegisterBeginResponse returns the OPAQUE response for a reset.
type RecoverRegisterBeginResponse struct {
	Protocol       int       `json:"protocol"`
	OPAQUEResponse []byte    `json:"opaque_response"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// RecoverFinishRequest completes a token-authorized reset. New recovery
// material replaces the old credential atomically.
type RecoverFinishRequest struct {
	Protocol     int        `json:"protocol"`
	Token        string     `json:"token"`
	OPAQUERecord []byte     `json:"opaque_record"`
	ExportWrap   SealedBlob `json:"export_wrap"`
	RecoveryWrap SealedBlob `json:"recovery_wrap"`
}

// RecoverFinishResponse confirms a reset.
type RecoverFinishResponse struct {
	Protocol  int       `json:"protocol"`
	AccountID string    `json:"account_id"`
	ResetAt   time.Time `json:"reset_at"`
}
