// Package sbom generates deterministic SPDX 2.3 JSON software bills of
// materials for Toget release artifacts.
package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// SPDXVersion is the emitted SPDX specification version.
	SPDXVersion = "SPDX-2.3"
	// DataLicense is required by the SPDX specification.
	DataLicense = "CC0-1.0"
	// DefaultNamespaceBase is used for document namespaces.
	DefaultNamespaceBase = "https://toget.dev/spdx"
	// ToolCreator identifies the generator in creationInfo.
	ToolCreator = "Tool: toget-official"
)

// Module describes one Go module in the build.
type Module struct {
	Path    string
	Version string
	Main    bool
	Comment string
}

// Options describes one artifact's SBOM.
type Options struct {
	App       string
	Version   string
	GoVersion string
	OS        string
	Arch      string
	CreatedAt time.Time
	Modules   []Module
	// NamespaceBase overrides DefaultNamespaceBase.
	NamespaceBase string
}

// ExternalRef is an SPDX package external reference.
type ExternalRef struct {
	ReferenceCategory string `json:"referenceCategory"`
	ReferenceType     string `json:"referenceType"`
	ReferenceLocator  string `json:"referenceLocator"`
}

// Package is an SPDX package entry.
type Package struct {
	SPDXID           string        `json:"SPDXID"`
	Name             string        `json:"name"`
	VersionInfo      string        `json:"versionInfo,omitempty"`
	DownloadLocation string        `json:"downloadLocation"`
	FilesAnalyzed    bool          `json:"filesAnalyzed"`
	LicenseConcluded string        `json:"licenseConcluded"`
	LicenseDeclared  string        `json:"licenseDeclared"`
	CopyrightText    string        `json:"copyrightText"`
	Comment          string        `json:"comment,omitempty"`
	ExternalRefs     []ExternalRef `json:"externalRefs,omitempty"`
}

// Relationship is an SPDX relationship between two elements.
type Relationship struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
	RelationshipType   string `json:"relationshipType"`
}

// CreationInfo is the SPDX creation metadata.
type CreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

// Document is an SPDX 2.3 JSON document.
type Document struct {
	SPDXID            string         `json:"SPDXID"`
	SPDXVersion       string         `json:"spdxVersion"`
	CreationInfo      CreationInfo   `json:"creationInfo"`
	DataLicense       string         `json:"dataLicense"`
	DocumentNamespace string         `json:"documentNamespace"`
	Name              string         `json:"name"`
	DocumentDescribes []string       `json:"documentDescribes"`
	Packages          []Package      `json:"packages"`
	Relationships     []Relationship `json:"relationships,omitempty"`
}

