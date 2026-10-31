//go:build linux

package nat

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// defaultGateway reads the IPv4 default route from /proc/net/route.
func defaultGateway() (string, error) {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return "", fmt.Errorf("nat: read default route: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	first := true
	for scanner.Scan() {
		line := scanner.Text()
		if first {
			first = false
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 10, 32)
		if err != nil || flags&0x0002 == 0 {
			continue
		}
		ip, err := littleEndianIPv4(fields[2])
		if err != nil || ip.IsUnspecified() {
			continue
		}
		return ip.String(), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("nat: read default route: %w", err)
	}
	return "", errors.New("nat: no IPv4 default route")
}

func littleEndianIPv4(value string) (net.IP, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 4 {
		return nil, errors.New("invalid IPv4 address")
	}
	return net.IPv4(raw[3], raw[2], raw[1], raw[0]), nil
}
