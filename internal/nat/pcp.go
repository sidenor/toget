package nat

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	pcpVersion       = 2
	pcpOpMap         = 1
	pcpOpMapResponse = 129
	pcpRequestSize   = 60
	pcpResponseSize  = 60
	pcpProtocolTCP   = 6
	pcpProtocolUDP   = 17
	pcpAttempts      = 3
	pcpRetryDelay    = 250 * time.Millisecond
)

var pcpErrorNames = map[byte]string{
	0:  "success",
	1:  "unsupported version",
	2:  "not authorized",
	3:  "malformed request",
	4:  "unsupported opcode",
	5:  "unsupported option",
	6:  "malformed option",
	7:  "network failure",
	8:  "no resources",
	9:  "unsupported protocol",
	10: "user quota exceeded",
	11: "cannot provide external address",
	12: "address mismatch",
	13: "excessive remote peers",
}

// mapPCP asks a PCP server for a TCP or UDP mapping. PCP returns the assigned
// external port and address, so one exchange is enough to advertise a
// reachable endpoint.
func (m *Manager) mapPCP(ctx context.Context, protocol Protocol, internalPort, suggestedExternalPort int, lifetime time.Duration) (Mapping, error) {
	gateway, err := m.pcpGateway()
	if err != nil {
		return Mapping{}, err
	}
	seconds := uint32(lifetime / time.Second)
	if seconds == 0 {
		seconds = 3600
	}
	nonce, response, err := m.pcpRequest(ctx, gateway, protocol, internalPort, suggestedExternalPort, seconds)
	if err != nil {
		return Mapping{}, err
	}
	externalPort, externalIP, lifetime, err := parsePCPMapResponse(response, nonce, protocol, internalPort)
	if err != nil {
		return Mapping{}, err
	}
	_ = nonce
	return Mapping{
		Method: MethodPCP, Protocol: protocol,
		InternalPort: internalPort, ExternalPort: externalPort,
		ExternalIP: externalIP, Lifetime: lifetime,
	}, nil
}

func (m *Manager) unmapPCP(ctx context.Context, mapping Mapping) error {
	gateway, err := m.pcpGateway()
	if err != nil {
		return err
	}
	_, _, err = m.pcpRequest(ctx, gateway, mapping.Protocol, mapping.InternalPort, mapping.ExternalPort, 0)
	return err
}

func (m *Manager) pcpRequest(ctx context.Context, gateway string, protocol Protocol, internalPort, externalPort int, lifetime uint32) ([]byte, []byte, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", gateway)
	if err != nil {
		return nil, nil, fmt.Errorf("pcp: dial gateway: %w", err)
	}
	defer conn.Close()

	clientIP := net.IPv4zero
	if local, ok := conn.LocalAddr().(*net.UDPAddr); ok && local != nil {
		clientIP = local.IP
	}
	request := make([]byte, pcpRequestSize)
	request[0] = pcpVersion
	request[1] = pcpOpMap
	binary.BigEndian.PutUint32(request[4:8], lifetime)
	copy(request[8:24], clientIP.To16())
	nonce := request[24:36]
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("pcp: nonce: %w", err)
	}
	request[36] = pcpProtocolUDP
	if protocol == TCP {
		request[36] = pcpProtocolTCP
	}
	binary.BigEndian.PutUint16(request[40:42], uint16(internalPort))
	binary.BigEndian.PutUint16(request[42:44], uint16(externalPort))
	// Suggest all-zero external address so the gateway chooses its WAN address.
	response := make([]byte, pcpResponseSize)
	var lastErr error
	for attempt := 0; attempt < pcpAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(pcpRetryDelay * time.Duration(attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, nil, ctx.Err()
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
		n, err := conn.Read(response)
		if err != nil {
			lastErr = err
			continue
		}
		if n < pcpResponseSize {
			lastErr = errors.New("pcp: response is too short")
			continue
		}
		if response[0] != pcpVersion || response[1] != pcpOpMapResponse {
			lastErr = errors.New("pcp: invalid response header")
			continue
		}
		if !bytesEqual(response[24:36], nonce) {
			lastErr = errors.New("pcp: mapping nonce does not match")
			continue
		}
		if result := response[3]; result != 0 {
			name := pcpErrorNames[result]
			if name == "" {
				name = "unknown error"
			}
			return nil, nil, fmt.Errorf("pcp: gateway error %d (%s)", result, name)
		}
		return append([]byte(nil), nonce...), append([]byte(nil), response[:n]...), nil
	}
	return nil, nil, fmt.Errorf("pcp: no response from gateway: %w", lastErr)
}

func parsePCPMapResponse(response, nonce []byte, protocol Protocol, internalPort int) (int, net.IP, time.Duration, error) {
	if len(response) < pcpResponseSize || !bytesEqual(response[24:36], nonce) {
		return 0, nil, 0, errors.New("pcp: response does not match the request")
	}
	wantProtocol := byte(pcpProtocolUDP)
	if protocol == TCP {
		wantProtocol = pcpProtocolTCP
	}
	if response[36] != wantProtocol {
		return 0, nil, 0, errors.New("pcp: gateway returned another protocol")
	}
	if got := int(binary.BigEndian.Uint16(response[40:42])); got != internalPort {
		return 0, nil, 0, errors.New("pcp: gateway returned another internal port")
	}
	externalPort := int(binary.BigEndian.Uint16(response[42:44]))
	if externalPort == 0 {
		return 0, nil, 0, errors.New("pcp: gateway returned no external port")
	}
	ip := net.IP(append([]byte(nil), response[44:60]...))
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	ip = validateExternalIP(ip)
	if ip == nil {
		return 0, nil, 0, errors.New("pcp: gateway returned no usable external address")
	}
	lifetime := time.Duration(binary.BigEndian.Uint32(response[4:8])) * time.Second
	return externalPort, ip, lifetime, nil
}
