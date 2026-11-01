package hosted

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/requestauth"
)

const maxResponseBytes = 8 << 20

var (
	// ErrHostUnavailable means the host could not be reached.
	ErrHostUnavailable = errors.New("account host is unavailable")
	// ErrInvalidHost means the host's manifest or protocol was not acceptable.
	ErrInvalidHost = errors.New("account host identity is invalid")
	// ErrInvalidCredentials means the account ID or password was rejected.
	ErrInvalidCredentials = errors.New("invalid account or password")
	// ErrRecoveryMismatch means a recovery file does not belong to the account
	// or host being reset.
	ErrRecoveryMismatch = errors.New("recovery credential does not match this account")
	// ErrSessionInvalid means a bearer session is unknown or expired.
	ErrSessionInvalid = errors.New("invalid session")
)

// APIError is a structured host error response.
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e == nil {
		return "account host error"
	}
	if e.Message == "" {
		return fmt.Sprintf("account host returned %d", e.Status)
	}
	return e.Message
}

// Client talks to one account host. Direct addresses pin the host
// certificate; identity addresses use ordinary Web PKI TLS to the edge and
// pin the host's self-certifying ID from its signed manifest.
type Client struct {
	origin      string
	hostURL     string
	hostID      string
	fingerprint string
	httpClient  *http.Client
	now         func() time.Time

	mu   sync.Mutex
	info *Info
}

type clientOptions struct {
	rootCAs *x509.CertPool
}

// ClientOption customizes a hosted client. Options only affect identity-pin
// (#id=) addresses; direct certificate pins deliberately ignore extra roots.
type ClientOption func(*clientOptions)

// WithRootCAs supplies the roots used to verify the edge certificate for an
// identity-pin host. It is intended for tests and private deployments; nil
// keeps the system roots.
func WithRootCAs(roots *x509.CertPool) ClientOption {
	return func(options *clientOptions) {
		options.rootCAs = roots
	}
}

// NewClient creates a client for an HTTPS host address. hostURL must carry
// either a #fp=<sha256> certificate fingerprint for a direct connection or a
// #id=<tog1:s:...> self-certifying host identity for a TLS-terminating proxy.
func NewClient(hostURL string, options ...ClientOption) (*Client, error) {
	address, err := account.ParseHostURL(hostURL)
	if err != nil {
		return nil, err
	}
	var tlsConfig *tls.Config
	if address.Fingerprint != "" {
		tlsConfig, err = PinnedTLSConfig(address.Fingerprint)
		if err != nil {
			return nil, err
		}
	} else {
		settings := clientOptions{}
		for _, option := range options {
			if option != nil {
				option(&settings)
			}
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: settings.rootCAs}
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second, KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &Client{
		origin: address.Origin, hostURL: strings.TrimSuffix(strings.TrimSpace(hostURL), "/"),
		hostID: address.HostID, fingerprint: address.Fingerprint,
		httpClient: &http.Client{
			Transport: transport, Timeout: 60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("account host redirects are not allowed")
			},
		},
		now: time.Now,
	}, nil
}

// NewBrowserClient creates a client for the reset page. The browser already
// trusts the origin that served the page; certificate pinning cannot be
// applied from inside a same-origin web application.
func NewBrowserClient(origin string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("browser host origin is invalid")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("browser host must use HTTPS")
	}
	base := strings.TrimSuffix(parsed.String(), "/")
	return &Client{
		origin: base, hostURL: base,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		now:        time.Now,
	}, nil
}

// Origin returns the unpinned HTTPS origin.
func (c *Client) Origin() string { return c.origin }

// HostURL returns the exact host address, including its trust anchor.
func (c *Client) HostURL() string { return c.hostURL }

// PinnedURL is the historical name for HostURL. It also returns identity-pin
// addresses, so callers that persist an account host should use this value.
func (c *Client) PinnedURL() string { return c.hostURL }

// HostID returns the expected self-certifying host ID, if this is an identity
// pin. It is empty for direct certificate pins and the browser client.
func (c *Client) HostID() string { return c.hostID }

// Fingerprint returns the pinned certificate fingerprint, if any.
func (c *Client) Fingerprint() string { return c.fingerprint }

