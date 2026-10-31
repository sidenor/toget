package node

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/sealbox"
)

const (
	// PairPrefix identifies a portable device-pairing code.
	PairPrefix = "toget-pair:v1:"
	// MaxPairCodeLength bounds pasted pairing input.
	MaxPairCodeLength = 64 << 10
	// PairTTL is how long a pairing code may be used.
	PairTTL = 10 * time.Minute
	// PairApprovedTTL is how long an approved pairing can still be claimed.
	PairApprovedTTL = 10 * time.Minute
	// MaxActivePairings bounds in-memory pairing sessions.
	MaxActivePairings = 16
	// MaxPairRooms bounds rooms transferred by one pairing.
	MaxPairRooms = room.MaxDeviceSnapshotRooms
	// maxPairRoomsPerBundle bounds one recovery response. The new device
	// follows NextOffset until HasMore is false.
	maxPairRoomsPerBundle = 64
	// maxPairBundleBytes keeps one recovery response below the node client's
	// response limit even when rooms have long histories.
	maxPairBundleBytes = 24 << 20
	// MaxPairJoinSessions bounds in-flight joins on the new device.
	MaxPairJoinSessions = 8
)

var (
	// ErrPairingDisabled means LAN node transport is disabled.
	ErrPairingDisabled = errors.New("node transport is disabled")
	// ErrPairingNotFound means no pairing matches the request.
	ErrPairingNotFound = errors.New("pairing not found")
	// ErrPairingExpired means a pairing code is no longer usable.
	ErrPairingExpired = errors.New("pairing expired")
	// ErrPairingCanceled means the approving device canceled the pairing.
	ErrPairingCanceled = errors.New("pairing canceled")
	// ErrPairingState means the pairing is not ready for this step.
	ErrPairingState = errors.New("pairing is not in the right state")
	// ErrPairJoinNotFound means no in-flight join matches the request.
	ErrPairJoinNotFound = errors.New("pairing join not found")
)

// PairCode carries everything the new device needs to reach the approving
// node. The TLS fingerprint is in the URL fragment and never travels in an
// HTTP request.
type PairCode struct {
	Protocol    int       `json:"protocol"`
	PairingID   string    `json:"pairing_id"`
	Secret      string    `json:"secret"`
	URL         string    `json:"url"`
	ServerID    string    `json:"server_id"`
	AccountID   string    `json:"account_id"`
	AccountName string    `json:"account_name,omitempty"`
	DeviceName  string    `json:"device_name,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// EncodePairCode serializes a pairing code for display.
func EncodePairCode(code PairCode) (string, error) {
	if code.Protocol == 0 {
		code.Protocol = Protocol
	}
	if code.Protocol != Protocol {
		return "", fmt.Errorf("%w: unsupported protocol", ErrPairingState)
	}
	if code.PairingID == "" || len(code.Secret) < 32 || code.ServerID == "" || !digitsOnly(code.AccountID, 14) {
		return "", fmt.Errorf("%w: pairing code is incomplete", ErrPairingState)
	}
	if code.ExpiresAt.IsZero() {
		return "", fmt.Errorf("%w: pairing code has no expiry", ErrPairingState)
	}
	if _, _, err := ParsePinnedURL(code.URL); err != nil {
		return "", err
	}
	data, err := json.Marshal(code)
	if err != nil {
		return "", fmt.Errorf("encode pairing code: %w", err)
	}
	return PairPrefix + base64.RawURLEncoding.EncodeToString(data), nil
}

// DecodePairCode parses a portable pairing code.
func DecodePairCode(raw string) (*PairCode, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, PairPrefix) {
		return nil, fmt.Errorf("%w: not a pairing code", ErrPairingState)
	}
	if len(raw) > MaxPairCodeLength {
		return nil, fmt.Errorf("%w: pairing code is too large", ErrPairingState)
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, PairPrefix))
	if err != nil {
		return nil, fmt.Errorf("%w: bad pairing code encoding", ErrPairingState)
	}
	var code PairCode
	if err := json.Unmarshal(data, &code); err != nil {
		return nil, fmt.Errorf("%w: bad pairing code payload", ErrPairingState)
	}
	if code.Protocol != Protocol || code.PairingID == "" || len(code.Secret) < 32 ||
		code.ServerID == "" || !digitsOnly(code.AccountID, 14) || code.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("%w: pairing code is incomplete", ErrPairingState)
	}
	if _, _, err := ParsePinnedURL(code.URL); err != nil {
		return nil, err
	}
	return &code, nil
}

func digitsOnly(value string, length int) bool {
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

// PairRequest is a new device's signed request to join an account.
type PairRequest struct {
	PairingID           string `json:"pairing_id"`
	Secret              string `json:"secret"`
	AccountID           string `json:"account_id"`
	DeviceID            string `json:"device_id"`
	DeviceName          string `json:"device_name"`
	SigningPublicKey    []byte `json:"signing_public_key"`
	EncryptionPublicKey []byte `json:"encryption_public_key"`
	// NodeURL is the new device's pinned node address. It lets the account
	// sync reach this device later without waiting for a reverse connection.
	NodeURL string `json:"node_url,omitempty"`
}

// PairDevice is the public identity of one device in a pairing bundle.
type PairDevice struct {
	ID                  string     `json:"id"`
	AccountID           string     `json:"account_id"`
	Name                string     `json:"name"`
	PublicKey           []byte     `json:"public_key"`
	EncryptionPublicKey []byte     `json:"encryption_public_key,omitempty"`
	Certificate         string     `json:"certificate"`
	NodeURL             string     `json:"node_url,omitempty"`
	Role                string     `json:"role"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

// PairAccount is the account metadata transferred during pairing.
type PairAccount struct {
	ID             string    `json:"id"`
	Code           string    `json:"code"`
	Name           string    `json:"name"`
	PublicKey      []byte    `json:"public_key"`
	PasswordHash   string    `json:"password_hash"`
	CreatedAt      time.Time `json:"created_at"`
	OriginServerID string    `json:"origin_server_id"`
	HostURL        string    `json:"host_url,omitempty"`
	KeyGeneration  int       `json:"key_generation,omitempty"`
	KeyRotations   []string  `json:"key_rotations,omitempty"`
}

// PairBundle is the one-time account recovery payload returned after the
// approving device accepts the request. Account private material is sealed to
// the new device; the transport is already pinned TLS.
type PairBundle struct {
	Account          PairAccount               `json:"account"`
	AccountKeySealed string                    `json:"account_key_sealed"`
	Device           PairDevice                `json:"device"`
	Siblings         []PairDevice              `json:"siblings"`
	Rooms            []room.DeviceRoomSnapshot `json:"rooms"`
	Warnings         []string                  `json:"warnings,omitempty"`
	ServerTime       time.Time                 `json:"server_time"`
	HasMore          bool                      `json:"has_more"`
	NextOffset       int                       `json:"next_offset"`
}

// PairingComplete is polled by the new device after its request is submitted.
type PairingComplete struct {
	Status string      `json:"status"`
	Reason string      `json:"reason,omitempty"`
	Bundle *PairBundle `json:"bundle,omitempty"`
}

// PairingView describes a pairing session to its approving device.
type PairingView struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	DeviceID    string    `json:"device_id,omitempty"`
	DeviceName  string    `json:"device_name,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
	Code        string    `json:"code,omitempty"`
	Warnings    []string  `json:"warnings,omitempty"`
}

type pairSession struct {
	ID          string
	Secret      string
	Code        string
	AccountID   string
	CreatedBy   string
	Status      string
	Request     *PairRequest
	Certificate string
	DeviceID    string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ApprovedAt  *time.Time
}

// PairJoinView describes the new device's side of a pairing.
type PairJoinView struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	DeviceName  string    `json:"device_name"`
	Fingerprint string    `json:"fingerprint"`
	AccountID   string    `json:"account_id"`
	Error       string    `json:"error,omitempty"`
	Warnings    []string  `json:"warnings,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type pairJoin struct {
	ID             string
	Status         string
	DeviceName     string
	DeviceID       string
	AccountID      string
	Fingerprint    string
	Error          string
	Warnings       []string
	Code           *PairCode
	SigningKey     ed25519.PrivateKey
	EncryptionPub  []byte
	EncryptionPriv []byte
	// Password unlocks and initializes the local vault during import. It lives
	// only in this in-memory join session.
	Password  string
	StartedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
	cancel    context.CancelFunc
}

