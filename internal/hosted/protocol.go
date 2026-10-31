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
	// RegisterBeginPath reserves an account ID and starts OPAQUE registration.
	RegisterBeginPath = "/host/v1/account/register/begin"
	// RegisterFinishPath completes registration with the client record and blobs.
	RegisterFinishPath = "/host/v1/account/register/finish"
	// LoginBeginPath starts a PAKE login.
	LoginBeginPath = "/host/v1/account/login/begin"
	// LoginFinishPath finishes a PAKE login and releases encrypted account blobs.
	LoginFinishPath = "/host/v1/account/login/finish"
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

// SyncRequest exchanges the account's public device set and, after a key
// rotation, the new key material. Authentication is the bearer session.
type SyncRequest struct {
	Protocol      int                    `json:"protocol"`
	KeyGeneration int                    `json:"key_generation,omitempty"`
	PublicKey     []byte                 `json:"public_key,omitempty"`
	KeyRotations  []string               `json:"key_rotations,omitempty"`
	KeyBundle     *SealedBlob            `json:"key_bundle,omitempty"`
	Devices       []account.DeviceRecord `json:"devices"`
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
