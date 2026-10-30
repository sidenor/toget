package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/room"
)

func setupAccountRoom(t *testing.T) (*testNode, string) {
	t.Helper()
	node := newTestNode(t, nil)
	resp, body := node.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Aiden","password":"testpass123","device_name":"test"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account = %d %s", resp.StatusCode, body)
	}
	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms", testToken, `{"name":"Files"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create room = %d %s", resp.StatusCode, body)
	}
	var created struct {
		Room room.Room `json:"room"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	return node, created.Room.ID
}

func TestAttachmentFlow(t *testing.T) {
	node, roomID := setupAccountRoom(t)
	uploadPath := "/api/v1/rooms/" + roomID + "/attachments?name=" + url.QueryEscape("report.txt") + "&mime=" + url.QueryEscape("text/plain")
	resp, body := node.request(t, http.MethodPost, uploadPath, testToken, "the secret payload", map[string]string{"Content-Type": "text/plain"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload = %d %s", resp.StatusCode, body)
	}
	attachment := decode[room.Attachment](t, body)
	if attachment.ID == "" || attachment.Name != "report.txt" || attachment.Size != 18 {
		t.Fatalf("attachment = %s", body)
	}

	resp, body = node.request(t, http.MethodGet, "/api/v1/attachments/"+attachment.ID+"/content", testToken, "", nil)
	if resp.StatusCode != http.StatusOK || string(body) != "the secret payload" {
		t.Fatalf("content = %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, "report.txt") {
		t.Fatalf("content-disposition = %q", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q", got)
	}

	resp, body = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/messages", testToken,
		`{"text":"see file","attachments":["`+attachment.ID+`"]}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("message with attachment = %d %s", resp.StatusCode, body)
	}
	event := decode[room.Event](t, body)
	var payload room.MessagePostedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0] != attachment.ID {
		t.Fatalf("payload = %#v", payload)
	}

	resp, _ = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/messages", testToken,
		`{"text":"again","attachments":["`+attachment.ID+`"]}`, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("reused attachment = %d, want 409", resp.StatusCode)
	}

	resp, _ = node.request(t, http.MethodGet, "/api/v1/rooms/"+roomID+"/attachments", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list attachments = %d", resp.StatusCode)
	}

	resp, _ = node.request(t, http.MethodGet, "/api/v1/attachments/missing/content", testToken, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing attachment = %d, want 404", resp.StatusCode)
	}
}

func TestAttachmentUploadLimits(t *testing.T) {
	node, roomID := setupAccountRoom(t)
	resp, _ := node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/attachments?name=empty.txt", testToken, "", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty upload = %d, want 400", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodPost, "/api/v1/rooms/"+roomID+"/attachments?name="+url.QueryEscape(strings.Repeat("a", 300)), testToken, "x", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("long name = %d, want 400", resp.StatusCode)
	}
	resp, _ = node.request(t, http.MethodGet, "/api/v1/rooms/missing/attachments", testToken, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing room = %d, want 404", resp.StatusCode)
	}
}
