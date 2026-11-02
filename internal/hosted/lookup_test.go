package hosted_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
)

func TestAccountLookupRoundTrip(t *testing.T) {
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	lookup, err := hosted.SignAccountLookup(hosted.AccountLookup{
		Protocol: hosted.Protocol, AccountID: server.Code + "000001", Name: "Alice",
		HostID: server.ID, HostCode: server.Code,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}, server.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := hosted.VerifyAccountLookup(lookup, server.PublicKey); err != nil {
		t.Fatal(err)
	}

	tampered := lookup
	tampered.Name = "Mallory"
	if err := hosted.VerifyAccountLookup(tampered, server.PublicKey); err == nil {
		t.Fatal("tampered account lookup verified")
	}
	expired := lookup
	expired.IssuedAt = now.Add(-10 * time.Minute)
	expired.ExpiresAt = now.Add(-5 * time.Minute)
	expired, err = hosted.SignAccountLookup(expired, server.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := hosted.VerifyAccountLookup(expired, server.PublicKey); err == nil {
		t.Fatal("expired account lookup verified")
	}
	other, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := hosted.VerifyAccountLookup(lookup, other.PublicKey); err == nil {
		t.Fatal("account lookup verified under another host key")
	}
}

func TestHostedPublicAccountLookupAndLocalResolve(t *testing.T) {
	ctx := context.Background()
	h := newHostHarness(t)
	const password = "correct horse battery staple"
	registered, err := h.client.Register(ctx, hosted.RegisterOptions{
		Name: "Alice", Email: "alice@example.com", Password: password, DeviceName: "Laptop",
	})
	if err != nil {
		t.Fatal(err)
	}

	lookup, err := h.client.LookupAccount(ctx, registered.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if lookup.AccountID != registered.AccountID || lookup.Name != "Alice" ||
		lookup.HostID != h.hostID || lookup.HostCode != h.service.HostCode() {
		t.Fatalf("lookup = %+v", lookup)
	}
	if _, err := h.client.LookupAccount(ctx, h.service.HostCode()+"999999"); err == nil {
		t.Fatal("unknown account lookup succeeded")
	}
	if _, err := h.client.LookupAccount(ctx, "00000000"+registered.AccountID[8:]); err == nil {
		t.Fatal("account lookup for another host code succeeded")
	}

	signer := hosted.RoomSigner{
		AccountID: registered.AccountID, DeviceID: registered.Device.ID,
		PrivateKey: registered.Device.PrivateKey,
	}
	resolved, err := h.client.ResolveAccount(ctx, signer, registered.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AccountID != registered.AccountID || resolved.Name != "Alice" ||
		resolved.Kind != "account" || !resolved.Self || resolved.HostCode != h.service.HostCode() {
		t.Fatalf("resolved = %+v", resolved)
	}
	if _, err := h.client.ResolveAccount(ctx, signer, h.service.HostCode()+"999999"); err == nil {
		t.Fatal("unknown account resolved")
	}
}

func TestAccountLookupRejectsOuterJSON(t *testing.T) {
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	lookup, err := hosted.SignAccountLookup(hosted.AccountLookup{
		Protocol: hosted.Protocol, AccountID: server.Code + "000001", Name: "Alice",
		HostID: server.ID, HostCode: server.Code,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}, server.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(lookup)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	generic["account_id"] = server.Code + "000002"
	tampered, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	var parsed hosted.AccountLookup
	if err := json.Unmarshal(tampered, &parsed); err != nil {
		t.Fatal(err)
	}
	if err := hosted.VerifyAccountLookup(parsed, server.PublicKey); err == nil {
		t.Fatal("modified account id verified")
	}
}
