package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPushSubscriptionLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	sub := &PushSubscription{
		ID: "sub-1", Endpoint: "https://push.example.test/abc",
		P256DH: "p256dh-key", Auth: "auth-secret", Language: "zh",
		UserAgent: "test", Label: "Laptop", Enabled: true,
		MessageEvents: true, ApprovalEvents: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.SavePushSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetPushSubscription(ctx, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != sub.Endpoint || got.Label != "Laptop" || !got.Enabled || !got.MessageEvents || !got.ApprovalEvents {
		t.Fatalf("stored subscription = %#v", got)
	}

	// Re-posting the same endpoint refreshes keys without resetting prefs.
	got.Enabled = false
	got.MessageEvents = false
	got.Label = "Desk"
	if err := db.UpdatePushSubscription(ctx, got); err != nil {
		t.Fatal(err)
	}
	refreshed := &PushSubscription{
		ID: "different-id", Endpoint: sub.Endpoint, P256DH: "new-key", Auth: "new-auth",
		Language: "en", UserAgent: "second", UpdatedAt: now,
	}
	if err := db.SavePushSubscription(ctx, refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed.ID != "sub-1" {
		t.Fatalf("upsert ID = %q", refreshed.ID)
	}
	got, err = db.GetPushSubscription(ctx, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.P256DH != "new-key" || got.Auth != "new-auth" || got.Enabled || got.MessageEvents ||
		got.Label != "Desk" || got.Language != "en" || got.UserAgent != "second" {
		t.Fatalf("refreshed subscription = %#v", got)
	}

	if err := db.RecordPushResult(ctx, "sub-1", now, false); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetPushSubscription(ctx, "sub-1")
	if got.FailureCount != 1 || got.LastFailureAt == nil {
		t.Fatalf("failure record = %#v", got)
	}
	if err := db.RecordPushResult(ctx, "sub-1", now, true); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetPushSubscription(ctx, "sub-1")
	if got.FailureCount != 0 || got.LastSuccessAt == nil {
		t.Fatalf("success record = %#v", got)
	}

	list, err := db.ListPushSubscriptions(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %#v, %v", list, err)
	}
	if err := db.UpdatePushSubscription(ctx, &PushSubscription{ID: "missing"}); !errors.Is(err, ErrPushSubscriptionNotFound) {
		t.Fatalf("missing update = %v", err)
	}
	if err := db.DeletePushSubscription(ctx, "missing"); !errors.Is(err, ErrPushSubscriptionNotFound) {
		t.Fatalf("missing delete = %v", err)
	}
	if err := db.DeletePushSubscription(ctx, "sub-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetPushSubscription(ctx, "sub-1"); !errors.Is(err, ErrPushSubscriptionNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if raw, err := db.GetSetting(ctx, "missing"); err != nil || raw != "" {
		t.Fatalf("missing setting = %q, %v", raw, err)
	}
	if err := db.SetSetting(ctx, "notifications.muted_until", "2026-11-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if raw, err := db.GetSetting(ctx, "notifications.muted_until"); err != nil || raw == "" {
		t.Fatalf("setting = %q, %v", raw, err)
	}
	if err := db.SetSetting(ctx, "notifications.muted_until", "updated"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := db.GetSetting(ctx, "notifications.muted_until"); raw != "updated" {
		t.Fatalf("updated setting = %q", raw)
	}
	if err := db.DeleteSetting(ctx, "notifications.muted_until"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := db.GetSetting(ctx, "notifications.muted_until"); raw != "" {
		t.Fatalf("deleted setting = %q", raw)
	}
}