// Info fetches and verifies the host's signed description.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.info != nil {
		return c.info, nil
	}
	var info Info
	if err := c.doRaw(ctx, http.MethodGet, InfoPath, "", nil, &info); err != nil {
		return nil, err
	}
	if info.Protocol != Protocol || info.HostID == "" || info.HostCode == "" ||
		len(info.Manifest) == 0 {
		return nil, fmt.Errorf("%w: incomplete host description", ErrInvalidHost)
	}
	manifest, err := identity.VerifyManifestJSON(info.Manifest)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHost, err)
	}
	if manifest.ServerID != info.HostID || manifest.ServerCode != info.HostCode {
		return nil, fmt.Errorf("%w: manifest does not match the host id", ErrInvalidHost)
	}
	if c.hostID != "" && info.HostID != c.hostID {
		return nil, fmt.Errorf("%w: host identity does not match the pinned host id", ErrInvalidHost)
	}
	if err := ValidOPAQUEConfig(info.OPAQUEConfig); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHost, err)
	}
	c.info = &info
	return c.info, nil
}

// RegisterOptions creates an official-hosted account.
type RegisterOptions struct {
	Name       string
	Email      string
	Password   string
	AccountKey ed25519.PrivateKey
	DeviceName string
	NodeURL    string
	// Now overrides the local clock in tests.
	Now time.Time
}

// DeviceMaterial is a freshly generated device identity.
type DeviceMaterial struct {
	ID                   string
	AccountID            string
	Name                 string
	PublicKey            ed25519.PublicKey
	PrivateKey           ed25519.PrivateKey
	EncryptionPublicKey  []byte
	EncryptionPrivateKey []byte
	Certificate          string
	CreatedAt            time.Time
}

// RegistrationResult is everything a device needs to adopt a new account.
type RegistrationResult struct {
	AccountID             string
	AccountCode           string
	Name                  string
	HostURL               string
	HostID                string
	HostCode              string
	AccountPublicKey      []byte
	AccountPrivateKey     ed25519.PrivateKey
	Device                DeviceMaterial
	MasterKey             []byte
	RecoveryCredential    *RecoveryCredential
	RecoveryCredentialRaw []byte
	SessionToken          string
	SessionExpiresAt      time.Time
	EmailSent             bool
	CreatedAt             time.Time
}

