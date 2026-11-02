//go:build !js

// The account host's HTTP service is native-only. The reset page compiles this
// package for GOOS=js and only needs the client.
package hosted

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/big"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bytemare/opaque"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/requestauth"
)

const (
	maxLoginStates       = 10000
	maxLoginFailures     = 10
	loginFailureWindow   = 15 * time.Minute
	generalRequestLimit  = 600
	generalRequestWindow = time.Minute
	registerRequestLimit = 30
	loginRequestLimit    = 30
	recoverRequestLimit  = 6
	// A single reset legitimately calls several token-authorized endpoints:
	// context, material (often twice while the user checks files), begin, and
	// finish. Keep the mail-sending request endpoint tight, but allow the
	// high-entropy token endpoints enough headroom for one reset flow.
	recoverTokenLimit = 30
	emailRequestLimit = 6
	maxRecoveryMails  = 3
)

// Options configures an account host.
type Options struct {
	// Store must already have a loaded host identity via EnsureHost.
	Store *Store
	// Mailer sends verification and recovery email. Nil disables email.
	Mailer Mailer
	Logger *slog.Logger
	// PublicURL is the browser-facing HTTPS origin, for example
	// https://accounts.example.com. It must not carry a certificate pin.
	PublicURL string
	// ResetFS contains the static reset page (index.html, wasm, assets).
	ResetFS fs.FS
	// DisableRateLimit is for tests and trusted loopback deployments.
	DisableRateLimit bool
	// TrustedProxies are CIDRs whose forwarded client-IP headers are honored.
	// Leave empty when clients connect directly; otherwise an attacker could
	// choose their own rate-limit bucket by spoofing a header.
	TrustedProxies []netip.Prefix
	// RoomMemberLimit and RoomDeviceLimit bound one official room. Zero
	// applies the default caps; values below one are rejected by New.
	RoomMemberLimit int
	RoomDeviceLimit int
	// DirectoryLookup resolves a server code to an already-verified directory
	// record. Nil disables cross-host account discovery.
	DirectoryLookup func(ctx context.Context, serverCode string) (*DirectoryRecord, error)
	// Now is overridden in tests.
	Now func() time.Time
}

// Service is the multi-tenant official account host.
type Service struct {
	store          *Store
	mailer         Mailer
	logger         *slog.Logger
	host           *HostState
	opaqueHost     *OPAQUEHost
	publicURL      string
	resetFS        fs.FS
	started        time.Time
	now            func() time.Time
	rateLimited    bool
	trustedProxies []netip.Prefix

	roomMemberLimit int
	roomDeviceLimit int

	loginMu sync.Mutex
	logins  map[string]*pendingLogin

	deviceNonces     *requestauth.NonceCache
	federationNonces *requestauth.NonceCache

	limiter  *rateLimiter
	failures *loginFailures

	directoryLookup func(ctx context.Context, serverCode string) (*DirectoryRecord, error)
}

type pendingLogin struct {
	accountID string
	output    *opaque.ServerOutput
	fake      bool
	expires   time.Time
}

