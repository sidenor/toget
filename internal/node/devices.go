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
	rooms, err := s.rooms.ListRooms(ctx)
	if err != nil {
		return nil, err
	}
	warnings := make([]string, 0)
	for i := range rooms {
		record := rooms[i]
		devices, err := s.rooms.ListDevices(ctx, record.ID)
		if err != nil {
			continue
		}
		if !containsDevice(devices, deviceID) {
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
			if _, err := s.rooms.RevokeDevice(ctx, record.ID, snapshot.Account.ID, deviceID, snapshot.Device.ID); err != nil {
				warnings = append(warnings, fmt.Sprintf("could not revoke the device in %s: %v", record.Name, err))
			}
			continue
		}
		if err := s.revokeDeviceAtHome(ctx, &record, snapshot, deviceID); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not revoke the device in %s: %v", record.Name, err))
		}
	}
	return warnings, nil
}

func containsDevice(devices []room.RoomDevice, deviceID string) bool {
	for i := range devices {
		if devices[i].DeviceID == deviceID {
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