// Register creates an account on the host. The caller must save the recovery
// credential before discarding it: without it, a forgotten password cannot
// recover the account's history.
func (c *Client) Register(ctx context.Context, options RegisterOptions) (*RegistrationResult, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(options.Name)
	if name == "" || len(name) > MaxNameLength {
		return nil, errors.New("invalid account name")
	}
	if options.Password == "" {
		return nil, errors.New("password is required")
	}
	accountKey := options.AccountKey
	if len(accountKey) == 0 {
		_, accountKey, err = identity.NewAccountKey()
		if err != nil {
			return nil, err
		}
	}
	if len(accountKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid account key")
	}
	accountPublicKey := accountKey.Public().(ed25519.PublicKey)
	now := options.Now.UTC()
	if now.IsZero() {
		now = c.now().UTC()
	}
	now = now.Truncate(time.Millisecond)

	registrationClient, err := NewOPAQUERegistrationClient(info.OPAQUEConfig, info.HostID)
	if err != nil {
		return nil, err
	}
	defer registrationClient.Clear()
	opaqueRequest, err := registrationClient.RegisterInit(options.Password)
	if err != nil {
		return nil, err
	}
	var begin RegisterBeginResponse
	if err := c.do(ctx, http.MethodPost, RegisterBeginPath, "", RegisterBeginRequest{
		Protocol: Protocol, Name: name, Email: strings.TrimSpace(options.Email),
		AccountPublicKey: accountPublicKey, OPAQUERequest: opaqueRequest,
	}, &begin); err != nil {
		return nil, err
	}
	if begin.Protocol != Protocol || !digits(begin.AccountID, 14) || !digits(begin.AccountCode, 6) ||
		!strings.HasSuffix(begin.AccountID, begin.AccountCode) ||
		begin.HostID != info.HostID || begin.HostCode != info.HostCode ||
		!bytes.Equal(begin.OPAQUEConfig, info.OPAQUEConfig) {
		return nil, fmt.Errorf("%w: registration response is inconsistent", ErrInvalidHost)
	}
	if !strings.HasPrefix(begin.AccountID, info.HostCode) {
		return nil, fmt.Errorf("%w: account id does not belong to this host", ErrInvalidHost)
	}

	opaqueRecord, exportKey, err := registrationClient.RegisterFinishFor(begin.AccountID, begin.OPAQUEResponse)
	if err != nil {
		return nil, err
	}
	deviceID := identity.NewID()
	devicePub, devicePriv, err := identity.NewDeviceKey()
	if err != nil {
		return nil, err
	}
	encryptionPub, encryptionPriv, err := identity.NewEncryptionKey()
	if err != nil {
		return nil, err
	}
	certificate, err := identity.SignDeviceCertificate(deviceID, begin.AccountID, devicePub, encryptionPub, accountKey, now)
	if err != nil {
		return nil, err
	}
	deviceName := strings.TrimSpace(options.DeviceName)
	if deviceName == "" {
		deviceName = "This device"
	}
	if len(deviceName) > 64 {
		deviceName = deviceName[:64]
	}
	deviceRecord := account.DeviceRecord{
		ID: deviceID, AccountID: begin.AccountID, Name: deviceName,
		PublicKey: devicePub, EncryptionPublicKey: encryptionPub,
		Certificate: string(certificate), NodeURL: strings.TrimSpace(options.NodeURL),
		Role: "node", CreatedAt: now,
	}

	masterKey, err := NewMasterKey()
	if err != nil {
		return nil, err
	}
	credential, recoveryKey, err := NewRecoveryCredential(begin.AccountID, c.hostURL, now)
	if err != nil {
		return nil, err
	}
	exportWrap, err := WrapExportKey(exportKey, masterKey, begin.AccountID)
	if err != nil {
		return nil, err
	}
	recoveryWrap, err := WrapRecoveryKey(recoveryKey, masterKey, begin.AccountID)
	if err != nil {
		return nil, err
	}
	bundle, err := NewKeyBundle(begin.AccountID, name, accountKey, 1, nil, now)
	if err != nil {
		return nil, err
	}
	keyBundle, err := SealKeyBundle(masterKey, bundle)
	if err != nil {
		return nil, err
	}
	var finish RegisterFinishResponse
	if err := c.do(ctx, http.MethodPost, RegisterFinishPath, "", RegisterFinishRequest{
		Protocol: Protocol, AccountID: begin.AccountID, OPAQUERecord: opaqueRecord,
		ExportWrap: exportWrap, RecoveryWrap: recoveryWrap, KeyBundle: keyBundle,
		Device: deviceRecord,
	}, &finish); err != nil {
		return nil, err
	}
	if finish.Protocol != Protocol || finish.AccountID != begin.AccountID || finish.HostID != info.HostID {
		return nil, fmt.Errorf("%w: registration finish is inconsistent", ErrInvalidHost)
	}
	credentialRaw, err := credential.Encode()
	if err != nil {
		return nil, err
	}
	return &RegistrationResult{
		AccountID: begin.AccountID, AccountCode: begin.AccountCode,
		Name: begin.Name, HostURL: c.hostURL,
		HostID: info.HostID, HostCode: info.HostCode,
		AccountPublicKey:  append([]byte(nil), accountPublicKey...),
		AccountPrivateKey: append(ed25519.PrivateKey(nil), accountKey...),
		Device: DeviceMaterial{
			ID: deviceID, AccountID: begin.AccountID, Name: deviceName,
			PublicKey: devicePub, PrivateKey: devicePriv,
			EncryptionPublicKey: encryptionPub, EncryptionPrivateKey: encryptionPriv,
			Certificate: string(certificate), CreatedAt: now,
		},
		MasterKey: masterKey, RecoveryCredential: credential,
		RecoveryCredentialRaw: credentialRaw,
		SessionToken:          finish.SessionToken, SessionExpiresAt: finish.SessionExpiresAt,
		EmailSent: finish.EmailSent, CreatedAt: finish.CreatedAt,
	}, nil
}

// LoginResult is an authenticated account plus decrypted signing material.
type LoginResult struct {
	AccountID         string
	Name              string
	HostID            string
	HostCode          string
	HostURL           string
	PublicKey         []byte
	KeyGeneration     int
	KeyRotations      []string
	MasterKey         []byte
	AccountPrivateKey ed25519.PrivateKey
	KeyBundle         *KeyBundle
	Devices           []account.DeviceRecord
	EmailVerified     bool
	SessionToken      string
	SessionExpiresAt  time.Time
	ExportWrap        SealedBlob
	RecoveryWrap      SealedBlob
	KeyBundleBlob     SealedBlob
	CreatedAt         time.Time
}

