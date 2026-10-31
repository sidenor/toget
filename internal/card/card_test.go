package card_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/card"
)

func TestCardRoundTrip(t *testing.T) {
	c, err := card.New("  Alice  ", json.RawMessage(`{"kind":"identity"}`), json.RawMessage(`{"kind":"node"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, card.Prefix) {
		t.Fatalf("encoded card has no prefix: %s", encoded)
	}
	decoded, err := card.Decode(" \n" + encoded[:len(card.Prefix)+20] + "\n" + encoded[len(card.Prefix)+20:] + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Label != "Alice" || string(decoded.Identity) != `{"kind":"identity"}` ||
		string(decoded.Node) != `{"kind":"node"}` {
		t.Fatalf("decoded card = %+v", decoded)
	}
	if _, err := card.QRPNG(encoded, 4); err != nil {
		t.Fatalf("render QR: %v", err)
	}
	text, err := card.QRText(encoded)
	if err != nil || !strings.Contains(text, "█") && !strings.Contains(text, "▀") {
		t.Fatalf("terminal QR = %q, %v", text, err)
	}
}

func TestCardRejectsMalformedInput(t *testing.T) {
	cases := []string{
		"",
		"hello",
		card.Prefix,
		card.Prefix + "!!!",
		card.Prefix + base64.RawURLEncoding.EncodeToString([]byte("not gzip")),
	}
	for _, raw := range cases {
		if _, err := card.Decode(raw); err == nil {
			t.Fatalf("Decode(%q) succeeded", raw)
		}
	}
	if _, err := card.New(strings.Repeat("x", 200), json.RawMessage(`{}`), json.RawMessage(`{}`)); err == nil {
		t.Fatal("oversized label was accepted")
	}
	if _, err := card.New("x", json.RawMessage(`{`), json.RawMessage(`{}`)); err == nil {
		t.Fatal("invalid manifest JSON was accepted")
	}
}

func TestStorePersistsAndRemoves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cards.json")
	store, err := card.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := card.New("Bob", json.RawMessage(`{}`), json.RawMessage(`{}`))
	encoded, _ := c.Encode()
	entry := card.Entry{
		ID: "tog1:s:BOB", Label: "Bob", ServerCode: "12345678",
		AccountID: "12345678901234", Card: encoded, AddedAt: time.Now().UTC(),
	}
	if err := store.Put(entry); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("card store mode = %o", info.Mode().Perm())
	}
	reopened, err := card.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	list := reopened.List()
	if len(list) != 1 || list[0].ID != entry.ID || list[0].Label != "Bob" {
		t.Fatalf("reopened list = %+v", list)
	}
	if ok, err := reopened.Remove(entry.ID); err != nil || !ok {
		t.Fatalf("remove = %v, %v", ok, err)
	}
	reopened, err = card.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 0 {
		t.Fatal("removed card came back")
	}
}

func TestStoreSurvivesCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cards.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := card.Open(path)
	if err == nil {
		t.Fatal("corrupt store was accepted")
	}
	if store == nil || len(store.List()) != 0 {
		t.Fatal("corrupt store should still yield an empty usable store")
	}
}

func TestQRPNG(t *testing.T) {
	data, err := card.QRPNG("toget-card:v1:test", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("QR output is not a PNG")
	}
	if _, err := card.QRPNG("x", 1); err == nil {
		t.Fatal("invalid scale was accepted")
	}
}
