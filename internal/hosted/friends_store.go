//go:build !js

package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// FriendRequestRecord is one stored friend request. Sealed maps a target
// device ID to an invite code sealed to that device's X25519 key.
type FriendRequestRecord struct {
	ID            string
	FromAccountID string
	ToAccountID   string
	RoomID        string
	Sealed        map[string]string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// StoredFriendRequest is one incoming request as presented to a device.
type StoredFriendRequest struct {
	ID            string
	FromAccountID string
	FromName      string
	ToAccountID   string
	RoomID        string
	Sealed        map[string]string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// StoredSentFriendRequest is one outgoing request as presented to its sender.
type StoredSentFriendRequest struct {
	ID          string
	ToAccountID string
	ToName      string
	RoomID      string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// SaveFriendRequest replaces any earlier request from the same sender to the
// same recipient for the same room. Replacing instead of updating preserves
// the new request ID, which is bound into every seal's context.
func (s *Store) SaveFriendRequest(ctx context.Context, record FriendRequestRecord) error {
	if record.ID == "" || record.FromAccountID == "" || record.ToAccountID == "" ||
		record.RoomID == "" || len(record.Sealed) == 0 {
		return errors.New("hosted: friend request is incomplete")
	}
	encoded, err := json.Marshal(record.Sealed)
	if err != nil {
		return fmt.Errorf("hosted: encode friend request seals: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("hosted: begin friend request: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM hosted_friend_requests
		  WHERE from_account_id = ? AND to_account_id = ? AND room_id = ?`,
		record.FromAccountID, record.ToAccountID, record.RoomID); err != nil {
		return fmt.Errorf("hosted: replace friend request: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO hosted_friend_requests
		    (id, from_account_id, to_account_id, room_id, sealed, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.FromAccountID, record.ToAccountID, record.RoomID,
		string(encoded), record.CreatedAt.UnixMilli(), record.ExpiresAt.UnixMilli()); err != nil {
		return fmt.Errorf("hosted: save friend request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("hosted: commit friend request: %w", err)
	}
	return nil
}

// CountFriendRequestsFrom bounds pending outgoing requests per sender.
func (s *Store) CountFriendRequestsFrom(ctx context.Context, accountID string, now time.Time) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hosted_friend_requests
		  WHERE from_account_id = ? AND expires_at > ?`,
		accountID, now.UnixMilli()).Scan(&count); err != nil {
		return 0, fmt.Errorf("hosted: count friend requests: %w", err)
	}
	return count, nil
}

// ListFriendRequestsForDevice returns unexpired requests that carry a seal
// for this device. Requests sealed only to other devices are omitted.
func (s *Store) ListFriendRequestsForDevice(ctx context.Context, accountID, deviceID string, now time.Time) ([]StoredFriendRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT f.id, f.from_account_id, a.name, f.to_account_id, f.room_id,
		        f.sealed, f.created_at, f.expires_at
		   FROM hosted_friend_requests f
		   JOIN hosted_accounts a ON a.id = f.from_account_id
		  WHERE f.to_account_id = ? AND f.expires_at > ?
		  ORDER BY f.created_at DESC, f.id
		  LIMIT ?`,
		accountID, now.UnixMilli(), MaxFriendRequestsPerAccount)
	if err != nil {
		return nil, fmt.Errorf("hosted: list friend requests: %w", err)
	}
	defer rows.Close()
	var requests []StoredFriendRequest
	for rows.Next() {
		var request StoredFriendRequest
		var encoded string
		var created, expires int64
		if err := rows.Scan(&request.ID, &request.FromAccountID, &request.FromName,
			&request.ToAccountID, &request.RoomID, &encoded, &created, &expires); err != nil {
			return nil, fmt.Errorf("hosted: scan friend request: %w", err)
		}
		if err := json.Unmarshal([]byte(encoded), &request.Sealed); err != nil {
			return nil, fmt.Errorf("hosted: decode friend request seals: %w", err)
		}
		if request.Sealed[deviceID] == "" {
			continue
		}
		request.CreatedAt = time.UnixMilli(created).UTC()
		request.ExpiresAt = time.UnixMilli(expires).UTC()
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

// ListFriendRequestsFrom returns a sender's unexpired outgoing requests with
// the recipient's current display name.
func (s *Store) ListFriendRequestsFrom(ctx context.Context, accountID string, now time.Time) ([]StoredSentFriendRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT f.id, f.to_account_id, a.name, f.room_id, f.created_at, f.expires_at
		   FROM hosted_friend_requests f
		   JOIN hosted_accounts a ON a.id = f.to_account_id
		  WHERE f.from_account_id = ? AND f.expires_at > ?
		  ORDER BY f.created_at DESC, f.id
		  LIMIT ?`,
		accountID, now.UnixMilli(), MaxFriendRequestsPerAccount)
	if err != nil {
		return nil, fmt.Errorf("hosted: list sent friend requests: %w", err)
	}
	defer rows.Close()
	var requests []StoredSentFriendRequest
	for rows.Next() {
		var request StoredSentFriendRequest
		var created, expires int64
		if err := rows.Scan(&request.ID, &request.ToAccountID, &request.ToName,
			&request.RoomID, &created, &expires); err != nil {
			return nil, fmt.Errorf("hosted: scan sent friend request: %w", err)
		}
		request.CreatedAt = time.UnixMilli(created).UTC()
		request.ExpiresAt = time.UnixMilli(expires).UTC()
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

// CancelFriendRequest removes one request only when the sender owns it. It is
// idempotent, so a request already answered by the recipient returns false.
func (s *Store) CancelFriendRequest(ctx context.Context, fromAccountID, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM hosted_friend_requests WHERE id = ? AND from_account_id = ?`,
		id, fromAccountID)
	if err != nil {
		return false, fmt.Errorf("hosted: cancel friend request: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hosted: cancel friend request rows: %w", err)
	}
	return affected > 0, nil
}

// DeleteFriendRequest removes one request only when it belongs to the
// recipient account. It is idempotent.
func (s *Store) DeleteFriendRequest(ctx context.Context, toAccountID, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM hosted_friend_requests WHERE id = ? AND to_account_id = ?`,
		id, toAccountID)
	if err != nil {
		return false, fmt.Errorf("hosted: delete friend request: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("hosted: delete friend request rows: %w", err)
	}
	return affected > 0, nil
}

// DeleteExpiredFriendRequests keeps the inbox tables small.
func (s *Store) DeleteExpiredFriendRequests(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM hosted_friend_requests WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return fmt.Errorf("hosted: delete expired friend requests: %w", err)
	}
	return nil
}

// GetFriendRequest loads one request for diagnostics and tests.
func (s *Store) GetFriendRequest(ctx context.Context, id string) (*StoredFriendRequest, error) {
	var request StoredFriendRequest
	var encoded string
	var created, expires int64
	err := s.db.QueryRowContext(ctx,
		`SELECT f.id, f.from_account_id, a.name, f.to_account_id, f.room_id,
		        f.sealed, f.created_at, f.expires_at
		   FROM hosted_friend_requests f
		   JOIN hosted_accounts a ON a.id = f.from_account_id
		  WHERE f.id = ?`, id).Scan(&request.ID, &request.FromAccountID,
		&request.FromName, &request.ToAccountID, &request.RoomID,
		&encoded, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hosted: get friend request: %w", err)
	}
	if err := json.Unmarshal([]byte(encoded), &request.Sealed); err != nil {
		return nil, fmt.Errorf("hosted: decode friend request seals: %w", err)
	}
	request.CreatedAt = time.UnixMilli(created).UTC()
	request.ExpiresAt = time.UnixMilli(expires).UTC()
	return &request, nil
}