// New creates a host service.
func New(options Options) (*Service, error) {
	if options.Store == nil {
		return nil, errors.New("host store is required")
	}
	state := options.Store.Host()
	if state == nil {
		return nil, errors.New("host identity is not loaded")
	}
	opaqueHost, err := NewOPAQUEHost(state.OPAQUEConfig, state.OPAQUEMaterial)
	if err != nil {
		return nil, err
	}
	publicURL, err := normalizePublicURL(options.PublicURL)
	if err != nil {
		return nil, err
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	memberLimit := options.RoomMemberLimit
	if memberLimit == 0 {
		memberLimit = MaxRoomMembers
	}
	deviceLimit := options.RoomDeviceLimit
	if deviceLimit == 0 {
		deviceLimit = MaxRoomDevices
	}
	if memberLimit < 1 || deviceLimit < 1 {
		return nil, errors.New("room limits must be at least one")
	}
	for _, prefix := range options.TrustedProxies {
		if !prefix.IsValid() {
			return nil, errors.New("trusted proxy prefix is invalid")
		}
	}
	return &Service{
		store: options.Store, mailer: options.Mailer, logger: logger,
		host: state, opaqueHost: opaqueHost, publicURL: publicURL,
		resetFS: options.ResetFS, started: now().UTC().Truncate(time.Second),
		now: now, rateLimited: !options.DisableRateLimit,
		trustedProxies:  append([]netip.Prefix(nil), options.TrustedProxies...),
		roomMemberLimit: memberLimit, roomDeviceLimit: deviceLimit,
		logins:           make(map[string]*pendingLogin),
		deviceNonces:     requestauth.NewNonceCache(),
		federationNonces: requestauth.NewNonceCache(),
		limiter:          newRateLimiter(),
		failures:         newLoginFailures(),
		directoryLookup:  options.DirectoryLookup,
	}, nil
}

// HostID returns the self-certifying host identity.
func (s *Service) HostID() string { return s.host.Server.ID }

// HostCode returns the eight-digit host code embedded in every account ID.
func (s *Service) HostCode() string { return s.host.Server.Code }

// HostName returns the operator-visible host name.
func (s *Service) HostName() string { return s.host.Name }

// Manifest returns the signed host manifest.
func (s *Service) Manifest() json.RawMessage {
	return append(json.RawMessage(nil), s.host.ManifestJSON...)
}

// Handler returns the host's HTTP handler.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+HealthPath, s.handleHealth)
	mux.HandleFunc("GET "+InfoPath, s.handleInfo)
	mux.HandleFunc("GET "+AccountLookupPrefix+"{accountID}", s.handleAccountLookup)
	mux.HandleFunc("POST "+ResolveAccountPath, s.handleResolveAccount)
	mux.HandleFunc("POST "+RegisterBeginPath, s.handleRegisterBegin)
	mux.HandleFunc("POST "+RegisterFinishPath, s.handleRegisterFinish)
	mux.HandleFunc("POST "+LoginBeginPath, s.handleLoginBegin)
	mux.HandleFunc("POST "+LoginFinishPath, s.handleLoginFinish)
	mux.HandleFunc("POST "+DeviceSessionPath, s.handleDeviceSession)
	mux.HandleFunc("POST "+SyncPath, s.handleSync)
	mux.HandleFunc("GET "+VaultPath, s.handleVaultGet)
	mux.HandleFunc("PUT "+VaultPath, s.handleVaultPut)
	mux.HandleFunc("POST "+RoomListPath, s.handleRoomList)
	mux.HandleFunc("POST "+RoomCreatePath, s.handleRoomCreate)
	mux.HandleFunc("POST "+RoomSyncPath, s.handleRoomSync)
	mux.HandleFunc("POST "+RoomEventPath, s.handleRoomEvent)
	mux.HandleFunc("POST "+RoomInvitePath, s.handleRoomInvite)
	mux.HandleFunc("POST "+RoomJoinPath, s.handleRoomJoin)
	mux.HandleFunc("POST "+RoomKeysPath, s.handleRoomKeys)
	mux.HandleFunc("POST "+RoomLeavePath, s.handleRoomLeave)
	mux.HandleFunc("POST "+RoomRotatePath, s.handleRoomRotate)
	mux.HandleFunc("POST "+RoomDevicesPath, s.handleRoomDevices)
	mux.HandleFunc("POST "+FriendRequestPath, s.handleFriendRequestCreate)
	mux.HandleFunc("POST "+FriendRequestListPath, s.handleFriendRequestList)
	mux.HandleFunc("POST "+FriendRequestDeletePath, s.handleFriendRequestDelete)
	mux.HandleFunc("POST "+FriendRequestSentPath, s.handleFriendRequestSent)
	mux.HandleFunc("POST "+FriendRequestCancelPath, s.handleFriendRequestCancel)
	mux.HandleFunc("POST "+FederationAccountPath, s.handleFederationAccountLookup)
	mux.HandleFunc("POST "+FederationFriendRequestPath, s.handleFederationFriendRequest)
	mux.HandleFunc("POST "+FederationFriendRequestCancelPath, s.handleFederationFriendRequestCancel)
	mux.HandleFunc("POST "+FederationFriendRequestDeclinePath, s.handleFederationFriendRequestDecline)
	mux.HandleFunc("POST "+PasswordBeginPath, s.handlePasswordBegin)
	mux.HandleFunc("POST "+PasswordFinishPath, s.handlePasswordFinish)
	mux.HandleFunc("POST "+RecoveryRotatePath, s.handleRecoveryRotate)
	mux.HandleFunc("POST "+EmailVerifyPath, s.handleEmailVerify)
	mux.HandleFunc("GET "+EmailVerifyPath, s.handleEmailVerifyPage)
	mux.HandleFunc("POST "+EmailResendPath, s.handleEmailResend)
	mux.HandleFunc("POST "+RecoverRequestPath, s.handleRecoverRequest)
	mux.HandleFunc("POST "+RecoverContextPath, s.handleRecoverContext)
	mux.HandleFunc("POST "+RecoverMaterialPath, s.handleRecoverMaterial)
	mux.HandleFunc("POST "+RecoverRegisterBeginPath, s.handleRecoverRegisterBegin)
	mux.HandleFunc("POST "+RecoverFinishPath, s.handleRecoverFinish)
	if s.resetFS != nil {
		mux.HandleFunc("GET /reset", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/reset/", http.StatusMovedPermanently)
		})
		mux.Handle("GET /reset/", http.StripPrefix("/reset/", newResetAssets(s.resetFS)))
	}
	return s.recoverPanic(s.securityHeaders(s.cors(s.generalLimit(mux))))
}

// cors lets the browser client call a room host that is not the page origin.
// Requests carry no cookies; every mutation is authorized by a device
// signature or bearer session, so a wildcard origin cannot enable CSRF.
func (s *Service) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/host/") {
			next.ServeHTTP(w, r)
			return
		}
		header := w.Header()
		header.Set("Access-Control-Allow-Origin", "*")
		header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		header.Set("Access-Control-Allow-Headers",
			"Accept, Authorization, Content-Type, "+
				requestauth.HeaderTimestamp+", "+requestauth.HeaderNonce+", "+
				requestauth.HeaderSignature+", "+requestauth.HeaderAccount+", "+
				requestauth.HeaderDevice+", "+HeaderDeviceCertificate)
		header.Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) generalLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.rateLimited && !s.limiter.allow("api:"+s.clientIP(r), generalRequestLimit, generalRequestWindow, s.now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again later")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/host/") {
			header.Set("Cross-Origin-Resource-Policy", "cross-origin")
		} else {
			header.Set("Cross-Origin-Resource-Policy", "same-origin")
		}
		if strings.HasPrefix(r.URL.Path, "/reset") {
			header.Set("Content-Security-Policy",
				"default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; "+
					"connect-src 'self'; style-src 'self'; img-src 'self' data:; "+
					"base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		} else {
			header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			header.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("hosted request panic", "path", r.URL.Path, "panic", recovered)
				writeError(w, http.StatusInternalServerError, "internal", "request failed")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "protocol": Protocol, "host_id": s.host.Server.ID,
	})
}

