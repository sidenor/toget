package api

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/sidenor/toget/internal/notify"
	"github.com/sidenor/toget/internal/storage"
)

// NotificationSubscriptionView deliberately omits the push endpoint and
// browser keys. Those are capabilities the browser already holds; the settings
// UI only needs enough information to identify and manage a device.
type NotificationSubscriptionView struct {
	ID             string     `json:"id"`
	EndpointHost   string     `json:"endpoint_host"`
	Label          string     `json:"label"`
	Language       string     `json:"language"`
	UserAgent      string     `json:"user_agent"`
	Enabled        bool       `json:"enabled"`
	MessageEvents  bool       `json:"message_events"`
	ApprovalEvents bool       `json:"approval_events"`
	MutedUntil     *time.Time `json:"muted_until,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt  *time.Time `json:"last_failure_at,omitempty"`
	FailureCount   int        `json:"failure_count"`
}

// NotificationStatusView is the state needed to register a browser and manage
// subscriptions.
type NotificationStatusView struct {
	PublicKey     string                         `json:"public_key"`
	Subscriptions []NotificationSubscriptionView `json:"subscriptions"`
	MutedUntil    *time.Time                     `json:"muted_until,omitempty"`
}

type updateNotificationSubscriptionRequest struct {
	Label          *string `json:"label,omitempty"`
	Enabled        *bool   `json:"enabled,omitempty"`
	MessageEvents  *bool   `json:"message_events,omitempty"`
	ApprovalEvents *bool   `json:"approval_events,omitempty"`
	MutedUntil     *string `json:"muted_until,omitempty"`
}

type muteNotificationsRequest struct {
	Hours int `json:"hours"`
}

func notificationSubscriptionView(sub storage.PushSubscription) NotificationSubscriptionView {
	host := ""
	if parsed, err := url.Parse(sub.Endpoint); err == nil {
		host = parsed.Hostname()
	}
	return NotificationSubscriptionView{
		ID: sub.ID, EndpointHost: host, Label: sub.Label, Language: sub.Language,
		UserAgent: sub.UserAgent, Enabled: sub.Enabled, MessageEvents: sub.MessageEvents,
		ApprovalEvents: sub.ApprovalEvents, MutedUntil: sub.MutedUntil,
		CreatedAt: sub.CreatedAt, LastSuccessAt: sub.LastSuccessAt,
		LastFailureAt: sub.LastFailureAt, FailureCount: sub.FailureCount,
	}
}

func (s *Server) notificationsAvailable(w http.ResponseWriter) bool {
	if s.notifications != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "notifications_disabled", "push notifications are not available")
	return false
}

func (s *Server) notificationStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.notifications.Status(r.Context())
	if err != nil {
		writeNotificationError(w, err)
		return
	}
	view := NotificationStatusView{
		PublicKey: status.PublicKey, Subscriptions: make([]NotificationSubscriptionView, 0, len(status.Subscriptions)),
		MutedUntil: status.MutedUntil,
	}
	for _, sub := range status.Subscriptions {
		view.Subscriptions = append(view.Subscriptions, notificationSubscriptionView(sub))
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleGetNotifications(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.notificationsAvailable(w) {
		return
	}
	s.notificationStatus(w, r)
}

func (s *Server) handleSubscribeNotifications(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.notificationsAvailable(w) {
		return
	}
	var req notify.SubscriptionInput
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	sub, err := s.notifications.Subscribe(r.Context(), req)
	if err != nil {
		writeNotificationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, notificationSubscriptionView(*sub))
}

func (s *Server) handleUpdateNotificationSubscription(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.notificationsAvailable(w) {
		return
	}
	var req updateNotificationSubscriptionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	patch := notify.SubscriptionPatch{
		Label: req.Label, Enabled: req.Enabled, Messages: req.MessageEvents, Approvals: req.ApprovalEvents,
	}
	if req.MutedUntil != nil {
		if *req.MutedUntil == "" {
			patch.ClearMute = true
		} else {
			until, err := time.Parse(time.RFC3339, *req.MutedUntil)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_time", "muted_until must be RFC3339 or empty")
				return
			}
			patch.MutedUntil = &until
		}
	}
	sub, err := s.notifications.UpdateSubscription(r.Context(), r.PathValue("id"), patch)
	if err != nil {
		writeNotificationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notificationSubscriptionView(*sub))
}

func (s *Server) handleDeleteNotificationSubscription(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.notificationsAvailable(w) {
		return
	}
	if err := s.notifications.DeleteSubscription(r.Context(), r.PathValue("id")); err != nil {
		writeNotificationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestNotificationSubscription(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.notificationsAvailable(w) {
		return
	}
	if err := s.notifications.SendTest(r.Context(), r.PathValue("id")); err != nil {
		writeNotificationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

func (s *Server) handleMuteNotifications(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if !s.notificationsAvailable(w) {
		return
	}
	var req muteNotificationsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Hours < 0 || req.Hours > 24*7 {
		writeError(w, http.StatusBadRequest, "invalid_duration", "hours must be between 0 and 168")
		return
	}
	var until *time.Time
	if req.Hours > 0 {
		t := time.Now().Add(time.Duration(req.Hours) * time.Hour)
		until = &t
	}
	if err := s.notifications.SetMute(r.Context(), until); err != nil {
		writeNotificationError(w, err)
		return
	}
	s.notificationStatus(w, r)
}

func writeNotificationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, notify.ErrInvalidSubscription):
		writeError(w, http.StatusBadRequest, "invalid_subscription", "push subscription is invalid; enable notifications again")
	case errors.Is(err, notify.ErrSubscriptionNotFound):
		writeError(w, http.StatusNotFound, "subscription_not_found", "push subscription not found")
	default:
		writeError(w, http.StatusBadGateway, "push_failed", "could not deliver the push notification")
	}
}
