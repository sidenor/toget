package website

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedSiteIsComplete(t *testing.T) {
	assets := Assets()
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}
	if !strings.Contains(string(index), `id="root"`) {
		t.Fatal("embedded index does not contain the app root")
	}

	entries, err := fs.ReadDir(assets, "assets")
	if err != nil {
		t.Fatalf("read embedded assets: %v", err)
	}
	var scripts, styles int
	for _, entry := range entries {
		switch {
		case strings.HasSuffix(entry.Name(), ".js"):
			scripts++
		case strings.HasSuffix(entry.Name(), ".css"):
			styles++
		}
	}
	if scripts == 0 || styles == 0 {
		t.Fatalf("embedded build has %d scripts and %d styles, want both", scripts, styles)
	}
}
