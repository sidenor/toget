package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type testArchiveEntry struct {
	Name     string
	Body     []byte
	Type     byte
	Linkname string
	Mode     int64
}

func testFundingKeys(t *testing.T) (*SigningKey, *SigningKey, ReleaseKey, *Verifier) {
	t.Helper()
	root, err := GenerateSigningKey(KindRoot)
	if err != nil {
		t.Fatal(err)
	}
	release, err := GenerateSigningKey(KindRelease)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	cert, err := CertifyRelease(root, release.Public, now.Add(-time.Hour), now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return root, release, cert, NewVerifier(root.Public)
}

func writeTarGz(t *testing.T, path string, entries []testArchiveEntry) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		typ := entry.Type
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := entry.Mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name:     entry.Name,
			Mode:     mode,
			Typeflag: typ,
			Linkname: entry.Linkname,
		}
		if typ == tar.TypeReg || typ == tar.TypeRegA {
			header.Size = int64(len(entry.Body))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tw.Write(entry.Body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, path string, entries []testArchiveEntry) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		mode := os.FileMode(entry.Mode)
		switch entry.Type {
		case tar.TypeDir:
			header.SetMode(os.ModeDir | 0o755)
		case tar.TypeSymlink:
			header.SetMode(os.ModeSymlink | 0o777)
			mode = os.ModeSymlink | 0o777
		default:
			if mode == 0 {
				mode = 0o644
			}
			header.SetMode(mode)
		}
		_ = mode
		writer, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.Body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func fakeBinary(app string) []byte {
	return fakeBinaryWithSchema(app, 9)
}

func fakeBinaryWithSchema(app string, schema int) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\necho '{\"app\":%q,\"protocol\":1,\"schema\":%d,\"os\":%q,\"arch\":%q}'\n",
		app, schema, runtime.GOOS, runtime.GOARCH))
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

type testRelease struct {
	Manifest    *Manifest
	Channel     *Channel
	ManifestRaw []byte
	Artifact    []byte
}

func buildTestRelease(t *testing.T, dir, release, baseURL string, root, key *SigningKey, cert ReleaseKey, binary []byte) testRelease {
	t.Helper()
	archive := filepath.Join(dir, "daemon", runtime.GOOS+"-"+runtime.GOARCH, "v"+release, "toget.tar.gz")
	writeTarGz(t, archive, []testArchiveEntry{
		{Name: "toget", Body: binary, Mode: 0o755},
		{Name: "LICENSE", Body: []byte("license\n"), Mode: 0o644},
	})
	manifest, err := BuildManifest(BuildManifestOptions{
		Dir:         dir,
		Release:     release,
		BaseURL:     baseURL,
		Executable:  "toget",
		Protocol:    1,
		Schema:      9,
		MinApp:      "0.1.0",
		MinProtocol: 1,
		Level:       LevelRecommended,
		Changelog:   "test release " + release,
		PublishedAt: time.Now().UTC().Add(-time.Minute),
		Key:         key,
		Cert:        cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw := mustJSON(t, manifest)
	manifestPath := filepath.Join(dir, "manifests", "v"+release+".json")
	writeFile(t, manifestPath, append(manifestRaw, '\n'))

	manifestRef := baseURL + "/manifests/v" + release + ".json"
	if baseURL == "" {
		manifestRef = "manifests/v" + release + ".json"
	}
	channel, err := BuildChannel(BuildChannelOptions{
		Channel:     "stable",
		Release:     release,
		ManifestRef: manifestRef,
		Rollout:     100,
		PublishedAt: time.Now().UTC().Add(-time.Minute),
		Key:         key,
		Cert:        cert,
	})
	if err != nil {
		t.Fatal(err)
	}
	channelRaw := mustJSON(t, channel)
	writeFile(t, filepath.Join(dir, "channels", "stable.json"), append(channelRaw, '\n'))
	return testRelease{Manifest: manifest, Channel: channel, ManifestRaw: manifestRaw, Artifact: binary}
}

func testVerifierFromRoot(t *testing.T, root *SigningKey) *Verifier {
	t.Helper()
	if len(root.Public) != ed25519.PublicKeySize {
		t.Fatal("bad root key")
	}
	return NewVerifier(root.Public)
}

func decodeManifest(t *testing.T, raw []byte) Manifest {
	t.Helper()
	var manifest Manifest
	if err := decodeStrict(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func sortedArtifactKeys(manifest *Manifest) []string {
	keys := make([]string, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		keys = append(keys, artifact.OS+"/"+artifact.Arch)
	}
	return keys
}

func sameBytes(a, b []byte) bool {
	return bytes.Equal(a, b)
}
