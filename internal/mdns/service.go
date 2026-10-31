// Package mdns implements the small multicast-DNS subset Toget uses for
// same-LAN discovery. Advertisements are hints only: a node's server ID,
// certificate pin, and signed manifests remain the authority for every
// connection.
package mdns

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mdnsPort      = 5353
	queryEvery    = 10 * time.Second
	announceEvery = 60 * time.Second

	// The browse cache is bounded so a hostile or noisy LAN cannot grow it
	// without limit. These are far above the number of nodes a person can
	// reasonably have on one link.
	maxCacheTTL  = 600 * time.Second
	maxPeers     = 256
	maxHosts     = 512
	maxTXTFields = 32
	maxHostAddrs = 16
)

type socket struct {
	conn    *net.UDPConn
	iface   *net.Interface
	group   *net.UDPAddr
	network string
}

type peerEntry struct {
	instance  string
	ptrExpiry time.Time
	srvExpiry time.Time
	txtExpiry time.Time
	host      string
	port      int
	txt       map[string]string
	lastSeen  time.Time
}

type hostEntry struct {
	addresses []net.IP
	expiry    time.Time
}

// Service advertises one node and browses for other Toget nodes on every
// multicast-capable interface. A nil *Service is safe to use for Peers,
// Lookup, and Refresh.
type Service struct {
	info   Info
	logger *slog.Logger

	sockets []*socket

	mu      sync.RWMutex
	peers   map[string]*peerEntry
	hosts   map[string]*hostEntry
	started bool

	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}
	queryWake chan struct{}
}

// New validates an advertisement and returns a service. Start binds the
// multicast sockets.
func New(info Info, logger *slog.Logger) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}
	fingerprint, err := normalizeFingerprint(info.Fingerprint)
	if err != nil {
		return nil, err
	}
	info.Fingerprint = fingerprint
	if err := validateInfo(info); err != nil {
		return nil, err
	}
	info.Addresses = sortedIPs(info.Addresses)
	return &Service{
		info: info, logger: logger,
		peers: make(map[string]*peerEntry), hosts: make(map[string]*hostEntry),
		done: make(chan struct{}), queryWake: make(chan struct{}, 1),
	}, nil
}

// Start joins the mDNS groups on all available interfaces and starts the
// announcement and browse loops. It returns after the sockets are bound.
// Canceling ctx shuts the service down.
func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return errors.New("mdns: service is nil")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	var sockets []*socket
	for i := range interfaces {
		iface := interfaces[i]
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagMulticast == 0 && iface.Flags&net.FlagLoopback == 0 {
			continue
		}
		for _, network := range []string{"udp4", "udp6"} {
			so, listenErr := listenMulticast(&iface, network)
			if listenErr != nil {
				s.logger.Debug("mdns interface unavailable", "interface", iface.Name, "network", network, "error", listenErr)
				continue
			}
			sockets = append(sockets, so)
		}
	}
	if len(sockets) == 0 {
		return errors.New("mdns: no multicast interface is available")
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		for _, so := range sockets {
			so.conn.Close()
		}
		return errors.New("mdns: already started")
	}
	s.started = true
	s.sockets = sockets
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Unlock()

	for _, so := range sockets {
		go s.readLoop(so)
	}
	go s.run(runCtx)
	return nil
}

func listenMulticast(iface *net.Interface, network string) (*socket, error) {
	var group *net.UDPAddr
	switch network {
	case "udp4":
		group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort}
	case "udp6":
		group = &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: mdnsPort, Zone: iface.Name}
	default:
		return nil, errors.New("mdns: unsupported network")
	}
	conn, err := net.ListenMulticastUDP(network, iface, group)
	if err != nil {
		return nil, err
	}
	if err := conn.SetReadBuffer(64 << 10); err != nil {
		conn.Close()
		return nil, err
	}
	return &socket{conn: conn, iface: iface, group: group, network: network}, nil
}

func (s *Service) run(ctx context.Context) {
	s.announce(TTL)
	s.query()
	announceTimer := time.NewTimer(time.Second)
	defer announceTimer.Stop()
	announceTicker := time.NewTicker(announceEvery)
	defer announceTicker.Stop()
	queryTicker := time.NewTicker(queryEvery)
	defer queryTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.shutdown()
			return
		case <-announceTimer.C:
			s.announce(TTL)
		case <-announceTicker.C:
			s.announce(TTL)
		case <-queryTicker.C:
			s.query()
		case <-s.queryWake:
			s.query()
		}
	}
}

