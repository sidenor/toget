package directory

import (
	"testing"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/directoryproto"
	"github.com/sidenor/toget/internal/identity"
)

func newTestDirectory(t *testing.T) *Service {
	t.Helper()
	service, err := New(Options{Paths: config.Paths{Home: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestDirectoryHostPublishAndLookup(t *testing.T) {
	service := newTestDirectory(t)
	server, err := identity.NewServer([]identity.Endpoint{{Transport: "https", Addr: "host.example:443"}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := identity.SignHostManifest(server, "Host", "https://host.example", "", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PublishHost(server.ManifestJSON, manifest); err != nil {
		t.Fatal(err)
	}
	response, err := service.Lookup(server.Code)
	if err != nil {
		t.Fatal(err)
	}
	if response.Kind != directoryproto.RecordKindHost {
		t.Fatalf("kind = %q", response.Kind)
	}
	if len(response.HostManifest) == 0 || len(response.NodeManifest) != 0 {
		t.Fatalf("lookup manifests = host %d, node %d", len(response.HostManifest), len(response.NodeManifest))
	}
	parsed, err := identity.VerifyHostManifest(response.HostManifest, server.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.URL != "https://host.example" {
		t.Fatalf("host URL = %q", parsed.URL)
	}
	if _, err := service.Lookup("00000000"); err == nil {
		t.Fatal("unknown code resolved")
	}
}

func TestDirectoryRejectsHostManifestMismatch(t *testing.T) {
	service := newTestDirectory(t)
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := identity.SignHostManifest(other, "Other", "https://other.example", "", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PublishHost(server.ManifestJSON, manifest); err == nil {
		t.Fatal("host manifest signed by another server was published")
	}
}

func TestDirectoryRejectsExpiredHostRecord(t *testing.T) {
	service := newTestDirectory(t)
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Sign in the past with a short lifetime so the record is already stale.
	now := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	manifest, err := identity.SignHostManifest(server, "Host", "https://host.example", "", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PublishHost(server.ManifestJSON, manifest); err == nil {
		t.Fatal("expired host manifest was published")
	}
}

func TestDirectoryPersistsHostRecords(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{Home: dir}
	service, err := New(Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := identity.SignHostManifest(server, "Host", "https://host.example", "", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PublishHost(server.ManifestJSON, manifest); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(Options{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	response, err := reloaded.Lookup(server.Code)
	if err != nil {
		t.Fatal(err)
	}
	if response.Kind != directoryproto.RecordKindHost {
		t.Fatalf("reloaded kind = %q", response.Kind)
	}
	if _, err := identity.VerifyHostManifest(response.HostManifest, server.PublicKey); err != nil {
		t.Fatal(err)
	}
}
