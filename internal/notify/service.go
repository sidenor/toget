package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/storage"
)

const (
	KindApproval = "approval"
	KindMessage  = "message"

	mutedSetting = "notifications.muted_until"
	queueSize    = 32
)

// Delivery windows keep a burst of events from turning into a stream of
// notifications. Approvals are allowed through sooner than ordinary messages.
var (
	approvalWindow = 5 * time.Second
	messageWindow  = 20 * time.Second
	maxEventAge    = 2 * time.Minute
)

var (
	// ErrInvalidSubscription means a browser supplied malformed push keys or
	// an endpoint that is not HTTPS (loopback development is allowed HTTP).
	ErrInvalidSubscription = errors.New("invalid push subscription")
	// ErrSubscriptionNotFound means the subscription was already removed.
	ErrSubscriptionNotFound = errors.New("push subscription not found")
)

// Store is the subset of storage used by notifications.
type Store interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	ListPushSubscriptions(ctx context.Context) ([]storage.PushSubscription, error)
	GetPushSubscription(ctx context.Context, id string) (*storage.PushSubscription, error)
	SavePushSubscription(ctx context.Context, sub *storage.PushSubscription) error
	UpdatePushSubscription(ctx context.Context, sub *storage.PushSubscription) error
	DeletePushSubscription(ctx context.Context, id string) error
	RecordPushResult(ctx context.Context, id string, at time.Time, success bool) error
}

// Status is the notification state returned to the settings UI.
type Status struct {
	PublicKey     string                     `json:"public_key"`
	Subscriptions []storage.PushSubscription `json:"subscriptions"`
	MutedUntil    *time.Time                 `json:"muted_until,omitempty"`
}

// Service generates VAPID credentials, stores subscriptions, and delivers
// content-free pushes in the background.
type Service struct {
	store  Store
	keys   *VAPIDKeys
	logger *slog.Logger
	now    func() time.Time
	self   func(ctx context.Context) string
	client *httpClient

	queue    chan notice
	lastSent map[string]time.Time

	mu sync.Mutex
}

type notice struct {
	kind string
	age  time.Time
}

// Option configures a notification service.
type Option func(*Service)

// WithSelfAccount installs a function returning the local account ID, so
// messages authored on this device do not notify the same device.
func WithSelfAccount(fn func(ctx context.Context) string) Option {
	return func(s *Service) { s.self = fn }
}

// WithNow overrides the clock in tests.
func WithNow(fn func() time.Time) Option {
	return func(s *Service) { s.now = fn }
}

// New creates a notification service. VAPID keys are generated on first use
// and kept in the local settings table.
func New(ctx context.Context, store Store, logger *slog.Logger, opts ...Option) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}
	keys, err := LoadOrCreateVAPID(ctx, store)
	if err != nil {
		return nil, err
	}
	s := &Service{
		store:    store,
		keys:     keys,
		logger:   logger,
		now:      time.Now,
		client:   newHTTPClient(),
		queue:    make(chan notice, queueSize),
		lastSent: make(map[string]time.Time),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// PublicKey returns the VAPID application-server public key.
func (s *Service) PublicKey() string { return s.keys.Public }

// Start runs the delivery worker until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case n := <-s.queue:
				s.deliver(ctx, n)
			}
		}
	}()
}

// HandleRoomEvent queues notifications for approvals and messages. The event
// payload is read only to classify it; no text, sender, room name, or approval
// description is copied into the push payload.
func (s *Service) HandleRoomEvent(ctx context.Context, event *room.Event) {
	if s == nil || event == nil {
		return
	}
	now := s.now()
	if !event.CreatedAt.IsZero() && now.Sub(event.CreatedAt) > maxEventAge {
		return
	}
	var kind string
	switch event.Type {
	case room.EventApprovalRequested:
		kind = KindApproval
	case room.EventMessagePosted:
		if s.self != nil && event.AuthorID != "" && event.AuthorID == s.self(ctx) {
			// Human messages authored on this account are already visible here.
			// Agent replies are authored by the same account but should still
			// reach the owner's other windows and devices.
			var body room.MessagePostedPayload
			if json.Unmarshal(event.Payload, &body) != nil || body.AgentID == "" {
				return
			}
		}
		kind = KindMessage
	default:
		return
	}
	select {
	case s.queue <- notice{kind: kind, age: now}:
	default:
		s.logger.Debug("notification queue full; dropping event", "kind", kind)
	}
}

