package resetpage_test

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"

	resetpage "github.com/sidenor/toget/web/reset"
)

func TestEmbeddedResetPage(t *testing.T) {
	assets := resetpage.FS()
	for _, name := range []string{
		"index.html", "main.js", "style.css", "favicon.svg",
		"wasm_exec.js", "toget-reset.wasm",
	} {
		info, err := fs.Stat(assets, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", name)
		}
	}

	wasm, err := fs.ReadFile(assets, "toget-reset.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if len(wasm) < 4 || !bytes.Equal(wasm[:4], []byte{0x00, 0x61, 0x73, 0x6d}) {
		t.Fatal("toget-reset.wasm does not start with the WebAssembly magic")
	}

	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)
	for _, want := range []string{
		`src="wasm_exec.js"`, `src="main.js"`, `href="style.css"`,
		`id="view-reset"`, `id="recovery-file"`, `id="download-button"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html is missing %s", want)
		}
	}
	if strings.Contains(html, "onclick=") || strings.Contains(html, "onload=") {
		t.Fatal("index.html contains inline event handlers, which the CSP forbids")
	}
	if strings.Contains(html, "http://") || strings.Contains(html, "https://") {
		t.Fatal("index.html references an external origin")
	}
}
