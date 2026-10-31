package api_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/sidenor/toget/internal/node"
)

func TestCardExportImportLookupAndRemove(t *testing.T) {
	host := newTestNodeWithLAN(t)
	resp, body := host.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Card Host","password":"testpass123","device_name":"host"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host account = %d %s", resp.StatusCode, body)
	}
	resp, body = host.request(t, http.MethodGet, "/api/v1/card", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get card = %d %s", resp.StatusCode, body)
	}
	exported := decode[struct {
		Card node.CardView `json:"card"`
	}](t, body)
	if exported.Card.ServerID == "" || exported.Card.Card == "" || exported.Card.Label != "Card Host" {
		t.Fatalf("exported card = %+v", exported.Card)
	}

	resp, body = host.request(t, http.MethodGet, "/api/v1/card/qr", testToken, "", nil)
	if resp.StatusCode != http.StatusOK || !bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("card QR = %d %q", resp.StatusCode, body[:min(len(body), 16)])
	}
	if resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("card QR content type = %q", resp.Header.Get("Content-Type"))
	}

	guest := newTestNodeWithLAN(t)
	resp, body = guest.request(t, http.MethodPost, "/api/v1/account", testToken,
		`{"name":"Card Guest","password":"testpass123","device_name":"guest"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create guest account = %d %s", resp.StatusCode, body)
	}
	resp, body = guest.request(t, http.MethodPost, "/api/v1/cards", testToken,
		fmt.Sprintf(`{"card":%q,"label":"Host card"}`, exported.Card.Card), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add card = %d %s", resp.StatusCode, body)
	}
	added := decode[struct {
		Card node.CardView `json:"card"`
	}](t, body)
	if added.Card.Label != "Host card" || added.Card.ServerID != exported.Card.ServerID {
		t.Fatalf("added card = %+v", added.Card)
	}

	resp, body = guest.request(t, http.MethodGet, "/api/v1/cards", testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list cards = %d %s", resp.StatusCode, body)
	}
	list := decode[struct {
		Cards []node.CardView `json:"cards"`
	}](t, body)
	if len(list.Cards) != 1 || list.Cards[0].Label != "Host card" {
		t.Fatalf("card list = %+v", list.Cards)
	}

	resp, body = guest.request(t, http.MethodGet, "/api/v1/node/lookup?code="+exported.Card.ServerCode, testToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lookup = %d %s", resp.StatusCode, body)
	}
	record := decode[node.DirectoryRecord](t, body)
	if record.Source != "card" || record.ServerID != exported.Card.ServerID ||
		record.AccountID != exported.Card.AccountID {
		t.Fatalf("lookup record = %+v", record)
	}

	resp, body = guest.request(t, http.MethodPost, "/api/v1/cards", testToken,
		`{"card":"toget-card:v1:not-a-card"}`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid card = %d %s", resp.StatusCode, body)
	}

	resp, body = guest.request(t, http.MethodDelete,
		"/api/v1/cards/"+url.PathEscape(exported.Card.ServerID), testToken, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove card = %d %s", resp.StatusCode, body)
	}
	resp, body = guest.request(t, http.MethodGet, "/api/v1/node/lookup?code="+exported.Card.ServerCode, testToken, "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("lookup after removal = %d %s", resp.StatusCode, body)
	}
}
