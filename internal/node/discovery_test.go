package node_test

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/mdns"
	"github.com/sidenor/toget/internal/node"
)

func TestMDNSDiscoveryAndVerifiedLookup(t *testing.T) {
	ctx := context.Background()
	host := newNodeWithOptions(t, "LAN Host", func(opts *node.Options) {
		opts.MDNS = "on"
	})
	guest := newNodeWithOptions(t, "LAN Guest", func(opts *node.Options) {
		opts.MDNS = "on"
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, peer := range guest.node.Discover(ctx) {
			if peer.ServerID == host.snapshot.Server.ID {
				goto discovered
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Skip("multicast discovery is unavailable in this environment")
discovered:

	record, err := guest.node.LookupServer(ctx, host.snapshot.Server.Code)
	if err != nil {
		t.Fatal(err)
	}
	if record.ServerID != host.snapshot.Server.ID || record.AccountID != host.snapshot.Account.ID {
		t.Fatalf("lookup returned the wrong server: %+v", record)
	}
	if record.Source != "mdns" {
		t.Fatalf("lookup source = %q, want mdns", record.Source)
	}
	if record.TLSFingerprint != host.node.Fingerprint() {
		t.Fatalf("fingerprint = %q, want %q", record.TLSFingerprint, host.node.Fingerprint())
	}
	if record.DirectURL == "" || record.URL == "" {
		t.Fatalf("lookup returned an empty address: %+v", record)
	}
}

func TestMDNSPeerVerificationRejectsTampering(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Verify Host")
	guest := newNode(t, "Verify Guest")
	info, err := host.node.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hostName, portText, err := net.SplitHostPort(strings.TrimPrefix(info.Endpoint, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	peer := mdns.Peer{
		ServerID: info.ServerID, ServerCode: info.ServerCode, Protocol: info.Protocol,
		TLSFingerprint: info.TLSFingerprint, Port: port,
		Addresses: []string{hostName}, LastSeen: time.Now().UTC(),
	}
	record, err := node.VerifyMDNSPeerForTest(guest.node, ctx, peer)
	if err != nil {
		t.Fatalf("valid peer was rejected: %v", err)
	}
	if record.ServerID != info.ServerID || record.Source != "mdns" {
		t.Fatalf("unexpected record: %+v", record)
	}

	badID := peer
	badID.ServerID = "tog1:s:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := node.VerifyMDNSPeerForTest(guest.node, ctx, badID); err == nil {
		t.Fatal("peer with a mismatched server ID was accepted")
	}

	badPin := peer
	badPin.TLSFingerprint = strings.Repeat("cd", 32)
	if _, err := node.VerifyMDNSPeerForTest(guest.node, ctx, badPin); err == nil {
		t.Fatal("peer with a mismatched certificate pin was accepted")
	}
}
