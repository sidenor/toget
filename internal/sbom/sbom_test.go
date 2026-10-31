package sbom

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func testModules() []Module {
	return []Module{
		{Path: "github.com/sidenor/toget", Main: true},
		{Path: "github.com/google/uuid", Version: "v1.6.0"},
		{Path: "golang.org/x/crypto", Version: "v0.58.0"},
	}
}

func TestBuildSPDXDeterministic(t *testing.T) {
	created := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	options := Options{
		App: "toget", Version: "1.2.3", GoVersion: "go1.27.3",
		OS: "linux", Arch: "amd64", CreatedAt: created, Modules: testModules(),
	}
	document, err := Build(options)
	if err != nil {
		t.Fatal(err)
	}
	if document.SPDXVersion != SPDXVersion || document.DataLicense != DataLicense || document.SPDXID != "SPDXRef-DOCUMENT" {
		t.Fatalf("document = %+v", document)
	}
	if document.CreationInfo.Created != "2026-10-31T12:00:00Z" {
		t.Fatalf("created = %q", document.CreationInfo.Created)
	}
	if len(document.Packages) != 3 {
		t.Fatalf("packages = %d", len(document.Packages))
	}
	var main Package
	for _, pkg := range document.Packages {
		if pkg.Name == "toget" {
			main = pkg
		}
	}
	if main.SPDXID == "" || main.VersionInfo != "1.2.3" || main.LicenseDeclared != "Apache-2.0" {
		t.Fatalf("main package = %+v", main)
	}
	if len(document.DocumentDescribes) != 1 || document.DocumentDescribes[0] != main.SPDXID {
		t.Fatalf("documentDescribes = %+v", document.DocumentDescribes)
	}
	if len(document.Relationships) != 3 {
		t.Fatalf("relationships = %+v", document.Relationships)
	}
	first, err := Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || !bytes.HasSuffix(first, []byte("\n")) {
		t.Fatal("SBOM marshaling is not deterministic")
	}
	if !bytes.Contains(first, []byte(`"referenceLocator": "pkg:golang/github.com/google/uuid@v1.6.0"`)) {
		t.Fatalf("missing purl:\n%s", first)
	}

	otherPlatform := options
	otherPlatform.Arch = "arm64"
	other, err := Build(otherPlatform)
	if err != nil {
		t.Fatal(err)
	}
	if document.DocumentNamespace == other.DocumentNamespace {
		t.Fatal("namespace did not change with the artifact platform")
	}
}

func TestBuildSPDXRejectsBadInput(t *testing.T) {
	base := Options{App: "toget", Version: "1.0.0", Modules: []Module{{Path: "main", Main: true}}}
	cases := map[string]func(*Options){
		"no app":         func(o *Options) { o.App = "" },
		"no version":     func(o *Options) { o.Version = "" },
		"no modules":     func(o *Options) { o.Modules = nil },
		"no main":        func(o *Options) { o.Modules = []Module{{Path: "dep", Version: "v1.0.0"}} },
		"two main":       func(o *Options) { o.Modules = append(o.Modules, Module{Path: "other", Main: true}) },
		"empty path":     func(o *Options) { o.Modules = []Module{{Main: true}} },
		"long version":   func(o *Options) { o.Version = strings.Repeat("x", 129) },
		"empty module v": func(o *Options) { o.Modules = []Module{{Path: "main", Main: true, Version: strings.Repeat("x", 129)}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			opts := base
			opts.Modules = append([]Module(nil), base.Modules...)
			mutate(&opts)
			if _, err := Build(opts); err == nil {
				t.Fatal("Build accepted invalid input")
			}
		})
	}
}

func TestDecodeGoList(t *testing.T) {
	input := `{"ImportPath":"fmt","Standard":true}
{"ImportPath":"github.com/sidenor/toget/cmd/toget","Module":{"Path":"github.com/sidenor/toget","Main":true}}
{"ImportPath":"github.com/google/uuid","Module":{"Path":"github.com/google/uuid","Version":"v1.6.0"}}
{"ImportPath":"github.com/google/uuid","Module":{"Path":"github.com/google/uuid","Version":"v1.6.0"}}
{"ImportPath":"github.com/old/mod","Module":{"Path":"github.com/old/mod","Version":"v1.0.0","Replace":{"Path":"github.com/new/mod","Version":"v1.1.0"}}}
`
	modules, err := decodeGoList(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 3 {
		t.Fatalf("modules = %+v", modules)
	}
	if !modules[0].Main || modules[0].Path != "github.com/sidenor/toget" {
		t.Fatalf("main module = %+v", modules[0])
	}
	if modules[1].Path != "github.com/google/uuid" || modules[1].Version != "v1.6.0" {
		t.Fatalf("dependency = %+v", modules[1])
	}
	if modules[2].Path != "github.com/new/mod" || modules[2].Comment == "" {
		t.Fatalf("replacement = %+v", modules[2])
	}
}

func TestDecodeGoListRequiresMain(t *testing.T) {
	if _, err := decodeGoList(strings.NewReader(`{"ImportPath":"x","Module":{"Path":"dep","Version":"v1.0.0"}}`)); err == nil {
		t.Fatal("decodeGoList accepted output without a main module")
	}
	if _, err := decodeGoList(strings.NewReader(`not json`)); err == nil {
		t.Fatal("decodeGoList accepted invalid JSON")
	}
}
