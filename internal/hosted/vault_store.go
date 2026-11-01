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

// HostedVault is one account's sealed browser-state blob.
type HostedVault struct {
	Blob      SealedBlob
	Version   int64
	UpdatedAt time.Time
}

// GetVault returns the current vault, or ErrVaultNotFound when none exists.
func (s *Store) GetVault(ctx context.Context, accountID string) (*HostedVault, error) {
	var blobJSON string
	var version, updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT blob, version, updated_at FROM hosted_vaults WHERE account_id = ?`, accountID).
		Scan(&blobJSON, &version, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrVaultNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hosted: get vault: %w", err)
	}
	blob, err := decodeVaultBlob(blobJSON)
	if err != nil {
		return nil, err
	}
	return &HostedVault{Blob: blob, Version: version, UpdatedAt: timeFromMillis(updated)}, nil
}

// PutVault installs a new vault version. baseVersion 0 means the vault must
// not exist yet; otherwise it must match the stored version exactly.
func (s *Store) PutVault(ctx context.Context, accountID string, blob SealedBlob, baseVersion int64) (*HostedVault, error) {
	if !validVaultBlob(blob) {
		return nil, errors.New("hosted: invalid vault blob")
	}
	encoded, err := encodeVaultBlob(blob)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("hosted: begin vault write: %w", err)
	}
	defer tx.Rollback()

	var current int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM hosted_vaults WHERE account_id = ?`, accountID).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if baseVersion != 0 {
			return nil, ErrVaultConflict
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO hosted_vaults (account_id, blob, version, updated_at) VALUES (?, ?, 1, ?)`,
			accountID, encryptedVaultJSON(encoded), now.UnixMilli()); err != nil {
			return nil, fmt.Errorf("hosted: insert vault: %w", err)
		}
		current = 1
	case err != nil:
		return nil, fmt.Errorf("hosted: read vault version: %w", err)
	default:
		if current != baseVersion {
			return nil, ErrVaultConflict
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE hosted_vaults SET blob = ?, version = version + 1, updated_at = ? WHERE account_id = ? AND version = ?`,
			encryptedVaultJSON(encoded), now.UnixMilli(), accountID, baseVersion)
		if err != nil {
			return nil, fmt.Errorf("hosted: update vault: %w", err)
		}
		if affected, _ := res.RowsAffected(); affected != 1 {
			return nil, ErrVaultConflict
		}
		current++
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("hosted: commit vault: %w", err)
	}
	return &HostedVault{Blob: blob, Version: current, UpdatedAt: now}, nil
}

func encodeVaultBlob(blob SealedBlob) ([]byte, error) {
	return json.Marshal(blob)
}

func decodeVaultBlob(encoded string) (SealedBlob, error) {
	var blob SealedBlob
	if err := json.Unmarshal([]byte(encoded), &blob); err != nil || !validVaultBlob(blob) {
		return SealedBlob{}, errors.New("hosted: stored vault is invalid")
	}
	return blob, nil
}

// encryptedVaultJSON keeps the column type explicit and makes it obvious the
// host stores ciphertext, not vault fields.
func encryptedVaultJSON(encoded []byte) string { return string(encoded) }