func (s *Service) deliver(ctx context.Context, n notice) {
	if s.muted(ctx) {
		return
	}
	subs, err := s.store.ListPushSubscriptions(ctx)
	if err != nil {
		s.logger.Warn("list push subscriptions", "error", err)
		return
	}
	for i := range subs {
		sub := &subs[i]
		if !s.eligible(sub, n.kind, s.now()) {
			continue
		}
		if !s.allow(sub.ID, n.kind, s.now()) {
			continue
		}
		if err := s.send(ctx, sub, n.kind); err != nil {
			s.logger.Debug("push delivery failed", "subscription", sub.ID, "kind", n.kind, "error", err)
		}
	}
}

func (s *Service) eligible(sub *storage.PushSubscription, kind string, now time.Time) bool {
	if sub == nil || !sub.Enabled {
		return false
	}
	if sub.MutedUntil != nil && sub.MutedUntil.After(now) {
		return false
	}
	if kind == KindApproval {
		return sub.ApprovalEvents
	}
	return sub.MessageEvents
}

func (s *Service) allow(id, kind string, now time.Time) bool {
	window := messageWindow
	if kind == KindApproval {
		window = approvalWindow
	}
	key := id + ":" + kind
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastSent[key]; ok && now.Sub(last) < window {
		return false
	}
	if len(s.lastSent) > 256 {
		for k, at := range s.lastSent {
			if now.Sub(at) > 10*time.Minute {
				delete(s.lastSent, k)
			}
		}
	}
	s.lastSent[key] = now
	return true
}

func (s *Service) send(ctx context.Context, sub *storage.PushSubscription, kind string) error {
	lang := languageFor(sub.Language)
	payload, err := json.Marshal(struct {
		V    int    `json:"v"`
		Type string `json:"type"`
		Lang string `json:"lang"`
	}{V: 1, Type: kind, Lang: lang})
	if err != nil {
		return err
	}
	body, err := encryptPayload(sub.P256DH, sub.Auth, payload)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := newPushRequest(reqCtx, *sub, kind, body)
	if err != nil {
		return err
	}
	auth, err := vapidAuthorization(*s.keys, sub.Endpoint, s.now())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	resp, err := s.client.Do(req)
	if err != nil {
		_ = s.store.RecordPushResult(context.Background(), sub.ID, s.now(), false)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = s.store.RecordPushResult(context.Background(), sub.ID, s.now(), true)
		return nil
	}
	_ = s.store.RecordPushResult(context.Background(), sub.ID, s.now(), false)
	if resp.StatusCode == 404 || resp.StatusCode == 410 {
		_ = s.store.DeletePushSubscription(context.Background(), sub.ID)
		return fmt.Errorf("push endpoint is gone (%d); subscription removed", resp.StatusCode)
	}
	return fmt.Errorf("push service returned %d", resp.StatusCode)
}

// SendTest delivers one test notification to a subscription, bypassing mute
// and rate limiting so the user can check browser permissions.
func (s *Service) SendTest(ctx context.Context, id string) error {
	sub, err := s.store.GetPushSubscription(ctx, id)
	if errors.Is(err, storage.ErrPushSubscriptionNotFound) {
		return ErrSubscriptionNotFound
	}
	if err != nil {
		return err
	}
	return s.send(ctx, sub, KindMessage)
}

// Status returns notification settings and the subscriptions visible to the
// local owner. Browser keys are intentionally omitted from the API view.
func (s *Service) Status(ctx context.Context) (*Status, error) {
	subs, err := s.store.ListPushSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	muted, err := s.MutedUntil(ctx)
	if err != nil {
		return nil, err
	}
	return &Status{PublicKey: s.keys.Public, Subscriptions: subs, MutedUntil: muted}, nil
}

// SubscriptionInput is a browser PushSubscription plus local display hints.
type SubscriptionInput struct {
	Endpoint  string `json:"endpoint"`
	P256DH    string `json:"p256dh"`
	Auth      string `json:"auth"`
	Language  string `json:"language"`
	UserAgent string `json:"user_agent"`
	Label     string `json:"label"`
}