// Login performs an OPAQUE password exchange and decrypts the account key.
func (c *Client) Login(ctx context.Context, accountID, password string) (*LoginResult, error) {
	if !digits(accountID, 14) || password == "" {
		return nil, ErrInvalidCredentials
	}
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(accountID, info.HostCode) {
		return nil, ErrInvalidCredentials
	}
	opaqueClient, err := NewOPAQUEClient(info.OPAQUEConfig, accountID, info.HostID)
	if err != nil {
		return nil, err
	}
	defer opaqueClient.Clear()
	ke1, err := opaqueClient.LoginInit(password)
	if err != nil {
		return nil, err
	}
	var begin LoginBeginResponse
	if err := c.do(ctx, http.MethodPost, LoginBeginPath, "", LoginBeginRequest{
		Protocol: Protocol, AccountID: accountID, KE1: ke1,
	}, &begin); err != nil {
		return nil, err
	}
	if begin.Protocol != Protocol || begin.LoginID == "" || len(begin.KE2) == 0 {
		return nil, fmt.Errorf("%w: login response is inconsistent", ErrInvalidHost)
	}
	ke3, exportKey, err := opaqueClient.LoginFinish(begin.KE2)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	var material AccountMaterial
	if err := c.do(ctx, http.MethodPost, LoginFinishPath, "", LoginFinishRequest{
		Protocol: Protocol, LoginID: begin.LoginID, KE3: ke3,
	}, &material); err != nil {
		return nil, err
	}
	if material.Protocol != Protocol || material.AccountID != accountID ||
		material.HostID != info.HostID || material.SessionToken == "" {
		return nil, fmt.Errorf("%w: account material is inconsistent", ErrInvalidHost)
	}
	masterKey, err := OpenExportWrap(exportKey, material.ExportWrap, accountID)
	if err != nil {
		return nil, fmt.Errorf("%w: password wrap did not open", ErrInvalidCredentials)
	}
	bundle, err := OpenKeyBundle(masterKey, material.KeyBundle, accountID, material.KeyGeneration)
	if err != nil {
		return nil, fmt.Errorf("%w: account key bundle did not open", ErrInvalidHost)
	}
	privateKey := ed25519.PrivateKey(append([]byte(nil), bundle.AccountPrivateKey...))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if !bytes.Equal(publicKey, material.PublicKey) {
		return nil, fmt.Errorf("%w: account key does not match the host record", ErrInvalidHost)
	}
	devices, err := validateDeviceRecords(material.Devices, accountID, publicKey, material.KeyRotations, material.KeyGeneration, c.now().UTC())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHost, err)
	}
	return &LoginResult{
		AccountID: material.AccountID, Name: material.Name,
		HostID: material.HostID, HostCode: material.HostCode, HostURL: c.hostURL,
		PublicKey:     append([]byte(nil), material.PublicKey...),
		KeyGeneration: material.KeyGeneration, KeyRotations: material.KeyRotations,
		MasterKey: masterKey, AccountPrivateKey: privateKey, KeyBundle: bundle,
		Devices: devices, EmailVerified: material.EmailVerified,
		SessionToken: material.SessionToken, SessionExpiresAt: material.SessionExpiresAt,
		ExportWrap: material.ExportWrap, RecoveryWrap: material.RecoveryWrap,
		KeyBundleBlob: material.KeyBundle, CreatedAt: material.CreatedAt,
	}, nil
}

func validateDeviceRecords(records []account.DeviceRecord, accountID string, publicKey ed25519.PublicKey, rotations []string, generation int, now time.Time) ([]account.DeviceRecord, error) {
	keys, err := account.VerificationKeys(&account.Account{
		ID: accountID, PublicKey: publicKey, KeyGeneration: generation, KeyRotations: rotations,
	})
	if err != nil {
		return nil, err
	}
	out := make([]account.DeviceRecord, 0, len(records))
	for _, record := range records {
		validated, _, err := account.ValidateDeviceRecord(record, accountID, keys, now)
		if err != nil {
			return nil, err
		}
		if validated.RevokedAt != nil && record.Revocation == "" {
			return nil, errors.New("revoked device is missing its proof")
		}
		out = append(out, record)
	}
	return out, nil
}

