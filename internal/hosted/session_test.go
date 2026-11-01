package hosted_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/account"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/requestauth"
)

func registerTestDevice(t *testing.T, h *hostHarness) *hosted.RegistrationResult {
	t.Helper()
	registered, err := h.client.Register(context.Background(), hosted.RegisterOptions{
		Name: "Aiden", Email: "aiden@example.com",
		Password: "correct horse battery staple", DeviceName: "Laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	return registered
}

func signedDeviceSessionRequest(t *testing.T, h *hostHarness, accountID, deviceID string,
	headerAccount, headerDevice string, key ed25519.PrivateKey) *http.Response {
	t.Helper()
	body, err := json.Marshal(hosted.DeviceSessionRequest{
		Protocol: hosted.Protocol, AccountID: accountID, DeviceID: deviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, h.server.URL+hosted.DeviceSessionPath, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(requestauth.HeaderAccount, headerAccount)
	req.Header.Set(requestauth.HeaderDevice, headerDevice)
	if err := requestauth.Sign(req, body, key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestDeviceSessionRejectsBadSignature(t *testing.T) {
	h := newHostHarness(t)
	registered := registerTestDevice(t, h)

	_, wrongKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	resp := signedDeviceSessionRequest(t, h, registered.AccountID, registered.Device.ID,
		registered.AccountID, registered.Device.ID, wrongKey)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	var body hosted.Error
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "unauthorized" {
		t.Fatalf("error = %+v", body)
	}
}

func TestDeviceSessionRejectsIdentityMismatch(t *testing.T) {
	h := newHostHarness(t)
	registered := registerTestDevice(t, h)

	// The signed body names one device while the identity headers name another.
	resp := signedDeviceSessionRequest(t, h, registered.AccountID, registered.Device.ID,
		registered.AccountID, "some-other-device", registered.Device.PrivateKey)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	var body hosted.Error
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "forbidden" {
		t.Fatalf("error = %+v", body)
	}
}

func TestDeviceSessionRejectsRevokedDevice(t *testing.T) {
	ctx := context.Background()
	h := newHostHarness(t)
	registered := registerTestDevice(t, h)

	acct, err := h.store.GetAccount(ctx, registered.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := h.store.GetDevice(ctx, registered.AccountID, registered.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	revokedAt := time.Now().UTC().Truncate(time.Millisecond)
	proof, err := identity.SignAccountRevocation(registered.AccountID, registered.Device.ID, revokedAt, registered.AccountPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	revoked := *record
	revoked.Revocation = proof
	revoked.RevokedAt = &revokedAt
	summary, err := h.store.SyncDevices(ctx, acct, []account.DeviceRecord{revoked})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Revoked != 1 {
		t.Fatalf("revocation summary = %+v", summary)
	}

	_, _, err = h.client.DeviceSession(ctx, registered.AccountID, registered.Device.ID, registered.Device.PrivateKey)
	if !errors.Is(err, hosted.ErrSessionInvalid) {
		t.Fatalf("device session error = %v, want session invalid", err)
	}
}
