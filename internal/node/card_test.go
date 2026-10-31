package node_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/card"
	"github.com/sidenor/toget/internal/node"
)

func TestCardOwnAddLookupAndRemove(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Card Host")
	guest := newNode(t, "Card Guest")

	own, err := host.node.OwnCard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if own.Label != "Card Host" || !strings.HasPrefix(own.Card, card.Prefix) ||
		own.ServerID != host.snapshot.Server.ID || own.AccountID != host.snapshot.Account.ID {
		t.Fatalf("own card = %+v", own)
	}
	if _, err := host.node.AddCard(ctx, own.Card, ""); err == nil {
		t.Fatal("a node accepted its own card")
	}

	added, err := guest.node.AddCard(ctx, own.Card, "Host card")
	if err != nil {
		t.Fatal(err)
	}
	if added.Label != "Host card" || added.ServerID != own.ServerID || added.AddedAt == nil {
		t.Fatalf("added card = %+v", added)
	}
	cards := guest.node.Cards()
	if len(cards) != 1 || cards[0].Label != "Host card" {
		t.Fatalf("guest cards = %+v", cards)
	}

	record, err := guest.node.LookupServer(ctx, host.snapshot.Server.Code)
	if err != nil {
		t.Fatal(err)
	}
	if record.Source != "card" || record.ServerID != host.snapshot.Server.ID ||
		record.AccountID != host.snapshot.Account.ID || record.TLSFingerprint == "" {
		t.Fatalf("card lookup = %+v", record)
	}

	if err := guest.node.RemoveCard(host.snapshot.Server.ID); err != nil {
		t.Fatal(err)
	}
	if len(guest.node.Cards()) != 0 {
		t.Fatal("card was not removed")
	}
	if _, err := guest.node.LookupServer(ctx, host.snapshot.Server.Code); err == nil {
		t.Fatal("lookup succeeded without a card, directory, or LAN discovery")
	}
}

func TestCardRejectsTamperedManifests(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Tamper Host")
	guest := newNode(t, "Tamper Guest")
	own, err := host.node.OwnCard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := card.Decode(own.Card)
	if err != nil {
		t.Fatal(err)
	}

	var nodeManifest node.Manifest
	if err := json.Unmarshal(decoded.Node, &nodeManifest); err != nil {
		t.Fatal(err)
	}
	nodeManifest.Endpoint = strings.Replace(nodeManifest.Endpoint, "https://", "https://evil.", 1)
	tamperedNode, err := json.Marshal(nodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := card.New("tampered", decoded.Identity, tamperedNode)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := tampered.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.node.AddCard(ctx, encoded, ""); err == nil {
		t.Fatal("card with a tampered node manifest was accepted")
	}

	var identityManifest map[string]any
	if err := json.Unmarshal(decoded.Identity, &identityManifest); err != nil {
		t.Fatal(err)
	}
	identityManifest["server_code"] = "00000000"
	tamperedIdentity, err := json.Marshal(identityManifest)
	if err != nil {
		t.Fatal(err)
	}
	tampered, err = card.New("tampered", tamperedIdentity, decoded.Node)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = tampered.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.node.AddCard(ctx, encoded, ""); err == nil {
		t.Fatal("card with a tampered identity manifest was accepted")
	}
}

func TestCardFallsBackFromInvalidEmbeddedLabel(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Label Host")
	guest := newNode(t, "Label Guest")
	own, err := host.node.OwnCard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := card.Decode(own.Card)
	if err != nil {
		t.Fatal(err)
	}
	portable, err := card.New("bad\x00label", decoded.Identity, decoded.Node)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := portable.Encode()
	if err != nil {
		t.Fatal(err)
	}
	added, err := guest.node.AddCard(ctx, encoded, "")
	if err != nil {
		t.Fatalf("an unsigned embedded label should not block import: %v", err)
	}
	if added.Label != own.ServerCode {
		t.Fatalf("label = %q, want server code %q", added.Label, own.ServerCode)
	}
}

func TestCardAcceptsExpiredNodeManifestButVerifiesLiveNode(t *testing.T) {
	ctx := context.Background()
	host := newNode(t, "Expired Card Host")
	guest := newNode(t, "Expired Card Guest")
	own, err := host.node.OwnCard(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := card.Decode(own.Card)
	if err != nil {
		t.Fatal(err)
	}
	var nodeManifest node.Manifest
	if err := json.Unmarshal(decoded.Node, &nodeManifest); err != nil {
		t.Fatal(err)
	}
	nodeManifest.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	serverKey, err := host.accounts.UnsealServerKey(host.snapshot.Server)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.SignNodeManifestForTest(&nodeManifest, serverKey); err != nil {
		t.Fatal(err)
	}
	expiredNode, err := json.Marshal(nodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := card.New("expired node manifest", decoded.Identity, expiredNode)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := expired.Encode()
	if err != nil {
		t.Fatal(err)
	}
	added, err := guest.node.AddCard(ctx, encoded, "")
	if err != nil {
		t.Fatalf("expired node manifest should still be importable: %v", err)
	}
	if !added.NodeExpiresAt.Before(time.Now().UTC()) {
		t.Fatalf("expected an expired node manifest, got %s", added.NodeExpiresAt)
	}
	record, err := guest.node.LookupServer(ctx, host.snapshot.Server.Code)
	if err != nil {
		t.Fatalf("live verification should refresh the expired card: %v", err)
	}
	if record.Source != "card" || record.ServerID != host.snapshot.Server.ID {
		t.Fatalf("unexpected record: %+v", record)
	}
}
