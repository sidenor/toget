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

// RemoteAccountRecord is a cached, host-signed statement about an account on
// another host. The signature inside Record is what makes the cache safe to
// use: a stale or tampered row cannot be turned into a valid device.
type RemoteAccountRecord struct {
	AccountID string
	HostID    string
	HostCode  string
	HostURL   string
	Record    FederatedAccount
	FetchedAt time.Time
	ExpiresAt time.Time
}

// SaveRemoteAccount caches one verified federated account record.
func (s *Store) SaveRemoteAccount(ctx context.Context, record RemoteAccountRecord) error {
	if record.AccountID == "" || record.HostID == "" || record.HostCode == "" ||
		record.Record.AccountID != record.AccountID {
		return errors.New("hosted: remote account record is incomplete")
	}
	encoded, err := json.Marshal(record.Record)
	if err != nil {
		return fmt.Errorf("hosted: encode remote account: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO hosted_remote_accounts
		    (account_id, host_id, host_code, host_url, record, fetched_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id) DO UPDATE SET
		    host_id = excluded.host_id,
		    host_code = excluded.host_code,
		    host_url = excluded.host_url,
		    record = excluded.record,
		    fetched_at = excluded.fetched_at,
		    expires_at = excluded.expires_at`,
		record.AccountID, record.HostID, record.HostCode, record.HostURL,
		encoded, record.FetchedAt.UnixMilli(), record.ExpiresAt.UnixMilli()); err != nil {
		return fmt.Errorf("hosted: save remote account: %w", err)
	}
	return nil
}

// GetRemoteAccount returns a cached record that has not expired.
func (s *Store) GetRemoteAccount(ctx context.Context, accountID string, now time.Time) (*RemoteAccountRecord, error) {
	var record RemoteAccountRecord
	var encoded []byte
	var fetched, expires int64
	err := s.db.QueryRowContext(ctx,
		`SELECT account_id, host_id, host_code, host_url, record, fetched_at, expires_at
		   FROM hosted_remote_accounts WHERE account_id = ? AND expires_at > ?`,
		accountID, now.UnixMilli()).Scan(&record.AccountID, &record.HostID,
		&record.HostCode, &record.HostURL, &encoded, &fetched, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hosted: get remote account: %w", err)
	}
	if err := json.Unmarshal(encoded, &record.Record); err != nil {
		return nil, fmt.Errorf("hosted: decode remote account: %w", err)
	}
	record.FetchedAt = time.UnixMilli(fetched).UTC()
	record.ExpiresAt = time.UnixMilli(expires).UTC()
	return &record, nil
}

// DeleteExpiredRemoteAccounts keeps the federation cache bounded.
func (s *Store) DeleteExpiredRemoteAccounts(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM hosted_remote_accounts WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return fmt.Errorf("hosted: delete expired remote accounts: %w", err)
	}
	return nil
}

// GetHostedRoomDeviceByAccountDevice returns the most recent room device row
// for an account/device pair on this host. Remote device authentication uses
// it to keep trusting an already known device even if its home host is
// temporarily unreachable.
func (s *Store) GetHostedRoomDeviceByAccountDevice(ctx context.Context, accountID, deviceID string) (*HostedRoomDevice, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT room_id, device_id, account_id, display_name, signing_public_key,
		       encryption_public_key, history, from_seq, joined_at, revoked_at
		  FROM hosted_room_devices
		 WHERE account_id = ? AND device_id = ? AND revoked_at IS NULL
		 ORDER BY joined_at DESC LIMIT 1`, accountID, deviceID)
	var d HostedRoomDevice
	var joined int64
	var revoked sql.NullInt64
	if err := row.Scan(&d.RoomID, &d.DeviceID, &d.AccountID, &d.DisplayName, &d.SigningPublicKey,
		&d.EncryptionPublicKey, &d.History, &d.FromSeq, &joined, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHostedRoomForbidden
		}
		return nil, fmt.Errorf("hosted: get room device by account device: %w", err)
	}
	d.JoinedAt = timeFromMillis(joined)
	d.RevokedAt = nullableTime(revoked)
	return &d, nil
}