// Subscribe validates and stores a browser subscription. Re-subscribing the
// same endpoint refreshes its keys and preserves the user's preferences.
func (s *Service) Subscribe(ctx context.Context, in SubscriptionInput) (*storage.PushSubscription, error) {
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	in.P256DH = strings.TrimSpace(in.P256DH)
	in.Auth = strings.TrimSpace(in.Auth)
	in.Language = languageFor(in.Language)
	in.UserAgent = truncate(strings.TrimSpace(in.UserAgent), 256)
	in.Label = truncate(strings.TrimSpace(in.Label), 64)
	if err := validateSubscription(in); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	sub := &storage.PushSubscription{
		ID: identity.NewID(), Endpoint: in.Endpoint, P256DH: in.P256DH, Auth: in.Auth,
		Language: in.Language, UserAgent: in.UserAgent, Label: in.Label,
		Enabled: true, MessageEvents: true, ApprovalEvents: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.SavePushSubscription(ctx, sub); err != nil {
		return nil, err
	}
	// Save upserts by endpoint and may have selected a new ID.
	stored, err := s.store.GetPushSubscription(ctx, sub.ID)
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// SubscriptionPatch changes mutable preferences. Nil fields stay unchanged;
// ClearMute removes a per-device mute.
type SubscriptionPatch struct {
	Label      *string
	Enabled    *bool
	Messages   *bool
	Approvals  *bool
	MutedUntil *time.Time
	ClearMute  bool
}

// UpdateSubscription applies a preferences patch.
func (s *Service) UpdateSubscription(ctx context.Context, id string, patch SubscriptionPatch) (*storage.PushSubscription, error) {
	sub, err := s.store.GetPushSubscription(ctx, id)
	if errors.Is(err, storage.ErrPushSubscriptionNotFound) {
		return nil, ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, err
	}
	if patch.Label != nil {
		sub.Label = truncate(strings.TrimSpace(*patch.Label), 64)
	}
	if patch.Enabled != nil {
		sub.Enabled = *patch.Enabled
	}
	if patch.Messages != nil {
		sub.MessageEvents = *patch.Messages
	}
	if patch.Approvals != nil {
		sub.ApprovalEvents = *patch.Approvals
	}
	if patch.MutedUntil != nil {
		until := patch.MutedUntil.UTC()
		sub.MutedUntil = &until
	}
	if patch.ClearMute {
		sub.MutedUntil = nil
	}
	if err := s.store.UpdatePushSubscription(ctx, sub); err != nil {
		if errors.Is(err, storage.ErrPushSubscriptionNotFound) {
			return nil, ErrSubscriptionNotFound
		}
		return nil, err
	}
	return sub, nil
}

// DeleteSubscription removes a browser subscription.
func (s *Service) DeleteSubscription(ctx context.Context, id string) error {
	err := s.store.DeletePushSubscription(ctx, id)
	if errors.Is(err, storage.ErrPushSubscriptionNotFound) {
		return ErrSubscriptionNotFound
	}
	return err
}

// MutedUntil returns the global mute deadline, or nil when notifications are
// on.
func (s *Service) MutedUntil(ctx context.Context) (*time.Time, error) {
	raw, err := s.store.GetSetting(ctx, mutedSetting)
	if err != nil || raw == "" {
		return nil, err
	}
	until, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, nil
	}
	if !until.After(s.now()) {
		return nil, nil
	}
	return &until, nil
}

// SetMute turns notifications off through until, or back on when until is nil
// or in the past.
func (s *Service) SetMute(ctx context.Context, until *time.Time) error {
	if until == nil || !until.After(s.now()) {
		return s.store.SetSetting(ctx, mutedSetting, "")
	}
	return s.store.SetSetting(ctx, mutedSetting, until.UTC().Format(time.RFC3339))
}

func (s *Service) muted(ctx context.Context) bool {
	until, err := s.MutedUntil(ctx)
	return err == nil && until != nil
}

func validateSubscription(in SubscriptionInput) error {
	if in.Endpoint == "" || len(in.Endpoint) > 2048 || in.P256DH == "" || in.Auth == "" {
		return ErrInvalidSubscription
	}
	if _, err := endpointAudience(in.Endpoint); err != nil {
		return ErrInvalidSubscription
	}
	key, err := base64.RawURLEncoding.DecodeString(in.P256DH)
	if err != nil || len(key) != 65 || key[0] != 4 {
		return ErrInvalidSubscription
	}
	auth, err := base64.RawURLEncoding.DecodeString(in.Auth)
	if err != nil || len(auth) != 16 {
		return ErrInvalidSubscription
	}
	return nil
}

func endpointAudience(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("invalid push endpoint")
	}
	if u.Scheme == "https" {
		return "https://" + u.Host, nil
	}
	if u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return "http://" + u.Host, nil
	}
	return "", errors.New("push endpoint must use HTTPS")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func languageFor(raw string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "zh") {
		return "zh"
	}
	return "en"
}

func truncate(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}
