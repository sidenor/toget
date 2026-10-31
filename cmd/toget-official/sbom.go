package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/sbom"
	"github.com/sidenor/toget/internal/update"
)

func newReleaseSBOMCommand() *cobra.Command {
	var (
		dir, release, out, moduleDir, packages, createdAt string
		force                                             bool
	)
	cmd := &cobra.Command{
		Use:   "sbom",
		Short: "Generate a deterministic SPDX 2.3 SBOM for a release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" || release == "" {
				return errors.New("--dir and --release are required")
			}
			if err := validateReleaseVersion(release); err != nil {
				return err
			}
			if moduleDir == "" {
				moduleDir = "."
			}
			created, err := sbomTime(createdAt)
			if err != nil {
				return err
			}
			patterns, err := splitPatterns(packages)
			if err != nil {
				return err
			}
			if out == "" {
				out = filepath.Join(dir, "sbom", "toget-"+release+".spdx.json")
			}
			document, err := buildSBOM(cmd.Context(), release, moduleDir, patterns, created)
			if err != nil {
				return err
			}
			if err := sbom.WriteFile(out, document, force); err != nil {
				return err
			}
			fmt.Printf("SBOM:       %s\n", out)
			fmt.Printf("Packages:   %d\n", len(document.Packages))
			fmt.Printf("Namespace:  %s\n", document.DocumentNamespace)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "release tree")
	cmd.Flags().StringVar(&release, "release", "", "canonical SemVer release")
	cmd.Flags().StringVar(&out, "out", "", "SBOM output path (default <dir>/sbom/toget-<release>.spdx.json)")
	cmd.Flags().StringVar(&moduleDir, "module-dir", ".", "Go module directory used for the dependency graph")
	cmd.Flags().StringVar(&packages, "packages", "./cmd/toget", "comma-separated Go package patterns to inspect")
	cmd.Flags().StringVar(&createdAt, "created-at", "", "RFC3339 creation time (default SOURCE_DATE_EPOCH or now)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing SBOM")
	return cmd
}

func buildSBOM(ctx context.Context, release, moduleDir string, patterns []string, created time.Time) (*sbom.Document, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	modules, goVersion, err := sbom.CollectGoModules(ctx, moduleDir, patterns)
	if err != nil {
		return nil, err
	}
	return sbom.Build(sbom.Options{
		App:       "toget",
		Version:   release,
		GoVersion: goVersion,
		OS:        "any",
		Arch:      "any",
		CreatedAt: created,
		Modules:   modules,
	})
}

// ensureReleaseSBOM reuses a valid generated SBOM or creates one. Automatic
// generation keeps a release from being published without a dependency list.
func ensureReleaseSBOM(ctx context.Context, dir, release, moduleDir string, created time.Time, regenerate bool) (string, error) {
	if moduleDir == "" {
		moduleDir = "."
	}
	path := filepath.Join(dir, "sbom", "toget-"+release+".spdx.json")
	if !regenerate {
		if _, err := os.Stat(path); err == nil {
			if _, err := sbom.ReadFile(path); err != nil {
				return "", fmt.Errorf("existing SBOM %s is invalid: %w (regenerate with --force or pass --sbom)", path, err)
			}
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	document, err := buildSBOM(ctx, release, moduleDir, []string{"./cmd/toget"}, created)
	if err != nil {
		return "", fmt.Errorf("generate SBOM: %w (pass --no-sbom to publish without one)", err)
	}
	if err := sbom.WriteFile(path, document, regenerate); err != nil {
		return "", err
	}
	return path, nil
}

func validateReleaseVersion(release string) error {
	parsed, err := update.ParseSemVer(release)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if parsed.String() != release {
		return errors.New("release must be a canonical SemVer without a leading v or build metadata")
	}
	return nil
}

func splitPatterns(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	patterns := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("--packages contains an empty pattern")
		}
		patterns = append(patterns, part)
	}
	if len(patterns) == 0 {
		return nil, errors.New("--packages is empty")
	}
	return patterns, nil
}

func sbomTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if epoch := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH")); epoch != "" {
			if parsed, err := time.Parse(time.RFC3339, epoch); err == nil {
				return parsed.UTC(), nil
			}
			seconds, err := strconv.ParseInt(epoch, 10, 64)
			if err != nil || seconds < 0 {
				return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH must be a Unix timestamp or RFC3339 time")
			}
			return time.Unix(seconds, 0).UTC(), nil
		}
		return time.Now().UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("--created-at: %w", err)
	}
	return parsed.UTC(), nil
}
