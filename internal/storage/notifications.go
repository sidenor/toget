package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrPushSubscriptionNotFound means a push subscription does not exist.
var ErrPushSubscriptionNotFound = errors.New("push subscription not found")

// PushSubscription is one browser push endpoint. The endpoint and browser
// keys are capabilities, not message content.
type PushSubscription struct {
	ID             string
	Endpoint       string
	P256DH         string
	Auth           string
	Language       string
	UserAgent      string
	Label          string
	Enabled        bool
	MessageEvents  bool
	ApprovalEvents bool
	MutedUntil     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastSuccessAt  *time.Time
	LastFailureAt  *time.Time
	FailureCount   int
}

// GetSetting returns a settings value, or an empty string when it is absent.
func (d *DB) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("storage: get setting: %w", err)
	}
	return value, nil
}

// SetSetting writes a settings value.
func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("storage: empty setting key")
	}
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("storage: set setting: %w", err)
	}
	return nil
}

// DeleteSetting removes a settings value.
func (d *DB) DeleteSetting(ctx context.Context, key string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("storage: delete setting: %w", err)
	}
	return nil
}

// SavePushSubscription inserts or refreshes a subscription by endpoint.
func (d *DB) SavePushSubscription(ctx context.Context, sub *PushSubscription) error {
	if sub == nil || sub.ID == "" || sub.Endpoint == "" || sub.P256DH == "" || sub.Auth == "" {
		return errors.New("storage: invalid push subscription")
	}
	now := sub.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
		sub.UpdatedAt = now
	}
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = now
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin push subscription: %w", err)
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM push_subscriptions WHERE endpoint = ?`, sub.Endpoint).Scan(&existing)
	switch {
	case err == nil:
		sub.ID = existing
		_, err = tx.ExecContext(ctx, `
			UPDATE push_subscriptions
			SET p256dh = ?, auth = ?,
			    language = CASE WHEN ? = '' THEN language ELSE ? END,
			    user_agent = CASE WHEN ? = '' THEN user_agent ELSE ? END,
			    label = CASE WHEN ? = '' THEN label ELSE ? END,
			    updated_at = ?
			WHERE id = ?`,
			sub.P256DH, sub.Auth,
			sub.Language, sub.Language, sub.UserAgent, sub.UserAgent, sub.Label, sub.Label,
			toMillis(now), sub.ID)
		if err != nil {
			return fmt.Errorf("storage: refresh push subscription: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `
			INSERT INTO push_subscriptions
				(id, endpoint, p256dh, auth, language, user_agent, label, enabled,
				 message_events, approval_events, muted_until, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sub.ID, sub.Endpoint, sub.P256DH, sub.Auth, sub.Language, sub.UserAgent, sub.Label,
			boolInt(sub.Enabled), boolInt(sub.MessageEvents), boolInt(sub.ApprovalEvents), nullableMillis(sub.MutedUntil),
			toMillis(sub.CreatedAt), toMillis(now))
		if err != nil {
			return fmt.Errorf("storage: insert push subscription: %w", err)
		}
	default:
		return fmt.Errorf("storage: look up push subscription: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit push subscription: %w", err)
	}
	return nil
}

// ListPushSubscriptions returns all subscriptions, newest first.
func (d *DB) ListPushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, endpoint, p256dh, auth, language, user_agent, label, enabled,
		       message_events, approval_events, muted_until, created_at, updated_at,
		       last_success_at, last_failure_at, failure_count
		FROM push_subscriptions ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list push subscriptions: %w", err)
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		sub, err := scanPushSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list push subscriptions: %w", err)
	}
	return out, nil
}

// GetPushSubscription returns one subscription by ID.
func (d *DB) GetPushSubscription(ctx context.Context, id string) (*PushSubscription, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT id, endpoint, p256dh, auth, language, user_agent, label, enabled,
		       message_events, approval_events, muted_until, created_at, updated_at,
		       last_success_at, last_failure_at, failure_count
		FROM push_subscriptions WHERE id = ?`, id)
	sub, err := scanPushSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPushSubscriptionNotFound
	}
	return sub, err
}

func scanPushSubscription(row rowScanner) (*PushSubscription, error) {
	var sub PushSubscription
	var enabled, messages, approvals int
	var muted, success, failure sql.NullInt64
	var created, updated int64
	if err := row.Scan(&sub.ID, &sub.Endpoint, &sub.P256DH, &sub.Auth, &sub.Language,
		&sub.UserAgent, &sub.Label, &enabled, &messages, &approvals, &muted,
		&created, &updated, &success, &failure, &sub.FailureCount); err != nil {
		return nil, err
	}
	sub.Enabled = enabled != 0
	sub.MessageEvents = messages != 0
	sub.ApprovalEvents = approvals != 0
	sub.MutedUntil = millisPtr(muted)
	sub.CreatedAt = fromMillis(created)
	sub.UpdatedAt = fromMillis(updated)
	sub.LastSuccessAt = millisPtr(success)
	sub.LastFailureAt = millisPtr(failure)
	return &sub, nil
}

// UpdatePushSubscription writes mutable subscription fields.
func (d *DB) UpdatePushSubscription(ctx context.Context, sub *PushSubscription) error {
	if sub == nil || sub.ID == "" {
		return errors.New("storage: invalid push subscription")
	}
	now := time.Now().UTC()
	sub.UpdatedAt = now
	res, err := d.db.ExecContext(ctx, `
		UPDATE push_subscriptions
		SET label = ?, enabled = ?, message_events = ?, approval_events = ?,
		    muted_until = ?, updated_at = ?
		WHERE id = ?`,
		sub.Label, boolInt(sub.Enabled), boolInt(sub.MessageEvents), boolInt(sub.ApprovalEvents),
		nullableMillis(sub.MutedUntil), toMillis(now), sub.ID)
	if err != nil {
		return fmt.Errorf("storage: update push subscription: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: update push subscription: %w", err)
	}
	if n == 0 {
		return ErrPushSubscriptionNotFound
	}
	return nil
}

// DeletePushSubscription removes one subscription.
func (d *DB) DeletePushSubscription(ctx context.Context, id string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("storage: delete push subscription: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: delete push subscription: %w", err)
	}
	if n == 0 {
		return ErrPushSubscriptionNotFound
	}
	return nil
}

// RecordPushResult records a delivery attempt without changing preferences.
func (d *DB) RecordPushResult(ctx context.Context, id string, at time.Time, success bool) error {
	column := "last_failure_at"
	change := "failure_count = failure_count + 1"
	if success {
		column = "last_success_at"
		change = "failure_count = 0"
	}
	_, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE push_subscriptions SET %s = ?, %s WHERE id = ?`, column, change), toMillis(at), id)
	if err != nil {
		return fmt.Errorf("storage: record push result: %w", err)
	}
	return nil
}

func millisPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMillis(v.Int64)
	return &t
}
