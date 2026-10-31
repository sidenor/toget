// Package quicclient selects HTTP/3 for a pinned endpoint after a successful
// health probe and otherwise uses HTTPS. A request is never retried on a
// second transport: if HTTP/3 fails after being chosen, the caller sees the
// error and the next request falls back to HTTPS. That keeps ambiguous writes
// from being applied twice, matching the direct/relay fallback rule.
package quicclient

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go/http3"
)

const (
	// supportedTTL is how long a successful probe suppresses further probes.
	supportedTTL = 5 * time.Minute
	// unsupportedTTL is how long a failed probe keeps a request on HTTPS.
	unsupportedTTL = time.Minute
	// defaultProbeTimeout bounds the startup health request.
	defaultProbeTimeout = 2500 * time.Millisecond
)

type supportEntry struct {
	supported bool
	until     time.Time
}

var supportCache sync.Map // health URL -> supportEntry

// Transport is an http.RoundTripper-like client that picks HTTP/3 or HTTPS.
type Transport struct {
	healthURL    string
	https        *http.Client
	h3           *http.Client
	h3Transport  *http3.Transport
	probeTimeout time.Duration
}

// New builds a transport for one pinned endpoint. https must already carry
// the pinned TLS configuration and its normal timeout. timeout is applied to
// the HTTP/3 client as well; probeTimeout zero uses 2.5 seconds.
func New(endpoint, healthPath string, config *tls.Config, https *http.Client, timeout, probeTimeout time.Duration) *Transport {
	if probeTimeout <= 0 {
		probeTimeout = defaultProbeTimeout
	}
	quicTransport := &http3.Transport{
		TLSClientConfig:    config.Clone(),
		DisableCompression: true,
	}
	return &Transport{
		healthURL:    strings.TrimSuffix(endpoint, "/") + healthPath,
		https:        https,
		h3:           &http.Client{Transport: quicTransport, Timeout: timeout},
		h3Transport:  quicTransport,
		probeTimeout: probeTimeout,
	}
}

// Do sends a request over the preferred transport.
func (t *Transport) Do(req *http.Request) (*http.Response, error) {
	if t.useQUIC(req.Context()) {
		resp, err := t.h3.Do(req)
		if err != nil {
			// The request may have reached the peer. Do not retry it here;
			// remember that the next request should use HTTPS.
			supportCache.Store(t.healthURL, supportEntry{until: time.Now().Add(unsupportedTTL)})
			return nil, err
		}
		return resp, nil
	}
	return t.https.Do(req)
}

// Probe reports whether the endpoint answers its health check over the
// transport that Do will use. It never retries a failed health check on the
// other transport in the same call. A fresh successful HTTP/3 probe is the
// health check; cached support still gets a cheap confirmation request.
func (t *Transport) Probe(ctx context.Context) bool {
	if t == nil {
		return false
	}
	supported, fresh := t.selectQUIC(ctx)
	if !supported {
		return t.healthyHTTPS(ctx)
	}
	if fresh {
		return true
	}
	return t.healthyQUIC(ctx)
}

func (t *Transport) useQUIC(ctx context.Context) bool {
	supported, _ := t.selectQUIC(ctx)
	return supported
}

// selectQUIC reports whether Do should use HTTP/3, and whether that answer
// came from a probe made in this call rather than the cache.
func (t *Transport) selectQUIC(ctx context.Context) (supported, fresh bool) {
	if value, ok := supportCache.Load(t.healthURL); ok {
		entry := value.(supportEntry)
		if time.Now().Before(entry.until) {
			return entry.supported, false
		}
	}
	supported = t.healthyQUIC(ctx)
	ttl := unsupportedTTL
	if supported {
		ttl = supportedTTL
	}
	supportCache.Store(t.healthURL, supportEntry{supported: supported, until: time.Now().Add(ttl)})
	return supported, true
}

func (t *Transport) healthyQUIC(ctx context.Context) bool {
	if t == nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, t.probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, t.healthURL, nil)
	if err != nil {
		return false
	}
	resp, err := t.h3.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return resp.StatusCode == http.StatusOK
}

func (t *Transport) healthyHTTPS(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, t.probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, t.healthURL, nil)
	if err != nil {
		return false
	}
	resp, err := t.https.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return resp.StatusCode == http.StatusOK
}

// Close releases idle HTTPS connections and the HTTP/3 transport.
func (t *Transport) Close() {
	if t == nil {
		return
	}
	if t.h3Transport != nil {
		_ = t.h3Transport.Close()
	}
	if t.https != nil {
		t.https.CloseIdleConnections()
	}
}

// ForgetSupport drops the cached probe result for a health URL. Tests and
// configuration changes use it; ordinary callers should let the TTL expire.
func ForgetSupport(healthURL string) {
	supportCache.Delete(healthURL)
}

// ErrNoTransport reports a transport with no clients.
var ErrNoTransport = errors.New("quicclient: no transport")