func (s *Service) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Info{
		Protocol: Protocol, HostID: s.host.Server.ID, HostCode: s.host.Server.Code,
		Name: s.host.Name, Manifest: json.RawMessage(s.host.ManifestJSON),
		OPAQUEConfig:  s.host.OPAQUEConfig,
		EmailRecovery: s.mailer != nil && s.resetFS != nil && s.publicURL != "",
		ResetEnabled:  s.resetFS != nil, IssuedAt: s.started,
	})
}

func (s *Service) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "register", registerRequestLimit) {
		return
	}
	var request RegisterBeginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > MaxNameLength {
		writeError(w, http.StatusBadRequest, "invalid_name", "invalid account name")
		return
	}
	if len(request.AccountPublicKey) != ed25519.PublicKeySize {
		writeError(w, http.StatusBadRequest, "invalid_key", "invalid account key")
		return
	}
	if len(request.OPAQUERequest) == 0 || len(request.OPAQUERequest) > MaxOpaqueRequest {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid registration request")
		return
	}
	email := ""
	if strings.TrimSpace(request.Email) != "" {
		var ok bool
		email, ok = validEmail(request.Email)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_email", "invalid email address")
			return
		}
	}
	accountID, accountCode, err := s.newAccountID(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var emailCipher, emailHash []byte
	if email != "" {
		emailCipher, emailHash, err = s.store.SealEmail(accountID, email)
		if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	opaqueResponse, err := s.opaqueHost.RegistrationResponse(accountID, request.OPAQUERequest)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid registration request")
		return
	}
	now := s.now().UTC()
	pending := &PendingRegistration{
		AccountID: accountID, AccountCode: accountCode, Name: name,
		EmailCipher: emailCipher, EmailHash: emailHash,
		AccountPublicKey: append([]byte(nil), request.AccountPublicKey...),
		ExpiresAt:        now.Add(PendingTTL), CreatedAt: now,
	}
	if err := s.store.CreatePendingRegistration(r.Context(), pending); err != nil {
		if errors.Is(err, ErrTooManyPending) {
			writeError(w, http.StatusTooManyRequests, "busy", "too many signups in progress")
			return
		}
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RegisterBeginResponse{
		Protocol: Protocol, AccountID: accountID, AccountCode: accountCode,
		HostID: s.host.Server.ID, HostCode: s.host.Server.Code, Name: name,
		OPAQUEConfig: s.host.OPAQUEConfig, OPAQUEResponse: opaqueResponse,
		ExpiresAt: pending.ExpiresAt,
	})
}

func (s *Service) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "register", registerRequestLimit) {
		return
	}
	var request RegisterFinishRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol || !digits(request.AccountID, 14) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid registration")
		return
	}
	pending, err := s.store.GetPendingRegistration(r.Context(), request.AccountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_registration", "registration is missing or expired")
		return
	}
	if _, err := s.opaqueHost.RegistrationRecord(request.AccountID, request.OPAQUERecord); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_registration", "registration proof is invalid")
		return
	}
	keys := []ed25519.PublicKey{append(ed25519.PublicKey(nil), pending.AccountPublicKey...)}
	device, _, err := account.ValidateDeviceRecord(request.Device, request.AccountID, keys, s.now().UTC())
	if err != nil || request.Device.Revocation != "" {
		writeError(w, http.StatusBadRequest, "invalid_device", "device record is invalid")
		return
	}
	record := account.DeviceRecord{
		ID: device.ID, AccountID: device.AccountID, Name: device.Name,
		PublicKey:           append([]byte(nil), device.PublicKey...),
		EncryptionPublicKey: append([]byte(nil), device.EncryptionPublicKey...),
		Certificate:         device.Certificate, NodeURL: device.NodeURL,
		Role: device.Role, CreatedAt: device.CreatedAt,
	}
	stored, err := s.store.FinishRegistration(r.Context(), &RegistrationFinish{
		AccountID: request.AccountID, OPAQUERecord: request.OPAQUERecord,
		ExportWrap: request.ExportWrap, RecoveryWrap: request.RecoveryWrap,
		KeyBundle: request.KeyBundle, Device: record,
	})
	if err != nil {
		if errors.Is(err, ErrPendingRegistration) {
			writeError(w, http.StatusBadRequest, "invalid_registration", "registration is missing or expired")
			return
		}
		s.internal(w, r, err)
		return
	}

	sessionToken, sessionExpires, sessionErr := s.createSession(r.Context(), stored.ID, record.ID)
	if sessionErr != nil {
		s.logger.Error("hosted session after registration failed", "error", sessionErr)
	}
	emailSent := false
	if len(pending.EmailCipher) > 0 && s.mailer != nil && s.publicURL != "" && s.resetFS != nil {
		if email, err := s.store.OpenEmail(stored.ID, pending.EmailCipher); err == nil {
			if token, err := s.issueEmailToken(r.Context(), stored.ID, emailTokenPurposeVerify, VerifyTokenTTL); err == nil {
				if err := s.sendVerificationEmail(r.Context(), stored.ID, email, token); err != nil {
					s.logger.Error("hosted verification email failed", "error", err)
				} else {
					emailSent = true
				}
			} else {
				s.logger.Error("hosted verification token failed", "error", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, RegisterFinishResponse{
		Protocol: Protocol, AccountID: stored.ID, Name: stored.Name,
		HostID: s.host.Server.ID, SessionToken: sessionToken,
		SessionExpiresAt: sessionExpires, EmailSent: emailSent, CreatedAt: stored.CreatedAt,
	})
}

func (s *Service) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "login", loginRequestLimit) {
		return
	}
	var request LoginBeginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol || !digits(request.AccountID, 14) ||
		len(request.KE1) == 0 || len(request.KE1) > MaxOpaqueRequest {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid login request")
		return
	}
	if s.rateLimited && s.failures.blocked(request.AccountID, s.now()) {
		writeError(w, http.StatusTooManyRequests, "try_again", "too many attempts; try again later")
		return
	}
	var record *opaque.ClientRecord
	acct, err := s.store.GetAccount(r.Context(), request.AccountID)
	switch {
	case err == nil:
		record, err = s.opaqueHost.DecodeRecord(acct.ID, acct.OPAQUERecord)
		if err != nil {
			s.internal(w, r, err)
			return
		}
	case errors.Is(err, ErrAccountNotFound):
		record = nil
	default:
		s.internal(w, r, err)
		return
	}
	enrollment, err := s.opaqueHost.LoginBegin(request.AccountID, record, request.KE1)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid login request")
		return
	}
	loginID, err := randomToken()
	if err != nil {
		s.internal(w, r, err)
		return
	}
	expires := s.now().UTC().Add(LoginTTL)
	s.loginMu.Lock()
	s.pruneLoginsLocked(expires)
	if len(s.logins) >= maxLoginStates {
		s.loginMu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "busy", "login capacity is full")
		return
	}
	s.logins[loginID] = &pendingLogin{
		accountID: request.AccountID, output: enrollment.Output,
		fake: enrollment.Fake, expires: expires,
	}
	s.loginMu.Unlock()
	writeJSON(w, http.StatusOK, LoginBeginResponse{
		Protocol: Protocol, LoginID: loginID, KE2: enrollment.KE2, ExpiresAt: expires,
	})
}

