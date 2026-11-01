package hosted_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
)

func newProxyTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Test TLS proxy"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}},
	}
	server.StartTLS()
	return server
}

func (h *hostHarness) certificatePool() *x509.CertPool {
	roots := x509.NewCertPool()
	roots.AddCert(h.server.Certificate())
	return roots
}

func (h *hostHarness) identityURL() string {
	return hosted.IdentityURL("https://"+strings.TrimPrefix(h.server.URL, "https://"), h.hostID)
}

func (h *hostHarness) identityClient(t *testing.T) *hosted.Client {
	t.Helper()
	client, err := hosted.NewClient(h.identityURL(), hosted.WithRootCAs(h.certificatePool()))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestHostedIdentityPinLifecycle(t *testing.T) {
	ctx := context.Background()
	h := newHostHarness(t)
	client := h.identityClient(t)
	if client.HostID() != h.hostID || client.Fingerprint() != "" {
		t.Fatalf("identity client = id %q, fp %q", client.HostID(), client.Fingerprint())
	}
	if client.HostURL() != h.identityURL() {
		t.Fatalf("host URL = %q", client.HostURL())
	}
	if _, _, err := hosted.SplitPinnedURL(h.identityURL()); err == nil {
		t.Fatal("SplitPinnedURL accepted an identity-pin address")
	}
	info, err := client.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.HostID != h.hostID {
		t.Fatalf("host info id = %q", info.HostID)
	}
	const password = "correct horse battery staple"
	registered, err := client.Register(ctx, hosted.RegisterOptions{
		Name: "Aiden", Email: "aiden@example.com", Password: password,
		DeviceName: "Laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.HostURL != h.identityURL() || registered.HostID != h.hostID {
		t.Fatalf("registration host = %q / %q", registered.HostURL, registered.HostID)
	}
	credential, _, err := hosted.ParseRecoveryCredential(registered.RecoveryCredentialRaw)
	if err != nil {
		t.Fatal(err)
	}
	if credential.HostURL != h.identityURL() {
		t.Fatalf("recovery host URL = %q", credential.HostURL)
	}
	loggedIn, err := client.Login(ctx, registered.AccountID, password)
	if err != nil {
		t.Fatal(err)
	}
	if loggedIn.HostURL != h.identityURL() || loggedIn.HostID != h.hostID {
		t.Fatalf("login host = %q / %q", loggedIn.HostURL, loggedIn.HostID)
	}
	if len(loggedIn.MasterKey) == 0 || len(loggedIn.AccountPrivateKey) == 0 {
		t.Fatal("identity-pin login did not return account material")
	}

	// A TLS-terminating proxy can present a valid public certificate, but a
	// different host identity must still be rejected before account material
	// is sent.
	other, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	wrongOrigin := "https://" + strings.TrimPrefix(h.server.URL, "https://")
	wrongURL := hosted.IdentityURL(wrongOrigin, other.ID)
	wrong, err := hosted.NewClient(wrongURL, hosted.WithRootCAs(h.certificatePool()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Info(ctx); !errors.Is(err, hosted.ErrInvalidHost) {
		t.Fatalf("wrong host info error = %v", err)
	}
	if err := wrong.RequestRecovery(ctx, "aiden@example.com"); !errors.Is(err, hosted.ErrInvalidHost) {
		t.Fatalf("wrong host request error = %v", err)
	}
}

func TestHostedIdentityPinThroughTLSProxy(t *testing.T) {
	ctx := context.Background()
	h := newHostHarness(t)
	target, err := url.Parse(h.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		// The test proxy terminates TLS for clients and talks to the
		// self-signed test host; production uses a Cloudflare Origin CA or
		// cloudflared noTLSVerify.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
	proxyServer := newProxyTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cloudflare overwrites both headers at its edge.
		r.Header.Set("CF-Connecting-IP", "203.0.113.7")
		r.Header.Set("CF-Ray", "test-ray")
		proxy.ServeHTTP(w, r)
	}))
	defer proxyServer.Close()
	proxyOrigin := "https://" + strings.TrimPrefix(proxyServer.URL, "https://")
	proxyURL := hosted.IdentityURL(proxyOrigin, h.hostID)
	roots := x509.NewCertPool()
	roots.AddCert(proxyServer.Certificate())
	client, err := hosted.NewClient(proxyURL, hosted.WithRootCAs(roots))
	if err != nil {
		t.Fatal(err)
	}
	const password = "correct horse battery staple"
	registered, err := client.Register(ctx, hosted.RegisterOptions{
		Name: "Aiden", Email: "aiden@example.com", Password: password,
		DeviceName: "Laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.HostURL != proxyURL || registered.HostID != h.hostID {
		t.Fatalf("proxy registration host = %q / %q", registered.HostURL, registered.HostID)
	}
	credential, _, err := hosted.ParseRecoveryCredential(registered.RecoveryCredentialRaw)
	if err != nil {
		t.Fatal(err)
	}
	if credential.HostURL != proxyURL {
		t.Fatalf("proxy recovery host URL = %q", credential.HostURL)
	}
	if _, err := client.Login(ctx, registered.AccountID, password); err != nil {
		t.Fatalf("login through proxy: %v", err)
	}

	// The exact origin certificate cannot be pinned through the proxy: the
	// edge certificate is the Cloudflare one, not the origin certificate.
	fingerprintURL := "https://" + strings.TrimPrefix(proxyServer.URL, "https://") +
		"#fp=" + hosted.Fingerprint(h.server.Certificate())
	fingerprinted, err := hosted.NewClient(fingerprintURL, hosted.WithRootCAs(roots))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fingerprinted.Info(ctx); !errors.Is(err, hosted.ErrHostUnavailable) {
		t.Fatalf("origin fingerprint through proxy error = %v", err)
	}
}
