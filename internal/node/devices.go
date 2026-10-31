package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/room"
)

// RevokeAccountDevice revokes another device on the same account locally and
// propagates room revocations. Room homes rotate their epoch after revoking a
// device so it cannot decrypt later messages.
func (s *Service) RevokeAccountDevice(ctx context.Context, deviceID string) ([]string, error) {
	if s == nil || s.URL() == "" {
		return nil, ErrPairingDisabled
	}
	snapshot, err := s.accounts.Get(ctx)
	if err != nil {
		return nil, err
	}
	if deviceID == snapshot.Device.ID {
		return nil, account.ErrCurrentDevice
	}
	if err := s.accounts.RevokeDevice(ctx, deviceID); err != nil {
		return nil, err
	}
	s.TriggerAccountSync()
	return s.propagateDeviceRevocations(ctx, snapshot, []string{deviceID}), nil
}

// propagateDeviceRevocations applies account-level revocations to every room
// this node belongs to. Room homes rotate their epoch so a revoked device
// cannot read later messages. Failures are warnings: the account revocation is
// already durable and account sync retries propagation.
func (s *Service) propagateDeviceRevocations(ctx context.Context, snapshot *account.Snapshot, deviceIDs []string) []string {
	if len(deviceIDs) == 0 || snapshot.Device.RevokedAt != nil {
		return nil
	}
	wanted := make(map[string]bool, len(deviceIDs))
	for _, id := range deviceIDs {
		if id != "" && id != snapshot.Device.ID {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	rooms, err := s.rooms.ListRooms(ctx)
	if err != nil {
		return []string{"could not list rooms to propagate the device revocation: " + err.Error()}
	}
	warnings := make([]string, 0)
	for i := range rooms {
		record := rooms[i]
		devices, err := s.rooms.ListDevices(ctx, record.ID)
		if err != nil {
			continue
		}
		matched := make([]string, 0, len(wanted))
		for id := range wanted {
			// A room that already revoked the device needs no further work.
			// A device registered after an earlier propagation is still active
			// and must be revoked on this round.
			if hasActiveDevice(devices, id) {
				matched = append(matched, id)
			}
		}
		if len(matched) == 0 {
			continue
		}
		if record.HomeServerID == snapshot.Server.ID || record.HomeServerID == snapshot.Account.ServerID {
			if !containsDevice(devices, snapshot.Device.ID) {
				current, getErr := s.accounts.GetDeviceByID(ctx, snapshot.Device.ID)
				if getErr == nil {
					if _, _, regErr := s.rooms.RegisterDevice(ctx, record.ID, []byte(current.Certificate)); regErr != nil {
						warnings = append(warnings, fmt.Sprintf("could not register this device in %s: %v", record.Name, regErr))
						continue
					}
				}
			}
			for _, id := range matched {
				if _, err := s.rooms.RevokeDevice(ctx, record.ID, snapshot.Account.ID, id, snapshot.Device.ID); err != nil {
					warnings = append(warnings, fmt.Sprintf("could not revoke the device in %s: %v", record.Name, err))
				}
			}
			continue
		}
		for _, id := range matched {
			if err := s.revokeDeviceAtHome(ctx, &record, snapshot, id); err != nil {
				warnings = append(warnings, fmt.Sprintf("could not revoke the device in %s: %v", record.Name, err))
			}
		}
	}
	return warnings
}

func containsDevice(devices []room.RoomDevice, deviceID string) bool {
	for i := range devices {
		if devices[i].DeviceID == deviceID {
			return true
		}
	}
	return false
}

func hasActiveDevice(devices []room.RoomDevice, deviceID string) bool {
	for i := range devices {
		if devices[i].DeviceID == deviceID && devices[i].RevokedAt == nil {
			return true
		}
	}
	return false
}

func (s *Service) revokeDeviceAtHome(ctx context.Context, record *room.Room, snapshot *account.Snapshot, deviceID string) error {
	client, err := s.clientForRoom(ctx, record.ID)
	if err != nil {
		return err
	}
	defer client.Close()
	if _, err := client.RevokeDevice(ctx, record.ID, deviceID); err == nil {
		return nil
	} else {
		var remoteErr *RemoteError
		if !errors.As(err, &remoteErr) || remoteErr.Status != http.StatusForbidden {
			return err
		}
		if err := s.registerDeviceAtHome(ctx, client, record.ID, snapshot); err != nil {
			return err
		}
		_, err = client.RevokeDevice(ctx, record.ID, deviceID)
		return err
	}
}
