package hosted_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/hosted"
)

const testAccount = "12345678000001"

func accountKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestSealedBlobRoundTripAndTamper(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	blob, err := hosted.SealBlob(key, []byte("secret"), "aad")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := hosted.OpenBlob(key, blob, "aad")
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("open = %q, %v", plaintext, err)
	}
	if _, err := hosted.OpenBlob(key, blob, "other"); err == nil {
		t.Fatal("opened with the wrong AAD")
	}
	tampered := blob
	tampered.Ciphertext = append([]byte(nil), blob.Ciphertext...)
	tampered.Ciphertext[0] ^= 1
	if _, err := hosted.OpenBlob(key, tampered, "aad"); err == nil {
		t.Fatal("opened a tampered blob")
	}
	wrongAlgorithm := blob
	wrongAlgorithm.Algorithm = "aes256gcm"
	if _, err := hosted.OpenBlob(key, wrongAlgorithm, "aad"); err == nil {
		t.Fatal("opened an unknown algorithm")
	}
}

func TestMasterKeyWraps(t *testing.T) {
	masterKey, err := hosted.NewMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	exportKey := bytes.Repeat([]byte{7}, 64)
	exportWrap, err := hosted.WrapExportKey(exportKey, masterKey, testAccount)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := hosted.OpenExportWrap(exportKey, exportWrap, testAccount)
	if err != nil || !bytes.Equal(opened, masterKey) {
		t.Fatalf("export wrap = %x, %v", opened, err)
	}
	if _, err := hosted.OpenExportWrap(exportKey, exportWrap, "12345678000002"); err == nil {
		t.Fatal("opened an export wrap for another account")
	}
	if _, err := hosted.OpenExportWrap(bytes.Repeat([]byte{8}, 64), exportWrap, testAccount); err == nil {
		t.Fatal("opened an export wrap with the wrong export key")
	}

	recoveryKey, err := hosted.NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	recoveryWrap, err := hosted.WrapRecoveryKey(recoveryKey, masterKey, testAccount)
	if err != nil {
		t.Fatal(err)
	}
	opened, err = hosted.OpenRecoveryWrap(recoveryKey, recoveryWrap, testAccount)
	if err != nil || !bytes.Equal(opened, masterKey) {
		t.Fatalf("recovery wrap = %x, %v", opened, err)
	}
	if _, err := hosted.OpenRecoveryWrap(make([]byte, 32), recoveryWrap, testAccount); err == nil {
		t.Fatal("opened a recovery wrap with the wrong key")
	}
}

func TestKeyBundle(t *testing.T) {
	masterKey, err := hosted.NewMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv := accountKey(t)
	now := time.Now().UTC().Truncate(time.Second)
	bundle, err := hosted.NewKeyBundle(testAccount, "Aiden", priv, 2, []string{"rotation"}, now)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := hosted.SealKeyBundle(masterKey, bundle)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := hosted.OpenKeyBundle(masterKey, sealed, testAccount, 2)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Name != "Aiden" || opened.KeyGeneration != 2 ||
		!bytes.Equal(opened.AccountPrivateKey, priv) ||
		!bytes.Equal(ed25519.PrivateKey(opened.AccountPrivateKey).Public().(ed25519.PublicKey), pub) {
		t.Fatalf("unexpected bundle: %+v", opened)
	}
	if _, err := hosted.OpenKeyBundle(masterKey, sealed, testAccount, 1); err == nil {
		t.Fatal("opened a bundle with the wrong generation")
	}
	if _, err := hosted.OpenKeyBundle(make([]byte, 32), sealed, testAccount, 2); err == nil {
		t.Fatal("opened a bundle with the wrong master key")
	}
}

func TestRecoveryCredential(t *testing.T) {
	const hostURL = "https://host.example:7722#fp=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	credential, key, err := hosted.NewRecoveryCredential(testAccount, hostURL, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(credential.Filename(), ".togetkey") {
		t.Fatalf("filename = %q", credential.Filename())
	}
	data, err := credential.Encode()
	if err != nil {
		t.Fatal(err)
	}
	parsed, parsedKey, err := hosted.ParseRecoveryCredential(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.AccountID != testAccount || parsed.HostURL != hostURL || !bytes.Equal(parsedKey, key) {
		t.Fatalf("parsed = %+v", parsed)
	}
	var changed hosted.RecoveryCredential
	if err := json.Unmarshal(data, &changed); err != nil {
		t.Fatal(err)
	}
	// Pick a replacement that actually differs; base64 often starts with "A".
	replacement := byte('A')
	if changed.Key[0] == replacement {
		replacement = 'B'
	}
	changed.Key = string(replacement) + changed.Key[1:]
	tampered, err := json.Marshal(&changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := hosted.ParseRecoveryCredential(tampered); err == nil {
		t.Fatal("accepted a tampered credential")
	}
	var unknown bytes.Buffer
	unknown.WriteString(strings.TrimSpace(string(data))[:len(bytes.TrimSpace(data))-1])
	unknown.WriteString(`,"extra":true}`)
	if _, _, err := hosted.ParseRecoveryCredential(unknown.Bytes()); err == nil {
		t.Fatal("accepted unknown fields")
	}
}

func TestRecoveryCredentialRejectsBadHost(t *testing.T) {
	cases := []string{
		"",
		"http://host.example:7722#fp=0123",
		"https://host.example:7722",
		"https://host.example:7722/path#fp=" + strings.Repeat("a", 64),
		"https://host.example:7722#relay=x&fp=" + strings.Repeat("a", 64),
	}
	for _, hostURL := range cases {
		if _, _, err := hosted.NewRecoveryCredential(testAccount, hostURL, time.Now()); err == nil {
			t.Fatalf("accepted host URL %q", hostURL)
		}
	}
}
