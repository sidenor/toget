package hosted_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/api"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/hostedlink"
	"github.com/sidenor/toget/internal/room"
	"github.com/sidenor/toget/internal/session"
	"github.com/sidenor/toget/internal/version"
)

const hostedAPIToken = "hosted-api-token-0123456789-abcdef"

type hostedAPI struct {
	server   *httptest.Server
	token    string
	lastBody string
}

func newHostedAPI(t *testing.T, stack *localStack, sessions *hosted.SessionCache) *hostedAPI {
	t.Helper()
	handler := api.New(api.Options{
		Accounts: stack.accounts,
		Hosted:   hostedlink.New(stack.accounts, sessions),
		Rooms:    room.New(stack.db, stack.vault),
		Session:  session.New(session.DefaultTTL),
		Version:  version.Current(),
		Token:    hostedAPIToken,
		Listen:   "127.0.0.1:7718",
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &hostedAPI{server: server, token: hostedAPIToken}
}

func (a *hostedAPI) do(t *testing.T, method, path string, request, response any) int {
	t.Helper()
	var body io.Reader
	if request != nil {
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	a.lastBody = string(data)
	if response != nil && len(data) > 0 {
		if err := json.Unmarshal(data, response); err != nil {
			t.Fatalf("%s %s: decode response: %v (%s)", method, path, err, data)
		}
	}
	return resp.StatusCode
}

func TestHostedAccountAPI(t *testing.T) {
	ctx := context.Background()
	h := newHostHarness(t)
	const password = "correct horse battery staple"
	const newPassword = "a different password entirely"

	firstStack := newLocalStack(t)
	firstAPI := newHostedAPI(t, firstStack, hosted.NewSessionCache())
	var created struct {
		api.AccountView
		Recovery *api.RecoveryCredentialView `json:"recovery"`
	}
	status := firstAPI.do(t, http.MethodPost, "/api/v1/account", map[string]string{
		"name": "Aiden", "password": password, "device_name": "Laptop",
		"host_url": h.client.PinnedURL(), "email": "aiden@example.com",
	}, &created)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	if created.HostURL == "" || created.ID == "" {
		t.Fatalf("created account = %+v", created)
	}
	if created.Recovery == nil || created.Recovery.Content == "" {
		t.Fatal("hosted create did not return a recovery file")
	}
	if created.Recovery.Filename != "toget-account-recovery-"+created.ID+".togetkey" {
		t.Fatalf("recovery filename = %q", created.Recovery.Filename)
	}

	// A second node adopts the account through the public API.
	secondStack := newLocalStack(t)
	secondAPI := newHostedAPI(t, secondStack, hosted.NewSessionCache())
	var loggedIn struct {
		api.AccountView
		Warnings []string `json:"warnings"`
	}
	status = secondAPI.do(t, http.MethodPost, "/api/v1/account/hosted/login", map[string]string{
		"host_url": h.client.PinnedURL(), "account_id": created.ID,
		"password": password, "device_name": "Desktop",
	}, &loggedIn)
	if status != http.StatusCreated {
		t.Fatalf("hosted login status = %d", status)
	}
	if loggedIn.ID != created.ID || loggedIn.Device == nil || loggedIn.Device.Name != "Desktop" {
		t.Fatalf("logged in account = %+v", loggedIn)
	}

	// A hosted rotation requires the account password so the host can unwrap
	// and rewrap the account key. The first device has no local password at
	// this point, so it must reject an unauthenticated request before touching
	// the account.
	status = secondAPI.do(t, http.MethodPost, "/api/v1/account/rotate-key", map[string]string{}, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("rotate without password status = %d", status)
	}
	var rotatedKey struct {
		Generation int `json:"generation"`
	}
	status = secondAPI.do(t, http.MethodPost, "/api/v1/account/rotate-key", map[string]string{
		"password": password,
	}, &rotatedKey)
	if status != http.StatusOK || rotatedKey.Generation != 2 {
		t.Fatalf("rotate-key status = %d generation = %d", status, rotatedKey.Generation)
	}

	// The first device was offline for the rotation. Unlocking it with the
	// account password refreshes the host session in the background and
	// installs the newer signing key without a second prompt.
	status = firstAPI.do(t, http.MethodDelete, "/api/v1/session", nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("lock status = %d", status)
	}
	status = firstAPI.do(t, http.MethodPost, "/api/v1/session", map[string]string{"password": password}, nil)
	if status != http.StatusOK {
		t.Fatalf("unlock status = %d: %s", status, firstAPI.lastBody)
	}
	deadline := time.Now().Add(10 * time.Second)
	generation := 0
	for {
		snapshot, err := firstStack.accounts.Get(ctx)
		if err == nil {
			generation = snapshot.Account.KeyGeneration
			if generation == 2 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("first device did not install the rotated key: generation=%d err=%v", generation, err)
		}
		time.Sleep(25 * time.Millisecond)
	}

	status = secondAPI.do(t, http.MethodPost, "/api/v1/account/change-password", map[string]string{
		"current_password": password, "new_password": newPassword,
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("change password status = %d", status)
	}
	if _, err := h.client.Login(ctx, created.ID, password); err == nil {
		t.Fatal("old host password still works")
	}
	if _, err := h.client.Login(ctx, created.ID, newPassword); err != nil {
		t.Fatalf("new host password does not work: %v", err)
	}
	secondStack.accounts.LockVault()
	if _, err := secondStack.accounts.Login(ctx, newPassword); err != nil {
		t.Fatalf("new password does not unlock the second device: %v", err)
	}

	var rotated api.RecoveryCredentialView
	status = secondAPI.do(t, http.MethodPost, "/api/v1/account/recovery", map[string]any{
		"official": true, "password": newPassword,
	}, &rotated)
	if status != http.StatusCreated {
		t.Fatalf("official recovery status = %d", status)
	}
	if rotated.Content == "" || rotated.Filename == "" {
		t.Fatalf("rotated recovery = %+v", rotated)
	}

	// A local account keeps the old response shape and has no recovery file.
	localStack := newLocalStack(t)
	localAPI := newHostedAPI(t, localStack, hosted.NewSessionCache())
	var localCreated map[string]any
	status = localAPI.do(t, http.MethodPost, "/api/v1/account", map[string]string{
		"name": "Local", "password": password, "device_name": "Local device",
	}, &localCreated)
	if status != http.StatusCreated {
		t.Fatalf("local create status = %d", status)
	}
	if _, ok := localCreated["recovery"]; ok {
		t.Fatal("local account unexpectedly returned an official recovery file")
	}
	if localCreated["host_url"] != nil {
		t.Fatalf("local account host_url = %v", localCreated["host_url"])
	}
	if _, ok := localCreated["name"]; !ok {
		t.Fatalf("local account response is not flattened: %v", localCreated)
	}
}
