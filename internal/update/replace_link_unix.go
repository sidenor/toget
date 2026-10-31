//go:build !windows

package update

import "os"

// replaceSymlink atomically replaces dest with tmp. On Unix, renaming over an
// existing symlink replaces the link itself.
func replaceSymlink(tmp, dest string) error {
	return os.Rename(tmp, dest)
}
