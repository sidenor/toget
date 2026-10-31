package node_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/quicclient"
)

func TestNodeServesHTTP3(t *testing.T) {
	ctx := context.Background()
	n := newNodeWithOptions(t, "QUIC", func(options *node.Options) {
		options.QUIC = "on"
	})
	info, err := n.node.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !info.QUIC || !n.node.QUICEnabled() {
		t.Fatal("node did not report an active HTTP/3 listener")
	}

	config, err := node.PinnedTLSConfig(info.TLSFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	httpsClient := &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			ForceAttemptHTTP2: true,
			TLSClientConfig:   config,
			DialContext:       (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		},
		Timeout: 5 * time.Second,
	}
	transport := quicclient.New(info.Endpoint, "/toget/v1/health", config, httpsClient, 5*time.Second, 2*time.Second)
	defer transport.Close()
	quicclient.ForgetSupport(transportHealthURL(info.Endpoint))

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !transport.Probe(probeCtx) {
		t.Fatal("node health check failed over the selected transport")
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, info.Endpoint+"/toget/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.Do(req)
	if err != nil {
		t.Fatalf("HTTP/3 request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.ProtoMajor != 3 {
		t.Fatalf("protocol = %s, want HTTP/3", resp.Proto)
	}
}

func TestNodeQUICCanBeDisabled(t *testing.T) {
	ctx := context.Background()
	n := newNodeWithOptions(t, "HTTPS only", func(options *node.Options) {
		options.QUIC = "off"
	})
	info, err := n.node.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.QUIC || n.node.QUICEnabled() {
		t.Fatal("node reported HTTP/3 with quic disabled")
	}
}

func transportHealthURL(endpoint string) string {
	return endpoint + "/toget/v1/health"
}