func (s *Service) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "login", loginRequestLimit) {
		return
	}
	var request LoginFinishRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol || len(request.LoginID) == 0 ||
		len(request.KE3) == 0 || len(request.KE3) > MaxOpaqueRequest {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid login request")
		return
	}
	state := s.takeLogin(request.LoginID)
	if state == nil || !s.now().UTC().Before(state.expires) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid account or password")
		return
	}
	if s.rateLimited && s.failures.blocked(state.accountID, s.now()) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid account or password")
		return
	}
	if err := s.opaqueHost.LoginFinish(state.output, state.fake, request.KE3); err != nil {
		s.recordFailure(state.accountID)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid account or password")
		return
	}
	acct, err := s.store.GetAccount(r.Context(), state.accountID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.clearFailures(acct.ID)
	devices, err := s.store.ListDevices(r.Context(), acct.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sessionToken, sessionExpires, err := s.createSession(r.Context(), acct.ID, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if devices == nil {
		devices = []account.DeviceRecord{}
	}
	writeJSON(w, http.StatusOK, AccountMaterial{
		Protocol: Protocol, AccountID: acct.ID, Name: acct.Name,
		HostID: s.host.Server.ID, HostCode: s.host.Server.Code,
		PublicKey:     append([]byte(nil), acct.PublicKey...),
		KeyGeneration: acct.KeyGeneration, KeyRotations: acct.KeyRotations,
		ExportWrap: acct.ExportWrap, RecoveryWrap: acct.RecoveryWrap,
		KeyBundle: acct.KeyBundle, Devices: devices, EmailVerified: acct.EmailVerified,
		SessionToken: sessionToken, SessionExpiresAt: sessionExpires,
		CreatedAt: acct.CreatedAt,
	})
}

// handleDeviceSession exchanges an account-signed device certificate and a
// device-key signature for a short-lived bearer session. It never asks for the
// account password, so a device can keep syncing while its local vault is
// unlocked even when the user does not remember the host password.
func (s *Service) handleDeviceSession(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "device-session", 120) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	var request DeviceSessionRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol || !digits(request.AccountID, 14) ||
		request.DeviceID == "" || len(request.DeviceID) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid device session request")
		return
	}
	if !requestauth.ConstantTimeEqual(r.Header.Get(requestauth.HeaderAccount), request.AccountID) ||
		!requestauth.ConstantTimeEqual(r.Header.Get(requestauth.HeaderDevice), request.DeviceID) {
		writeError(w, http.StatusForbidden, "forbidden", "request identity does not match the device session request")
		return
	}
	acct, err := s.store.GetAccount(r.Context(), request.AccountID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return
	}
	record, err := s.store.GetDevice(r.Context(), request.AccountID, request.DeviceID)
	if err != nil || record.RevokedAt != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return
	}
	keys, err := accountKeys(acct)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return
	}
	var cert *identity.DeviceCertificate
	for _, key := range keys {
		candidate, verifyErr := identity.VerifyDeviceCertificateSignature([]byte(record.Certificate), key)
		if verifyErr == nil && candidate.AccountID == request.AccountID && candidate.DeviceID == request.DeviceID {
			cert = candidate
			break
		}
	}
	if cert == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return
	}
	publicKey, err := identity.DecodePublicKey(cert.DevicePubKey)
	if err != nil || !bytes.Equal(publicKey, record.PublicKey) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device is not authorized")
		return
	}
	if err := requestauth.Verify(r, body, publicKey, s.deviceNonces, s.now()); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "device signature is invalid")
		return
	}
	token, expires, err := s.createSession(r.Context(), acct.ID, request.DeviceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DeviceSessionResponse{
		Protocol: Protocol, SessionToken: token, SessionExpiresAt: expires,
	})
}

