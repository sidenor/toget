// Package resetpage embeds the static account recovery page served by the
// official Toget account host at /reset/.
//
// The page performs OPAQUE registration, recovery-file unwrapping, and key
// re-encryption in WebAssembly in the browser. Nothing secret is sent to the
// host. Rebuild the assets with web/reset/build.sh after editing index.html,
// main.js, style.css, or the WASM command.
package resetpage

import (
	"embed"
	"io/fs"
)

//go:embed dist
var content embed.FS

// FS returns the reset page's static files.
func FS() fs.FS {
	sub, err := fs.Sub(content, "dist")
	if err != nil {
		panic("resetpage: embedded assets are missing: " + err.Error())
	}
	return sub
}
