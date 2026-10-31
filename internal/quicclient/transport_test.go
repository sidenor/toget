package quicclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

type testPair struct {
	endpoint    string
	fingerprint string
	certificate tls.Certificate
	https       *http.Server
	tcp         net.Listener
	h3          *http3.Server
	udp         net.PacketConn
	httpsHits   atomic.Int64
	h3Hits      atomic.Int64
}

func newTestPair(t *testing.T) *testPair {
	t.Helper()
	certificate := selfSigned(t, "127.0.0.1")

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	addr := tcp.Addr().String()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split address: %v", err)
	}
	udp, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		_ = tcp.Close()
		t.Fatalf("listen udp: %v", err)
	}

	pair := &testPair{
		endpoint: "https://" + addr, certificate: certificate, tcp: tcp, udp: udp,
	}
	fingerprint := sha256.Sum256(certificate.Certificate[0])
	pair.fingerprint = hex.EncodeToString(fingerprint[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		pair.httpsHits.Add(1)
		_, _ = io.WriteString(w, "https")
	})
	pair.https = &http.Server{Handler: mux}
	tlsListener := tls.NewListener(tcp, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
	})
	go func() { _ = pair.https.Serve(tlsListener) }()

	h3Mux := http.NewServeMux()
	h3Mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	h3Mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		pair.h3Hits.Add(1)
		_, _ = io.WriteString(w, "h3")
	})
	pair.h3 = &http3.Server{
		Handler:   h3Mux,
		TLSConfig: http3.ConfigureTLSConfig(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}),
	}
	go func() { _ = pair.h3.Serve(udp) }()

	t.Cleanup(func() {
		_ = pair.h3.Close()
		_ = pair.https.Close()
	})
	return pair
}

func (p *testPair) pinnedConfig(t *testing.T) *tls.Config {
	t.Helper()
	want := strings.ToLower(p.fingerprint)
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			sum := sha256.Sum256(rawCerts[0])
			if hex.EncodeToString(sum[:]) != want {
				t.Fatalf("certificate fingerprint mismatch")
			}
			return nil
		},
	}
}

func (p *testPair) httpsClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: true,
		TLSClientConfig:   p.pinnedConfig(t),
		DialContext:       (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func get(t *testing.T, transport *Transport, path string) (string, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(transport.healthURL, "/health")+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := transport.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return string(data), nil
}

func TestProbeSelectsHTTP3(t *testing.T) {
	pair := newTestPair(t)
	transport := New(pair.endpoint, "/health", pair.pinnedConfig(t), pair.httpsClient(t), 5*time.Second, time.Second)
	defer transport.Close()
	ForgetSupport(transport.healthURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !transport.Probe(ctx) {
		t.Fatal("probe did not report HTTP/3")
	}
	body, err := get(t, transport, "/echo")
	if err != nil {
		t.Fatalf("request over HTTP/3: %v", err)
	}
	if body != "h3" {
		t.Fatalf("body = %q, want h3", body)
	}
	if got := pair.httpsHits.Load(); got != 0 {
		t.Fatalf("HTTPS saw %d requests, want 0", got)
	}
	if got := pair.h3Hits.Load(); got != 1 {
		t.Fatalf("HTTP/3 saw %d requests, want 1", got)
	}
}

func TestFallsBackToHTTPSWhenQUICUnavailable(t *testing.T) {
	pair := newTestPair(t)
	// Close the HTTP/3 listener while keeping HTTPS up.
	if err := pair.h3.Close(); err != nil {
		t.Fatalf("close http3: %v", err)
	}
	transport := New(pair.endpoint, "/health", pair.pinnedConfig(t), pair.httpsClient(t), 5*time.Second, 400*time.Millisecond)
	defer transport.Close()
	ForgetSupport(transport.healthURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !transport.Probe(ctx) {
		t.Fatal("HTTPS health check failed")
	}
	body, err := get(t, transport, "/echo")
	if err != nil {
		t.Fatalf("request over HTTPS: %v", err)
	}
	if body != "https" {
		t.Fatalf("body = %q, want https", body)
	}
	if got := pair.h3Hits.Load(); got != 0 {
		t.Fatalf("HTTP/3 saw %d requests, want 0", got)
	}
	if got := pair.httpsHits.Load(); got != 1 {
		t.Fatalf("HTTPS saw %d requests, want 1", got)
	}
}

func TestHTTP3FailureIsNotRetried(t *testing.T) {
	pair := newTestPair(t)
	transport := New(pair.endpoint, "/health", pair.pinnedConfig(t), pair.httpsClient(t), 2*time.Second, time.Second)
	defer transport.Close()
	ForgetSupport(transport.healthURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !transport.Probe(ctx) {
		t.Fatal("probe did not report HTTP/3")
	}
	if err := pair.h3.Close(); err != nil {
		t.Fatalf("close http3: %v", err)
	}

	if _, err := get(t, transport, "/echo"); err == nil {
		t.Fatal("request over a closed HTTP/3 listener succeeded")
	}
	if got := pair.httpsHits.Load(); got != 0 {
		t.Fatalf("failed request was retried on HTTPS (%d hits)", got)
	}

	// The failed attempt marks HTTP/3 unsupported, so the next request uses
	// HTTPS without retrying the ambiguous one.
	body, err := get(t, transport, "/echo")
	if err != nil {
		t.Fatalf("fallback request: %v", err)
	}
	if body != "https" {
		t.Fatalf("body = %q, want https", body)
	}
	if got := pair.httpsHits.Load(); got != 1 {
		t.Fatalf("HTTPS saw %d requests, want 1", got)
	}
}

func TestForgetSupportReprobes(t *testing.T) {
	pair := newTestPair(t)
	transport := New(pair.endpoint, "/health", pair.pinnedConfig(t), pair.httpsClient(t), 5*time.Second, time.Second)
	defer transport.Close()
	ForgetSupport(transport.healthURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !transport.Probe(ctx) {
		t.Fatal("probe did not report HTTP/3")
	}
	ForgetSupport(transport.healthURL)
	if !transport.Probe(ctx) {
		t.Fatal("second probe after ForgetSupport failed")
	}
	if _, err := get(t, transport, "/echo"); err != nil {
		t.Fatalf("request after reprobe: %v", err)
	}
}

func selfSigned(t *testing.T, host string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP(host)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}
	return pair
}