func (s *Service) handleSync(w http.ResponseWriter, r *http.Request) {
	acct, sessionHash := s.authenticate(w, r)
	if acct == nil {
		return
	}
	var request SyncRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	if len(request.Devices) > MaxDeviceRecords {
		writeError(w, http.StatusBadRequest, "too_many_devices", "too many device records")
		return
	}
	if request.KeyGeneration > 0 {
		if request.KeyGeneration < acct.KeyGeneration {
			writeError(w, http.StatusConflict, "key_conflict", "account key generation is stale")
			return
		}
		if err := s.store.UpdateKeyMaterial(r.Context(), acct.ID, request.KeyGeneration,
			request.PublicKey, request.KeyRotations, request.KeyBundle); err != nil {
			if errors.Is(err, ErrKeyMaterial) {
				writeError(w, http.StatusConflict, "key_conflict", "account key material conflicts with this host")
				return
			}
			s.internal(w, r, err)
			return
		}
		updated, err := s.store.GetAccount(r.Context(), acct.ID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		acct = updated
	}
	summary, err := s.store.SyncDevices(r.Context(), acct, request.Devices)
	if err != nil {
		if errors.Is(err, account.ErrTooManyDeviceRecords) || errors.Is(err, account.ErrInvalidDeviceRecord) {
			writeError(w, http.StatusBadRequest, "invalid_device", "device records are invalid")
			return
		}
		s.internal(w, r, err)
		return
	}
	if request.SessionDeviceID != "" {
		if len(request.SessionDeviceID) > 128 {
			writeError(w, http.StatusBadRequest, "invalid_device", "session device is invalid")
			return
		}
		record, err := s.store.GetDevice(r.Context(), acct.ID, request.SessionDeviceID)
		if err != nil || record.RevokedAt != nil {
			writeError(w, http.StatusBadRequest, "invalid_device", "session device is not registered")
			return
		}
		if err := s.store.BindSessionDevice(r.Context(), acct.ID, sessionHash, request.SessionDeviceID); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	// A revoked device must lose its live bearer session immediately, but
	// only that device: other browsers on the account keep working.
	for _, record := range request.Devices {
		if record.RevokedAt == nil {
			continue
		}
		current, err := s.store.GetDevice(r.Context(), acct.ID, record.ID)
		if err != nil || current.RevokedAt == nil {
			continue
		}
		if err := s.store.DeleteDeviceSessions(r.Context(), acct.ID, record.ID); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, SyncResponse{
		Protocol: Protocol, Devices: summary.Devices,
		KeyGeneration: acct.KeyGeneration, PublicKey: acct.PublicKey,
		KeyRotations: acct.KeyRotations, Added: summary.Added,
		Updated: summary.Updated, Revoked: summary.Revoked,
		Skipped: summary.Skipped, Warnings: summary.Warnings,
		EmailVerified: acct.EmailVerified,
	})
}

func (s *Service) handlePasswordBegin(w http.ResponseWriter, r *http.Request) {
	acct, _ := s.authenticate(w, r)
	if acct == nil {
		return
	}
	if s.limited(w, r, "password", loginRequestLimit) {
		return
	}
	var request PasswordBeginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol || len(request.OPAQUERequest) == 0 ||
		len(request.OPAQUERequest) > MaxOpaqueRequest {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid password request")
		return
	}
	response, err := s.opaqueHost.RegistrationResponse(acct.ID, request.OPAQUERequest)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid password request")
		return
	}
	writeJSON(w, http.StatusOK, PasswordBeginResponse{
		Protocol: Protocol, OPAQUEResponse: response,
		ExpiresAt: s.now().UTC().Add(PendingTTL),
	})
}

func (s *Service) handlePasswordFinish(w http.ResponseWriter, r *http.Request) {
	acct, sessionHash := s.authenticate(w, r)
	if acct == nil {
		return
	}
	if s.limited(w, r, "password", loginRequestLimit) {
		return
	}
	var request PasswordFinishRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	if _, err := s.opaqueHost.RegistrationRecord(acct.ID, request.OPAQUERecord); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_password_record", "invalid password record")
		return
	}
	if err := s.store.UpdatePassword(r.Context(), acct.ID, request.OPAQUERecord, request.ExportWrap); err != nil {
		if errors.Is(err, ErrInvalidBlob) {
			writeError(w, http.StatusBadRequest, "invalid_password_record", "invalid password record")
			return
		}
		s.internal(w, r, err)
		return
	}
	if err := s.store.DeleteOtherSessions(r.Context(), acct.ID, sessionHash); err != nil {
		s.logger.Error("hosted session cleanup failed", "error", err)
	}
	writeJSON(w, http.StatusOK, PasswordFinishResponse{Protocol: Protocol, ChangedAt: s.now().UTC()})
}

