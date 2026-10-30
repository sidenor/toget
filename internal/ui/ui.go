// Package ui exposes the embedded local web UI.
package ui

import (
	"io/fs"

	webapp "github.com/sidenor/toget/web/app"
)

// FS returns the built single-page app rooted at its dist directory.
func FS() fs.FS {
	sub, err := fs.Sub(webapp.Dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}