func (s *Service) readLoop(so *socket) {
	buffer := make([]byte, MaxPacket)
	for {
		n, _, err := so.conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		if n == 0 || n > MaxPacket {
			continue
		}
		packet := append([]byte(nil), buffer[:n]...)
		response, records, err := observations(packet)
		if err != nil {
			s.logger.Debug("mdns packet rejected", "error", err)
			continue
		}
		if response {
			s.apply(records)
			continue
		}
		asked, err := questions(packet)
		if err != nil || !wantsName(asked, s.info) {
			continue
		}
		s.respond(so)
	}
}

func (s *Service) respond(so *socket) {
	addresses := familyAddresses(s.addressesFor(so), so.network)
	if len(addresses) == 0 {
		return
	}
	packet, err := advertisementPacket(s.info, addresses, TTL)
	if err != nil {
		s.logger.Debug("mdns response could not be built", "error", err)
		return
	}
	if _, err := so.conn.WriteToUDP(packet, so.group); err != nil {
		s.logger.Debug("mdns response failed", "interface", so.iface.Name, "error", err)
	}
}

func (s *Service) announce(ttl uint32) {
	for _, so := range s.sockets {
		addresses := familyAddresses(s.addressesFor(so), so.network)
		if len(addresses) == 0 {
			continue
		}
		packet, err := advertisementPacket(s.info, addresses, ttl)
		if err != nil {
			s.logger.Debug("mdns announcement could not be built", "error", err)
			continue
		}
		if _, err := so.conn.WriteToUDP(packet, so.group); err != nil {
			s.logger.Debug("mdns announcement failed", "interface", so.iface.Name, "error", err)
		}
	}
}

func (s *Service) query() {
	packet, err := queryPacket()
	if err != nil {
		s.logger.Debug("mdns query could not be built", "error", err)
		return
	}
	for _, so := range s.sockets {
		if _, err := so.conn.WriteToUDP(packet, so.group); err != nil {
			s.logger.Debug("mdns query failed", "interface", so.iface.Name, "error", err)
		}
	}
}

func (s *Service) addressesFor(so *socket) []net.IP {
	if len(s.info.Addresses) > 0 {
		return s.info.Addresses
	}
	if so.iface == nil {
		return nil
	}
	addresses, err := so.iface.Addrs()
	if err != nil {
		return nil
	}
	out := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil {
			continue
		}
		if ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		out = append(out, ip)
	}
	return sortedIPs(out)
}

func (s *Service) apply(records []observation) {
	if len(records) == 0 {
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		ttl := time.Duration(record.ttl) * time.Second
		if ttl > maxCacheTTL {
			ttl = maxCacheTTL
		}
		expires := now.Add(ttl)
		switch record.kind {
		case "ptr":
			entry := s.peers[record.target]
			if record.ttl == 0 {
				delete(s.peers, record.target)
				continue
			}
			if !validInstance(record.target) {
				continue
			}
			if entry == nil {
				if len(s.peers) >= maxPeers {
					continue
				}
				entry = &peerEntry{instance: record.target, txt: make(map[string]string)}
				s.peers[record.target] = entry
			}
			entry.ptrExpiry = expires
			entry.lastSeen = now
		case "srv":
			if !validInstance(record.name) {
				continue
			}
			entry := s.peers[record.name]
			if entry == nil {
				if len(s.peers) >= maxPeers {
					continue
				}
				entry = &peerEntry{instance: record.name, txt: make(map[string]string)}
				s.peers[record.name] = entry
			}
			if record.ttl == 0 {
				entry.srvExpiry = time.Time{}
				entry.host = ""
				entry.port = 0
				continue
			}
			if record.port < 1 || record.port > 65535 || record.target == "" {
				continue
			}
			entry.host = record.target
			entry.port = record.port
			entry.srvExpiry = expires
			entry.lastSeen = now
		case "txt":
			if !validInstance(record.name) {
				continue
			}
			entry := s.peers[record.name]
			if entry == nil {
				if len(s.peers) >= maxPeers {
					continue
				}
				entry = &peerEntry{instance: record.name, txt: make(map[string]string)}
				s.peers[record.name] = entry
			}
			if record.ttl == 0 {
				entry.txtExpiry = time.Time{}
				entry.txt = make(map[string]string)
				continue
			}
			key, value, ok := parseTXT(record.text)
			if !ok {
				continue
			}
			if _, exists := entry.txt[key]; !exists && len(entry.txt) >= maxTXTFields {
				continue
			}
			entry.txt[key] = value
			entry.txtExpiry = expires
			entry.lastSeen = now
		case "a", "aaaa":
			if record.ttl == 0 {
				delete(s.hosts, record.name)
				continue
			}
			host := s.hosts[record.name]
			if host == nil {
				if len(s.hosts) >= maxHosts {
					continue
				}
				host = &hostEntry{}
				s.hosts[record.name] = host
			}
			if len(host.addresses) < maxHostAddrs {
				host.addresses = append(host.addresses, record.address)
			}
			host.expiry = expires
		}
	}
	s.pruneLocked(now)
}

