package update

import (
	"archive/tar"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractTarGz(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "release.tar.gz")
	writeTarGz(t, archive, []testArchiveEntry{
		{Name: "toget", Body: []byte("#!/bin/sh\n"), Mode: 0o755},
		{Name: "LICENSE", Body: []byte("license"), Mode: 0o644},
		{Name: "lib", Type: tar.TypeDir, Mode: 0o755},
		{Name: "lib/data.txt", Body: []byte("data"), Mode: 0o644},
	})
	dest := filepath.Join(dir, "out")
	executable, err := ExtractArchive(archive, FormatTarGz, dest, "toget")
	if err != nil {
		t.Fatal(err)
	}
	if executable != filepath.Join(dest, "toget") {
		t.Fatalf("executable = %q", executable)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("executable mode = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(dest, "lib", "data.txt"))
	if err != nil || string(data) != "data" {
		t.Fatalf("data = %q, %v", data, err)
	}
	licenseInfo, _ := os.Stat(filepath.Join(dest, "LICENSE"))
	if licenseInfo.Mode().Perm() != 0o644 {
		t.Fatalf("license mode = %o", licenseInfo.Mode().Perm())
	}
}

func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "release.zip")
	writeZip(t, archive, []testArchiveEntry{
		{Name: "toget.exe", Body: []byte("binary"), Mode: 0o755},
		{Name: "docs", Type: tar.TypeDir},
		{Name: "docs/readme.txt", Body: []byte("readme")},
	})
	dest := filepath.Join(dir, "out")
	executable, err := ExtractArchive(archive, FormatZip, dest, "toget.exe")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil || string(data) != "binary" {
		t.Fatalf("executable = %q, %v", data, err)
	}
}

func TestExtractArchiveRejectsUnsafeEntries(t *testing.T) {
	cases := []struct {
		name     string
		format   string
		entries  []testArchiveEntry
		execName string
	}{
		{"tar traversal", FormatTarGz, []testArchiveEntry{{Name: "../evil", Body: []byte("x")}}, "toget"},
		{"tar absolute", FormatTarGz, []testArchiveEntry{{Name: "/evil", Body: []byte("x")}}, "toget"},
		{"tar nested traversal", FormatTarGz, []testArchiveEntry{{Name: "a/../../evil", Body: []byte("x")}}, "toget"},
		{"tar backslash", FormatTarGz, []testArchiveEntry{{Name: `a\\evil`, Body: []byte("x")}}, "toget"},
		{"tar symlink", FormatTarGz, []testArchiveEntry{{Name: "link", Type: tar.TypeSymlink, Linkname: "/etc/passwd"}, {Name: "toget", Body: []byte("x")}}, "toget"},
		{"tar hardlink", FormatTarGz, []testArchiveEntry{{Name: "link", Type: tar.TypeLink, Linkname: "toget"}, {Name: "toget", Body: []byte("x")}}, "toget"},
		{"tar fifo", FormatTarGz, []testArchiveEntry{{Name: "fifo", Type: tar.TypeFifo}, {Name: "toget", Body: []byte("x")}}, "toget"},
		{"tar duplicate", FormatTarGz, []testArchiveEntry{{Name: "toget", Body: []byte("x")}, {Name: "toget", Body: []byte("y")}}, "toget"},
		{"tar dot", FormatTarGz, []testArchiveEntry{{Name: "./../evil", Body: []byte("x")}}, "toget"},
		{"tar missing executable", FormatTarGz, []testArchiveEntry{{Name: "other", Body: []byte("x")}}, "toget"},
		{"tar executable is directory", FormatTarGz, []testArchiveEntry{{Name: "toget", Type: tar.TypeDir}}, "toget"},
		{"zip traversal", FormatZip, []testArchiveEntry{{Name: "../evil", Body: []byte("x")}}, "toget"},
		{"zip absolute", FormatZip, []testArchiveEntry{{Name: "/evil", Body: []byte("x")}}, "toget"},
		{"zip symlink", FormatZip, []testArchiveEntry{{Name: "link", Type: tar.TypeSymlink, Body: []byte("/etc/passwd")}, {Name: "toget", Body: []byte("x")}}, "toget"},
		{"zip duplicate", FormatZip, []testArchiveEntry{{Name: "toget", Body: []byte("x")}, {Name: "toget", Body: []byte("y")}}, "toget"},
		{"zip missing executable", FormatZip, []testArchiveEntry{{Name: "other", Body: []byte("x")}}, "toget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "release")
			if tc.format == FormatTarGz {
				archive += ".tar.gz"
				writeTarGz(t, archive, tc.entries)
			} else {
				archive += ".zip"
				writeZip(t, archive, tc.entries)
			}
			dest := filepath.Join(dir, "out")
			if _, err := ExtractArchive(archive, tc.format, dest, tc.execName); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatal("extraction directory was left behind")
			}
		})
	}
}

func TestExtractArchiveRequiresEmptyDestination(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "release.tar.gz")
	writeTarGz(t, archive, []testArchiveEntry{{Name: "toget", Body: []byte("x")}})
	dest := filepath.Join(dir, "out")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractArchive(archive, FormatTarGz, dest, "toget"); err == nil {
		t.Fatal("existing destination was accepted")
	}
}

func TestExtractArchiveRejectsTooManyFiles(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "many.tar.gz")
	entries := make([]testArchiveEntry, 0, MaxExtractedFiles+1)
	for i := 0; i < MaxExtractedFiles+1; i++ {
		entries = append(entries, testArchiveEntry{Name: fmt.Sprintf("file-%d", i), Body: []byte("x")})
	}
	writeTarGz(t, archive, entries)
	dest := filepath.Join(dir, "out")
	if _, err := ExtractArchive(archive, FormatTarGz, dest, "toget"); err == nil {
		t.Fatal("archive with too many files was accepted")
	}
}

func TestWindowsReservedName(t *testing.T) {
	reserved := []string{"CON", "con", "CON.txt", "PRN.log", "AUX", "NUL.json", "COM1", "COM9.txt", "LPT1.log", "CONIN$", "CONOUT$.txt"}
	for _, name := range reserved {
		if !windowsReservedName(name) {
			t.Errorf("windowsReservedName(%q) = false", name)
		}
	}
	allowed := []string{"console", "COM0", "COM10", "LPT0", "LPT10", "file.txt", ".CON"}
	for _, name := range allowed {
		if windowsReservedName(name) {
			t.Errorf("windowsReservedName(%q) = true", name)
		}
	}
}

func TestCleanArchiveName(t *testing.T) {
	valid := map[string]string{
		"toget":      "toget",
		"./toget":    "toget",
		"dir/file":   "dir/file",
		"./dir/file": "dir/file",
		"dir/":       "dir",
	}
	for raw, want := range valid {
		got, err := cleanArchiveName(raw)
		if err != nil || got != want {
			t.Errorf("cleanArchiveName(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	invalid := []string{
		"", "..", "../x", "/x", `a\\b`, "a//b", "a/./b", "a:b",
		"a/\x01b", "a/\x7fb",
	}
	for _, raw := range invalid {
		if _, err := cleanArchiveName(raw); err == nil {
			t.Errorf("cleanArchiveName(%q) unexpectedly succeeded", raw)
		}
	}
}