func (s *Service) handleRecoveryRotate(w http.ResponseWriter, r *http.Request) {
	acct, _ := s.authenticate(w, r)
	if acct == nil {
		return
	}
	var request RecoveryRotateRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	if err := s.store.UpdateRecoveryWrap(r.Context(), acct.ID, request.RecoveryWrap); err != nil {
		if errors.Is(err, ErrInvalidBlob) {
			writeError(w, http.StatusBadRequest, "invalid_recovery", "invalid recovery credential")
			return
		}
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleEmailVerify(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "email", emailRequestLimit) {
		return
	}
	var request EmailVerifyRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	hash, err := HashEmailToken(request.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_token", "verification link is invalid or expired")
		return
	}
	token, err := s.store.ConsumeEmailToken(r.Context(), hash, emailTokenPurposeVerify)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_token", "verification link is invalid or expired")
		return
	}
	if err := s.store.MarkEmailVerified(r.Context(), token.AccountID); err != nil {
		s.internal(w, r, err)
		return
	}
	acct, err := s.store.GetAccount(r.Context(), token.AccountID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	email, _ := s.store.OpenEmail(acct.ID, acct.EmailCipher)
	writeJSON(w, http.StatusOK, EmailVerifyResponse{Protocol: Protocol, Email: email, Verified: true})
}

func (s *Service) handleEmailVerifyPage(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "email", emailRequestLimit) {
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	hash, err := HashEmailToken(token)
	if err != nil {
		s.verificationPage(w, false)
		return
	}
	consumed, err := s.store.ConsumeEmailToken(r.Context(), hash, emailTokenPurposeVerify)
	if err != nil {
		s.verificationPage(w, false)
		return
	}
	if err := s.store.MarkEmailVerified(r.Context(), consumed.AccountID); err != nil {
		s.logger.Error("hosted email verification failed", "error", err)
		s.verificationPage(w, false)
		return
	}
	s.verificationPage(w, true)
}

func (s *Service) verificationPage(w http.ResponseWriter, ok bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
	}
	fmt.Fprintf(w, `<!doctype html><html lang="zh"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>邮箱验证 · Toget</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1.5rem;color:#1f2937}p{line-height:1.7}</style>
<h1>邮箱验证</h1><p>%s</p></html>`,
		map[bool]string{
			true:  "验证成功。现在可以关闭这个页面，回到 Toget 继续。",
			false: "这个验证链接无效或已过期。请在 Toget 中重新发送验证邮件。",
		}[ok])
}

func (s *Service) handleEmailResend(w http.ResponseWriter, r *http.Request) {
	acct, _ := s.authenticate(w, r)
	if acct == nil {
		return
	}
	if s.limited(w, r, "email", emailRequestLimit) {
		return
	}
	var request EmailResendRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	if s.mailer == nil || s.publicURL == "" || s.resetFS == nil {
		writeError(w, http.StatusConflict, "email_unavailable", "this host cannot send email")
		return
	}
	if len(acct.EmailCipher) == 0 {
		writeError(w, http.StatusConflict, "email_unavailable", "this account has no email address")
		return
	}
	email, err := s.store.OpenEmail(acct.ID, acct.EmailCipher)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if email == "" {
		writeError(w, http.StatusConflict, "email_unavailable", "this account has no email address")
		return
	}
	token, err := s.issueEmailToken(r.Context(), acct.ID, emailTokenPurposeVerify, VerifyTokenTTL)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.sendVerificationEmail(r.Context(), acct.ID, email, token); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleRecoverRequest(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "recover", recoverRequestLimit) {
		return
	}
	var request RecoverRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	// The response never reflects whether an account exists.
	defer writeJSON(w, http.StatusAccepted, map[string]any{"protocol": Protocol})
	if s.mailer == nil || s.publicURL == "" || s.resetFS == nil {
		return
	}
	refs := s.recoveryTargets(r.Context(), request.User)
	sent := 0
	for _, ref := range refs {
		if sent >= maxRecoveryMails {
			break
		}
		acct, err := s.store.GetAccount(r.Context(), ref.ID)
		if err != nil || !acct.EmailVerified || len(acct.EmailCipher) == 0 {
			continue
		}
		email, err := s.store.OpenEmail(acct.ID, acct.EmailCipher)
		if err != nil || email == "" {
			continue
		}
		token, err := s.issueEmailToken(r.Context(), acct.ID, emailTokenPurposeReset, ResetTokenTTL)
		if err != nil {
			s.logger.Error("hosted recovery token failed", "error", err)
			continue
		}
		if err := s.sendRecoveryEmail(r.Context(), acct.ID, email, token); err != nil {
			s.logger.Error("hosted recovery email failed", "error", err)
			continue
		}
		sent++
	}
}

func (s *Service) recoveryTargets(ctx context.Context, user string) []AccountRef {
	user = strings.TrimSpace(user)
	if strings.Contains(user, "@") {
		email, ok := validEmail(user)
		if !ok {
			return nil
		}
		hash, err := s.store.EmailIndex(email)
		if err != nil {
			return nil
		}
		refs, err := s.store.ListAccountsByEmailHash(ctx, hash)
		if err != nil {
			return nil
		}
		return refs
	}
	if !digits(user, 14) {
		return nil
	}
	ref, err := s.store.GetAccountRef(ctx, user)
	if err != nil {
		return nil
	}
	return []AccountRef{*ref}
}

