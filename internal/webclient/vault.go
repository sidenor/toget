package webclient

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/hosted"
)

// The browser vault is an encrypted document stored by the blind account
// host. It carries only room metadata and epoch keys, never messages: with
// the keys, a new browser can decrypt room history from the room home.
//
// Version 2 records each room's home host so rooms can live on a server other
// than the account host. Version 1 documents are still readable.
const (
	vaultDocumentVersion       = 2
	vaultDocumentVersionLegacy = 1
)

type vaultRoom struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Topic     string         `json:"topic,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	HomeURL   string         `json:"home_url,omitempty"`
	HostID    string         `json:"home_host_id,omitempty"`
	OwnerID   string         `json:"owner_account_id,omitempty"`
	Epoch     int            `json:"epoch,omitempty"`
	Keys      map[int][]byte `json:"keys,omitempty"`
	History   string         `json:"history,omitempty"`
	JoinedAt  time.Time      `json:"joined_at,omitempty"`
	ReadSeq   int64          `json:"read_seq,omitempty"`
	UpdatedAt time.Time      `json:"updated_at,omitempty"`
}

type vaultDocument struct {
	Version   int         `json:"version"`
	AccountID string      `json:"account_id"`
	Rooms     []vaultRoom `json:"rooms"`
}

func vaultAAD(accountID string) string {
	return "toget:web-vault:v1:" + accountID
}

// VaultResult reports whether the encrypted vault is current on the host.
type VaultResult struct {
	Version int64 `json:"version"`
	Pending bool  `json:"pending"`
}

// FlushVault retries a vault write after an offline or transient failure.
func (c *Client) FlushVault(ctx context.Context) (*VaultResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account == nil {
		return nil, Errorf("signed_out", "还没有登录。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return nil, err
	}
	if err := c.refreshSessionLocked(ctx, client); err != nil {
		return nil, err
	}
	if err := c.saveVaultLocked(ctx); err != nil {
		return nil, err
	}
	return &VaultResult{Version: c.state.Account.VaultVersion}, nil
}

func (c *Client) loadVaultLocked(ctx context.Context, client *hosted.Client) (bool, error) {
	acct := c.state.Account
	if acct == nil {
		return false, Errorf("signed_out", "还没有登录。")
	}
	response, err := client.Vault(ctx, acct.SessionToken)
	if err != nil {
		var apiError *hosted.APIError
		if errors.As(err, &apiError) && apiError.Status == 404 {
			acct.VaultVersion = 0
			return false, nil
		}
		return false, mapHostError(err)
	}
	acct.VaultVersion = response.Version
	if response.Blob == nil || response.Version == 0 {
		return false, nil
	}
	plain, err := hosted.OpenBlob(acct.MasterKey, *response.Blob, vaultAAD(acct.ID))
	if err != nil {
		return false, Errorf("vault_corrupt", "房间同步数据无法解密，请重新登录或从备份恢复。")
	}
	var document vaultDocument
	if err := json.Unmarshal(plain, &document); err != nil ||
		document.Version < vaultDocumentVersionLegacy ||
		document.Version > vaultDocumentVersion || document.AccountID != acct.ID {
		return false, Errorf("vault_corrupt", "房间同步数据已损坏。")
	}
	c.mergeVaultLocked(document)
	return true, nil
}

func (c *Client) mergeVaultLocked(document vaultDocument) {
	acct := c.state.Account
	if acct == nil {
		return
	}
	for _, incoming := range document.Rooms {
		if incoming.ID == "" || len(incoming.Keys) == 0 {
			continue
		}
		existing := c.roomLocked(incoming.ID)
		if existing == nil {
			epoch := incoming.Epoch
			if epoch < 1 {
				epoch = maxEpoch(incoming.Keys)
			}
			if epoch < 1 {
				epoch = 1
			}
			homeURL, hostID := acct.HostURL, acct.HostID
			if incoming.HostID != "" {
				homeURL, hostID = incoming.HomeURL, incoming.HostID
			}
			kind, _ := normalizeRoomKind(incoming.Kind)
			room := &RoomState{
				ID: incoming.ID, Name: incoming.Name, Topic: incoming.Topic, Kind: kind,
				HomeURL: homeURL, HostID: hostID,
				OwnerAccountID: incoming.OwnerID, Epoch: epoch,
				Keys: cloneKeys(incoming.Keys), History: incoming.History,
				JoinedAt: incoming.JoinedAt, ReadSeq: incoming.ReadSeq,
				UpdatedAt: incoming.UpdatedAt,
			}
			c.upsertRoomLocked(room)
			continue
		}
		if existing.HostID == "" && incoming.HostID != "" {
			existing.HostID = incoming.HostID
			existing.HomeURL = incoming.HomeURL
		}
		if existing.Kind == "" {
			if kind, err := normalizeRoomKind(incoming.Kind); err == nil {
				existing.Kind = kind
			}
		}
		for epoch, key := range incoming.Keys {
			if epoch >= 1 && len(key) == 32 && len(existing.Keys[epoch]) != 32 {
				existing.Keys[epoch] = append([]byte(nil), key...)
			}
		}
		if incoming.UpdatedAt.After(existing.UpdatedAt) {
			if strings.TrimSpace(incoming.Name) != "" {
				existing.Name = incoming.Name
			}
			existing.Topic = incoming.Topic
			if incoming.OwnerID != "" {
				existing.OwnerAccountID = incoming.OwnerID
			}
			if incoming.History != "" {
				existing.History = incoming.History
			}
			if !incoming.JoinedAt.IsZero() {
				existing.JoinedAt = incoming.JoinedAt
			}
			existing.UpdatedAt = incoming.UpdatedAt
		}
		if incoming.Epoch > existing.Epoch {
			existing.Epoch = incoming.Epoch
		}
		if incoming.ReadSeq > existing.ReadSeq {
			existing.ReadSeq = incoming.ReadSeq
		}
	}
}

// saveVaultLocked always writes to the account host, even when the change
// that triggered it belongs to a room on another server.
func (c *Client) saveVaultLocked(ctx context.Context) error {
	acct := c.state.Account
	if acct == nil {
		return Errorf("signed_out", "还没有登录。")
	}
	client, err := c.hostedClient()
	if err != nil {
		return err
	}
	if err := c.refreshSessionLocked(ctx, client); err != nil {
		return err
	}
	acct.VaultDirty = true
	for attempt := 0; attempt < 4; attempt++ {
		document := c.vaultFromStateLocked()
		plain, err := json.Marshal(document)
		if err != nil {
			return Errorf("internal", "无法整理房间同步数据。")
		}
		blob, err := hosted.SealBlob(acct.MasterKey, plain, vaultAAD(acct.ID))
		if err != nil {
			return Errorf("internal", "无法加密房间同步数据。")
		}
		response, err := client.PutVault(ctx, acct.SessionToken, blob, acct.VaultVersion)
		if err == nil {
			acct.VaultVersion = response.Version
			acct.VaultDirty = false
			return nil
		}
		var apiError *hosted.APIError
		if errors.As(err, &apiError) && apiError.Code == "vault_conflict" {
			if _, loadErr := c.loadVaultLocked(ctx, client); loadErr != nil {
				return loadErr
			}
			continue
		}
		return mapHostError(err)
	}
	return Errorf("vault_conflict", "同步冲突，请稍后重试。")
}

func (c *Client) vaultFromStateLocked() vaultDocument {
	acct := c.state.Account
	document := vaultDocument{Version: vaultDocumentVersion, AccountID: acct.ID}
	rooms := c.activeRoomsLocked()
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].ID < rooms[j].ID })
	for _, room := range rooms {
		if len(room.Keys) == 0 {
			continue
		}
		document.Rooms = append(document.Rooms, vaultRoom{
			ID: room.ID, Name: room.Name, Topic: room.Topic, Kind: room.Kind,
			HomeURL: room.HomeURL, HostID: room.HostID,
			OwnerID: room.OwnerAccountID, Epoch: room.Epoch,
			Keys: cloneKeys(room.Keys), History: room.History,
			JoinedAt: room.JoinedAt, ReadSeq: room.ReadSeq,
			UpdatedAt: room.UpdatedAt,
		})
	}
	return document
}

func cloneKeys(keys map[int][]byte) map[int][]byte {
	out := make(map[int][]byte, len(keys))
	for epoch, key := range keys {
		if epoch >= 1 && len(key) == 32 {
			out[epoch] = append([]byte(nil), key...)
		}
	}
	return out
}

func maxEpoch(keys map[int][]byte) int {
	max := 0
	for epoch := range keys {
		if epoch > max {
			max = epoch
		}
	}
	return max
}
