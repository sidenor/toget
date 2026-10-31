package node

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/sidenor/toget/internal/nat"
)

const (
	natLifetime    = time.Hour
	natRenewalMax  = 30 * time.Minute
	natRetryMin    = 30 * time.Second
	natRetryMax    = 5 * time.Minute
	natUnmapBudget = 5 * time.Second
)

// natLoop maintains TCP and UDP port mappings for the node listener. A
// failure never stops the node; it retries with backoff and leaves HTTPS and
// relay fallback available.
func (s *Service) natLoop(ctx context.Context, port int) {
	defer s.releaseNAT()
	backoff := natRetryMin
	for {
		mappings, external, method, err := s.mapNAT(ctx, port)
		if err == nil {
			s.setNATState(mappings, method, external)
			if external == "" {
				s.logger.Info("NAT port mappings active but no public address is known", "method", method)
			} else {
				s.logger.Info("NAT port mapping active", "method", method, "external", external)
			}
			backoff = natRetryMin
			if !sleepOrDone(ctx, natRenewal(mappings)) {
				return
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		s.clearNATState()
		s.logger.Warn("NAT port mapping failed", "error", err)
		if !sleepOrDone(ctx, backoff) {
			return
		}
		if backoff < natRetryMax {
			backoff *= 2
			if backoff > natRetryMax {
				backoff = natRetryMax
			}
		}
	}
}

func (s *Service) mapNAT(ctx context.Context, port int) ([]nat.Mapping, string, string, error) {
	var mappings []nat.Mapping
	var failures []error
	tcpMapping, tcpErr := s.nat.Map(ctx, nat.TCP, port, port, natLifetime)
	if tcpErr != nil {
		failures = append(failures, tcpErr)
	} else {
		mappings = append(mappings, tcpMapping)
	}
	udpMapping, udpErr := s.nat.Map(ctx, nat.UDP, port, port, natLifetime)
	if udpErr != nil {
		failures = append(failures, udpErr)
	} else {
		mappings = append(mappings, udpMapping)
	}
	if ctx.Err() != nil {
		return nil, "", "", ctx.Err()
	}
	if len(mappings) == 0 {
		return nil, "", "", errors.Join(failures...)
	}
	method := mappings[0].Method
	external := ""
	if tcpErr == nil {
		ip := tcpMapping.ExternalIP
		if ip == nil {
			// A NAT-PMP gateway reports the mapping and public address in
			// separate exchanges; UPnP may not implement GetExternalIPAddress.
			// STUN is the last source of the public address.
			if discovered, err := s.nat.ExternalIP(ctx); err == nil {
				ip = discovered
			}
		}
		if ip != nil {
			external = net.JoinHostPort(ip.String(), strconv.Itoa(tcpMapping.ExternalPort))
		}
	}
	return mappings, external, method, nil
}

// natRenewal refreshes before the shortest mapping lifetime expires.
func natRenewal(mappings []nat.Mapping) time.Duration {
	wait := natRenewalMax
	for _, mapping := range mappings {
		candidate := mapping.Lifetime / 2
		if candidate <= 0 {
			continue
		}
		if candidate < wait {
			wait = candidate
		}
	}
	if wait < 10*time.Second {
		wait = 10 * time.Second
	}
	return wait
}

func (s *Service) setNATState(mappings []nat.Mapping, method, external string) {
	s.natMu.Lock()
	s.natMappings = append([]nat.Mapping(nil), mappings...)
	s.natMethod = method
	s.natExternal = external
	s.natMu.Unlock()
}

func (s *Service) clearNATState() {
	s.natMu.Lock()
	s.natMappings = nil
	s.natMethod = ""
	s.natExternal = ""
	s.natMu.Unlock()
}

// releaseNAT removes mappings when the node stops. It is best-effort and
// bounded so shutdown cannot hang on a silent gateway.
func (s *Service) releaseNAT() {
	s.natMu.Lock()
	mappings := append([]nat.Mapping(nil), s.natMappings...)
	s.natMappings = nil
	s.natMethod = ""
	s.natExternal = ""
	s.natMu.Unlock()
	if s.nat == nil || len(mappings) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), natUnmapBudget)
	defer cancel()
	for _, mapping := range mappings {
		if err := s.nat.Unmap(ctx, mapping); err != nil {
			s.logger.Debug("could not remove NAT mapping", "method", mapping.Method, "protocol", mapping.Protocol, "error", err)
		}
	}
}

func (s *Service) natInfo() (method, endpoint, url string) {
	s.natMu.RLock()
	method, endpoint = s.natMethod, s.natExternal
	s.natMu.RUnlock()
	if endpoint == "" {
		return method, "", ""
	}
	s.mu.RLock()
	fingerprint := s.fingerprint
	s.mu.RUnlock()
	if fingerprint != "" {
		url = NodeURL("https://"+endpoint, fingerprint)
	}
	return method, endpoint, url
}

// externalEndpoint returns the public host:port, if a TCP mapping and a
// public address are both known.
func (s *Service) externalEndpoint() string {
	_, endpoint, _ := s.natInfo()
	return endpoint
}

func sleepOrDone(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
