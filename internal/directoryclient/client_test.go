package directoryclient

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sidenor/toget/internal/directoryproto"
	"github.com/sidenor/toget/internal/identity"
)

func TestParseLookupHostRecord(t *testing.T) {
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	hostManifest, err := identity.SignHostManifest(server, "Host", "https://host.example", "", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(directoryproto.LookupResponse{
		Protocol: directoryproto.Protocol, Kind: directoryproto.RecordKindHost,
		IdentityManifest: server.ManifestJSON, HostManifest: hostManifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := ParseLookup(data)
	if err != nil {
		t.Fatal(err)
	}
	if record.Kind != directoryproto.RecordKindHost || record.ServerID != server.ID ||
		record.ServerCode != server.Code || record.HostManifest == nil {
		t.Fatalf("record = %+v", record)
	}
	if record.HostManifest.URL != "https://host.example" {
		t.Fatalf("host URL = %q", record.HostManifest.URL)
	}
}

func TestParseLookupRejectsTamperedManifest(t *testing.T) {
	server, err := identity.NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	hostManifest, err := identity.SignHostManifest(server, "Host", "https://host.example", "", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(hostManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["url"] = "https://attacker.example"
	tampered, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(directoryproto.LookupResponse{
		Protocol: directoryproto.Protocol, Kind: directoryproto.RecordKindHost,
		IdentityManifest: server.ManifestJSON, HostManifest: tampered,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLookup(data); err == nil {
		t.Fatal("tampered directory record was accepted")
	}
}

func TestSplitBaseAcceptsOriginAndDirectoryMount(t *testing.T) {
	cases := map[string]string{
		"https://toget.vu":                      "https://toget.vu",
		"https://toget.vu/":                     "https://toget.vu",
		"https://toget.vu/directory":            "https://toget.vu",
		"https://toget.vu/directory/":           "https://toget.vu",
		"https://host:7720#fp=abcdef":           "https://host:7720",
		"https://host:7720/directory#fp=abcdef": "https://host:7720",
	}
	for input, want := range cases {
		base, _, err := splitBase(input)
		if err != nil {
			t.Fatalf("splitBase(%q): %v", input, err)
		}
		if base != want {
			t.Fatalf("splitBase(%q) = %q, want %q", input, base, want)
		}
	}
	if _, _, err := splitBase("https://toget.vu/somewhere"); err == nil {
		t.Fatal("arbitrary directory path was accepted")
	}
	if _, _, err := splitBase("http://toget.vu"); err == nil {
		t.Fatal("plain HTTP directory was accepted")
	}
}
