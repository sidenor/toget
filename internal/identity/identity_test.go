package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewServerManifestVerifies(t *testing.T) {
	server, err := NewServer([]Endpoint{{Transport: "quic", Addr: "127.0.0.1:7717"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(server.Code) != 8 {
		t.Fatalf("server code %q is not 8 digits", server.Code)
	}
	if !strings.HasPrefix(server.ID, ServerPrefix) {
		t.Fatalf("server ID %q lacks prefix", server.ID)
	}

	manifest, err := VerifyManifestJSON(server.ManifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ServerID != server.ID {
		t.Fatalf("server ID mismatch: %q != %q", manifest.ServerID, server.ID)
	}
	if manifest.ServerCode != server.Code {
		t.Fatalf("server code mismatch: %q != %q", manifest.ServerCode, server.Code)
	}
	if len(manifest.Endpoints) != 1 || manifest.Endpoints[0].Addr != "127.0.0.1:7717" {
		t.Fatalf("endpoints were not preserved: %#v", manifest.Endpoints)
	}
}

func TestExpiredManifestIsRejected(t *testing.T) {
	server, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	expired := server.Manifest
	expired.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	data, err := SignManifest(expired, server.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyManifestJSON(data); err == nil {
		t.Fatal("expired manifest was accepted")
	}

	future := server.Manifest
	future.IssuedAt = time.Now().UTC().Add(time.Hour)
	future.ExpiresAt = time.Now().UTC().AddDate(1, 0, 0)
	data, err = SignManifest(future, server.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyManifestJSON(data); err == nil {
		t.Fatal("future-issued manifest was accepted")
	}
}

func TestManifestTamperIsRejected(t *testing.T) {
	server, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(server.ManifestJSON), `"server_code":"`+server.Code+`"`, `"server_code":"00000000"`, 1)
	if tampered == string(server.ManifestJSON) {
		t.Fatal("test did not modify the manifest")
	}
	if _, err := VerifyManifestJSON([]byte(tampered)); err == nil {
		t.Fatal("tampered manifest was accepted")
	}
}

func TestServerCodeIsStableAndKeyBound(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	nonce := []byte("0123456789abcdef")
	first := ServerCode(pub, nonce)
	second := ServerCode(pub, nonce)
	if first != second {
		t.Fatalf("server code is not deterministic: %q != %q", first, second)
	}
	if len(first) != 8 {
		t.Fatalf("server code %q is not 8 digits", first)
	}
	if got := ServerCode(pub, []byte("different-nonce")); got == first {
		t.Fatal("different nonce produced the same code")
	}
}

func TestDeviceCertificate(t *testing.T) {
	accountPub, accountPriv, err := NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	devicePub, _, err := NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptionPub, _, err := NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	data, err := SignDeviceCertificate("device-1", "12345678123456", devicePub, encryptionPub, accountPriv, now)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := VerifyDeviceCertificate(data, accountPub)
	if err != nil {
		t.Fatal(err)
	}
	if cert.DeviceID != "device-1" || cert.AccountID != "12345678123456" {
		t.Fatalf("unexpected certificate: %#v", cert)
	}
	otherPub, _, err := NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDeviceCertificate(data, otherPub); err == nil {
		t.Fatal("certificate verified with the wrong account key")
	}

	expired, err := SignDeviceCertificate("device-1", "12345678123456", devicePub, encryptionPub, accountPriv, now.AddDate(-2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDeviceCertificate(expired, accountPub); err == nil {
		t.Fatal("expired device certificate was accepted")
	}
}

func TestAccountID(t *testing.T) {
	if got := AccountID("12345678", "901234"); got != "12345678901234" {
		t.Fatalf("AccountID = %q", got)
	}
	if len(AccountID("12345678", "901234")) != 14 {
		t.Fatal("account ID is not 14 digits")
	}
}

func TestAccountRevocationSignAndVerify(t *testing.T) {
	accountPub, accountPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	revokedAt := time.Now().UTC().Truncate(time.Millisecond)
	encoded, err := SignAccountRevocation("12345678901234", "device-1", revokedAt, accountPriv)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := VerifyAccountRevocation(encoded, accountPub)
	if err != nil {
		t.Fatal(err)
	}
	if proof.AccountID != "12345678901234" || proof.DeviceID != "device-1" || !proof.RevokedAt.Equal(revokedAt) {
		t.Fatalf("unexpected revocation: %#v", proof)
	}
	if _, err := VerifyAccountRevocation(encoded, ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))); err == nil {
		t.Fatal("revocation verified with the wrong account key")
	}
	data, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var statement AccountRevocation
	if err := json.Unmarshal(data, &statement); err != nil {
		t.Fatal(err)
	}
	statement.DeviceID = "device-2"
	tampered, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAccountRevocation(base64.RawStdEncoding.EncodeToString(tampered), accountPub); err == nil {
		t.Fatal("tampered revocation was accepted")
	}
	if _, err := VerifyAccountRevocation("not-base64", accountPub); err == nil {
		t.Fatal("malformed revocation was accepted")
	}
}

func TestDeviceCertificateSignatureAllowsExpiredCertificate(t *testing.T) {
	accountPub, accountPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	devicePub, _, err := NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptionPub, _, err := NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := SignDeviceCertificate("device-1", "account-1", devicePub, encryptionPub, accountPriv, time.Now().AddDate(-2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDeviceCertificate(cert, accountPub); err == nil {
		t.Fatal("expired certificate was accepted")
	}
	if _, err := VerifyDeviceCertificateSignature(cert, accountPub); err != nil {
		t.Fatalf("signature-only verification rejected an expired certificate: %v", err)
	}
}
