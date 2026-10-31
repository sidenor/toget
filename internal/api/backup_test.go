package api_test

import (
	"net/http"
	"os"
	"testing"

	"github.com/sidenor/toget/internal/api"
	"github.com/sidenor/toget/internal/backup"
)

func TestBackupAndRestoreAPI(t *testing.T) {
	node := newTestNode(t, nil)
	resp, body := node.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Aiden","password":"testpass123","device_name":"test"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms", testToken, `{"name":"Backups"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create room = %d %s", resp.StatusCode, body)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/backups", testToken, "", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create backup = %d %s", resp.StatusCode, body)
	}
	created := decode[backup.Archive](t, body)
	if created.Name == "" || created.Size == 0 || created.Manifest.SchemaVersion == 0 {
		t.Fatalf("created backup = %#v", created)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/backups", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list backups = %d %s", resp.StatusCode, body)
	}
	list := decode[struct {
		Backups []backup.Archive `json:"backups"`
	}](t, body)
	if len(list.Backups) != 1 || list.Backups[0].Name != created.Name {
		t.Fatalf("backups = %#v", list.Backups)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/backups/"+created.Name+"/download", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download backup = %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Disposition"); got == "" {
		t.Fatalf("missing download disposition")
	}
	if int64(len(body)) != created.Size {
		t.Fatalf("download size = %d, want %d", len(body), created.Size)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/restore/uploads", testToken, string(body), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload restore = %d %s", resp.StatusCode, body)
	}
	upload := decode[backup.Upload](t, body)
	if upload.ID == "" || upload.Size != created.Size {
		t.Fatalf("upload = %#v", upload)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/restore", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore status = %d %s", resp.StatusCode, body)
	}
	empty := decode[api.RestoreStatusView](t, body)
	if empty.Staged != nil {
		t.Fatalf("unexpected staged restore: %#v", empty.Staged)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/restore", testToken, `{"upload_id":"`+upload.ID+`"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("stage restore = %d %s", resp.StatusCode, body)
	}
	staged := decode[backup.Pending](t, body)
	if staged.Manifest.Format != backup.Format || staged.Size != created.Size {
		t.Fatalf("staged = %#v", staged)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/restore", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("staged status = %d %s", resp.StatusCode, body)
	}
	status := decode[api.RestoreStatusView](t, body)
	if status.Staged == nil || status.Staged.StagedAt.IsZero() {
		t.Fatalf("staged status = %#v", status)
	}
	// No restart command is configured by this fixture.
	resp, body = node.request(t, http.MethodPost, "/api/v1/restore/restart", testToken, "", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("restart without command = %d %s", resp.StatusCode, body)
	}

	resp, _ = node.request(t, http.MethodDelete, "/api/v1/restore", testToken, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel restore = %d", resp.StatusCode)
	}
	resp, body = node.request(t, http.MethodGet, "/api/v1/restore", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status after cancel = %d %s", resp.StatusCode, body)
	}
	if got := decode[api.RestoreStatusView](t, body); got.Staged != nil {
		t.Fatalf("restore still staged: %#v", got.Staged)
	}

	// Stage an existing backup, then cancel it.
	resp, body = node.request(t, http.MethodPost, "/api/v1/restore", testToken, `{"backup_name":"`+created.Name+`"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("stage local backup = %d %s", resp.StatusCode, body)
	}
	resp, _ = node.request(t, http.MethodDelete, "/api/v1/restore", testToken, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel local restore = %d", resp.StatusCode)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/restore/uploads", testToken, "not a zip", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid upload = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/restore", testToken, `{"backup_name":"missing.toget-backup"}`, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing backup = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/restore", testToken, `{"backup_name":"../toget.db"}`, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unsafe backup name = %d %s", resp.StatusCode, body)
	}

	resp, _ = node.request(t, http.MethodDelete, "/api/v1/backups/"+created.Name, testToken, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete backup = %d", resp.StatusCode)
	}
	if _, err := os.Stat(node.dir + "/backups/" + created.Name); !os.IsNotExist(err) {
		t.Fatalf("backup still exists: %v", err)
	}
}

func TestBackupAPIRequiresUnlock(t *testing.T) {
	node := newTestNode(t, nil)
	resp, body := node.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Aiden","password":"testpass123","device_name":"test"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}
	// The session is unlocked; lock the vault and then verify data routes stop.
	resp, _ = node.request(t, http.MethodDelete, "/api/v1/session", testToken, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("lock = %d", resp.StatusCode)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/backups", testToken, "", nil)
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("locked backup = %d %s", resp.StatusCode, body)
	}
}