// DeviceSession exchanges the local device key for a short-lived host
// session. The host checks the account-signed device certificate, so this
// works without the account password and without storing one on the host.
func (c *Client) DeviceSession(ctx context.Context, accountID, deviceID string, deviceKey ed25519.PrivateKey) (string, time.Time, error) {
	if !digits(accountID, 14) || deviceID == "" || len(deviceKey) != ed25519.PrivateKeySize {
		return "", time.Time{}, ErrInvalidCredentials
	}
	var response DeviceSessionResponse
	if err := c.doDeviceSigned(ctx, DeviceSessionPath, accountID, deviceID, deviceKey, DeviceSessionRequest{
		Protocol: Protocol, AccountID: accountID, DeviceID: deviceID,
	}, &response); err != nil {
		return "", time.Time{}, err
	}
	if response.Protocol != Protocol || response.SessionToken == "" {
		return "", time.Time{}, fmt.Errorf("%w: device session response is inconsistent", ErrInvalidHost)
	}
	return response.SessionToken, response.SessionExpiresAt, nil
}

// VerifyEmail consumes an email verification token.
func (c *Client) VerifyEmail(ctx context.Context, token string) (*EmailVerifyResponse, error) {
	var response EmailVerifyResponse
	if err := c.do(ctx, http.MethodPost, EmailVerifyPath, "", EmailVerifyRequest{
		Protocol: Protocol, Token: token,
	}, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol || !response.Verified {
		return nil, fmt.Errorf("%w: verification response is inconsistent", ErrInvalidHost)
	}
	return &response, nil
}

// ResendEmail asks the host for another verification message.
func (c *Client) ResendEmail(ctx context.Context, sessionToken string) error {
	if sessionToken == "" {
		return ErrSessionInvalid
	}
	return c.do(ctx, http.MethodPost, EmailResendPath, sessionToken, EmailResendRequest{Protocol: Protocol}, nil)
}

// RequestRecovery asks the host to email reset links. The host always accepts
// the request, whether or not the account exists.
func (c *Client) RequestRecovery(ctx context.Context, user string) error {
	return c.do(ctx, http.MethodPost, RecoverRequestPath, "", RecoverRequest{
		Protocol: Protocol, User: strings.TrimSpace(user),
	}, nil)
}

// Sync exchanges public device records.
func (c *Client) Sync(ctx context.Context, sessionToken string, request SyncRequest) (*SyncResponse, error) {
	if sessionToken == "" {
		return nil, ErrSessionInvalid
	}
	request.Protocol = Protocol
	var response SyncResponse
	if err := c.do(ctx, http.MethodPost, SyncPath, sessionToken, request, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: sync response is inconsistent", ErrInvalidHost)
	}
	return &response, nil
}

// ChangePassword re-registers a new password and rewraps the master key.
func (c *Client) ChangePassword(ctx context.Context, sessionToken, accountID, newPassword string, masterKey []byte) error {
	if sessionToken == "" {
		return ErrSessionInvalid
	}
	if !digits(accountID, 14) || newPassword == "" || len(masterKey) != MasterKeySize {
		return errors.New("password change is incomplete")
	}
	info, err := c.Info(ctx)
	if err != nil {
		return err
	}
	registrationClient, err := NewOPAQUERegistrationClient(info.OPAQUEConfig, info.HostID)
	if err != nil {
		return err
	}
	defer registrationClient.Clear()
	opaqueRequest, err := registrationClient.RegisterInit(newPassword)
	if err != nil {
		return err
	}
	var begin PasswordBeginResponse
	if err := c.do(ctx, http.MethodPost, PasswordBeginPath, sessionToken, PasswordBeginRequest{
		Protocol: Protocol, OPAQUERequest: opaqueRequest,
	}, &begin); err != nil {
		return err
	}
	if begin.Protocol != Protocol || len(begin.OPAQUEResponse) == 0 {
		return fmt.Errorf("%w: password response is inconsistent", ErrInvalidHost)
	}
	opaqueRecord, exportKey, err := registrationClient.RegisterFinishFor(accountID, begin.OPAQUEResponse)
	if err != nil {
		return err
	}
	exportWrap, err := WrapExportKey(exportKey, masterKey, accountID)
	if err != nil {
		return err
	}
	var finish PasswordFinishResponse
	return c.do(ctx, http.MethodPost, PasswordFinishPath, sessionToken, PasswordFinishRequest{
		Protocol: Protocol, OPAQUERecord: opaqueRecord, ExportWrap: exportWrap,
	}, &finish)
}

// RotateRecovery creates and installs a new recovery credential. The caller
// must persist the returned file; the old file stops working immediately.
func (c *Client) RotateRecovery(ctx context.Context, sessionToken, accountID string, masterKey []byte) (*RecoveryCredential, []byte, error) {
	if sessionToken == "" {
		return nil, nil, ErrSessionInvalid
	}
	if !digits(accountID, 14) || len(masterKey) != MasterKeySize {
		return nil, nil, errors.New("recovery rotation is incomplete")
	}
	credential, recoveryKey, err := NewRecoveryCredential(accountID, c.hostURL, c.now().UTC())
	if err != nil {
		return nil, nil, err
	}
	recoveryWrap, err := WrapRecoveryKey(recoveryKey, masterKey, accountID)
	if err != nil {
		return nil, nil, err
	}
	if err := c.do(ctx, http.MethodPost, RecoveryRotatePath, sessionToken, RecoveryRotateRequest{
		Protocol: Protocol, RecoveryWrap: recoveryWrap,
	}, nil); err != nil {
		return nil, nil, err
	}
	raw, err := credential.Encode()
	if err != nil {
		return nil, nil, err
	}
	return credential, raw, nil
}

// RecoverContext resolves a reset token.
func (c *Client) RecoverContext(ctx context.Context, token string) (*RecoverContext, error) {
	var response RecoverContext
	if err := c.do(ctx, http.MethodPost, RecoverContextPath, "", RecoverContextRequest{
		Protocol: Protocol, Token: token,
	}, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: recovery context is inconsistent", ErrInvalidHost)
	}
	return &response, nil
}

// RecoverMaterial fetches the ciphertext a reset needs.
func (c *Client) RecoverMaterial(ctx context.Context, token string) (*RecoverMaterial, error) {
	var response RecoverMaterial
	if err := c.do(ctx, http.MethodPost, RecoverMaterialPath, "", RecoverMaterialRequest{
		Protocol: Protocol, Token: token,
	}, &response); err != nil {
		return nil, err
	}
	if response.Protocol != Protocol {
		return nil, fmt.Errorf("%w: recovery material is inconsistent", ErrInvalidHost)
	}
	return &response, nil
}

// RecoveryInspection describes a reset after verifying that the recovery file
// matches the account. It lets a reset page confirm the file before asking for
// a new password.
type RecoveryInspection struct {
	AccountID     string
	Name          string
	EmailHint     string
	HostID        string
	KeyGeneration int
	ExpiresAt     time.Time
}

type recoveryState struct {
	info        *Info
	context     *RecoverContext
	material    *RecoverMaterial
	credential  *RecoveryCredential
	recoveryKey []byte
	masterKey   []byte
	bundle      *KeyBundle
}

func (s *recoveryState) clear() {
	if s == nil {
		return
	}
	wipe(s.recoveryKey)
	wipe(s.masterKey)
	if s.bundle != nil {
		wipe(s.bundle.AccountPrivateKey)
	}
}

// openRecovery verifies a reset token and recovery file, and unlocks MK.
func (c *Client) openRecovery(ctx context.Context, token string, credentialData []byte) (*recoveryState, error) {
	if token == "" {
		return nil, errors.New("reset token is missing")
	}
	credential, recoveryKey, err := ParseRecoveryCredential(credentialData)
	if err != nil {
		return nil, err
	}
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}
	if !sameHostURL(credential.HostURL, c.hostURL) {
		return nil, ErrRecoveryMismatch
	}
	if address, err := account.ParseHostURL(credential.HostURL); err != nil ||
		(address.HostID != "" && address.HostID != info.HostID) {
		// Browser reset pages have no certificate pin, but an identity-pinned
		// recovery file still names the exact host that issued it.
		return nil, ErrRecoveryMismatch
	}
	recoveryContext, err := c.RecoverContext(ctx, token)
	if err != nil {
		return nil, err
	}
	if recoveryContext.AccountID != credential.AccountID ||
		recoveryContext.HostID != info.HostID || !recoveryContext.RecoveryReady {
		return nil, ErrRecoveryMismatch
	}
	material, err := c.RecoverMaterial(ctx, token)
	if err != nil {
		return nil, err
	}
	state := &recoveryState{
		info: info, context: recoveryContext, material: material,
		credential: credential, recoveryKey: recoveryKey,
	}
	if material.AccountID != credential.AccountID {
		state.clear()
		return nil, ErrRecoveryMismatch
	}
	masterKey, err := OpenRecoveryWrap(recoveryKey, material.RecoveryWrap, credential.AccountID)
	if err != nil {
		state.clear()
		return nil, ErrRecoveryMismatch
	}
	state.masterKey = masterKey
	bundle, err := OpenKeyBundle(masterKey, material.KeyBundle, credential.AccountID, material.KeyGeneration)
	if err != nil {
		state.clear()
		return nil, ErrRecoveryMismatch
	}
	state.bundle = bundle
	publicKey := ed25519.PrivateKey(bundle.AccountPrivateKey).Public().(ed25519.PublicKey)
	if !bytes.Equal(publicKey, material.PublicKey) {
		state.clear()
		return nil, ErrRecoveryMismatch
	}
	return state, nil
}

