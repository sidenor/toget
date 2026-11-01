package hosted_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
)

type hostHarness struct {
	store   *hosted.Store
	mailer  *hosted.MemoryMailer
	service *hosted.Service
	server  *httptest.Server
	client  *hosted.Client
	hostID  string
	dir     string
}

func newHostHarness(t *testing.T) *hostHarness {
	t.Helper()
	dir := t.TempDir()
	store, err := hosted.OpenStore(filepath.Join(dir, "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsureHost(context.Background(), "Test Host", []identity.Endpoint{
		{Transport: "https", Addr: "127.0.0.1:7722"},
	}); err != nil {
		t.Fatal(err)
	}
	mailer := &hosted.MemoryMailer{}
	service, err := hosted.New(hosted.Options{
		Store: store, Mailer: mailer, PublicURL: "https://host.example",
		ResetFS: fstest.MapFS{"index.html": &fstest.MapFile{
			Data: []byte("<!doctype html><title>reset</title>"),
		}},
		DisableRateLimit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(service.Handler())
	t.Cleanup(server.Close)
	fingerprint := hosted.Fingerprint(server.Certificate())
	hostURL := "https://" + strings.TrimPrefix(server.URL, "https://") + "#fp=" + fingerprint
	client, err := hosted.NewClient(hostURL)
	if err != nil {
		t.Fatal(err)
	}
	return &hostHarness{
		store: store, mailer: mailer, service: service, server: server,
		client: client, hostID: service.HostID(), dir: dir,
	}
}

func tokenFromMessage(t *testing.T, message hosted.Message, key string) string {
	t.Helper()
	for _, field := range strings.Fields(message.Text) {
		if !strings.HasPrefix(field, "https://") {
			continue
		}
		parsed, err := url.Parse(field)
		if err != nil {
			continue
		}
		values, err := url.ParseQuery(parsed.Fragment)
		if err != nil {
			continue
		}
		if token := values.Get(key); token != "" {
			return token
		}
	}
	t.Fatalf("message has no %s token: %q", key, message.Text)
	return ""
}

func TestHostedAccountLifecycle(t *testing.T) {
	ctx := context.Background()
	h := newHostHarness(t)
	const password = "correct horse battery staple"
	const newPassword = "a different password entirely"
	const email = "Aiden@Example.com"

	registered, err := h.client.Register(ctx, hosted.RegisterOptions{
		Name: "Aiden", Email: email, Password: password, DeviceName: "Laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(registered.AccountID) != 14 || registered.AccountCode != registered.AccountID[8:] {
		t.Fatalf("account id = %q, code = %q", registered.AccountID, registered.AccountCode)
	}
	if registered.SessionToken == "" {
		t.Fatal("registration did not return a session")
	}
	if len(registered.RecoveryCredentialRaw) == 0 {
		t.Fatal("registration did not return a recovery credential")
	}
	if h.mailer.Len() != 1 {
		t.Fatalf("verification mails = %d", h.mailer.Len())
	}
	verifyMessage, _ := h.mailer.Last()
	if !strings.Contains(verifyMessage.To, "aiden@example.com") {
		t.Fatalf("verification recipient = %q", verifyMessage.To)
	}
	verifyToken := tokenFromMessage(t, verifyMessage, "verify")
	verified, err := h.client.VerifyEmail(ctx, verifyToken)
	if err != nil {
		t.Fatal(err)
	}
	if !verified.Verified || verified.Email != "aiden@example.com" {
		t.Fatalf("verification = %+v", verified)
	}

	loggedIn, err := h.client.Login(ctx, registered.AccountID, password)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loggedIn.MasterKey, registered.MasterKey) {
		t.Fatal("login recovered a different master key")
	}
	if !bytes.Equal(loggedIn.AccountPrivateKey, registered.AccountPrivateKey) {
		t.Fatal("login recovered a different account key")
	}
	if !loggedIn.EmailVerified {
		t.Fatal("login did not report the verified email")
	}
	if _, err := h.client.Login(ctx, registered.AccountID, "wrong password"); err == nil {
		t.Fatal("wrong password logged in")
	}

	// Sync a second device signed by the account key.
	now := time.Now().UTC().Truncate(time.Millisecond)
	devicePub, _, err := identity.NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	encPub, _, err := identity.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	deviceID := identity.NewID()
	cert, err := identity.SignDeviceCertificate(deviceID, registered.AccountID, devicePub, encPub,
		registered.AccountPrivateKey, now)
	if err != nil {
		t.Fatal(err)
	}
	syncResponse, err := h.client.Sync(ctx, loggedIn.SessionToken, hosted.SyncRequest{
		Devices: []account.DeviceRecord{{
			ID: deviceID, AccountID: registered.AccountID, Name: "Phone",
			PublicKey: devicePub, EncryptionPublicKey: encPub,
			Certificate: string(cert), Role: "node", CreatedAt: now,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if syncResponse.Added != 1 || len(syncResponse.Devices) != 2 {
		t.Fatalf("sync = %+v", syncResponse)
	}

	// Change the password and keep the same master key.
	if err := h.client.ChangePassword(ctx, loggedIn.SessionToken, registered.AccountID, newPassword, loggedIn.MasterKey); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Login(ctx, registered.AccountID, password); err == nil {
		t.Fatal("old password still works after a change")
	}
	changed, err := h.client.Login(ctx, registered.AccountID, newPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(changed.MasterKey, registered.MasterKey) {
		t.Fatal("password change lost the master key")
	}

	// Recover the password with the offline credential.
	if err := h.client.RequestRecovery(ctx, email); err != nil {
		t.Fatal(err)
	}
	resetMessage, ok := h.mailer.Last()
	if !ok || !strings.Contains(resetMessage.Subject, "重置") {
		t.Fatalf("reset mail = %+v", resetMessage)
	}
	resetToken := tokenFromMessage(t, resetMessage, "reset")
	inspection, err := h.client.InspectRecovery(ctx, resetToken, registered.RecoveryCredentialRaw)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.AccountID != registered.AccountID || inspection.Name != "Aiden" ||
		inspection.EmailHint == "" || inspection.KeyGeneration != 1 {
		t.Fatalf("inspection = %+v", inspection)
	}
	wrongCredential, _, err := hosted.NewRecoveryCredential(registered.AccountID, h.client.PinnedURL(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wrongRaw, err := wrongCredential.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.InspectRecovery(ctx, resetToken, wrongRaw); !errors.Is(err, hosted.ErrRecoveryMismatch) {
		t.Fatalf("wrong recovery file inspection error = %v", err)
	}
	reset, err := h.client.ResetPassword(ctx, resetToken, registered.RecoveryCredentialRaw, "reset password one")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reset.MasterKey, registered.MasterKey) ||
		!bytes.Equal(reset.AccountPrivateKey, registered.AccountPrivateKey) {
		t.Fatal("reset changed the account's secret material")
	}
	resetLogin, err := h.client.Login(ctx, registered.AccountID, "reset password one")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resetLogin.MasterKey, registered.MasterKey) {
		t.Fatal("login after reset lost the master key")
	}

	// The recovery credential was rotated: the old file must fail, the new
	// file must work.
	if err := h.client.RequestRecovery(ctx, registered.AccountID); err != nil {
		t.Fatal(err)
	}
	nextMessage, _ := h.mailer.Last()
	nextToken := tokenFromMessage(t, nextMessage, "reset")
	if _, err := h.client.ResetPassword(ctx, nextToken, registered.RecoveryCredentialRaw, "old file password"); err == nil {
		t.Fatal("the old recovery file still resets the account")
	}
	if err := h.client.RequestRecovery(ctx, registered.AccountID); err != nil {
		t.Fatal(err)
	}
	thirdMessage, _ := h.mailer.Last()
	thirdToken := tokenFromMessage(t, thirdMessage, "reset")
	if _, err := h.client.ResetPassword(ctx, thirdToken, reset.RecoveryCredentialRaw, "reset password two"); err != nil {
		t.Fatal(err)
	}

	// The request path must not reveal whether an account exists.
	if err := h.client.RequestRecovery(ctx, "nobody@example.com"); err != nil {
		t.Fatal(err)
	}

	recoveryKey, err := base64.RawURLEncoding.DecodeString(registered.RecoveryCredential.Key)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPlaintextOnDisk(t, h.dir, password, newPassword, "reset password one",
		registered.MasterKey, registered.AccountPrivateKey, recoveryKey)
}

func assertNoPlaintextOnDisk(t *testing.T, dir string, values ...any) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var blobs [][]byte
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		blobs = append(blobs, data)
	}
	for _, value := range values {
		var needle []byte
		switch typed := value.(type) {
		case string:
			needle = []byte(typed)
		case []byte:
			needle = typed
		case ed25519.PrivateKey:
			needle = typed
			// The seed is the first half; check both to catch either form.
			for _, part := range [][]byte{typed, typed.Seed()} {
				for _, blob := range blobs {
					if bytes.Contains(blob, part) {
						t.Fatalf("host disk contains plaintext account key material")
					}
				}
			}
			continue
		default:
			t.Fatalf("unsupported check value %T", value)
		}
		for _, blob := range blobs {
			if len(needle) > 0 && bytes.Contains(blob, needle) {
				t.Fatalf("host disk contains a plaintext secret (%d bytes)", len(needle))
			}
		}
	}
}
