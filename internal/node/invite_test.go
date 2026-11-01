package node_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/node"
)

func TestOfficialInviteAcceptsCertificateAndIdentityPins(t *testing.T) {
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("ab", 32)
	for _, homeURL := range []string{
		"https://accounts.example.com#fp=" + fingerprint,
		"https://accounts.example.com#id=" + server.ID,
	} {
		code := node.InviteCode{
			Secret: "0123456789abcdef", URL: homeURL, ServerID: server.ID,
			RoomID: "room-1", RoomName: "Room", HomeKind: node.InviteKindOfficial,
		}
		encoded, err := node.EncodeInvite(code)
		if err != nil {
			t.Fatalf("EncodeInvite(%q): %v", homeURL, err)
		}
		decoded, err := node.DecodeInvite(encoded)
		if err != nil {
			t.Fatalf("DecodeInvite(%q): %v", homeURL, err)
		}
		if decoded.URL != homeURL || decoded.ServerID != server.ID ||
			decoded.HomeKind != node.InviteKindOfficial {
			t.Fatalf("decoded = %+v", decoded)
		}
	}
}

func TestOfficialInviteRejectsHostIdentityMismatch(t *testing.T) {
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	base := node.InviteCode{
		Secret: "0123456789abcdef", ServerID: server.ID, RoomID: "room-1",
		HomeKind: node.InviteKindOfficial,
	}
	bad := []node.InviteCode{base, base, base, base}
	bad[0].URL = "https://accounts.example.com#id=" + other.ID
	bad[1].URL = "https://accounts.example.com"
	bad[2].URL = "https://accounts.example.com#id=" + other.ID
	bad[2].ServerID = "not-a-server-id"
	bad[3].URL = "https://accounts.example.com#fp=" + strings.Repeat("cd", 32)
	bad[3].ServerID = "not-a-server-id"
	for i, code := range bad {
		if _, err := node.EncodeInvite(code); !errors.Is(err, node.ErrInvalidInviteCode) {
			t.Fatalf("case %d error = %v", i, err)
		}
	}

	// Node-hosted invites still require an exact certificate pin.
	nodeCode := node.InviteCode{
		Secret: "0123456789abcdef", URL: "https://node.example.com#id=" + server.ID,
		ServerID: server.ID, RoomID: "room-1", HomeKind: node.InviteKindNode,
	}
	if _, err := node.EncodeInvite(nodeCode); err == nil {
		t.Fatal("node invite accepted an identity pin")
	}
	nodeCode.URL = "https://node.example.com#fp=" + strings.Repeat("ab", 32)
	if _, err := node.EncodeInvite(nodeCode); err != nil {
		t.Fatalf("node invite with certificate pin: %v", err)
	}
}