// InspectRecovery verifies a recovery file against an account without
// changing anything. The caller still needs the file for the actual reset.
func (c *Client) InspectRecovery(ctx context.Context, token string, credentialData []byte) (*RecoveryInspection, error) {
	state, err := c.openRecovery(ctx, token, credentialData)
	if err != nil {
		return nil, err
	}
	defer state.clear()
	return &RecoveryInspection{
		AccountID: state.credential.AccountID, Name: state.context.Name,
		EmailHint: state.context.EmailHint, HostID: state.context.HostID,
		KeyGeneration: state.material.KeyGeneration, ExpiresAt: state.context.ExpiresAt,
	}, nil
}

// ResetResult is the outcome of a recovery-file-authorized password reset.
type ResetResult struct {
	AccountID             string
	MasterKey             []byte
	AccountPrivateKey     ed25519.PrivateKey
	KeyBundle             *KeyBundle
	RecoveryCredential    *RecoveryCredential
	RecoveryCredentialRaw []byte
	ResetAt               time.Time
}

// ResetPassword uses a recovery credential to replace a forgotten password.
// The old history is recoverable only because the recovery file unwraps MK.
func (c *Client) ResetPassword(ctx context.Context, token string, credentialData []byte, newPassword string) (*ResetResult, error) {
	if token == "" || newPassword == "" {
		return nil, errors.New("reset is incomplete")
	}
	state, err := c.openRecovery(ctx, token, credentialData)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			state.clear()
		}
	}()
	credential := state.credential
	info := state.info
	masterKey := state.masterKey
	bundle := state.bundle

	registrationClient, err := NewOPAQUERegistrationClient(info.OPAQUEConfig, info.HostID)
	if err != nil {
		return nil, err
	}
	defer registrationClient.Clear()
	opaqueRequest, err := registrationClient.RegisterInit(newPassword)
	if err != nil {
		return nil, err
	}
	var begin RecoverRegisterBeginResponse
	if err := c.do(ctx, http.MethodPost, RecoverRegisterBeginPath, "", RecoverRegisterBeginRequest{
		Protocol: Protocol, Token: token, OPAQUERequest: opaqueRequest,
	}, &begin); err != nil {
		return nil, err
	}
	if begin.Protocol != Protocol || len(begin.OPAQUEResponse) == 0 {
		return nil, fmt.Errorf("%w: reset response is inconsistent", ErrInvalidHost)
	}
	opaqueRecord, exportKey, err := registrationClient.RegisterFinishFor(credential.AccountID, begin.OPAQUEResponse)
	if err != nil {
		return nil, err
	}
	exportWrap, err := WrapExportKey(exportKey, masterKey, credential.AccountID)
	wipe(exportKey)
	if err != nil {
		return nil, err
	}
	newCredential, newRecoveryKey, err := NewRecoveryCredential(credential.AccountID, credential.HostURL, c.now().UTC())
	if err != nil {
		return nil, err
	}
	defer wipe(newRecoveryKey)
	recoveryWrap, err := WrapRecoveryKey(newRecoveryKey, masterKey, credential.AccountID)
	if err != nil {
		return nil, err
	}
	var finish RecoverFinishResponse
	if err := c.do(ctx, http.MethodPost, RecoverFinishPath, "", RecoverFinishRequest{
		Protocol: Protocol, Token: token, OPAQUERecord: opaqueRecord,
		ExportWrap: exportWrap, RecoveryWrap: recoveryWrap,
	}, &finish); err != nil {
		return nil, err
	}
	if finish.Protocol != Protocol || finish.AccountID != credential.AccountID {
		return nil, fmt.Errorf("%w: reset finish is inconsistent", ErrInvalidHost)
	}
	raw, err := newCredential.Encode()
	if err != nil {
		return nil, err
	}
	keep = true
	return &ResetResult{
		AccountID: credential.AccountID, MasterKey: masterKey,
		AccountPrivateKey:     ed25519.PrivateKey(append([]byte(nil), bundle.AccountPrivateKey...)),
		KeyBundle:             bundle,
		RecoveryCredential:    newCredential,
		RecoveryCredentialRaw: raw,
		ResetAt:               finish.ResetAt,
	}, nil
}

