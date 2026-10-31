//go:build windows

package update

import (
	"errors"
	"fmt"
	"os"
)

// replaceSymlink replaces dest with tmp. MoveFileEx cannot reliably replace a
// directory symlink on Windows, so remove the old link first. The destination
// is checked with Lstat: this never removes a real file or directory.
func replaceSymlink(tmp, dest string) error {
	info, err := os.Lstat(dest)
	if errors.Is(err, os.ErrNotExist) {
		return os.Rename(tmp, dest)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("refusing to replace %s: it is not a symlink", dest)
	}
	if err := os.Remove(dest); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
