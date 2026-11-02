package directoryclient_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/directory"
	"github.com/sidenor/toget/internal/directoryclient"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
)

type testHost struct {
	service *hosted.Service
	server  *httptest.Server
	client  *hosted.Client
}

func newTestHost(t *testing.T, name string, lookup func(context.Context, string) (*hosted.DirectoryRecord, error)) *testHost {
	t.Helper()
	store, err := hosted.OpenStore(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsureHost(context.Background(), name, []identity.Endpoint{
		{Transport: "https", Addr: "127.0.0.1:7722"},
	}); err != nil {
		t.Fatal(err)
	}
	service, err := hosted.New(hosted.Options{
		Store: store, Mailer: &hosted.MemoryMailer{}, DisableRateLimit: true,
		DirectoryLookup: lookup,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(service.Handler())
	t.Cleanup(server.Close)
	fingerprint := hosted.Fingerprint(server.Certificate())
	client, err := hosted.NewClient("https://" + strings.TrimPrefix(server.URL, "https://") + "#fp=" + fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return &testHost{service: service, server: server, client: client}
}

func TestCrossHostResolveThroughDirectory(t *testing.T) {
	ctx := context.Background()
	directoryService, err := directory.New(directory.Options{
		Listen: "127.0.0.1:0", Paths: config.Paths{Home: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := directoryService.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directoryService.Close() })

	hostB := newTestHost(t, "Host B", nil)
	hostManifest, err := hostB.service.SignHostManifest(
		hostB.server.URL, hosted.Fingerprint(hostB.server.Certificate()), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := directoryclient.PublishHost(ctx, directoryService.URL(), hostB.service.Manifest(), hostManifest); err != nil {
		t.Fatal(err)
	}

	hostA := newTestHost(t, "Host A", func(ctx context.Context, code string) (*hosted.DirectoryRecord, error) {
		return directoryclient.Fetch(ctx, directoryService.URL(), code)
	})
	alice, err := hostA.client.Register(ctx, hosted.RegisterOptions{
		Name: "Alice", Email: "alice@example.com", Password: "correct horse battery staple", DeviceName: "Laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := hostB.client.Register(ctx, hosted.RegisterOptions{
		Name: "Bob", Email: "bob@example.com", Password: "correct horse battery staple", DeviceName: "Phone",
	})
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := hostA.client.ResolveAccount(ctx, hosted.RoomSigner{
		AccountID: alice.AccountID, DeviceID: alice.Device.ID, PrivateKey: alice.Device.PrivateKey,
	}, bob.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AccountID != bob.AccountID || resolved.Name != "Bob" ||
		resolved.Kind != "account" || resolved.Self {
		t.Fatalf("resolved = %+v", resolved)
	}
	if resolved.HostID != hostB.service.HostID() || resolved.HostCode != hostB.service.HostCode() {
		t.Fatalf("resolved host = %s/%s", resolved.HostID, resolved.HostCode)
	}
	if resolved.ServerName != "Host B" {
		t.Fatalf("server name = %q", resolved.ServerName)
	}
}
