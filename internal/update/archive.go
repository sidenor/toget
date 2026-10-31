package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// MaxExtractedFiles bounds the number of regular files in an archive.
	MaxExtractedFiles = 2048
	// MaxExtractedFile bounds one uncompressed file.
	MaxExtractedFile = 256 << 20
	// MaxExtractedBytes bounds the total uncompressed archive contents.
	MaxExtractedBytes = 1 << 30
	// MaxArchivePath bounds one relative archive path.
	MaxArchivePath = 512
)

// ExtractArchive unpacks a verified release archive into dest, which must not
// exist yet. It returns the absolute path of the executable inside dest.
//
// Only regular files and directories are accepted. Absolute paths, "..",
// links, devices, duplicate entries, and oversized archives are rejected.
func ExtractArchive(archivePath, format, dest, executable string) (string, error) {
	if err := validateExecutablePath(executable); err != nil {
		return "", err
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("release archive is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > MaxArtifactBytes {
		return "", errors.New("release archive has an invalid size")
	}
	if dest == "" {
		return "", errors.New("extraction directory is empty")
	}
	if _, err := os.Lstat(dest); err == nil {
		return "", fmt.Errorf("extraction directory %s already exists", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dest)
		}
	}()

	switch format {
	case FormatTarGz:
		err = extractTarGz(archivePath, dest)
	case FormatZip:
		err = extractZip(archivePath, dest)
	default:
		return "", fmt.Errorf("unsupported archive format %q", format)
	}
	if err != nil {
		return "", err
	}
	target := filepath.Join(dest, filepath.FromSlash(executable))
	resolved, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	info, err = os.Lstat(resolved)
	if err != nil {
		return "", fmt.Errorf("archive does not contain executable %q: %w", executable, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("archive entry %q is not a regular file", executable)
	}
	if err := os.Chmod(resolved, 0o755); err != nil {
		return "", err
	}
	success = true
	return resolved, nil
}

func extractTarGz(archivePath, dest string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := gzip.NewReader(io.LimitReader(file, MaxArtifactBytes+1))
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer reader.Close()
	reader.Multistream(false)
	tarReader := tar.NewReader(reader)
	seen := map[string]string{}
	var files int
	var total int64
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		name, err := cleanArchiveName(header.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if err := addArchiveName(seen, name); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := makeArchiveDir(dest, name); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if strings.HasSuffix(header.Name, "/") {
				return fmt.Errorf("archive file entry %q ends with a slash", name)
			}
			if header.Size < 0 || header.Size > MaxExtractedFile {
				return fmt.Errorf("archive entry %q is too large", name)
			}
			total += header.Size
			if total > MaxExtractedBytes {
				return errors.New("archive contents are too large")
			}
			files++
			if files > MaxExtractedFiles {
				return errors.New("archive contains too many files")
			}
			if err := writeArchiveFile(dest, name, tarReader, header.Size, archiveMode(header.Mode)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry %q has unsupported type %q", name, header.Typeflag)
		}
	}
}

func extractZip(archivePath, dest string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	zipReader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	seen := map[string]string{}
	var files int
	var total int64
	for _, entry := range zipReader.File {
		name, err := cleanArchiveName(entry.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if err := addArchiveName(seen, name); err != nil {
			return err
		}
		if strings.HasSuffix(entry.Name, "/") {
			if err := makeArchiveDir(dest, name); err != nil {
				return err
			}
			continue
		}
		mode := entry.Mode()
		switch {
		case mode.IsDir():
			if err := makeArchiveDir(dest, name); err != nil {
				return err
			}
		case mode.IsRegular():
			if entry.Flags&0x1 != 0 {
				return fmt.Errorf("archive entry %q is encrypted", name)
			}
			size := entry.UncompressedSize64
			if size > MaxExtractedFile {
				return fmt.Errorf("archive entry %q is too large", name)
			}
			total += int64(size)
			if total > MaxExtractedBytes {
				return errors.New("archive contents are too large")
			}
			files++
			if files > MaxExtractedFiles {
				return errors.New("archive contains too many files")
			}
			reader, err := entry.Open()
			if err != nil {
				return fmt.Errorf("open archive entry %q: %w", name, err)
			}
			err = writeArchiveFile(dest, name, reader, int64(size), archiveMode(int64(mode.Perm())))
			reader.Close()
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry %q has unsupported type", name)
		}
	}
	return nil
}

func writeArchiveFile(dest, name string, reader io.Reader, size int64, mode os.FileMode) error {
	target := filepath.Join(dest, filepath.FromSlash(name))
	if err := makeArchiveDir(dest, path.Dir(name)); err != nil && path.Dir(name) != "." {
		return err
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create archive file %q: %w", name, err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(reader, size+1))
	if copyErr != nil {
		file.Close()
		return fmt.Errorf("extract archive file %q: %w", name, copyErr)
	}
	if written != size {
		file.Close()
		return fmt.Errorf("archive entry %q has size %d, expected %d", name, written, size)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync archive file %q: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chmod(target, mode); err != nil {
		return err
	}
	return nil
}

func makeArchiveDir(dest, name string) error {
	if name == "" || name == "." {
		return nil
	}
	target := filepath.Join(dest, filepath.FromSlash(name))
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create archive directory %q: %w", name, err)
	}
	return nil
}

func archiveMode(mode int64) os.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func cleanArchiveName(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("archive contains an empty path")
	}
	if len(raw) > MaxArchivePath {
		return "", fmt.Errorf("archive path is too long: %q", raw)
	}
	if strings.ContainsRune(raw, 0) {
		return "", errors.New("archive path contains NUL")
	}
	if strings.ContainsRune(raw, '\\') {
		return "", fmt.Errorf("archive path %q contains a backslash", raw)
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("archive path %q is absolute", raw)
	}
	name := raw
	for strings.HasPrefix(name, "./") {
		name = strings.TrimPrefix(name, "./")
	}
	if name == "" || name == "." {
		return "", nil
	}
	cleaned := path.Clean(name)
	if cleaned != name && cleaned != strings.TrimSuffix(name, "/") {
		return "", fmt.Errorf("archive path %q is not clean", raw)
	}
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("archive path %q is unsafe", raw)
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("archive path %q is unsafe", raw)
		}
		if len(part) > 255 {
			return "", fmt.Errorf("archive path component is too long: %q", raw)
		}
		if strings.ContainsRune(part, ':') {
			return "", fmt.Errorf("archive path %q contains a colon", raw)
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f {
				return "", fmt.Errorf("archive path %q contains a control character", raw)
			}
		}
		if runtime.GOOS == "windows" {
			if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
				return "", fmt.Errorf("archive path %q is not valid on Windows", raw)
			}
			if windowsReservedName(part) {
				return "", fmt.Errorf("archive path %q uses a reserved Windows name", raw)
			}
		}
	}
	return cleaned, nil
}

// windowsReservedName reports whether a path component names a Windows
// device. Device names are reserved before the first dot, so both "CON" and
// "CON.txt" are unsafe.
func windowsReservedName(part string) bool {
	base := part
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToUpper(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '1' && base[3] <= '9'
}

func addArchiveName(seen map[string]string, name string) error {
	key := name
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		key = strings.ToLower(name)
	}
	if previous, ok := seen[key]; ok {
		return fmt.Errorf("archive contains duplicate entry %q (already %q)", name, previous)
	}
	seen[key] = name
	return nil
}
