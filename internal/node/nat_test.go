package node_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/relayproto"
)

// fakePCPGateway answers RFC 6887 MAP requests on loopback so the node's NAT
// loop can be exercised without a real router.
type fakePCPGateway struct {
	conn       *net.UDPConn
	result     byte
	externalIP net.IP
	requests   atomic.Int32
}

func startFakePCPGateway(t *testing.T, result byte, externalIP net.IP) *fakePCPGateway {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	gateway := &fakePCPGateway{conn: conn, result: result, externalIP: externalIP}
	t.Cleanup(func() { _ = conn.Close() })
	go gateway.serve()
	return gateway
}

func (g *fakePCPGateway) serve() {
	buf := make([]byte, 128)
	for {
		n, addr, err := g.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < 60 || buf[0] != 2 || buf[1] != 1 {
			continue
		}
		g.requests.Add(1)
		response := make([]byte, 60)
		response[0] = 2
		response[1] = 129
		response[3] = g.result
		copy(response[4:8], buf[4:8])
		copy(response[24:36], buf[24:36])
		response[36] = buf[36]
		copy(response[40:44], buf[40:44])
		if g.externalIP != nil {
			copy(response[44:60], g.externalIP.To16())
		}
		_, _ = g.conn.WriteToUDP(response, addr)
	}
}

func TestNodeNATMappingPublishesExternalEndpoint(t *testing.T) {
	gateway := startFakePCPGateway(t, 0, net.IPv4(198, 51, 100, 9))
	n := newNodeWithOptions(t, "NAT host", func(options *node.Options) {
		options.NAT = "auto"
		options.NATGateway = gateway.conn.LocalAddr().String()
		options.NATMethods = []string{"pcp"}
	})
	info := waitForInfo(t, n, func(info *node.Info) bool {
		return info.ExternalEndpoint != ""
	})
	if info.NATMethod != "pcp" {
		t.Fatalf("NAT method = %q", info.NATMethod)
	}
	_, port, err := net.SplitHostPort(strings.TrimPrefix(info.Endpoint, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	wantExternal := net.JoinHostPort("198.51.100.9", port)
	if info.ExternalEndpoint != wantExternal {
		t.Fatalf("external endpoint = %q, want %q", info.ExternalEndpoint, wantExternal)
	}
	if info.ExternalURL != node.NodeURL("https://"+wantExternal, info.TLSFingerprint) {
		t.Fatalf("external URL = %q", info.ExternalURL)
	}
	alternates := relayproto.SplitAlternates(info.URL)
	if len(alternates) != 1 || alternates[0] != wantExternal {
		t.Fatalf("URL alternates = %#v, want %q", alternates, wantExternal)
	}
	address := n.node.Address(context.Background())
	alternates = relayproto.SplitAlternates(address)
	if len(alternates) != 1 || alternates[0] != wantExternal {
		t.Fatalf("address alternates = %#v, want %q", alternates, wantExternal)
	}

	// The signed manifest carries the same alternate so a directory lookup or
	// saved card can pass it to clients.
	config, err := node.PinnedTLSConfig(info.TLSFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil, TLSClientConfig: config},
		Timeout:   5 * time.Second,
	}
	response, err := client.Get(info.Endpoint + "/toget/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var manifest node.Manifest
	if err := json.NewDecoder(response.Body).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ExternalEndpoint != wantExternal {
		t.Fatalf("manifest external endpoint = %q, want %q", manifest.ExternalEndpoint, wantExternal)
	}
}

func TestNodeNATFailureLeavesNodeHealthy(t *testing.T) {
	ctx := context.Background()
	gateway := startFakePCPGateway(t, 2, net.IPv4(198, 51, 100, 9)) // not authorized
	n := newNodeWithOptions(t, "NAT denied", func(options *node.Options) {
		options.NAT = "auto"
		options.NATGateway = gateway.conn.LocalAddr().String()
		options.NATMethods = []string{"pcp"}
	})
	deadline := time.Now().Add(5 * time.Second)
	for gateway.requests.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("node sent %d mapping requests, want TCP and UDP", gateway.requests.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	info, err := n.node.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.NATMethod != "" || info.ExternalEndpoint != "" {
		t.Fatalf("failed mapping was published: method=%q external=%q", info.NATMethod, info.ExternalEndpoint)
	}
	if len(relayproto.SplitAlternates(info.URL)) != 0 {
		t.Fatal("failed mapping added an alternate address")
	}
	config, err := node.PinnedTLSConfig(info.TLSFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil, TLSClientConfig: config},
		Timeout:   5 * time.Second,
	}
	response, err := client.Get(info.Endpoint + "/toget/v1/health")
	if err != nil {
		t.Fatalf("node became unreachable after NAT failure: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
}

func TestClientUsesPublicAlternateWhenLANUnreachable(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Alternate host")
	info, err := host.node.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, fingerprint, err := node.ParsePinnedURL(info.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(strings.TrimPrefix(endpoint, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	raw := relayproto.JoinAlternate(
		node.NodeURL("https://127.0.0.1:1", fingerprint),
		net.JoinHostPort("127.0.0.1", port),
	)
	key, err := host.accounts.UnsealDeviceKey(host.snapshot.Device)
	if err != nil {
		t.Fatal(err)
	}
	client, err := node.NewClientForURL(raw, host.snapshot.Account.ID, host.snapshot.Device.ID, key)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	manifest, err := client.Info(ctx)
	if err != nil {
		t.Fatalf("client did not fall back to the public alternate: %v", err)
	}
	if manifest.ServerID != host.snapshot.Account.ServerID {
		t.Fatalf("client reached the wrong node: %q", manifest.ServerID)
	}
}

func waitForInfo(t *testing.T, n *testNode, accept func(*node.Info) bool) *node.Info {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last *node.Info
	for time.Now().Before(deadline) {
		info, err := n.node.Info(context.Background())
		if err == nil {
			last = info
			if accept(info) {
				return info
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for node info: %#v", last)
	return nil
}
