package mdns

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func testInfo(id, code, port string) Info {
	return Info{
		ID:          id,
		Code:        code,
		Protocol:    1,
		Fingerprint: strings.Repeat("ab", 32),
		Port:        mustPort(port),
		Addresses:   []net.IP{net.ParseIP("192.0.2.10")},
	}
}

func mustPort(raw string) int {
	port, err := strconv.Atoi(raw)
	if err != nil {
		panic(err)
	}
	return port
}

func TestValidateInfo(t *testing.T) {
	good := testInfo("tog1:s:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "12345678", "7717")
	if err := validateInfo(good); err != nil {
		t.Fatalf("valid info rejected: %v", err)
	}
	cases := []struct {
		name string
		edit func(*Info)
	}{
		{"missing prefix", func(i *Info) { i.ID = "server" }},
		{"short code", func(i *Info) { i.Code = "1234567" }},
		{"non-digit code", func(i *Info) { i.Code = "1234567x" }},
		{"bad fingerprint", func(i *Info) { i.Fingerprint = "abcd" }},
		{"bad port", func(i *Info) { i.Port = 70000 }},
		{"unspecified address", func(i *Info) { i.Addresses = []net.IP{net.IPv4zero} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := good
			tc.edit(&info)
			if err := validateInfo(info); err == nil {
				t.Fatal("invalid info was accepted")
			}
		})
	}
}

func TestAdvertisementRoundTrip(t *testing.T) {
	info := testInfo("tog1:s:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "12345678", "7717")
	packet, err := advertisementPacket(info, info.Addresses, TTL)
	if err != nil {
		t.Fatal(err)
	}
	response, records, err := observations(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !response {
		t.Fatal("advertisement was not marked as a response")
	}
	service := &Service{
		peers: make(map[string]*peerEntry), hosts: make(map[string]*hostEntry),
	}
	service.apply(records)
	peers := service.Peers()
	if len(peers) != 1 {
		t.Fatalf("got %d peers, want 1", len(peers))
	}
	peer := peers[0]
	if peer.ServerID != info.ID || peer.ServerCode != info.Code ||
		peer.Protocol != info.Protocol || peer.Port != info.Port ||
		peer.TLSFingerprint != info.Fingerprint {
		t.Fatalf("unexpected peer: %+v", peer)
	}
	if len(peer.Addresses) != 1 || peer.Addresses[0] != "192.0.2.10" {
		t.Fatalf("unexpected addresses: %v", peer.Addresses)
	}
}

func TestQueryMatching(t *testing.T) {
	info := testInfo("tog1:s:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "87654321", "7718")
	packet, err := queryPacket()
	if err != nil {
		t.Fatal(err)
	}
	asked, err := questions(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !wantsName(asked, info) {
		t.Fatal("service query did not match the advertisement")
	}
	if wantsName([]dnsmessage.Question{{
		Name: dnsmessage.MustNewName("_other._tcp.local."), Type: dnsmessage.TypePTR,
		Class: dnsmessage.ClassINET,
	}}, info) {
		t.Fatal("unrelated service query matched")
	}
}

func TestCacheExpires(t *testing.T) {
	info := testInfo("tog1:s:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", "11223344", "7717")
	packet, err := advertisementPacket(info, info.Addresses, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, records, err := observations(packet)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		peers: make(map[string]*peerEntry), hosts: make(map[string]*hostEntry),
	}
	service.apply(records)
	if len(service.Peers()) != 1 {
		t.Fatal("peer was not cached")
	}
	time.Sleep(1100 * time.Millisecond)
	if peers := service.Peers(); len(peers) != 0 {
		t.Fatalf("expired peer remains: %+v", peers)
	}
}

func TestServiceDiscovery(t *testing.T) {
	first := testInfo("tog1:s:DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD", "55667788", "17717")
	second := testInfo("tog1:s:EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE", "88776655", "17718")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a, err := New(first, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx); err != nil {
		t.Skipf("multicast unavailable: %v", err)
	}
	defer a.Close()
	b, err := New(second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ctx); err != nil {
		t.Skipf("multicast unavailable on second socket: %v", err)
	}
	defer b.Close()

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if peers := b.Lookup(first.Code); len(peers) == 1 {
			if peers[0].ServerID != first.ID {
				t.Fatalf("wrong peer: %+v", peers[0])
			}
			return
		}
		b.Refresh()
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("did not discover the first service; peers=%+v", b.Peers())
}