func (s *Service) pruneLocked(now time.Time) {
	for name, host := range s.hosts {
		if !now.Before(host.expiry) {
			delete(s.hosts, name)
		}
	}
	for instance, entry := range s.peers {
		if entry.ptrExpiry.IsZero() || !now.Before(entry.ptrExpiry) {
			delete(s.peers, instance)
			continue
		}
		// A peer that has not refreshed its SRV/TXT records is no longer
		// usable even if the PTR record is still alive.
		if entry.srvExpiry.IsZero() || !now.Before(entry.srvExpiry) ||
			entry.txtExpiry.IsZero() || !now.Before(entry.txtExpiry) {
			continue
		}
	}
}

func (s *Service) peerSnapshotsLocked(now time.Time) []Peer {
	out := make([]Peer, 0, len(s.peers))
	for _, entry := range s.peers {
		if entry.ptrExpiry.IsZero() || !now.Before(entry.ptrExpiry) ||
			entry.srvExpiry.IsZero() || !now.Before(entry.srvExpiry) ||
			entry.txtExpiry.IsZero() || !now.Before(entry.txtExpiry) {
			continue
		}
		if entry.txt["txtv"] != txtFormatVersion {
			continue
		}
		serverID := entry.txt["id"]
		code := entry.txt["code"]
		fingerprint, err := normalizeFingerprint(entry.txt["fp"])
		if err != nil || !strings.HasPrefix(serverID, "tog1:s:") || len(serverID) > 160 ||
			len(code) != 8 || entry.port < 1 || entry.port > 65535 {
			continue
		}
		validCode := true
		for _, digit := range code {
			if digit < '0' || digit > '9' {
				validCode = false
				break
			}
		}
		if !validCode {
			continue
		}
		protocol, err := strconv.Atoi(entry.txt["proto"])
		if err != nil || protocol < 1 {
			continue
		}
		if s.info.ID != "" && serverID == s.info.ID {
			continue
		}
		host := s.hosts[entry.host]
		if host == nil || !now.Before(host.expiry) {
			continue
		}
		addresses := sortedIPs(host.addresses)
		if len(addresses) == 0 {
			continue
		}
		values := make([]string, 0, len(addresses))
		for _, address := range addresses {
			values = append(values, address.String())
		}
		out = append(out, Peer{
			ServerID: serverID, ServerCode: code, Protocol: protocol,
			TLSFingerprint: fingerprint, Port: entry.port,
			Addresses: values, LastSeen: entry.lastSeen,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].ServerID < out[j].ServerID
		}
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out
}

// Peers returns current discoveries sorted by most recently seen. A peer is
// still only a hint until its signed manifests are verified.
func (s *Service) Peers() []Peer {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	return s.peerSnapshotsLocked(now)
}

// Lookup returns cached discoveries advertising code.
func (s *Service) Lookup(code string) []Peer {
	var out []Peer
	for _, peer := range s.Peers() {
		if peer.ServerCode == code {
			out = append(out, peer)
		}
	}
	return out
}

// Refresh asks the browser loop to send a new query immediately. It never
// blocks and is harmless before Start.
func (s *Service) Refresh() {
	if s == nil {
		return
	}
	select {
	case s.queryWake <- struct{}{}:
	default:
	}
}

// Close sends a goodbye packet and releases every multicast socket.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.shutdown()
	<-s.done
	return nil
}

func (s *Service) shutdown() {
	s.closeOnce.Do(func() {
		s.mu.RLock()
		cancel := s.cancel
		s.mu.RUnlock()
		if cancel != nil {
			cancel()
		}
		s.announce(0)
		s.mu.RLock()
		sockets := append([]*socket(nil), s.sockets...)
		s.mu.RUnlock()
		for _, so := range sockets {
			_ = so.conn.Close()
		}
		close(s.done)
	})
}
