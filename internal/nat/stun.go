package nat

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	stunBindingRequest  = 0x0001
	stunBindingSuccess  = 0x0101
	stunBindingError    = 0x0111
	stunMagicCookie     = 0x2112A442
	stunHeaderSize      = 20
	stunAttrMapped      = 0x0001
	stunAttrXORMapped   = 0x0020
	stunAttrErrorCode   = 0x0009
	stunMaxDatagramSize = 1500
)

// discoverSTUN performs one RFC 5389 binding request and returns the
// reflexive address. The connected UDP socket only accepts the response from
// the configured server.
func discoverSTUN(ctx context.Context, server string, timeout time.Duration) (net.IP, int, error) {
	address, err := normalizeSTUNServer(server)
	if err != nil {
		return nil, 0, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", address)
	if err != nil {
		return nil, 0, fmt.Errorf("stun: dial: %w", err)
	}
	defer conn.Close()

	var transaction [12]byte
	if _, err := rand.Read(transaction[:]); err != nil {
		return nil, 0, fmt.Errorf("stun: transaction id: %w", err)
	}
	request := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(request[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(request[2:4], 0)
	binary.BigEndian.PutUint32(request[4:8], stunMagicCookie)
	copy(request[8:20], transaction[:])

	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, 0, err
	}
	if _, err := conn.Write(request); err != nil {
		return nil, 0, fmt.Errorf("stun: send: %w", err)
	}
	buf := make([]byte, stunMaxDatagramSize)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, 0, fmt.Errorf("stun: read: %w", err)
	}
	return parseSTUNResponse(buf[:n], transaction)
}

func normalizeSTUNServer(server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", errors.New("stun: no server is configured")
	}
	if host, port, err := net.SplitHostPort(server); err == nil {
		if host == "" {
			return "", errors.New("stun: server host is empty")
		}
		if port == "" {
			return net.JoinHostPort(host, "3478"), nil
		}
		return net.JoinHostPort(host, port), nil
	}
	if strings.HasPrefix(server, "[") && strings.HasSuffix(server, "]") {
		inner := strings.TrimSuffix(strings.TrimPrefix(server, "["), "]")
		if ip := net.ParseIP(inner); ip != nil {
			return net.JoinHostPort(inner, "3478"), nil
		}
	}
	if ip := net.ParseIP(server); ip != nil {
		return net.JoinHostPort(server, "3478"), nil
	}
	if strings.Contains(server, ":") {
		return "", fmt.Errorf("stun: invalid server %q", server)
	}
	return net.JoinHostPort(server, "3478"), nil
}

func parseSTUNResponse(data []byte, transaction [12]byte) (net.IP, int, error) {
	if len(data) < stunHeaderSize {
		return nil, 0, errors.New("stun: response is too short")
	}
	messageType := binary.BigEndian.Uint16(data[0:2])
	if messageType != stunBindingSuccess {
		if messageType == stunBindingError {
			return nil, 0, fmt.Errorf("stun: server error: %s", stunErrorCode(data))
		}
		return nil, 0, fmt.Errorf("stun: unexpected message type 0x%04x", messageType)
	}
	if binary.BigEndian.Uint32(data[4:8]) != stunMagicCookie {
		return nil, 0, errors.New("stun: magic cookie is missing")
	}
	if !bytesEqual(data[8:20], transaction[:]) {
		return nil, 0, errors.New("stun: transaction id does not match")
	}
	length := int(binary.BigEndian.Uint16(data[2:4]))
	if stunHeaderSize+length > len(data) {
		return nil, 0, errors.New("stun: truncated attributes")
	}
	attributes := data[stunHeaderSize : stunHeaderSize+length]
	var mapped net.IP
	var mappedPort int
	for len(attributes) >= 4 {
		kind := binary.BigEndian.Uint16(attributes[0:2])
		size := int(binary.BigEndian.Uint16(attributes[2:4]))
		valueEnd := 4 + size
		if valueEnd > len(attributes) {
			return nil, 0, errors.New("stun: truncated attribute")
		}
		value := attributes[4:valueEnd]
		switch kind {
		case stunAttrXORMapped:
			ip, port, err := parseMappedAddress(value, true, transaction)
			if err != nil {
				return nil, 0, err
			}
			mapped, mappedPort = ip, port
		case stunAttrMapped:
			if mapped == nil {
				ip, port, err := parseMappedAddress(value, false, transaction)
				if err != nil {
					return nil, 0, err
				}
				mapped, mappedPort = ip, port
			}
		}
		padded := (size + 3) &^ 3
		if 4+padded > len(attributes) {
			break
		}
		attributes = attributes[4+padded:]
	}
	if mapped == nil || mappedPort == 0 {
		return nil, 0, errors.New("stun: response has no mapped address")
	}
	return mapped, mappedPort, nil
}

func parseMappedAddress(value []byte, xor bool, transaction [12]byte) (net.IP, int, error) {
	if len(value) < 4 {
		return nil, 0, errors.New("stun: mapped address is too short")
	}
	family := value[1]
	port := int(binary.BigEndian.Uint16(value[2:4]))
	var ip net.IP
	switch family {
	case 0x01:
		if len(value) < 8 {
			return nil, 0, errors.New("stun: IPv4 mapped address is too short")
		}
		raw := append(net.IP(nil), value[4:8]...)
		if xor {
			cookie := make([]byte, 4)
			binary.BigEndian.PutUint32(cookie, stunMagicCookie)
			for i := range raw {
				raw[i] ^= cookie[i]
			}
			port ^= int(stunMagicCookie >> 16)
		}
		ip = net.IP(raw)
	case 0x02:
		if len(value) < 20 {
			return nil, 0, errors.New("stun: IPv6 mapped address is too short")
		}
		raw := append(net.IP(nil), value[4:20]...)
		if xor {
			mask := make([]byte, 16)
			binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
			copy(mask[4:], transaction[:])
			for i := range raw {
				raw[i] ^= mask[i]
			}
			port ^= int(stunMagicCookie >> 16)
		}
		ip = net.IP(raw)
	default:
		return nil, 0, fmt.Errorf("stun: unknown address family %d", family)
	}
	if port < 1 || port > 65535 {
		return nil, 0, errors.New("stun: mapped port is out of range")
	}
	return ip, port, nil
}

func stunErrorCode(data []byte) string {
	length := int(binary.BigEndian.Uint16(data[2:4]))
	if stunHeaderSize+length > len(data) {
		return "malformed error response"
	}
	attributes := data[stunHeaderSize : stunHeaderSize+length]
	for len(attributes) >= 4 {
		kind := binary.BigEndian.Uint16(attributes[0:2])
		size := int(binary.BigEndian.Uint16(attributes[2:4]))
		if 4+size > len(attributes) {
			break
		}
		if kind == stunAttrErrorCode && size >= 4 {
			class := int(attributes[4+2] & 0x07)
			number := int(attributes[4+3])
			return fmt.Sprintf("%d%d", class, number)
		}
		padded := (size + 3) &^ 3
		if 4+padded > len(attributes) {
			break
		}
		attributes = attributes[4+padded:]
	}
	return "unknown error"
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
