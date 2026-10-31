package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/api"
)

func createAccountForTest(t *testing.T, node *testNode) {
	t.Helper()
	resp, body := node.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Aiden","password":"oldpass123","device_name":"test"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}
}

func createRecoveryForTest(t *testing.T, node *testNode) api.RecoveryCredentialView {
	t.Helper()
	resp, body := node.request(t, http.MethodPost, "/api/v1/account/recovery", testToken, `{}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create recovery = %d %s", resp.StatusCode, body)
	}
	var credential api.RecoveryCredentialView
	if err := json.Unmarshal(body, &credential); err != nil {
		t.Fatal(err)
	}
	if credential.Content == "" || !strings.HasSuffix(credential.Filename, ".togetkey") {
		t.Fatalf("credential = %#v", credential)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	return credential
}

func TestAccountRecoveryAPI(t *testing.T) {
	node := newTestNode(t, nil)
	createAccountForTest(t, node)

	resp, body := node.request(t, http.MethodGet, "/api/v1/account/recovery", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("recovery status = %d %s", resp.StatusCode, body)
	}
	var status api.RecoveryStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if status.Enabled {
		t.Fatal("recovery is enabled before it was created")
	}

	credential := createRecoveryForTest(t, node)
	resp, body = node.request(t, http.MethodGet, "/api/v1/account/recovery", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("recovery status = %d %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.CreatedAt == nil || status.DeviceID == "" {
		t.Fatalf("status = %#v", status)
	}

	// Lock the vault, then reset with the recovery file. The reset route stays
	// available while locked and unlocks the session on success.
	resp, body = node.request(t, http.MethodDelete, "/api/v1/session", testToken, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("lock = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/account/recovery", testToken, `{}`, nil)
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("create recovery while locked = %d %s", resp.StatusCode, body)
	}
	resetBody, _ := json.Marshal(map[string]string{
		"credential": credential.Content, "new_password": "newpass456",
	})
	resp, body = node.request(t, http.MethodPost, "/api/v1/account/reset-password", testToken, string(resetBody), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset password = %d %s", resp.StatusCode, body)
	}
	var session api.SessionView
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatal(err)
	}
	if session.Locked || !session.HasAccount {
		t.Fatalf("session after reset = %#v", session)
	}

	resp, _ = node.request(t, http.MethodDelete, "/api/v1/session", testToken, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("lock again = %d", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodPost, "/api/v1/session", testToken, `{"password":"oldpass123"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password unlock = %d", resp.StatusCode)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/session", testToken, `{"password":"newpass456"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new password unlock = %d %s", resp.StatusCode, body)
	}

	if resp, body = node.request(t, http.MethodDelete, "/api/v1/account/recovery", testToken, "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove recovery = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/account/reset-password", testToken, string(resetBody), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "recovery_not_configured") {
		t.Fatalf("reset after removal = %d %s", resp.StatusCode, body)
	}
}

func TestAccountRecoveryAPIRejectsOtherDeviceFile(t *testing.T) {
	node := newTestNode(t, nil)
	createAccountForTest(t, node)
	other := newTestNode(t, nil)
	createAccountForTest(t, other)
	credential := createRecoveryForTest(t, other)
	body, _ := json.Marshal(map[string]string{
		"credential": credential.Content, "new_password": "newpass456",
	})
	resp, data := node.request(t, http.MethodPost, "/api/v1/account/reset-password", testToken, string(body), nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "invalid_recovery_credential") {
		t.Fatalf("other device reset = %d %s", resp.StatusCode, data)
	}
}

func TestChangePasswordAPI(t *testing.T) {
	node := newTestNode(t, nil)
	createAccountForTest(t, node)
	createRecoveryForTest(t, node)

	resp, body := node.request(t, http.MethodPost, "/api/v1/account/change-password", testToken,
		`{"current_password":"wrongpass","new_password":"newpass456"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong current password = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/account/change-password", testToken,
		`{"current_password":"oldpass123","new_password":"short"}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("short new password = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/account/change-password", testToken,
		`{"current_password":"oldpass123","new_password":"newpass456"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change password = %d %s", resp.StatusCode, body)
	}
	if resp, _ = node.request(t, http.MethodDelete, "/api/v1/session", testToken, "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("lock = %d", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodPost, "/api/v1/session", testToken, `{"password":"oldpass123"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password unlock = %d", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodPost, "/api/v1/session", testToken, `{"password":"newpass456"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new password unlock = %d", resp.StatusCode)
	}
}

func TestLocalPasswordResetAPI(t *testing.T) {
	node := newTestNode(t, nil)
	createAccountForTest(t, node)
	createRecoveryForTest(t, node)

	// The local-administrator path is deliberately unavailable while locked.
	if resp, _ := node.request(t, http.MethodDelete, "/api/v1/session", testToken, "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("lock = %d", resp.StatusCode)
	}
	resp, body := node.request(t, http.MethodPost, "/api/v1/account/reset-password", testToken,
		`{"local":true,"new_password":"newpass456"}`, nil)
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("locked local reset = %d %s", resp.StatusCode, body)
	}
	if resp, body = node.request(t, http.MethodPost, "/api/v1/session", testToken,
		`{"password":"oldpass123"}`, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unlock = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/account/reset-password", testToken,
		`{"local":true,"new_password":"short"}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("short local reset = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/account/reset-password", testToken,
		`{"local":true,"new_password":"newpass456"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local reset = %d %s", resp.StatusCode, body)
	}

	if resp, _ = node.request(t, http.MethodDelete, "/api/v1/session", testToken, "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("lock again = %d", resp.StatusCode)
	}
	if resp, _ = node.request(t, http.MethodPost, "/api/v1/session", testToken,
		`{"password":"oldpass123"}`, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password unlock = %d", resp.StatusCode)
	}
	if resp, body = node.request(t, http.MethodPost, "/api/v1/session", testToken,
		`{"password":"newpass456"}`, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("new password unlock = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodGet, "/api/v1/account/recovery", testToken, "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"enabled":true`) {
		t.Fatalf("recovery after local reset = %d %s", resp.StatusCode, body)
	}
}

func TestAccountRecoveryAPIRateLimit(t *testing.T) {
	node := newTestNode(t, nil)
	createAccountForTest(t, node)
	for i := 0; i < 5; i++ {
		resp, _ := node.request(t, http.MethodPost, "/api/v1/account/reset-password", testToken,
			`{"credential":"bad","new_password":"newpass456"}`, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("attempt %d = %d", i, resp.StatusCode)
		}
	}
	resp, body := node.request(t, http.MethodPost, "/api/v1/account/reset-password", testToken,
		`{"credential":"bad","new_password":"newpass456"}`, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rate limit = %d %s", resp.StatusCode, body)
	}
}