func (s *Service) pairingsLocked() map[string]*pairSession {
	if s.pairings == nil {
		s.pairings = make(map[string]*pairSession)
	}
	return s.pairings
}

func (s *Service) cleanPairingsLocked(now time.Time) {
	for id, session := range s.pairings {
		if now.After(session.ExpiresAt.Add(30 * time.Minute)) {
			delete(s.pairings, id)
		}
	}
}

func pairingView(session *pairSession, now time.Time) PairingView {
	status := session.Status
	if (status == "waiting" || status == "requested") && !now.Before(session.ExpiresAt) {
		status = "expired"
	}
	view := PairingView{
		ID: session.ID, Status: status, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt,
	}
	// The code is a bearer secret. Show it only while no device has claimed
	// the pairing; after that there is nothing useful to do with it.
	if status == "waiting" && session.Request == nil {
		view.Code = session.Code
	}
	if session.Request != nil {
		view.DeviceID = session.Request.DeviceID
		view.DeviceName = session.Request.DeviceName
		view.Fingerprint = identity.Fingerprint(ed25519.PublicKey(session.Request.SigningPublicKey))
	}
	return view
}

// CreatePairing starts a pairing session on the approving device.
func (s *Service) CreatePairing(ctx context.Context) (*PairingView, error) {
	if s == nil || s.URL() == "" {
		return nil, ErrPairingDisabled
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	secretRaw := make([]byte, 32)
	if _, err := rand.Read(secretRaw); err != nil {
		return nil, fmt.Errorf("generate pairing secret: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	code := PairCode{
		Protocol: Protocol, PairingID: identity.NewID(),
		Secret: base64.RawURLEncoding.EncodeToString(secretRaw),
		URL:    s.Address(ctx), ServerID: snapshot.Account.ServerID,
		AccountID: snapshot.Account.ID, AccountName: snapshot.Account.Name,
		DeviceName: snapshot.Device.Name, ExpiresAt: now.Add(PairTTL),
	}
	encoded, err := EncodePairCode(code)
	if err != nil {
		return nil, err
	}
	session := &pairSession{
		ID: code.PairingID, Secret: code.Secret, Code: encoded, AccountID: snapshot.Account.ID,
		CreatedBy: snapshot.Device.ID, Status: "waiting", CreatedAt: now, ExpiresAt: code.ExpiresAt,
	}
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	s.cleanPairingsLocked(now)
	active := 0
	for _, existing := range s.pairingsLocked() {
		if existing.AccountID == session.AccountID && (existing.Status == "waiting" || existing.Status == "requested" || existing.Status == "approving") {
			active++
		}
	}
	if active >= MaxActivePairings {
		return nil, fmt.Errorf("%w: too many active pairings", ErrPairingState)
	}
	s.pairingsLocked()[session.ID] = session
	view := pairingView(session, now)
	return &view, nil
}

// ListPairings returns pairing sessions belonging to the local account.
func (s *Service) ListPairings(ctx context.Context) ([]PairingView, error) {
	if s == nil {
		return nil, ErrPairingDisabled
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	s.cleanPairingsLocked(now)
	out := make([]PairingView, 0, len(s.pairings))
	for _, session := range s.pairingsLocked() {
		if session.AccountID != snapshot.Account.ID {
			continue
		}
		out = append(out, pairingView(session, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// RequestPairing records a new device's request. The handler has already
// verified that the request signature belongs to SigningPublicKey.
func (s *Service) RequestPairing(req *PairRequest) error {
	if s == nil {
		return ErrPairingDisabled
	}
	if req == nil || req.PairingID == "" || req.DeviceID == "" ||
		len(req.SigningPublicKey) != ed25519.PublicKeySize || len(req.EncryptionPublicKey) != 32 {
		return fmt.Errorf("%w: pairing request is incomplete", ErrPairingState)
	}
	req.DeviceName = strings.TrimSpace(req.DeviceName)
	if req.DeviceName == "" || len(req.DeviceName) > 64 {
		return fmt.Errorf("%w: device name is invalid", ErrPairingState)
	}
	req.NodeURL = strings.TrimSpace(req.NodeURL)
	if err := ValidateNodeURL(req.NodeURL); err != nil {
		return fmt.Errorf("%w: device node address is invalid: %v", ErrPairingState, err)
	}
	now := time.Now().UTC()
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	s.cleanPairingsLocked(now)
	session, ok := s.pairingsLocked()[req.PairingID]
	if !ok {
		return ErrPairingNotFound
	}
	if session.Status == "canceled" {
		return ErrPairingCanceled
	}
	if !now.Before(session.ExpiresAt) {
		session.Status = "expired"
		return ErrPairingExpired
	}
	if session.AccountID != req.AccountID || !ConstantTimeEqual(session.Secret, req.Secret) {
		return ErrPairingNotFound
	}
	if session.Request != nil {
		if session.Request.DeviceID == req.DeviceID &&
			ConstantTimeEqual(string(session.Request.SigningPublicKey), string(req.SigningPublicKey)) &&
			ConstantTimeEqual(string(session.Request.EncryptionPublicKey), string(req.EncryptionPublicKey)) {
			// A retry may carry a fresher address or display name.
			session.Request.NodeURL = req.NodeURL
			session.Request.DeviceName = req.DeviceName
			return nil
		}
		return fmt.Errorf("%w: another device already requested this pairing", ErrPairingState)
	}
	if session.Status != "waiting" {
		return ErrPairingState
	}
	copied := *req
	copied.SigningPublicKey = append([]byte(nil), req.SigningPublicKey...)
	copied.EncryptionPublicKey = append([]byte(nil), req.EncryptionPublicKey...)
	session.Request = &copied
	session.Status = "requested"
	return nil
}

// ApprovePairing signs a certificate for the requesting device, adds it to the
// account, and shares room keys for rooms hosted by this node.
func (s *Service) ApprovePairing(ctx context.Context, pairingID string) (*PairingView, error) {
	if s == nil {
		return nil, ErrPairingDisabled
	}
	now := time.Now().UTC()
	s.pairMu.Lock()
	session, ok := s.pairingsLocked()[pairingID]
	if !ok {
		s.pairMu.Unlock()
		return nil, ErrPairingNotFound
	}
	if session.Status == "canceled" {
		s.pairMu.Unlock()
		return nil, ErrPairingCanceled
	}
	if !now.Before(session.ExpiresAt) {
		session.Status = "expired"
		s.pairMu.Unlock()
		return nil, ErrPairingExpired
	}
	if session.Status != "requested" || session.Request == nil {
		s.pairMu.Unlock()
		return nil, ErrPairingState
	}
	session.Status = "approving"
	request := *session.Request
	accountID := session.AccountID
	s.pairMu.Unlock()

	warnings, err := s.approvePairing(ctx, accountID, &request)
	if err != nil {
		s.pairMu.Lock()
		if current, ok := s.pairingsLocked()[pairingID]; ok && current.Status == "approving" {
			current.Status = "requested"
		}
		s.pairMu.Unlock()
		return nil, err
	}
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	ses, ok := s.pairingsLocked()[pairingID]
	if !ok {
		return nil, ErrPairingNotFound
	}
	ses.Status = "approved"
	ses.DeviceID = request.DeviceID
	ses.ApprovedAt = &now
	ses.ExpiresAt = now.Add(PairApprovedTTL)
	view := pairingView(ses, now)
	view.Warnings = warnings
	return &view, nil
}

func (s *Service) approvePairing(ctx context.Context, accountID string, request *PairRequest) ([]string, error) {
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Account.ID != accountID {
		return nil, ErrPairingNotFound
	}
	if err := s.accounts.SetDeviceNodeURL(ctx, snapshot.Device.ID, s.Address(ctx)); err != nil {
		s.logger.Warn("pairing: record local node address", "error", err)
	}
	accountKey, err := s.accounts.UnsealPrivateKey(snapshot.Account)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	cert, err := identity.SignDeviceCertificate(request.DeviceID, accountID,
		ed25519.PublicKey(request.SigningPublicKey), request.EncryptionPublicKey, accountKey, now)
	if err != nil {
		return nil, err
	}
	existing, getErr := s.accounts.GetDeviceByID(ctx, request.DeviceID)
	switch {
	case getErr == nil:
		if existing.AccountID != accountID || existing.RevokedAt != nil ||
			!bytesEqual(existing.PublicKey, request.SigningPublicKey) ||
			!bytesEqual(existing.EncryptionPublicKey, request.EncryptionPublicKey) {
			return nil, fmt.Errorf("%w: device already belongs to another account", ErrPairingState)
		}
		existing.Certificate = string(cert)
		if request.NodeURL != "" {
			existing.NodeURL = request.NodeURL
		}
		if err := s.accounts.UpdateDevice(ctx, existing); err != nil {
			return nil, err
		}
	case errors.Is(getErr, account.ErrNotFound):
		device := &account.Device{
			ID: request.DeviceID, AccountID: accountID, Name: request.DeviceName,
			PublicKey:           append([]byte(nil), request.SigningPublicKey...),
			EncryptionPublicKey: append([]byte(nil), request.EncryptionPublicKey...),
			Certificate:         string(cert), NodeURL: request.NodeURL, Role: "node", CreatedAt: now,
		}
		if err := s.accounts.AddDevice(ctx, device); err != nil {
			return nil, err
		}
	default:
		return nil, getErr
	}
	// Renew certificates around pairing so every device can present a valid
	// certificate to room homes for the next year.
	if err := s.accounts.RefreshCertificates(ctx); err != nil {
		s.logger.Warn("pairing: refresh device certificates", "error", err)
	}

	var warnings []string
	rooms, err := s.rooms.ListRooms(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.accounts.GetDeviceByID(ctx, request.DeviceID)
	if err != nil {
		return nil, err
	}
	for i := range rooms {
		record := &rooms[i]
		if record.HomeServerID == "" && record.HomeNode == snapshot.Device.ID {
			if updated, homeErr := s.EnsureRoomHome(ctx, record.ID); homeErr == nil {
				record = updated
			}
		}
		if record.HomeServerID != snapshot.Account.ServerID {
			continue
		}
		member, memberErr := s.rooms.GetMember(ctx, record.ID, accountID)
		if memberErr != nil || member.Status != "active" {
			continue
		}
		if _, _, regErr := s.rooms.RegisterDevice(ctx, record.ID, []byte(current.Certificate)); regErr != nil {
			message := fmt.Sprintf("could not register the new device in %s: %v", record.Name, regErr)
			warnings = append(warnings, message)
			s.logger.Warn("pairing: register device in local room", "room", record.ID, "error", regErr)
		}
	}
	return warnings, nil
}

// CancelPairing cancels a waiting, requested, or approved session. A session
// already being approved finishes first so the account and room state stay
// consistent.
func (s *Service) CancelPairing(ctx context.Context, pairingID string) error {
	if s == nil {
		return ErrPairingDisabled
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return err
	}
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	session, ok := s.pairingsLocked()[pairingID]
	if !ok || session.AccountID != snapshot.Account.ID {
		return ErrPairingNotFound
	}
	if session.Status == "approving" {
		return ErrPairingState
	}
	session.Status = "canceled"
	return nil
}

// pairingRequestKey returns the requesting device public key for signature
// verification at the complete endpoint.
func (s *Service) pairingRequestKey(pairingID, deviceID, accountID string) (ed25519.PublicKey, error) {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	session, ok := s.pairingsLocked()[pairingID]
	if !ok || session.Request == nil || session.Request.DeviceID != deviceID || session.AccountID != accountID {
		return nil, ErrPairingNotFound
	}
	return append(ed25519.PublicKey(nil), session.Request.SigningPublicKey...), nil
}

// CompletePairing returns pending/approved/canceled state, and the recovery
// bundle once approved. offset resumes a bundle whose HasMore was true.
func (s *Service) CompletePairing(ctx context.Context, pairingID, deviceID string, offset int) (*PairingComplete, error) {
	if s == nil {
		return nil, ErrPairingDisabled
	}
	if offset < 0 || offset > MaxPairRooms {
		return nil, fmt.Errorf("%w: recovery offset is out of range", ErrPairingState)
	}
	now := time.Now().UTC()
	s.pairMu.Lock()
	session, ok := s.pairingsLocked()[pairingID]
	if !ok || session.Request == nil || session.Request.DeviceID != deviceID {
		s.pairMu.Unlock()
		return nil, ErrPairingNotFound
	}
	status := session.Status
	expires := session.ExpiresAt
	if (status == "waiting" || status == "requested") && !now.Before(expires) {
		session.Status = "expired"
		status = "expired"
	}
	if status != "approved" {
		s.pairMu.Unlock()
		switch status {
		case "waiting", "requested", "approving":
			return &PairingComplete{Status: "pending"}, nil
		case "canceled":
			return &PairingComplete{Status: "canceled", Reason: "the pairing was canceled"}, nil
		case "expired":
			return &PairingComplete{Status: "expired", Reason: "the pairing code expired"}, nil
		default:
			return nil, ErrPairingState
		}
	}
	request := *session.Request
	s.pairMu.Unlock()
	bundle, err := s.buildPairBundle(ctx, pairingID, &request, offset)
	if err != nil {
		return nil, err
	}
	// Cancellation may have raced with bundle assembly. Never hand out
	// recovery material after the user canceled.
	s.pairMu.Lock()
	current, ok := s.pairingsLocked()[pairingID]
	if !ok || current.Status != "approved" {
		s.pairMu.Unlock()
		return nil, ErrPairingCanceled
	}
	s.pairMu.Unlock()
	return &PairingComplete{Status: "approved", Bundle: bundle}, nil
}

func pairAccountKeyContext(pairingID, deviceID, accountID string) string {
	return "toget:pair-account-key:v1|" + pairingID + "|" + deviceID + "|" + accountID
}

func (s *Service) buildPairBundle(ctx context.Context, pairingID string, request *PairRequest, offset int) (*PairBundle, error) {
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Account.ID != request.AccountID {
		return nil, ErrPairingNotFound
	}
	accountKey, err := s.accounts.UnsealPrivateKey(snapshot.Account)
	if err != nil {
		return nil, err
	}
	sealedKey, err := sealbox.Seal(request.EncryptionPublicKey, accountKey,
		pairAccountKeyContext(pairingID, request.DeviceID, snapshot.Account.ID))
	if err != nil {
		return nil, err
	}
	devices, err := s.accounts.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	var current *account.Device
	siblings := make([]PairDevice, 0, len(devices))
	for i := range devices {
		device := &devices[i]
		if device.ID == request.DeviceID {
			current = device
			continue
		}
		// Revoked devices stay in the account for audit purposes, but they
		// are not useful to the new device and their certificates may have
		// expired because refresh skips them.
		if device.RevokedAt != nil {
			continue
		}
		siblings = append(siblings, pairDeviceFromAccount(device))
	}
	if current == nil {
		return nil, fmt.Errorf("%w: approved device is missing from the account", ErrPairingState)
	}
	if current.RevokedAt != nil {
		return nil, ErrPairingState
	}
	rooms, err := s.rooms.ListRooms(ctx)
	if err != nil {
		return nil, err
	}
	if len(rooms) > MaxPairRooms {
		rooms = rooms[:MaxPairRooms]
	}
	warnings := make([]string, 0)
	candidates := make([]*room.Room, 0, len(rooms))
	for i := range rooms {
		record := &rooms[i]
		if record.HomeServerID == "" && record.HomeNode == snapshot.Device.ID {
			if updated, homeErr := s.EnsureRoomHome(ctx, record.ID); homeErr == nil {
				record = updated
			}
		}
		member, memberErr := s.rooms.GetMember(ctx, record.ID, snapshot.Account.ID)
		if memberErr == nil && member.Status == "active" {
			candidates = append(candidates, record)
		}
	}
	end := offset + maxPairRoomsPerBundle
	if end > len(candidates) {
		end = len(candidates)
	}
	snapshots := make([]room.DeviceRoomSnapshot, 0, maxPairRoomsPerBundle)
	usedBytes := 0
	for i := offset; i < end; i++ {
		record := candidates[i]
		exported, exportErr := s.rooms.ExportDeviceRoom(ctx, record.ID, snapshot.Device.ID,
			snapshot.Account.ID, request.DeviceID, request.EncryptionPublicKey)
		if exportErr != nil {
			warnings = append(warnings, fmt.Sprintf("could not copy %s: %v", record.Name, exportErr))
			continue
		}
		if len(exported.Keys) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s has no keys this device can share yet", record.Name))
			continue
		}
		encoded, err := json.Marshal(exported)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not copy %s: %v", record.Name, err))
			continue
		}
		if len(snapshots) > 0 && usedBytes+len(encoded) > maxPairBundleBytes {
			end = i
			break
		}
		usedBytes += len(encoded)
		snapshots = append(snapshots, *exported)
	}
	return &PairBundle{
		Account: PairAccount{
			ID: snapshot.Account.ID, Code: snapshot.Account.Code, Name: snapshot.Account.Name,
			PublicKey: append([]byte(nil), snapshot.Account.PublicKey...), PasswordHash: snapshot.Account.PasswordHash,
			CreatedAt: snapshot.Account.CreatedAt, OriginServerID: snapshot.Account.OriginServerID,
			HostURL:       snapshot.Account.HostURL,
			KeyGeneration: snapshot.Account.KeyGeneration,
			KeyRotations:  append([]string(nil), snapshot.Account.KeyRotations...),
		},
		AccountKeySealed: string(sealedKey),
		Device:           pairDeviceFromAccount(current),
		Siblings:         siblings,
		Rooms:            snapshots,
		Warnings:         warnings,
		ServerTime:       time.Now().UTC(),
		HasMore:          end < len(candidates),
		NextOffset:       end,
	}, nil
}

func pairDeviceFromAccount(device *account.Device) PairDevice {
	return PairDevice{
		ID: device.ID, AccountID: device.AccountID, Name: device.Name,
		PublicKey:           append([]byte(nil), device.PublicKey...),
		EncryptionPublicKey: append([]byte(nil), device.EncryptionPublicKey...),
		Certificate:         device.Certificate, NodeURL: device.NodeURL, Role: device.Role,
		RevokedAt: device.RevokedAt, CreatedAt: device.CreatedAt,
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// StartPairJoin begins recovering an account onto this device. It returns
// immediately and runs the network exchange in the background.
func (s *Service) StartPairJoin(ctx context.Context, rawCode, deviceName string) (*PairJoinView, error) {
	return s.StartPairJoinWithPassword(ctx, rawCode, deviceName, "")
}

// StartPairJoinWithPassword begins account recovery and remembers the account
// password so the fresh device can initialize its password-wrapped vault when
// the bundle arrives.
func (s *Service) StartPairJoinWithPassword(ctx context.Context, rawCode, deviceName, password string) (*PairJoinView, error) {
	if s == nil || s.URL() == "" {
		return nil, ErrPairingDisabled
	}
	if _, err := s.accounts.Get(ctx); err == nil {
		return nil, account.ErrAlreadyExists
	} else if !errors.Is(err, account.ErrNotFound) {
		return nil, err
	}
	code, err := DecodePairCode(rawCode)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if !now.Before(code.ExpiresAt) {
		return nil, ErrPairingExpired
	}
	if code.ExpiresAt.After(now.Add(24 * time.Hour)) {
		return nil, fmt.Errorf("%w: pairing code lifetime is too long", ErrPairingState)
	}
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		deviceName = "This device"
	}
	if len(deviceName) > 64 {
		deviceName = deviceName[:64]
	}
	devicePub, devicePriv, err := identity.NewDeviceKey()
	if err != nil {
		return nil, err
	}
	encPub, encPriv, err := identity.NewEncryptionKey()
	if err != nil {
		return nil, err
	}
	deviceID := identity.NewID()
	join := &pairJoin{
		ID: identity.NewID(), Status: "waiting", DeviceName: deviceName, DeviceID: deviceID,
		AccountID: code.AccountID, Fingerprint: identity.Fingerprint(devicePub),
		Code: code, SigningKey: devicePriv, EncryptionPub: encPub, EncryptionPriv: encPriv,
		Password: password, StartedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	runCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	join.cancel = cancel
	if err := s.addPairJoin(join); err != nil {
		cancel()
		return nil, err
	}
	go s.runPairJoin(runCtx, join)
	// The background join may update the session immediately; build the first
	// view under the same lock as every other status read.
	s.joinMu.Lock()
	view := pairJoinView(join)
	s.joinMu.Unlock()
	return view, nil
}

func (s *Service) addPairJoin(join *pairJoin) error {
	s.joinMu.Lock()
	defer s.joinMu.Unlock()
	if s.pairJoins == nil {
		s.pairJoins = make(map[string]*pairJoin)
	}
	now := time.Now().UTC()
	for id, existing := range s.pairJoins {
		if now.After(existing.UpdatedAt.Add(30 * time.Minute)) {
			delete(s.pairJoins, id)
		}
	}
	if len(s.pairJoins) >= MaxPairJoinSessions {
		return fmt.Errorf("%w: too many pairing attempts", ErrPairingState)
	}
	s.pairJoins[join.ID] = join
	return nil
}

// PairJoinStatus returns the progress of a join started on this device.
func (s *Service) PairJoinStatus(ctx context.Context, id string) (*PairJoinView, error) {
	if s == nil {
		return nil, ErrPairingDisabled
	}
	s.joinMu.Lock()
	defer s.joinMu.Unlock()
	join, ok := s.pairJoins[id]
	if !ok {
		return nil, ErrPairJoinNotFound
	}
	view := pairJoinView(join)
	if join.Status == "ready" {
		// The join session holds no reusable secret once imported; remove it
		// after the caller has had a chance to read the result.
		delete(s.pairJoins, id)
	}
	return view, nil
}

// CancelPairJoin stops a join before it imports an account.
func (s *Service) CancelPairJoin(ctx context.Context, id string) error {
	if s == nil {
		return ErrPairJoinNotFound
	}
	s.joinMu.Lock()
	defer s.joinMu.Unlock()
	join, ok := s.pairJoins[id]
	if !ok {
		return ErrPairJoinNotFound
	}
	if join.Status == "ready" {
		return ErrPairingState
	}
	join.Status = "canceled"
	join.UpdatedAt = time.Now().UTC()
	if join.cancel != nil {
		join.cancel()
	}
	return nil
}

func pairJoinView(join *pairJoin) *PairJoinView {
	return &PairJoinView{
		ID: join.ID, Status: join.Status, DeviceName: join.DeviceName,
		Fingerprint: join.Fingerprint, AccountID: join.AccountID, Error: join.Error,
		Warnings: append([]string(nil), join.Warnings...), StartedAt: join.StartedAt, UpdatedAt: join.UpdatedAt,
	}
}

func (s *Service) updatePairJoin(join *pairJoin, status, detail string) {
	s.joinMu.Lock()
	defer s.joinMu.Unlock()
	join.Status = status
	join.Error = detail
	join.UpdatedAt = time.Now().UTC()
}

func (s *Service) finishPairJoin(join *pairJoin, status, detail string, warnings []string) {
	s.joinMu.Lock()
	defer s.joinMu.Unlock()
	join.Status = status
	join.Error = detail
	join.Warnings = warnings
	join.UpdatedAt = time.Now().UTC()
	if join.cancel != nil {
		join.cancel()
	}
}

func (s *Service) runPairJoin(ctx context.Context, join *pairJoin) {
	code := join.Code
	client, err := NewClientForURL(code.URL, code.AccountID, join.DeviceID, join.SigningKey)
	if err != nil {
		s.finishPairJoin(join, "failed", err.Error(), nil)
		return
	}
	defer client.Close()
	request := PairRequest{
		PairingID: code.PairingID, Secret: code.Secret, AccountID: code.AccountID,
		DeviceID: join.DeviceID, DeviceName: join.DeviceName,
		SigningPublicKey:    join.SigningKey.Public().(ed25519.PublicKey),
		EncryptionPublicKey: join.EncryptionPub,
		NodeURL:             s.Address(ctx),
	}
	if err := client.RequestPairing(ctx, request); err != nil {
		s.finishPairJoin(join, "failed", err.Error(), nil)
		return
	}
	s.updatePairJoin(join, "awaiting_approval", "")
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.Canceled {
				return // CancelPairJoin already recorded the state.
			}
			s.finishPairJoin(join, "failed", "pairing timed out", nil)
			return
		case <-ticker.C:
		}
		complete, err := client.CompletePairing(ctx, code.PairingID, 0)
		if err != nil {
			var remoteErr *RemoteError
			if errors.As(err, &remoteErr) && remoteErr.Status >= 400 && remoteErr.Status < 500 {
				s.finishPairJoin(join, "failed", err.Error(), nil)
				return
			}
			// Network errors are retryable until the deadline.
			continue
		}
		switch complete.Status {
		case "pending":
			s.updatePairJoin(join, "awaiting_approval", "")
		case "approved":
			if complete.Bundle == nil {
				s.finishPairJoin(join, "failed", "the approving device returned no recovery data", nil)
				return
			}
			s.updatePairJoin(join, "importing", "")
			warnings, importedRooms, err := s.claimPairBundle(ctx, client, join)
			if err != nil {
				s.finishPairJoin(join, "failed", err.Error(), warnings)
				return
			}
			for _, roomID := range importedRooms {
				if _, err := s.SyncRoom(ctx, roomID); err != nil {
					warnings = append(warnings, fmt.Sprintf("could not sync room %s yet: %v", roomID, err))
				}
			}
			if err := s.accounts.SetDeviceNodeURL(ctx, join.DeviceID, s.Address(ctx)); err != nil {
				warnings = append(warnings, fmt.Sprintf("could not record this device's node address: %v", err))
			}
			s.finishPairJoin(join, "ready", "", warnings)
			s.TriggerAccountSync()
			return
		case "canceled":
			s.finishPairJoin(join, "canceled", complete.Reason, nil)
			return
		case "expired":
			s.finishPairJoin(join, "expired", complete.Reason, nil)
			return
		default:
			s.finishPairJoin(join, "failed", "unexpected pairing state", nil)
			return
		}
	}
}

// completePairingPage fetches one recovery page. Network and 5xx failures are
// retried until the join deadline so a brief outage does not leave an account
// imported without its rooms.
func completePairingPage(ctx context.Context, client *Client, pairingID string, offset int) (*PairingComplete, error) {
	for {
		complete, err := client.CompletePairing(ctx, pairingID, offset)
		if err == nil {
			return complete, nil
		}
		var remoteErr *RemoteError
		if errors.As(err, &remoteErr) && remoteErr.Status >= 400 && remoteErr.Status < 500 {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// claimPairBundle follows HasMore pages and imports every page. Account
// import happens once; later pages only add rooms, which keeps a large first
// pairing from exceeding one node response.
func (s *Service) claimPairBundle(ctx context.Context, client *Client, join *pairJoin) ([]string, []string, error) {
	offset := 0
	warnings := make([]string, 0)
	importedRooms := make([]string, 0)
	for page := 0; page <= MaxPairRooms; page++ {
		complete, err := completePairingPage(ctx, client, join.Code.PairingID, offset)
		if err != nil {
			return warnings, importedRooms, err
		}
		if complete.Status != "approved" || complete.Bundle == nil {
			return warnings, importedRooms, fmt.Errorf("pairing became %s while importing", complete.Status)
		}
		bundle := complete.Bundle
		pageWarnings, pageRooms, err := s.importPairBundle(ctx, join, bundle)
		warnings = append(warnings, pageWarnings...)
		importedRooms = append(importedRooms, pageRooms...)
		if err != nil {
			return warnings, importedRooms, err
		}
		if !bundle.HasMore {
			return warnings, importedRooms, nil
		}
		if bundle.NextOffset <= offset || bundle.NextOffset > MaxPairRooms {
			return warnings, importedRooms, fmt.Errorf("%w: recovery pagination is invalid", ErrPairingState)
		}
		offset = bundle.NextOffset
	}
	return warnings, importedRooms, fmt.Errorf("%w: recovery has too many pages", ErrPairingState)
}

func (s *Service) importPairBundle(ctx context.Context, join *pairJoin, bundle *PairBundle) ([]string, []string, error) {
	if bundle == nil || bundle.Account.ID != join.AccountID || bundle.Device.ID != join.DeviceID {
		return nil, nil, fmt.Errorf("%w: recovery data does not match this pairing", ErrPairingState)
	}
	accountKey, err := sealbox.Open(join.EncryptionPriv, []byte(bundle.AccountKeySealed),
		pairAccountKeyContext(join.Code.PairingID, join.DeviceID, join.AccountID))
	if err != nil {
		return nil, nil, fmt.Errorf("open account key: %w", err)
	}
	if len(accountKey) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("%w: account key has the wrong size", account.ErrInvalidImport)
	}
	if !bytesEqual(ed25519.PrivateKey(accountKey).Public().(ed25519.PublicKey), bundle.Account.PublicKey) {
		return nil, nil, fmt.Errorf("%w: account key does not match the account", account.ErrInvalidImport)
	}
	existing, getErr := s.accounts.Get(ctx)
	switch {
	case getErr == nil:
		if existing.Account.ID != bundle.Account.ID || existing.Device.ID != join.DeviceID {
			return nil, nil, fmt.Errorf("%w: a different account already exists on this device", account.ErrAlreadyExists)
		}
	case errors.Is(getErr, account.ErrNotFound):
		siblings := make([]account.Device, 0, len(bundle.Siblings))
		for i := range bundle.Siblings {
			siblings = append(siblings, accountDeviceFromPair(bundle.Siblings[i]))
		}
		device := accountDeviceFromPair(bundle.Device)
		device.PrivateKey = append([]byte(nil), join.SigningKey...)
		device.EncryptionPrivateKey = append([]byte(nil), join.EncryptionPriv...)
		if _, err := s.accounts.Import(ctx, account.ImportSpec{
			AccountID: bundle.Account.ID, AccountCode: bundle.Account.Code, Name: bundle.Account.Name,
			AccountPublicKey: bundle.Account.PublicKey, AccountPrivateKey: ed25519.PrivateKey(accountKey),
			PasswordHash: bundle.Account.PasswordHash, CreatedAt: bundle.Account.CreatedAt,
			OriginServerID: bundle.Account.OriginServerID,
			HostURL:        bundle.Account.HostURL,
			KeyGeneration:  bundle.Account.KeyGeneration, KeyRotations: bundle.Account.KeyRotations,
			Password: join.Password, Device: device, Siblings: siblings,
		}); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, getErr
	}
	// The account is now local and usable. Room copy failures are warnings
	// because one unreachable home must not roll back a usable account.
	warnings := make([]string, 0, len(bundle.Warnings))
	warnings = append(warnings, bundle.Warnings...)
	importedRooms := make([]string, 0, len(bundle.Rooms))
	for i := range bundle.Rooms {
		snapshot := &bundle.Rooms[i]
		imported, err := s.rooms.ImportDeviceRoom(ctx, snapshot, bundle.Account.ID, join.DeviceID, join.EncryptionPriv)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not import %s: %v", snapshot.Room.Name, err))
			continue
		}
		importedRooms = append(importedRooms, imported.ID)
	}
	return warnings, importedRooms, nil
}

func accountDeviceFromPair(device PairDevice) account.Device {
	role := device.Role
	if role == "" {
		role = "node"
	}
	return account.Device{
		ID: device.ID, AccountID: device.AccountID, Name: device.Name,
		PublicKey:           append([]byte(nil), device.PublicKey...),
		EncryptionPublicKey: append([]byte(nil), device.EncryptionPublicKey...),
		Certificate:         device.Certificate, NodeURL: device.NodeURL, Role: role,
		RevokedAt: device.RevokedAt, CreatedAt: device.CreatedAt,
	}
}

const maxPairBody = 256 << 10

func (s *Service) handlePairRequest(w http.ResponseWriter, r *http.Request) {
	body, err := readNodeBody(w, r, maxPairBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var request PairRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if request.DeviceID == "" || request.AccountID == "" ||
		!ConstantTimeEqual(r.Header.Get(HeaderDevice), request.DeviceID) ||
		!ConstantTimeEqual(r.Header.Get(HeaderAccount), request.AccountID) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "request identity does not match the pairing request")
		return
	}
	if err := VerifyRequest(r, body, ed25519.PublicKey(request.SigningPublicKey), s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "pairing request signature is invalid")
		return
	}
	if err := s.RequestPairing(&request); err != nil {
		s.writePairingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
}

func (s *Service) handlePairComplete(w http.ResponseWriter, r *http.Request) {
	body, err := readNodeBody(w, r, maxPairBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req struct {
		PairingID string `json:"pairing_id"`
		Offset    int    `json:"offset"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.PairingID == "" || req.Offset < 0 {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "pairing_id is required")
		return
	}
	accountID := r.Header.Get(HeaderAccount)
	deviceID := r.Header.Get(HeaderDevice)
	publicKey, err := s.pairingRequestKey(req.PairingID, deviceID, accountID)
	if err != nil {
		s.writePairingError(w, err)
		return
	}
	if err := VerifyRequest(r, body, publicKey, s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "pairing request signature is invalid")
		return
	}
	complete, err := s.CompletePairing(r.Context(), req.PairingID, deviceID, req.Offset)
	if err != nil {
		s.writePairingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, complete)
}

func (s *Service) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	body, err := readNodeBody(w, r, maxPairBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req struct {
		RoomID      string `json:"room_id"`
		Certificate string `json:"certificate"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.RoomID == "" || req.Certificate == "" {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "room_id and certificate are required")
		return
	}
	parsed, err := identity.ParseDeviceCertificate([]byte(req.Certificate))
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "device certificate is invalid")
		return
	}
	if !ConstantTimeEqual(r.Header.Get(HeaderDevice), parsed.DeviceID) ||
		!ConstantTimeEqual(r.Header.Get(HeaderAccount), parsed.AccountID) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "request identity does not match the device certificate")
		return
	}
	publicKey, err := identity.DecodePublicKey(parsed.DevicePubKey)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "device certificate key is invalid")
		return
	}
	if err := VerifyRequest(r, body, publicKey, s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "device registration signature is invalid")
		return
	}
	device, wrapped, err := s.rooms.RegisterDevice(r.Context(), req.RoomID, []byte(req.Certificate))
	if err != nil {
		s.writeRoomError(w, err, 0)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": device, "wrapped_keys": wrapped})
}

func (s *Service) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	body, err := readNodeBody(w, r, maxPairBody)
	if err != nil {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var req struct {
		RoomID   string `json:"room_id"`
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.RoomID == "" || req.DeviceID == "" {
		writeNodeError(w, http.StatusBadRequest, "invalid_request", "room_id and device_id are required")
		return
	}
	device, err := s.requestDevice(r, req.RoomID)
	if err != nil {
		writeNodeError(w, http.StatusForbidden, "forbidden", "device is not trusted in this room")
		return
	}
	if !ConstantTimeEqual(r.Header.Get(HeaderAccount), device.AccountID) {
		writeNodeError(w, http.StatusForbidden, "forbidden", "account does not match the device")
		return
	}
	if err := VerifyRequest(r, body, device.PublicKey, s.nonces, time.Now()); err != nil {
		writeNodeError(w, http.StatusUnauthorized, "unauthorized", "device revocation signature is invalid")
		return
	}
	epoch, err := s.rooms.RevokeDevice(r.Context(), req.RoomID, device.AccountID, req.DeviceID, device.DeviceID)
	if err != nil {
		s.writeRoomError(w, err, 0)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "epoch": epoch})
}

func (s *Service) writePairingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrPairingNotFound), errors.Is(err, ErrPairJoinNotFound):
		writeNodeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, ErrPairingExpired):
		writeNodeError(w, http.StatusGone, "expired", err.Error())
	case errors.Is(err, ErrPairingCanceled):
		writeNodeError(w, http.StatusGone, "canceled", err.Error())
	case errors.Is(err, ErrPairingState):
		writeNodeError(w, http.StatusConflict, "conflict", err.Error())
	default:
		// Unknown failures are server-side and possibly transient (for
		// example a bundled room snapshot failing to build). Returning 5xx
		// lets the new device retry instead of abandoning the pairing.
		writeNodeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}
