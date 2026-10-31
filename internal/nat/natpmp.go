package nat

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	natpmpVersion       = 0
	natpmpOpPublicIP    = 0
	natpmpOpMapUDP      = 1
	natpmpOpDeleteUDP   = 2
	natpmpRespPublicIP  = 128
	natpmpRespMapUDP    = 129
	natpmpRespDeleteUDP = 130
	natpmpAttempts      = 3
	natpmpRetryDelay    = 250 * time.Millisecond
)

// mapNATPMP asks a NAT-PMP gateway to forward one UDP port. NAT-PMP has no
// TCP mapping operation, so this method is skipped for TCP.
func (m *Manager) mapNATPMP(ctx context.Context, internalPort, suggestedExternalPort int, lifetime time.Duration) (Mapping, error) {
	gateway, err := m.pcpGateway()
	if err != nil {
		return Mapping{}, err
	}
	seconds := uint32(lifetime / time.Second)
	if seconds == 0 {
		seconds = 3600
	}
	request := make([]byte, 12)
	request[0] = natpmpVersion
	request[1] = natpmpOpMapUDP
	binary.BigEndian.PutUint16(request[4:6], uint16(internalPort))
	binary.BigEndian.PutUint16(request[6:8], uint16(suggestedExternalPort))
	binary.BigEndian.PutUint32(request[8:12], seconds)
	response, err := m.natpmpRequest(ctx, gateway, request, natpmpRespMapUDP)
	if err != nil {
		return Mapping{}, err
	}
	if len(response) < 16 {
		return Mapping{}, errors.New("natpmp: mapping response is too short")
	}
	mappedInternal := int(binary.BigEndian.Uint16(response[8:10]))
	mappedExternal := int(binary.BigEndian.Uint16(response[10:12]))
	mappedLifetime := time.Duration(binary.BigEndian.Uint32(response[12:16])) * time.Second
	if mappedInternal != internalPort || mappedExternal == 0 {
		return Mapping{}, errors.New("natpmp: gateway returned a different mapping")
	}
	mapping := Mapping{
		Method: MethodNATPMP, Protocol: UDP,
		InternalPort: internalPort, ExternalPort: mappedExternal,
		Lifetime: mappedLifetime,
	}
	if lifetime > 0 && (mapping.Lifetime <= 0 || mapping.Lifetime > lifetime) {
		mapping.Lifetime = lifetime
	}
	// The mapping response has no external address. Ask separately; a
	// gateway that refuses only this query still leaves a usable mapping.
	if ip, err := m.natpmpPublicAddress(ctx, gateway); err == nil {
		mapping.ExternalIP = ip
	}
	return mapping, nil
}

func (m *Manager) natpmpPublicAddress(ctx context.Context, gateway string) (net.IP, error) {
	request := []byte{natpmpVersion, natpmpOpPublicIP}
	response, err := m.natpmpRequest(ctx, gateway, request, natpmpRespPublicIP)
	if err != nil {
		return nil, err
	}
	if len(response) < 12 {
		return nil, errors.New("natpmp: public address response is too short")
	}
	return validateExternalIP(net.IP(append([]byte(nil), response[8:12]...))), nil
}

func (m *Manager) unmapNATPMP(ctx context.Context, mapping Mapping) error {
	gateway, err := m.pcpGateway()
	if err != nil {
		return err
	}
	request := make([]byte, 12)
	request[0] = natpmpVersion
	request[1] = natpmpOpDeleteUDP
	binary.BigEndian.PutUint16(request[4:6], uint16(mapping.InternalPort))
	binary.BigEndian.PutUint16(request[6:8], uint16(mapping.ExternalPort))
	_, err = m.natpmpRequest(ctx, gateway, request, natpmpRespDeleteUDP)
	return err
}

func (m *Manager) natpmpRequest(ctx context.Context, gateway string, request []byte, wantOpcode byte) ([]byte, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", gateway)
	if err != nil {
		return nil, fmt.Errorf("natpmp: dial gateway: %w", err)
	}
	defer conn.Close()
	buf := make([]byte, 64)
	var lastErr error
	for attempt := 0; attempt < natpmpAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(natpmpRetryDelay * time.Duration(attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		deadline := time.Now().Add(m.timeout)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		_ = conn.SetDeadline(deadline)
		if _, err := conn.Write(request); err != nil {
			lastErr = err
			continue
		}
		n, err := conn.Read(buf)
		if err != nil {
			lastErr = err
			continue
		}
		if n < 8 || buf[0] != natpmpVersion || buf[1] != wantOpcode {
			lastErr = errors.New("natpmp: invalid response")
			continue
		}
		result := binary.BigEndian.Uint16(buf[2:4])
		if result != 0 {
			return nil, fmt.Errorf("natpmp: gateway error %d", result)
		}
		return append([]byte(nil), buf[:n]...), nil
	}
	return nil, fmt.Errorf("natpmp: no response from gateway: %w", lastErr)
}
