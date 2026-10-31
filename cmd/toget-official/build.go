package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/releasebuild"
)

func newReleaseBuildCommand() *cobra.Command {
	var (
		out, release, platforms, repoDir, packagePath string
		rootPublicKey, epochValue                     string
		verify, force, noSBOM, asJSON                 bool
	)
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build deterministic signed-release archives",
		Long: "Build every selected platform with -trimpath, no CGO, and a fixed source date, " +
			"then archive the binaries with stable metadata. With --verify each platform is built twice " +
			"and the archive hashes must match.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if release == "" {
				return errors.New("--release is required")
			}
			if out == "" {
				out = "dist/releases"
			}
			if repoDir == "" {
				repoDir = "."
			}
			if packagePath == "" {
				packagePath = releasebuild.DefaultPackage
			}
			epoch, err := releaseEpoch(epochValue, repoDir)
			if err != nil {
				return err
			}
			var selected []string
			for _, value := range strings.Split(platforms, ",") {
				if value = strings.TrimSpace(value); value != "" {
					selected = append(selected, value)
				}
			}
			artifacts, err := releasebuild.Build(cmd.Context(), releasebuild.Options{
				RepoDir: repoDir, OutDir: out, Release: release,
				Platforms: selected, RootPublicKey: rootPublicKey,
				SourceDateEpoch: epoch, Package: packagePath,
				Force: force, Verify: verify,
				Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			sbomPath := ""
			if !noSBOM {
				sbomPath, err = ensureReleaseSBOM(cmd.Context(), out, release, repoDir, time.Unix(epoch, 0).UTC(), force)
				if err != nil {
					return err
				}
			}
			commit, dirty := repositoryCommit(cmd.Context(), repoDir)
			if asJSON {
				payload := map[string]any{
					"release":   release,
					"epoch":     epoch,
					"commit":    commit,
					"dirty":     dirty,
					"sbom":      sbomPath,
					"artifacts": artifacts,
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(payload)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Release: %s\n", release)
			fmt.Fprintf(cmd.OutOrStdout(), "Epoch:   %s\n", time.Unix(epoch, 0).UTC().Format(time.RFC3339))
			if commit != "" {
				suffix := ""
				if dirty {
					suffix = " (working tree has changes)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Commit:  %s%s\n", commit, suffix)
			}
			if sbomPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "SBOM:    %s\n", sbomPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "dist/releases", "release tree output directory")
	cmd.Flags().StringVar(&release, "release", "", "canonical SemVer release")
	cmd.Flags().StringVar(&platforms, "platforms", releasebuild.DefaultPlatforms, "comma-separated os/arch list")
	cmd.Flags().StringVar(&repoDir, "repo", ".", "repository root")
	cmd.Flags().StringVar(&packagePath, "package", releasebuild.DefaultPackage, "Go package to build")
	cmd.Flags().StringVar(&rootPublicKey, "root-public-key", "", "root public key (base64url or key file) to embed")
	cmd.Flags().StringVar(&epochValue, "epoch", "", "SOURCE_DATE_EPOCH in seconds (default environment or git commit time)")
	cmd.Flags().BoolVar(&verify, "verify", false, "build twice and compare archive hashes")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing archives and SBOM")
	cmd.Flags().BoolVar(&noSBOM, "no-sbom", false, "do not generate an SBOM")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func releaseEpoch(value, repoDir string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH"))
	}
	if value != "" {
		epoch, err := strconv.ParseInt(value, 10, 64)
		if err != nil || epoch < 0 {
			return 0, errors.New("--epoch must be a non-negative Unix timestamp")
		}
		return epoch, nil
	}
	command := exec.Command("git", "-C", repoDir, "log", "-1", "--format=%ct")
	output, err := command.Output()
	if err != nil {
		return 0, nil
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || epoch < 0 {
		return 0, nil
	}
	return epoch, nil
}

// repositoryCommit returns the current commit and whether the working tree
// has changes, for release logs. It is best-effort.
func repositoryCommit(ctx context.Context, dir string) (string, bool) {
	commitCmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	commit, err := commitCmd.Output()
	if err != nil {
		return "", false
	}
	statusCmd := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain")
	status, err := statusCmd.Output()
	dirty := err == nil && len(strings.TrimSpace(string(status))) > 0
	return strings.TrimSpace(string(commit)), dirty
}
