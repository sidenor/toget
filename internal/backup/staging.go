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
	"sync"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/storage"
)

const (
	// MaxArchiveSize bounds an uploaded backup file. Restored contents are
	// bounded separately by MaxRestoreSize.
	MaxArchiveSize = MaxRestoreSize + 64<<20
	// UploadTTL is how long an inspected but unconfirmed upload is kept.
	UploadTTL = 24 * time.Hour
)

var (
	// ErrBackupNotFound means a named local backup does not exist.
	ErrBackupNotFound = errors.New("backup not found")
	// ErrNoPendingRestore means no restore is staged.
	ErrNoPendingRestore = errors.New("no restore is staged")
	// ErrArchiveTooLarge means an uploaded archive exceeds MaxArchiveSize.
	ErrArchiveTooLarge = errors.New("backup archive is too large")
)

// Archive is a backup file in the node's backup directory.
type Archive struct {
	Name     string   `json:"name"`
	Size     int64    `json:"size"`
	Manifest Manifest `json:"manifest"`
}

// Upload is an inspected archive waiting for confirmation.
type Upload struct {
	ID       string   `json:"id"`
	Size     int64    `json:"size"`
	Manifest Manifest `json:"manifest"`
}

// Pending is a staged restore that will run before the next database open.
type Pending struct {
	Manifest Manifest  `json:"manifest"`
	Size     int64     `json:"size"`
	StagedAt time.Time `json:"staged_at"`
}

// Service manages local backup files and staged restores. Restore itself runs
// before the database is opened, so the running API only validates and stages.
type Service struct {
	db    *storage.DB
	paths config.Paths
	now   func() time.Time
	mu    sync.Mutex
}

// NewService creates a backup service for one node.
func NewService(db *storage.DB, paths config.Paths) *Service {
	return &Service{db: db, paths: paths, now: time.Now}
}

// Create writes a new timestamped backup and returns its description.
func (s *Service) Create(ctx context.Context) (*Archive, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.paths.BackupsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create backup directory: %w", err)
	}
	name := "toget-" + s.now().UTC().Format("20060102-150405") + ".toget-backup"
	dest := filepath.Join(s.paths.BackupsDir, name)
	for n := 2; ; n++ {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("toget-%s-%d.toget-backup", s.now().UTC().Format("20060102-150405"), n)
		dest = filepath.Join(s.paths.BackupsDir, name)
	}
	manifest, err := Create(ctx, s.db, s.paths, dest)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dest)
	if err != nil {
		return nil, fmt.Errorf("stat backup: %w", err)
	}
	return &Archive{Name: name, Size: info.Size(), Manifest: *manifest}, nil
}

