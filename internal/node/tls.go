package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/relayproto"
)

// TLSMaterial is a loaded node certificate and its pinning fingerprint.
type TLSMaterial struct {
	Certificate tls.Certificate
	Fingerprint string
}

// Fingerprint returns the lowercase SHA-256 hex fingerprint of a certificate.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// ParseFingerprint validates a SHA-256 certificate fingerprint.
func ParseFingerprint(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimPrefix(raw, "sha256:")
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("invalid TLS certificate fingerprint")
	}
	return hex.EncodeToString(decoded), nil
}

// NodeURL appends a certificate fingerprint fragment to an HTTPS endpoint.
func NodeURL(endpoint, fingerprint string) string {
	return strings.TrimSuffix(endpoint, "/") + "#fp=" + fingerprint
}

// ParsePinnedURL returns the HTTPS endpoint and pinned fingerprint from a
// room home address or invite URL.
func ParsePinnedURL(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", "", errors.New("node address must be an HTTPS URL")
	}
	fragment := parsed.Fragment
	values, _ := url.ParseQuery(fragment)
	fingerprint := values.Get("fp")
	if fingerprint == "" {
		fingerprint = values.Get("sha256")
	}
	if fingerprint == "" {
		return "", "", errors.New("node address is missing a certificate fingerprint")
	}
	fingerprint, err = ParseFingerprint(fingerprint)
	if err != nil {
		return "", "", err
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String(), fingerprint, nil
}

// ValidateNodeURL checks a pinned direct node address and its optional relay
// route. An empty address is allowed for older clients that do not know their
// externally reachable address yet.
func ValidateNodeURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !account.ValidNodeURL(raw) {
		return errors.New("node address must be an HTTPS URL")
	}
	direct, relay := relayproto.SplitAddress(raw)
	if _, _, err := ParsePinnedURL(direct); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("node address is invalid")
	}
	if strings.Contains(parsed.Fragment, "relay=") && relay == "" {
		return errors.New("node relay route is invalid")
	}
	if relay != "" {
		if _, _, _, err := relayproto.ParseNodeRoute(relay); err != nil {
			return err
		}
	}
	return nil
}

// PinnedTLSConfig trusts exactly one self-signed certificate.
func PinnedTLSConfig(fingerprint string) (*tls.Config, error) {
	want, err := ParseFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // Replaced by exact certificate pinning below.
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("node presented no certificate")
			}
			sum := sha256.Sum256(rawCerts[0])
			if !strings.EqualFold(hex.EncodeToString(sum[:]), want) {
				return errors.New("node certificate does not match the pinned fingerprint")
			}
			return nil
		},
	}, nil
}

// LoadOrCreateTLS loads the node's persistent self-signed certificate or
// generates one. The private key is written owner-only.
func LoadOrCreateTLS(certPath, keyPath string, hosts []string) (*TLSMaterial, error) {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return nil, fmt.Errorf("create TLS directory: %w", err)
	}
	certExists := fileExists(certPath)
	keyExists := fileExists(keyPath)
	if certExists != keyExists {
		return nil, errors.New("node TLS certificate and key are inconsistent")
	}
	if certExists {
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load node TLS key pair: %w", err)
		}
		return materialFromPair(&pair)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate node TLS key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Toget Node", Organization: []string{"Toget"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	seen := map[string]bool{}
	addHost := func(host string) {
		host = strings.Trim(host, "[]")
		if host == "" || seen[host] {
			return
		}
		seen[host] = true
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	addHost("127.0.0.1")
	addHost("::1")
	for _, host := range hosts {
		addHost(host)
	}
	ifaces, _ := net.Interfaces()
	for i := range ifaces {
		addresses, _ := ifaces[i].Addrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.IsGlobalUnicast() {
				addHost(ip.String())
			}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("create node certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("encode node TLS key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writeExclusive(keyPath, keyPEM); err != nil {
		return nil, fmt.Errorf("write node TLS key: %w", err)
	}
	if err := writeExclusive(certPath, certPEM); err != nil {
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("write node TLS certificate: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load generated node certificate: %w", err)
	}
	return materialFromPair(&pair)
}

func materialFromPair(pair *tls.Certificate) (*TLSMaterial, error) {
	if len(pair.Certificate) == 0 {
		return nil, errors.New("node TLS certificate is empty")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse node TLS certificate: %w", err)
	}
	pair.Leaf = cert
	if time.Now().UTC().After(cert.NotAfter) {
		return nil, errors.New("node TLS certificate has expired")
	}
	return &TLSMaterial{Certificate: *pair, Fingerprint: Fingerprint(cert)}, nil
}

func writeExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// AdvertiseHostPort chooses the host:port peers should dial. When the bind
// address is wildcard, it selects the local address of the default route.
func AdvertiseHostPort(listen, advertise string) (string, error) {
	if advertise != "" {
		if parsed, err := url.Parse(advertise); err == nil && parsed.Host != "" {
			return parsed.Host, nil
		}
		if _, _, err := net.SplitHostPort(advertise); err == nil {
			return advertise, nil
		}
		return "", fmt.Errorf("invalid advertise address %q", advertise)
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("parse node listen address: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = OutboundIP()
	}
	if host == "" {
		return "", errors.New("could not determine a LAN address to advertise")
	}
	return net.JoinHostPort(host, port), nil
}

// OutboundIP returns the local address used for the default route without
// sending any packets.
func OutboundIP() string {
	conn, err := net.Dial("udp", "192.0.2.1:80")
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil && !addr.IP.IsLoopback() {
			return addr.IP.String()
		}
	}
	ifaces, _ := net.Interfaces()
	for i := range ifaces {
		if ifaces[i].Flags&net.FlagUp == 0 || ifaces[i].Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := ifaces[i].Addrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
				return ip.String()
			}
		}
	}
	return "127.0.0.1"
}