func (s *Service) handleRecoverContext(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "recover-token", recoverTokenLimit) {
		return
	}
	var request RecoverContextRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	hash, err := HashEmailToken(request.Token)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid_token", "reset link is invalid or expired")
		return
	}
	token, err := s.store.LookupEmailToken(r.Context(), hash, emailTokenPurposeReset)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid_token", "reset link is invalid or expired")
		return
	}
	ref, err := s.store.GetAccountRef(r.Context(), token.AccountID)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid_token", "reset link is invalid or expired")
		return
	}
	acct, err := s.store.GetAccount(r.Context(), token.AccountID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	hint := ""
	if email, err := s.store.OpenEmail(acct.ID, acct.EmailCipher); err == nil {
		hint = EmailHint(email)
	}
	writeJSON(w, http.StatusOK, RecoverContext{
		Protocol: Protocol, AccountID: ref.ID, Name: ref.Name,
		HostID: s.host.Server.ID, EmailHint: hint, Purpose: emailTokenPurposeReset,
		ExpiresAt: token.ExpiresAt, RecoveryReady: validateBlob(acct.RecoveryWrap) == nil,
	})
}

func (s *Service) handleRecoverMaterial(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "recover-token", recoverTokenLimit) {
		return
	}
	var request RecoverMaterialRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	hash, err := HashEmailToken(request.Token)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid_token", "reset link is invalid or expired")
		return
	}
	token, err := s.store.LookupEmailToken(r.Context(), hash, emailTokenPurposeReset)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid_token", "reset link is invalid or expired")
		return
	}
	acct, err := s.store.GetAccount(r.Context(), token.AccountID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RecoverMaterial{
		Protocol: Protocol, AccountID: acct.ID,
		PublicKey:     append([]byte(nil), acct.PublicKey...),
		KeyGeneration: acct.KeyGeneration, RecoveryWrap: acct.RecoveryWrap,
		ExportWrap: acct.ExportWrap, KeyBundle: acct.KeyBundle,
	})
}

func (s *Service) handleRecoverRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "recover-token", recoverTokenLimit) {
		return
	}
	var request RecoverRegisterBeginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol || len(request.OPAQUERequest) == 0 ||
		len(request.OPAQUERequest) > MaxOpaqueRequest {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid reset request")
		return
	}
	hash, err := HashEmailToken(request.Token)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid_token", "reset link is invalid or expired")
		return
	}
	token, err := s.store.LookupEmailToken(r.Context(), hash, emailTokenPurposeReset)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid_token", "reset link is invalid or expired")
		return
	}
	response, err := s.opaqueHost.RegistrationResponse(token.AccountID, request.OPAQUERequest)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid reset request")
		return
	}
	writeJSON(w, http.StatusOK, RecoverRegisterBeginResponse{
		Protocol: Protocol, OPAQUEResponse: response, ExpiresAt: token.ExpiresAt,
	})
}

func (s *Service) handleRecoverFinish(w http.ResponseWriter, r *http.Request) {
	if s.limited(w, r, "recover-token", recoverTokenLimit) {
		return
	}
	var request RecoverFinishRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if request.Protocol != Protocol {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "unsupported protocol")
		return
	}
	hash, err := HashEmailToken(request.Token)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid_token", "reset link is invalid or expired")
		return
	}
	token, err := s.store.LookupEmailToken(r.Context(), hash, emailTokenPurposeReset)
	if err != nil {
		writeError(w, http.StatusNotFound, "invalid_token", "reset link is invalid or expired")
		return
	}
	if _, err := s.opaqueHost.RegistrationRecord(token.AccountID, request.OPAQUERecord); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_reset", "invalid password record")
		return
	}
	if validateBlob(request.ExportWrap) != nil || validateBlob(request.RecoveryWrap) != nil {
		writeError(w, http.StatusBadRequest, "invalid_reset", "invalid reset material")
		return
	}
	if err := s.store.FinishRecovery(r.Context(), token.AccountID, hash,
		request.OPAQUERecord, request.ExportWrap, request.RecoveryWrap); err != nil {
		if errors.Is(err, ErrEmailTokenInvalid) {
			writeError(w, http.StatusNotFound, "invalid_token", "reset link is invalid or expired")
			return
		}
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RecoverFinishResponse{
		Protocol: Protocol, AccountID: token.AccountID, ResetAt: s.now().UTC(),
	})
}

func (s *Service) authenticate(w http.ResponseWriter, r *http.Request) (*AccountRecord, []byte) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return nil, nil
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	hash, err := HashEmailToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return nil, nil
	}
	accountID, err := s.store.SessionAccount(r.Context(), hash)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return nil, nil
	}
	acct, err := s.store.GetAccount(r.Context(), accountID)
	if err != nil {
		s.internal(w, r, err)
		return nil, nil
	}
	return acct, hash
}

func (s *Service) createSession(ctx context.Context, accountID, deviceID string) (string, time.Time, error) {
	token, hash, err := NewEmailToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := s.now().UTC().Add(SessionTTL)
	if err := s.store.CreateSession(ctx, accountID, hash, expires); err != nil {
		return "", time.Time{}, err
	}
	if deviceID != "" {
		if err := s.store.BindSessionDevice(ctx, accountID, hash, deviceID); err != nil {
			return "", time.Time{}, err
		}
	}
	return token, expires, nil
}