// List returns valid local backups, newest first. Corrupt files are reported
// with an empty manifest so the UI can show and remove them.
func (s *Service) List() ([]Archive, error) {
	entries, err := os.ReadDir(s.paths.BackupsDir)
	if errors.Is(err, os.ErrNotExist) {
		return []Archive{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	out := make([]Archive, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toget-backup") {
			continue
		}
		path := filepath.Join(s.paths.BackupsDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		item := Archive{Name: entry.Name(), Size: info.Size()}
		if manifest, err := ReadManifest(path); err == nil {
			item.Manifest = *manifest
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Manifest.CreatedAt.Equal(out[j].Manifest.CreatedAt) {
			return out[i].Manifest.CreatedAt.After(out[j].Manifest.CreatedAt)
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// Path resolves a backup name inside the backup directory.
func (s *Service) Path(name string) (string, error) {
	if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".toget-backup") {
		return "", ErrBackupNotFound
	}
	path := filepath.Join(s.paths.BackupsDir, name)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrBackupNotFound
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", ErrBackupNotFound
	}
	return path, nil
}

// Delete removes a local backup.
func (s *Service) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.Path(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// SaveUpload streams an archive into the upload area, verifies every entry,
// and returns its manifest. The upload must be discarded or staged later.
func (s *Service) SaveUpload(r io.Reader) (*Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.paths.RestoreDir, "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create upload directory: %w", err)
	}
	s.pruneUploadsLocked(dir)
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return nil, fmt.Errorf("create upload: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	limited := io.LimitReader(r, MaxArchiveSize+1)
	n, err := io.Copy(tmp, limited)
	if err != nil {
		tmp.Close()
		return nil, fmt.Errorf("receive backup: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("sync backup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close backup: %w", err)
	}
	if n > MaxArchiveSize {
		return nil, fmt.Errorf("%w: maximum is %d bytes", ErrArchiveTooLarge, MaxArchiveSize)
	}
	manifest, err := Verify(tmpPath)
	if err != nil {
		return nil, err
	}
	id := identity.NewID()
	final := filepath.Join(dir, id+".toget-backup")
	if err := os.Rename(tmpPath, final); err != nil {
		return nil, fmt.Errorf("store upload: %w", err)
	}
	if err := os.Chmod(final, 0o600); err != nil {
		return nil, err
	}
	return &Upload{ID: id, Size: n, Manifest: *manifest}, nil
}

func (s *Service) pruneUploadsLocked(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := s.now().Add(-UploadTTL)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// UploadPath resolves an uploaded archive.
func (s *Service) UploadPath(id string) (string, error) {
	if id == "" || filepath.Base(id) != id {
		return "", ErrBackupNotFound
	}
	path := filepath.Join(s.paths.RestoreDir, "uploads", id+".toget-backup")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrBackupNotFound
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", ErrBackupNotFound
	}
	return path, nil
}

// DiscardUpload removes an inspected upload.
func (s *Service) DiscardUpload(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.UploadPath(id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// StageUpload verifies and stages an inspected upload for the next restart.
func (s *Service) StageUpload(id string) (*Pending, error) {
	path, err := s.UploadPath(id)
	if err != nil {
		return nil, err
	}
	pending, err := s.stage(path)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	return pending, nil
}

// StageBackup stages one of the node's own backups for the next restart.
func (s *Service) StageBackup(name string) (*Pending, error) {
	path, err := s.Path(name)
	if err != nil {
		return nil, err
	}
	return s.stage(path)
}

func (s *Service) stage(source string) (*Pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.paths.RestoreDir, 0o700); err != nil {
		return nil, fmt.Errorf("create restore directory: %w", err)
	}
	manifest, err := Verify(source)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(s.paths.RestoreDir, ".pending-*")
	if err != nil {
		return nil, fmt.Errorf("stage restore: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	in, err := os.Open(source)
	if err != nil {
		tmp.Close()
		return nil, err
	}
	n, err := io.Copy(tmp, io.LimitReader(in, MaxArchiveSize+1))
	in.Close()
	if err != nil {
		tmp.Close()
		return nil, fmt.Errorf("stage restore: %w", err)
	}
	if n > MaxArchiveSize {
		tmp.Close()
		return nil, fmt.Errorf("%w: maximum is %d bytes", ErrArchiveTooLarge, MaxArchiveSize)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	archived := filepath.Join(s.paths.RestoreDir, "pending.toget-backup")
	if err := os.Rename(tmpPath, archived); err != nil {
		return nil, fmt.Errorf("stage restore: %w", err)
	}
	if err := os.Chmod(archived, 0o600); err != nil {
		return nil, err
	}
	staged := &Pending{Manifest: *manifest, Size: n, StagedAt: s.now().UTC().Truncate(time.Second)}
	data, err := json.MarshalIndent(staged, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	meta := filepath.Join(s.paths.RestoreDir, "pending.json")
	tmpMeta := meta + ".tmp"
	if err := os.WriteFile(tmpMeta, data, 0o600); err != nil {
		return nil, fmt.Errorf("write restore metadata: %w", err)
	}
	if err := os.Rename(tmpMeta, meta); err != nil {
		return nil, fmt.Errorf("write restore metadata: %w", err)
	}
	return staged, nil
}

// Pending returns the currently staged restore, if any.
func (s *Service) Pending() (*Pending, error) {
	return ReadPending(s.paths)
}

// CancelPending removes a staged restore. The node keeps running normally.
func (s *Service) CancelPending() error {
	return CancelStaged(s.paths)
}

// Verify checks an archive's manifest, entry list, and file checksums without
// extracting it.
func Verify(path string) (*Manifest, error) {
	manifest, err := ReadManifest(path)
	if err != nil {
		return nil, err
	}
	if manifest.Format != Format || manifest.Version != Version {
		return nil, fmt.Errorf("%w: unsupported format or version", ErrInvalidBackup)
	}
	if len(manifest.Files) == 0 {
		return nil, fmt.Errorf("%w: manifest has no files", ErrInvalidBackup)
	}
	if manifest.SchemaVersion < 0 || manifest.SchemaVersion > storage.CurrentSchemaVersion {
		return nil, fmt.Errorf("%w: database schema %d is newer than this build", ErrInvalidBackup, manifest.SchemaVersion)
	}
	wanted := make(map[string]File, len(manifest.Files))
	for _, f := range manifest.Files {
		if !safeRel(f.Path) || f.Size < 0 || len(f.SHA256) != sha256.Size*2 {
			return nil, fmt.Errorf("%w: invalid manifest entry %q", ErrInvalidBackup, f.Path)
		}
		if _, err := hex.DecodeString(f.SHA256); err != nil {
			return nil, fmt.Errorf("%w: invalid checksum for %q", ErrInvalidBackup, f.Path)
		}
		name := "data/" + f.Path
		if _, exists := wanted[name]; exists {
			return nil, fmt.Errorf("%w: duplicate entry %q", ErrInvalidBackup, name)
		}
		wanted[name] = f
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBackup, err)
	}
	defer zr.Close()
	seen := make(map[string]bool, len(wanted))
	var total int64
	for _, f := range zr.File {
		file, ok := wanted[f.Name]
		if !ok {
			if f.Name == "manifest.json" {
				continue
			}
			return nil, fmt.Errorf("%w: unexpected entry %s", ErrInvalidBackup, f.Name)
		}
		if f.FileInfo().IsDir() || f.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: entry %s is not a regular file", ErrInvalidBackup, f.Name)
		}
		if seen[f.Name] {
			return nil, fmt.Errorf("%w: duplicate entry %s", ErrInvalidBackup, f.Name)
		}
		seen[f.Name] = true
		if f.UncompressedSize64 > uint64(MaxRestoreSize) || total+int64(f.UncompressedSize64) > MaxRestoreSize {
			return nil, fmt.Errorf("%w: maximum is %d bytes", ErrArchiveTooLarge, MaxArchiveSize)
		}
		total += int64(f.UncompressedSize64)
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidBackup, err)
		}
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(rc, file.Size+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: read %s: %v", ErrInvalidBackup, f.Name, err)
		}
		if n != file.Size || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
			return nil, fmt.Errorf("%w: checksum mismatch for %s", ErrInvalidBackup, file.Path)
		}
	}
	for name := range wanted {
		if !seen[name] {
			return nil, fmt.Errorf("%w: missing %s", ErrInvalidBackup, name)
		}
	}
	return manifest, nil
}

// ReadPending reads staged restore metadata without verifying the archive.
func ReadPending(paths config.Paths) (*Pending, error) {
	data, err := os.ReadFile(filepath.Join(paths.RestoreDir, "pending.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoPendingRestore
	}
	if err != nil {
		return nil, err
	}
	var pending Pending
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, fmt.Errorf("read staged restore: %w", err)
	}
	if _, err := os.Stat(filepath.Join(paths.RestoreDir, "pending.toget-backup")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoPendingRestore
		}
		return nil, err
	}
	return &pending, nil
}

// CancelStaged removes a staged restore.
func CancelStaged(paths config.Paths) error {
	dir := paths.RestoreDir
	if err := os.Remove(filepath.Join(dir, "pending.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "pending.toget-backup")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ApplyPending restores a staged archive before the node opens its database.
// A failed restore is quarantined so a corrupt archive cannot cause a boot
// loop; the existing data is left in place by Restore's rollback.
func ApplyPending(ctx context.Context, paths config.Paths) (*Manifest, string, error) {
	_, err := ReadPending(paths)
	if errors.Is(err, ErrNoPendingRestore) {
		return nil, "", nil
	}
	if err != nil {
		quarantinePending(paths)
		return nil, "", err
	}
	archive := filepath.Join(paths.RestoreDir, "pending.toget-backup")
	if _, err := Verify(archive); err != nil {
		quarantinePending(paths)
		return nil, "", err
	}
	manifest, rollback, err := Restore(ctx, paths, archive)
	if err != nil {
		quarantinePending(paths)
		return nil, "", err
	}
	_ = CancelStaged(paths)
	return manifest, rollback, nil
}

func quarantinePending(paths config.Paths) {
	stamp := time.Now().UTC().Format("20060102-150405")
	for _, name := range []string{"pending.json", "pending.toget-backup"} {
		src := filepath.Join(paths.RestoreDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(paths.RestoreDir, "failed-"+stamp+"-"+name)
		_ = os.Rename(src, dst)
	}
}
