package releasebuild

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDeterministicArchives(t *testing.T) {
	epoch := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	data := []byte("toget binary contents\n")
	for _, name := range []string{"toget.tar.gz", "toget.zip"} {
		t.Run(name, func(t *testing.T) {
			firstDir := t.TempDir()
			secondDir := t.TempDir()
			first := filepath.Join(firstDir, name)
			second := filepath.Join(secondDir, name)
			if err := writeArchive(first, "toget", data, epoch); err != nil {
				t.Fatal(err)
			}
			if err := writeArchive(second, "toget", data, epoch); err != nil {
				t.Fatal(err)
			}
			firstData, err := os.ReadFile(first)
			if err != nil {
				t.Fatal(err)
			}
			secondData, err := os.ReadFile(second)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(firstData, secondData) {
				t.Fatal("archive bytes are not deterministic")
			}
			other := filepath.Join(t.TempDir(), name)
			if err := writeArchive(other, "toget", data, epoch.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			otherData, err := os.ReadFile(other)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(firstData, otherData) {
				t.Fatal("archive bytes ignored the source date epoch")
			}
		})
	}
}

func TestParsePlatforms(t *testing.T) {
	platforms, err := parsePlatforms([]string{"linux/amd64", "darwin/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if len(platforms) != 2 || platforms[1].OS != "darwin" || platforms[1].Arch != "arm64" {
		t.Fatalf("platforms = %+v", platforms)
	}
	cases := [][]string{
		{"linux"},
		{"Linux/amd64"},
		{"linux/amd64/extra"},
		{"linux/amd64", "linux/amd64"},
		{","},
	}
	for _, values := range cases {
		if _, err := parsePlatforms(values); err == nil {
			t.Errorf("parsePlatforms(%v) accepted invalid input", values)
		}
	}
}

func TestBuildEnvRemovesAmbientBuildSettings(t *testing.T) {
	env := buildEnv([]string{
		"PATH=/usr/bin",
		"GOOS=windows",
		"GOARCH=arm64",
		"CGO_ENABLED=1",
		"GOFLAGS=-tags=ambient",
		"GOWORK=/tmp/work",
		"HOME=/root",
	}, 1234)
	joined := strings.Join(env, "\n")
	for _, unwanted := range []string{"GOOS=windows", "GOARCH=arm64", "CGO_ENABLED=1", "GOFLAGS=-tags=ambient", "GOWORK=/tmp/work"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("buildEnv kept %s", unwanted)
		}
	}
	for _, wanted := range []string{"PATH=/usr/bin", "HOME=/root", "CGO_ENABLED=0", "GOWORK=off", "SOURCE_DATE_EPOCH=1234"} {
		if !strings.Contains(joined, wanted) {
			t.Errorf("buildEnv lost %s", wanted)
		}
	}
}

func TestArtifactPath(t *testing.T) {
	tar := artifactPath("/releases", "1.2.3", platform{OS: "linux", Arch: "amd64"})
	if tar != filepath.Join("/releases", "daemon", "linux-amd64", "v1.2.3", "toget-linux-amd64.tar.gz") {
		t.Fatalf("tar path = %s", tar)
	}
	zipPath := artifactPath("/releases", "1.2.3", platform{OS: "windows", Arch: "amd64"})
	if zipPath != filepath.Join("/releases", "daemon", "windows-amd64", "v1.2.3", "toget-windows-amd64.zip") {
		t.Fatalf("zip path = %s", zipPath)
	}
}

func TestBuildReproducible(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	if _, err := os.Stat("../.."); err != nil {
		t.Skip("repository source is not available")
	}
	targetOS := runtime.GOOS
	targetArch := runtime.GOARCH
	artifacts, err := Build(context.Background(), Options{
		RepoDir: "../..", OutDir: t.TempDir(), Release: "1.2.3",
		Platforms: []string{targetOS + "/" + targetArch},
		Package:   "./cmd/toget-official",
		Verify:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts = %+v", artifacts)
	}
	if artifacts[0].Size <= 0 || len(artifacts[0].SHA256) != 64 {
		t.Fatalf("artifact = %+v", artifacts[0])
	}
	if _, err := os.Stat(artifacts[0].Path); err != nil {
		t.Fatal(err)
	}
}

func TestBuildRejectsBadOptions(t *testing.T) {
	cases := map[string]Options{
		"bad release":  {OutDir: t.TempDir(), Release: "v1.0.0"},
		"build meta":   {OutDir: t.TempDir(), Release: "1.0.0+meta"},
		"no out":       {Release: "1.0.0"},
		"bad platform": {OutDir: t.TempDir(), Release: "1.0.0", Platforms: []string{"linux"}},
		"epoch":        {OutDir: t.TempDir(), Release: "1.0.0", SourceDateEpoch: -1},
		"root key":     {OutDir: t.TempDir(), Release: "1.0.0", RootPublicKey: "not-a-key"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Build(context.Background(), opts); err == nil {
				t.Fatal("Build accepted invalid options")
			}
		})
	}
}
