// Package website embeds the built public site served at the official origin.
package website

import (
	"embed"
	"io/fs"
)

// Dist contains the committed Vite build output. Rebuild it with
// `pnpm site:build` at the repository root after changing the site; `go build`
// embeds whatever is present.
//
//go:embed all:dist
var Dist embed.FS

// Assets returns the site root without the dist prefix.
func Assets() fs.FS {
	root, err := fs.Sub(Dist, "dist")
	if err != nil {
		panic("website: embedded dist is missing: " + err.Error())
	}
	return root
}