// ensureVerified fetches and verifies the host identity before an
// identity-pinned client sends account material for the first time. This
// prevents a credential from being disclosed to an origin that presents the
// proxy's valid edge certificate but cannot prove the pinned host ID.
func (c *Client) ensureVerified(ctx context.Context) error {
	if c == nil || c.hostID == "" {
		return nil
	}
	_, err := c.Info(ctx)
	return err
}

func (c *Client) do(ctx context.Context, method, path, sessionToken string, request, response any) error {
	if err := c.ensureVerified(ctx); err != nil {
		return err
	}
	return c.doRaw(ctx, method, path, sessionToken, request, response)
}

func (c *Client) doRaw(ctx context.Context, method, path, sessionToken string, request, response any) error {
	var body io.Reader
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sessionToken != "" {
		req.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	return c.execute(req, response)
}

// doDeviceSigned signs a request with the device key and sends the account and
// device identity headers. The body is hashed exactly as sent.
func (c *Client) doDeviceSigned(ctx context.Context, path, accountID, deviceID string, key ed25519.PrivateKey, request, response any) error {
	if err := c.ensureVerified(ctx); err != nil {
		return err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(requestauth.HeaderAccount, accountID)
	req.Header.Set(requestauth.HeaderDevice, deviceID)
	if err := requestauth.Sign(req, data, key, c.now().UTC()); err != nil {
		return err
	}
	return c.execute(req, response)
}

func (c *Client) execute(req *http.Request, response any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHostUnavailable, err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHostUnavailable, err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("%w: response is too large", ErrInvalidHost)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiError := &APIError{Status: resp.StatusCode}
		if len(data) > 0 {
			_ = json.Unmarshal(data, apiError)
		}
		if apiError.Code == "" {
			apiError.Message = http.StatusText(resp.StatusCode)
		}
		switch {
		case apiError.Code == "invalid_credentials":
			return fmt.Errorf("%w: %w", ErrInvalidCredentials, apiError)
		case resp.StatusCode == http.StatusUnauthorized:
			return fmt.Errorf("%w: %w", ErrSessionInvalid, apiError)
		default:
			return apiError
		}
	}
	if response == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, response); err != nil {
		return fmt.Errorf("%w: malformed response: %v", ErrInvalidHost, err)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameHostURL(a, b string) bool {
	parsedA, errA := url.Parse(strings.TrimSpace(a))
	parsedB, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil || parsedA.Host == "" || parsedB.Host == "" ||
		parsedA.Scheme != parsedB.Scheme || !strings.EqualFold(parsedA.Host, parsedB.Host) ||
		(parsedA.Path != "" && parsedA.Path != "/") ||
		(parsedB.Path != "" && parsedB.Path != "/") ||
		parsedA.User != nil || parsedB.User != nil {
		return false
	}
	fpA := hostFingerprint(parsedA)
	fpB := hostFingerprint(parsedB)
	if fpA != "" && fpB != "" && fpA != fpB {
		return false
	}
	idA := strings.TrimSpace(parseFragmentValue(parsedA, "id"))
	idB := strings.TrimSpace(parseFragmentValue(parsedB, "id"))
	if idA != "" && idB != "" && idA != idB {
		return false
	}
	// An anchor on only one side is allowed for the same-origin browser reset
	// page, which cannot see a certificate or host identity pin.
	return true
}

func hostFingerprint(parsed *url.URL) string {
	fingerprint := strings.ToLower(parseFragmentValue(parsed, "fp"))
	if fingerprint == "" {
		fingerprint = strings.ToLower(parseFragmentValue(parsed, "sha256"))
	}
	return strings.TrimPrefix(fingerprint, "sha256:")
}

func parseFragmentValue(parsed *url.URL, key string) string {
	values, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(values.Get(key), "sha256:")
}
