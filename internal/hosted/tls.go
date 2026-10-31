package hosted

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
)

// TLSMaterial is a host certificate and its pinning fingerprint.
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

// PinnedURL appends a certificate fingerprint to an HTTPS origin.
func PinnedURL(endpoint, fingerprint string) string {
	return strings.TrimSuffix(endpoint, "/") + "#fp=" + fingerprint
}

// SplitPinnedURL returns the HTTPS origin and pinned fingerprint.
func SplitPinnedURL(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", "", errors.New("host address must be an HTTPS URL")
	}
	values, _ := url.ParseQuery(parsed.Fragment)
	fingerprint := values.Get("fp")
	if fingerprint == "" {
		fingerprint = values.Get("sha256")
	}
	fingerprint, err = ParseFingerprint(fingerprint)
	if err != nil {
		return "", "", err
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return strings.TrimSuffix(parsed.String(), "/"), fingerprint, nil
}

// PinnedTLSConfig trusts exactly one certificate fingerprint.
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
				return errors.New("host presented no certificate")
			}
			sum := sha256.Sum256(rawCerts[0])
			if !strings.EqualFold(hex.EncodeToString(sum[:]), want) {
				return errors.New("host certificate does not match the pinned fingerprint")
			}
			return nil
		},
	}, nil
}

// LoadOrCreateTLS loads a CA-issued certificate pair, or generates a
// persistent self-signed pair for private deployments. Browsers only trust the
// reset page over a real CA certificate; pinned Toget clients work with either.
func LoadOrCreateTLS(certPath, keyPath string, hosts []string) (*TLSMaterial, error) {
	if certPath == "" && keyPath == "" {
		return nil, errors.New("TLS certificate and key paths are required")
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return nil, fmt.Errorf("create TLS directory: %w", err)
	}
	certExists := fileExists(certPath)
	keyExists := fileExists(keyPath)
	if certExists != keyExists {
		return nil, errors.New("host TLS certificate and key are inconsistent")
	}
	if certExists {
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load host TLS key pair: %w", err)
		}
		return materialFromPair(&pair)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host TLS key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Toget Account Host", Organization: []string{"Toget"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
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
	addHost("localhost")
	addHost("127.0.0.1")
	addHost("::1")
	for _, host := range hosts {
		addHost(host)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, priv.Public(), priv)
	if err != nil {
		return nil, fmt.Errorf("create host certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("encode host TLS key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writeExclusive(keyPath, keyPEM); err != nil {
		return nil, fmt.Errorf("write host TLS key: %w", err)
	}
	if err := writeExclusive(certPath, certPEM); err != nil {
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("write host TLS certificate: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load generated host certificate: %w", err)
	}
	return materialFromPair(&pair)
}

func materialFromPair(pair *tls.Certificate) (*TLSMaterial, error) {
	if len(pair.Certificate) == 0 {
		return nil, errors.New("host TLS certificate is empty")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse host TLS certificate: %w", err)
	}
	pair.Leaf = cert
	if time.Now().UTC().After(cert.NotAfter) {
		return nil, errors.New("host TLS certificate has expired")
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
