// Package nat discovers a public address and asks a local gateway to forward
// ports for a Toget node. It implements STUN (RFC 5389), PCP (RFC 6887),
// NAT-PMP (RFC 6886), and UPnP IGD port mapping. UDP hole punching is not
// implemented; these clients only make a node reachable when the gateway
// accepts an explicit mapping.
package nat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Protocol is a port-mapping protocol.
type Protocol string

const (
	// TCP maps the HTTPS listener.
	TCP Protocol = "tcp"
	// UDP maps the HTTP/3 listener.
	UDP Protocol = "udp"
)

// Method names accepted by Options.Methods.
const (
	MethodPCP    = "pcp"
	MethodNATPMP = "natpmp"
	MethodUPnP   = "upnp"
)

// Mapping is one active port forwarding.
type Mapping struct {
	Method       string
	Protocol     Protocol
	InternalPort int
	ExternalPort int
	ExternalIP   net.IP
	Lifetime     time.Duration
}

// Options configures a Manager. The zero value uses every method, discovers
// the default gateway on Linux, and skips STUN.
type Options struct {
	// Gateway is the NAT-PMP/PCP gateway as an IP or host:port. Empty uses the
	// default route on Linux.
	Gateway string
	// STUNServer is a host:port used to discover the public address. Empty
	// disables STUN.
	STUNServer string
	// Methods limits and orders the mapping methods. Empty tries pcp, natpmp,
	// then upnp. Valid names are "pcp", "natpmp", and "upnp".
	Methods []string
	// UPnPLocation points directly at an IGD description URL instead of
	// discovering one over SSDP. It is mainly for tests and diagnostics.
	UPnPLocation string
	// Timeout bounds one protocol exchange. Zero uses 2 seconds.
	Timeout time.Duration
	// Logger receives method failures at debug level.
	Logger *slog.Logger
}

// Manager performs port mappings with the configured methods.
type Manager struct {
	gateway    string
	stunServer string
	methods    []string
	upnpLoc    string
	timeout    time.Duration
	logger     *slog.Logger

	upnpMu  sync.Mutex
	upnpSvc *upnpService
}

// New validates options and returns a Manager.
func New(options Options) (*Manager, error) {
	methods := make([]string, 0, len(options.Methods))
	seen := map[string]bool{}
	allowed := map[string]bool{MethodPCP: true, MethodNATPMP: true, MethodUPnP: true}
	for _, raw := range options.Methods {
		name := strings.ToLower(strings.TrimSpace(raw))
		if !allowed[name] {
			return nil, fmt.Errorf("nat: unknown method %q", raw)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		methods = append(methods, name)
	}
	if len(methods) == 0 {
		methods = []string{MethodPCP, MethodNATPMP, MethodUPnP}
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		gateway:    strings.TrimSpace(options.Gateway),
		stunServer: strings.TrimSpace(options.STUNServer),
		methods:    methods,
		upnpLoc:    strings.TrimSpace(options.UPnPLocation),
		timeout:    timeout,
		logger:     logger,
	}, nil
}

// Map forwards one port. InternalPort is the local listener; the gateway is
// asked to preserve it as the external port when possible. A successful
// mapping from the first usable method is returned.
func (m *Manager) Map(ctx context.Context, protocol Protocol, internalPort, suggestedExternalPort int, lifetime time.Duration) (Mapping, error) {
	if m == nil {
		return Mapping{}, errors.New("nat: manager is nil")
	}
	if protocol != TCP && protocol != UDP {
		return Mapping{}, errors.New("nat: protocol must be tcp or udp")
	}
	if internalPort < 1 || internalPort > 65535 {
		return Mapping{}, errors.New("nat: internal port is out of range")
	}
	if suggestedExternalPort < 1 || suggestedExternalPort > 65535 {
		suggestedExternalPort = internalPort
	}
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	var failures []error
	for _, method := range m.methods {
		var (
			mapping Mapping
			err     error
		)
		switch method {
		case MethodPCP:
			mapping, err = m.mapPCP(ctx, protocol, internalPort, suggestedExternalPort, lifetime)
		case MethodNATPMP:
			if protocol != UDP {
				continue
			}
			mapping, err = m.mapNATPMP(ctx, internalPort, suggestedExternalPort, lifetime)
		case MethodUPnP:
			mapping, err = m.mapUPnP(ctx, protocol, internalPort, suggestedExternalPort, lifetime)
		}
		if err == nil {
			return mapping, nil
		}
		if ctx.Err() != nil {
			return Mapping{}, ctx.Err()
		}
		m.logger.Debug("NAT mapping method failed", "method", method, "protocol", protocol, "error", err)
		failures = append(failures, fmt.Errorf("%s: %w", method, err))
	}
	return Mapping{}, fmt.Errorf("nat: no port mapping method succeeded: %w", errors.Join(failures...))
}

// Unmap removes a mapping created by Map. It is best-effort; a gateway may
// already have expired the mapping.
func (m *Manager) Unmap(ctx context.Context, mapping Mapping) error {
	if m == nil {
		return nil
	}
	switch mapping.Method {
	case MethodPCP:
		return m.unmapPCP(ctx, mapping)
	case MethodNATPMP:
		return m.unmapNATPMP(ctx, mapping)
	case MethodUPnP:
		return m.unmapUPnP(ctx, mapping)
	default:
		return nil
	}
}

// ExternalIP asks STUN for the public address. It is independent of port
// mapping and may return an address different from a mapping's gateway
// address on multi-WAN routers.
func (m *Manager) ExternalIP(ctx context.Context) (net.IP, error) {
	if m == nil || m.stunServer == "" {
		return nil, errors.New("nat: STUN is not configured")
	}
	ip, _, err := discoverSTUN(ctx, m.stunServer, m.timeout)
	return ip, err
}

func gatewayHostPort(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" {
		return "", errors.New("nat: no gateway is configured")
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		if h == "" {
			return "", errors.New("nat: gateway host is empty")
		}
		if _, err := strconv.Atoi(p); err != nil {
			return "", fmt.Errorf("nat: gateway port: %w", err)
		}
		return host, nil
	}
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("nat: gateway %q is not an IP address", raw)
	}
	return net.JoinHostPort(host, "5351"), nil
}

func (m *Manager) pcpGateway() (string, error) {
	if m.gateway != "" {
		return gatewayHostPort(m.gateway)
	}
	ip, err := defaultGateway()
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip, "5351"), nil
}

func validateExternalIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		if v4.IsUnspecified() || v4.IsLoopback() {
			return nil
		}
		return v4
	}
	if ip.IsUnspecified() || ip.IsLoopback() {
		return nil
	}
	return ip
}