// Build validates the inputs and returns a deterministic SPDX document.
func Build(opts Options) (*Document, error) {
	opts.App = strings.TrimSpace(opts.App)
	opts.Version = strings.TrimSpace(opts.Version)
	opts.GoVersion = strings.TrimSpace(opts.GoVersion)
	opts.OS = strings.TrimSpace(opts.OS)
	opts.Arch = strings.TrimSpace(opts.Arch)
	if opts.App == "" || len(opts.App) > 128 {
		return nil, errors.New("SBOM app name is empty or too long")
	}
	if opts.Version == "" || len(opts.Version) > 128 {
		return nil, errors.New("SBOM version is empty or too long")
	}
	if len(opts.Modules) == 0 {
		return nil, errors.New("SBOM has no modules")
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now().UTC()
	}
	opts.CreatedAt = opts.CreatedAt.UTC().Truncate(time.Second)

	mainCount := 0
	modules := make([]Module, 0, len(opts.Modules))
	seen := make(map[string]bool, len(opts.Modules))
	for _, module := range opts.Modules {
		module.Path = strings.TrimSpace(module.Path)
		module.Version = strings.TrimSpace(module.Version)
		if module.Path == "" || len(module.Path) > 512 || len(module.Version) > 128 {
			return nil, fmt.Errorf("SBOM module %q is invalid", module.Path)
		}
		key := module.Path + "@" + module.Version + "@" + fmt.Sprint(module.Main)
		if seen[key] {
			continue
		}
		seen[key] = true
		if module.Main {
			mainCount++
		}
		modules = append(modules, module)
	}
	if mainCount != 1 {
		return nil, fmt.Errorf("SBOM requires exactly one main module, got %d", mainCount)
	}
	sort.Slice(modules, func(i, j int) bool {
		if modules[i].Main != modules[j].Main {
			return modules[i].Main
		}
		if modules[i].Path != modules[j].Path {
			return modules[i].Path < modules[j].Path
		}
		return modules[i].Version < modules[j].Version
	})

	usedIDs := map[string]bool{"SPDXRef-DOCUMENT": true}
	packages := make([]Package, 0, len(modules))
	relationships := make([]Relationship, 0, len(modules))
	var mainID string
	for _, module := range modules {
		id := packageID(module, usedIDs)
		pkg := Package{
			SPDXID:           id,
			Name:             module.Path,
			VersionInfo:      module.Version,
			DownloadLocation: "NOASSERTION",
			FilesAnalyzed:    false,
			LicenseConcluded: "NOASSERTION",
			LicenseDeclared:  "NOASSERTION",
			CopyrightText:    "NOASSERTION",
			Comment:          module.Comment,
			ExternalRefs: []ExternalRef{{
				ReferenceCategory: "PACKAGE-MANAGER",
				ReferenceType:     "purl",
				ReferenceLocator:  purl(module),
			}},
		}
		if module.Main {
			mainID = id
			pkg.Name = opts.App
			pkg.VersionInfo = opts.Version
			pkg.LicenseDeclared = "Apache-2.0"
		}
		packages = append(packages, pkg)
	}
	for _, pkg := range packages {
		if pkg.SPDXID == mainID {
			continue
		}
		relationships = append(relationships, Relationship{
			SPDXElementID:      mainID,
			RelatedSPDXElement: pkg.SPDXID,
			RelationshipType:   "DEPENDS_ON",
		})
	}

	namespaceBase := strings.TrimSuffix(strings.TrimSpace(opts.NamespaceBase), "/")
	if namespaceBase == "" {
		namespaceBase = DefaultNamespaceBase
	}
	namespace := fmt.Sprintf("%s/%s-%s-%s-%s-%s", namespaceBase,
		namespaceToken(opts.App), namespaceToken(opts.Version), namespaceToken(opts.OS),
		namespaceToken(opts.Arch), moduleDigest(modules, opts.GoVersion))
	document := &Document{
		SPDXID:      "SPDXRef-DOCUMENT",
		SPDXVersion: SPDXVersion,
		CreationInfo: CreationInfo{
			Created:  opts.CreatedAt.Format(time.RFC3339),
			Creators: []string{ToolCreator},
		},
		DataLicense:       DataLicense,
		DocumentNamespace: namespace,
		Name:              fmt.Sprintf("%s-%s-%s-%s", opts.App, opts.Version, opts.OS, opts.Arch),
		DocumentDescribes: []string{mainID},
		Packages:          packages,
		Relationships: append([]Relationship{{
			SPDXElementID:      "SPDXRef-DOCUMENT",
			RelatedSPDXElement: mainID,
			RelationshipType:   "DESCRIBES",
		}}, relationships...),
	}
	return document, nil
}

func packageID(module Module, used map[string]bool) string {
	base := "SPDXRef-Package-" + namespaceToken(module.Path)
	if module.Version != "" {
		base += "-" + namespaceToken(module.Version)
	}
	id := base
	for i := 2; used[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	used[id] = true
	return id
}

func namespaceToken(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.'
		if ok {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(builder.String(), "-.")
	if result == "" {
		return "unknown"
	}
	if len(result) > 96 {
		sum := sha256.Sum256([]byte(value))
		result = result[:72] + "-" + hex.EncodeToString(sum[:8])
	}
	return result
}

func purl(module Module) string {
	locator := "pkg:golang/" + strings.TrimPrefix(module.Path, "/")
	if module.Version != "" {
		locator += "@" + module.Version
	}
	return locator
}

func moduleDigest(modules []Module, goVersion string) string {
	hasher := sha256.New()
	hasher.Write([]byte("toget-sbom:v1\n"))
	hasher.Write([]byte(goVersion))
	hasher.Write([]byte{'\n'})
	for _, module := range modules {
		hasher.Write([]byte(module.Path))
		hasher.Write([]byte{0})
		hasher.Write([]byte(module.Version))
		hasher.Write([]byte{0})
		if module.Main {
			hasher.Write([]byte("main"))
		}
		hasher.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hasher.Sum(nil))[:16]
}

// Marshal encodes a document with stable indentation and a trailing newline.
func Marshal(document *Document) ([]byte, error) {
	if document == nil {
		return nil, errors.New("SBOM document is nil")
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// WriteFile writes a document atomically. It refuses to replace an existing
// file unless force is set.
func WriteFile(path string, document *Document, force bool) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("SBOM output path is empty")
	}
	data, err := Marshal(document)
	if err != nil {
		return err
	}
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("SBOM %s already exists (use --force to overwrite)", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".toget-sbom-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// ReadFile parses and minimally validates an SPDX document.
func ReadFile(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse SBOM: %w", err)
	}
	if document.SPDXVersion != SPDXVersion {
		return nil, fmt.Errorf("SBOM spdxVersion is %q, want %q", document.SPDXVersion, SPDXVersion)
	}
	if len(document.Packages) == 0 {
		return nil, errors.New("SBOM has no packages")
	}
	return &document, nil
}
