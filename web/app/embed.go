// Package webapp embeds the built local web UI.
package webapp

import "embed"

// Dist contains the committed Vite build output. Rebuild it with `pnpm build`
// in this directory (or `task web:build` at the repository root) after
// changing the UI; `go build` embeds whatever is present.
//
//go:embed all:dist
var Dist embed.FS
