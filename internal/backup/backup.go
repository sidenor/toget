// Package backup creates and restores portable, encrypted-at-rest node
// snapshots. A snapshot contains the SQLite database, the vault key, and
// attachment ciphertext; both are useless without the vault key, and the vault
// key is itself only useful with the account password.
package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/storage"
	"github.com/sidenor/toget/internal/version"
)

const (
	// Format identifies a Toget backup archive.
	Format = "toget-backup"
	// Version is the current archive format version.
	Version = 1
	// MaxRestoreSize bounds extracted data.
	MaxRestoreSize = 2 << 30
)

var (
	// ErrInvalidBackup means the archive is not a valid Toget backup.
	ErrInvalidBackup = errors.New("invalid backup archive")
	// ErrUnsafePath means an archive tried to escape the restore directory.
	ErrUnsafePath = errors.New("backup contains an unsafe path")
)

// File records one archived file.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest describes a backup archive.
type Manifest struct {
	Format        string    `json:"format"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	AppVersion    string    `json:"app_version"`
	SchemaVersion int       `json:"schema_version"`
	Files         []File    `json:"files"`
}

// Create writes a consistent snapshot to dest. The destination must not exist.
func Create(ctx context.Context, db *storage.DB, paths config.Paths, dest string) (*Manifest, error) {
	if db == nil {
		return nil, errors.New("backup: database is required")
	}
	if dest == "" {
		return nil, errors.New("backup: empty destination")
	}
	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf("backup: %s already exists", dest)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".toget-backup-*")
	if err != nil {
		return nil, fmt.Errorf("backup: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	snapshot := filepath.Join(tmp, "toget.db")
	if err := db.Backup(ctx, snapshot); err != nil {
		return nil, err
	}
	data := filepath.Join(tmp, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		return nil, fmt.Errorf("backup: create data dir: %w", err)
	}
	if err := moveFile(snapshot, filepath.Join(data, "toget.db")); err != nil {
		return nil, err
	}
	if err := copyFile(paths.VaultKey, filepath.Join(data, "vault.key"), 0o600); err != nil {
		return nil, fmt.Errorf("backup: copy vault key: %w", err)
	}
	if err := copyTree(paths.AttachmentsDir, filepath.Join(data, "attachments")); err != nil {
		return nil, fmt.Errorf("backup: copy attachments: %w", err)
	}

	manifest := &Manifest{
		Format:     Format,
		Version:    Version,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
		AppVersion: version.Current().App,
	}
	if v, err := db.SchemaVersion(ctx); err == nil {
		manifest.SchemaVersion = v
	}
	files, err := collectFiles(data)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		sum, size, err := hashFile(file)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(data, file)
		if err != nil {
			return nil, err
		}
		manifest.Files = append(manifest.Files, File{Path: filepath.ToSlash(rel), Size: size, SHA256: sum})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })

	if err := writeArchive(dest, data, manifest); err != nil {
		os.Remove(dest)
		return nil, err
	}
	return manifest, nil
}

func writeArchive(dest, data string, manifest *Manifest) error {
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup: create archive: %w", err)
	}
	zw := zip.NewWriter(out)
	for _, file := range manifest.Files {
		src := filepath.Join(data, filepath.FromSlash(file.Path))
		in, err := os.Open(src)
		if err != nil {
			zw.Close()
			out.Close()
			return fmt.Errorf("backup: open %s: %w", file.Path, err)
		}
		header := &zip.FileHeader{Name: "data/" + file.Path, Method: zip.Deflate}
		header.SetMode(0o600)
		header.Modified = manifest.CreatedAt
		w, err := zw.CreateHeader(header)
		if _, err = io.Copy(w, in); err != nil {
			in.Close()
			zw.Close()
			out.Close()
			return fmt.Errorf("backup: write %s: %w", file.Path, err)
		}
		in.Close()
	}
	header := &zip.FileHeader{Name: "manifest.json", Method: zip.Deflate}
	header.SetMode(0o600)
	w, err := zw.CreateHeader(header)
	if err == nil {
		err = json.NewEncoder(w).Encode(manifest)
	}
	if err != nil {
		zw.Close()
		out.Close()
		return fmt.Errorf("backup: write manifest: %w", err)
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return fmt.Errorf("backup: finish archive: %w", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("backup: sync archive: %w", err)
	}
	return out.Close()
}

// ReadManifest returns the manifest from an archive without extracting data.
func ReadManifest(path string) (*Manifest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBackup, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidBackup, err)
		}
		var manifest Manifest
		err = json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&manifest)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidBackup, err)
		}
		if manifest.Format != Format || manifest.Version != Version {
			return nil, fmt.Errorf("%w: unsupported format or version", ErrInvalidBackup)
		}
		return &manifest, nil
	}
	return nil, fmt.Errorf("%w: manifest missing", ErrInvalidBackup)
}

// Restore replaces a node's database, vault, and attachments with the archive
// contents. The caller must ensure the node is not running. Existing data is
// moved to a timestamped pre-restore directory instead of being deleted.
func Restore(ctx context.Context, paths config.Paths, archivePath string) (*Manifest, string, error) {
	manifest, err := ReadManifest(archivePath)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(paths.Home, 0o700); err != nil {
		return nil, "", fmt.Errorf("restore: create home: %w", err)
	}
	tmp, err := os.MkdirTemp(paths.Home, ".restore-*")
	if err != nil {
		return nil, "", fmt.Errorf("restore: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	data := filepath.Join(tmp, "data")
	if err := extract(archivePath, manifest, data); err != nil {
		return nil, "", err
	}
	restoredDB, err := storage.Open(filepath.Join(data, "toget.db"))
	if err != nil {
		return nil, "", fmt.Errorf("restore: validate database: %w", err)
	}
	schema, err := restoredDB.SchemaVersion(ctx)
	restoredDB.Close()
	if err != nil {
		return nil, "", fmt.Errorf("restore: read schema version: %w", err)
	}
	if schema > 3 {
		return nil, "", fmt.Errorf("%w: database schema %d is newer than this build", ErrInvalidBackup, schema)
	}
	if _, err := os.Stat(filepath.Join(data, "vault.key")); err != nil {
		return nil, "", fmt.Errorf("%w: vault key missing", ErrInvalidBackup)
	}

	rollback := filepath.Join(paths.Home, "pre-restore-"+time.Now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(rollback, 0o700); err != nil {
		return nil, "", fmt.Errorf("restore: create rollback dir: %w", err)
	}
	var moved []restoreRecord
	staged := []struct{ src, dst string }{
		{filepath.Join(data, "toget.db"), paths.DB},
		{filepath.Join(data, "vault.key"), paths.VaultKey},
		{filepath.Join(data, "attachments"), paths.AttachmentsDir},
	}
	for _, item := range staged {
		keep := filepath.Join(rollback, filepath.Base(item.dst))
		if _, err := os.Stat(item.dst); err == nil {
			if err := os.Rename(item.dst, keep); err != nil {
				rollbackAll(moved)
				return nil, "", fmt.Errorf("restore: move old %s: %w", item.dst, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			rollbackAll(moved)
			return nil, "", err
		} else {
			keep = ""
		}
		os.Remove(item.dst + "-wal")
		os.Remove(item.dst + "-shm")
		moved = append(moved, restoreRecord{dst: item.dst, keep: keep})
		if err := os.Rename(item.src, item.dst); err != nil {
			rollbackAll(moved)
			return nil, "", fmt.Errorf("restore: install %s: %w", item.dst, err)
		}
	}
	return manifest, rollback, nil
}

type restoreRecord struct{ dst, keep string }

func rollbackAll(moved []restoreRecord) {
	for i := len(moved) - 1; i >= 0; i-- {
		os.RemoveAll(moved[i].dst)
		if moved[i].keep != "" {
			_ = os.Rename(moved[i].keep, moved[i].dst)
		}
	}
}

func extract(archivePath string, manifest *Manifest, dest string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBackup, err)
	}
	defer zr.Close()
	wanted := make(map[string]File, len(manifest.Files))
	for _, f := range manifest.Files {
		if !safeRel(f.Path) {
			return ErrUnsafePath
		}
		wanted["data/"+f.Path] = f
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return fmt.Errorf("restore: create data dir: %w", err)
	}
	var total int64
	seen := make(map[string]bool, len(wanted))
	for _, f := range zr.File {
		file, ok := wanted[f.Name]
		if !ok {
			if f.Name == "manifest.json" {
				continue
			}
			return fmt.Errorf("%w: unexpected entry %s", ErrInvalidBackup, f.Name)
		}
		seen[f.Name] = true
		if f.UncompressedSize64 > uint64(MaxRestoreSize) || total+int64(f.UncompressedSize64) > MaxRestoreSize {
			return fmt.Errorf("%w: archive is too large", ErrInvalidBackup)
		}
		total += int64(f.UncompressedSize64)
		rel := strings.TrimPrefix(f.Name, "data/")
		outPath := filepath.Join(dest, filepath.FromSlash(rel))
		if !strings.HasPrefix(outPath, dest+string(os.PathSeparator)) {
			return ErrUnsafePath
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidBackup, err)
		}
		out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			rc.Close()
			return err
		}
		hash := sha256.New()
		n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(rc, file.Size+1))
		rc.Close()
		closeErr := out.Close()
		if err != nil {
			return fmt.Errorf("restore: extract %s: %w", rel, err)
		}
		if closeErr != nil {
			return closeErr
		}
		if n != file.Size || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
			return fmt.Errorf("%w: checksum mismatch for %s", ErrInvalidBackup, rel)
		}
	}
	for name := range wanted {
		if !seen[name] {
			return fmt.Errorf("%w: missing %s", ErrInvalidBackup, name)
		}
	}
	return nil
}

func safeRel(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "..") {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	return clean != "." && !strings.HasPrefix(clean, ".."+string(os.PathSeparator))
}

func collectFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), n, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst, 0o600)
}

func copyTree(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(dst, 0o700)
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target, 0o600)
	})
}
