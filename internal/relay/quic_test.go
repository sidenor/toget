package relay_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/node"
	"github.com/sidenor/toget/internal/quicclient"
	"github.com/sidenor/toget/internal/relay"
)

func TestRelayServesHTTP3(t *testing.T) {
	paths, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	service, err := relay.New(relay.Options{
		Listen: "127.0.0.1:0", Paths: paths, QUIC: "on",
		PollWait: 250 * time.Millisecond, WaitTimeout: 5 * time.Second,
		QueueTTL: time.Minute, ResponseTTL: time.Minute, SessionTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if !service.QUICEnabled() {
		t.Fatal("relay did not start an HTTP/3 listener")
	}

	base, fingerprint, err := node.ParsePinnedURL(service.URL())
	if err != nil {
		t.Fatal(err)
	}
	config, err := node.PinnedTLSConfig(fingerprint)
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
	transport := quicclient.New(base, "/relay/v1/health", config, httpsClient, 5*time.Second, 2*time.Second)
	defer transport.Close()
	quicclient.ForgetSupport(base + "/relay/v1/health")

	probeCtx, cancelProbe := context.WithTimeout(ctx, 5*time.Second)
	defer cancelProbe()
	if !transport.Probe(probeCtx) {
		t.Fatal("relay health check failed over the selected transport")
	}
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, base+"/relay/v1/health", nil)
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

func TestRelayRejectsInvalidQUICSetting(t *testing.T) {
	paths, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.New(relay.Options{Listen: "127.0.0.1:0", Paths: paths, QUIC: "maybe"}); err == nil {
		t.Fatal("invalid relay QUIC setting was accepted")
	}
}