func (s *Service) issueEmailToken(ctx context.Context, accountID, purpose string, ttl time.Duration) (string, error) {
	token, hash, err := NewEmailToken()
	if err != nil {
		return "", err
	}
	if err := s.store.CreateEmailToken(ctx, accountID, purpose, hash, s.now().UTC().Add(ttl)); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) sendVerificationEmail(ctx context.Context, accountID, email, token string) error {
	link := s.publicURL + "/reset/#" + url.Values{"verify": {token}}.Encode()
	return s.mailer.Send(ctx, Message{
		To: email, Subject: "验证你的 Toget 账号邮箱",
		Text: fmt.Sprintf("Toget 账号 %s 正在绑定这个邮箱。\n\n请打开下面的链接完成验证（24 小时内有效）：\n%s\n\n如果不是你本人操作，请忽略这封邮件。\n", accountID, link),
	})
}

func (s *Service) sendRecoveryEmail(ctx context.Context, accountID, email, token string) error {
	link := s.publicURL + "/reset/#" + url.Values{"reset": {token}}.Encode()
	return s.mailer.Send(ctx, Message{
		To: email, Subject: "重置你的 Toget 账号密码",
		Text: fmt.Sprintf("有人请求重置 Toget 账号 %s 的密码。\n\n请打开下面的链接（30 分钟内有效）：\n%s\n\n重置需要你的账号恢复文件；没有恢复文件就无法恢复历史记录。\n如果不是你本人操作，请忽略这封邮件。\n", accountID, link),
	})
}

func (s *Service) newAccountID(ctx context.Context) (string, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		n, err := rand.Int(rand.Reader, big.NewInt(900000))
		if err != nil {
			return "", "", err
		}
		code := fmt.Sprintf("%06d", n.Int64()+100000)
		id := identity.AccountID(s.host.Server.Code, code)
		if _, err := s.store.GetAccount(ctx, id); err == nil {
			continue
		} else if !errors.Is(err, ErrAccountNotFound) {
			return "", "", err
		}
		if _, err := s.store.GetPendingRegistration(ctx, id); err == nil {
			continue
		} else if !errors.Is(err, ErrPendingRegistration) {
			return "", "", err
		}
		return id, code, nil
	}
	return "", "", errors.New("could not allocate an account id")
}

func (s *Service) takeLogin(id string) *pendingLogin {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	state := s.logins[id]
	delete(s.logins, id)
	return state
}

func (s *Service) pruneLoginsLocked(now time.Time) {
	for id, state := range s.logins {
		if !now.Before(state.expires) {
			delete(s.logins, id)
		}
	}
}

func (s *Service) limited(w http.ResponseWriter, r *http.Request, bucket string, limit int) bool {
	if !s.rateLimited {
		return false
	}
	key := s.clientIP(r) + ":" + bucket
	if s.limiter.allow(key, limit, time.Minute, s.now()) {
		return false
	}
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again later")
	return true
}

func (s *Service) recordFailure(accountID string) {
	if !s.rateLimited {
		return
	}
	s.failures.fail(accountID, s.now())
}

func (s *Service) clearFailures(accountID string) {
	if s.rateLimited {
		s.failures.clear(accountID)
	}
}

func (s *Service) internal(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		s.logger.Error("hosted request failed", "path", r.URL.Path, "error", err)
	}
	writeError(w, http.StatusInternalServerError, "internal", "request failed")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if contentType := strings.TrimSpace(r.Header.Get("Content-Type")); contentType != "" &&
		!strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		return errors.New("unsupported content type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request carries more than one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, Error{Code: code, Message: message})
}

func validEmail(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxEmailLength || strings.ContainsAny(raw, "\r\n") {
		return "", false
	}
	address, err := mail.ParseAddress(raw)
	if err != nil || address.Name != "" || address.Address != raw {
		return "", false
	}
	if !strings.Contains(address.Address, "@") {
		return "", false
	}
	return normalizeEmail(address.Address), true
}

func normalizePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("public URL must be an HTTPS origin")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type rateWindow struct {
	start time.Time
	count int
}

type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]*rateWindow
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{windows: make(map[string]*rateWindow)}
}

func (l *rateLimiter) allow(key string, limit int, per time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.windows) > 8192 {
		for existing, window := range l.windows {
			if now.Sub(window.start) > per {
				delete(l.windows, existing)
			}
		}
	}
	window, ok := l.windows[key]
	if !ok || now.Sub(window.start) >= per {
		l.windows[key] = &rateWindow{start: now, count: 1}
		return true
	}
	if window.count >= limit {
		return false
	}
	window.count++
	return true
}

type failureEntry struct {
	count   int
	resetAt time.Time
}

type loginFailures struct {
	mu      sync.Mutex
	entries map[string]*failureEntry
}

func newLoginFailures() *loginFailures {
	return &loginFailures{entries: make(map[string]*failureEntry)}
}

func (f *loginFailures) blocked(accountID string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry := f.entries[accountID]
	if entry == nil {
		return false
	}
	if !now.Before(entry.resetAt) {
		delete(f.entries, accountID)
		return false
	}
	return entry.count >= maxLoginFailures
}

func (f *loginFailures) fail(accountID string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.entries) > 8192 {
		for key, entry := range f.entries {
			if !now.Before(entry.resetAt) {
				delete(f.entries, key)
			}
		}
	}
	entry := f.entries[accountID]
	if entry == nil || !now.Before(entry.resetAt) {
		f.entries[accountID] = &failureEntry{count: 1, resetAt: now.Add(loginFailureWindow)}
		return
	}
	entry.count++
}

func (f *loginFailures) clear(accountID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, accountID)
}
