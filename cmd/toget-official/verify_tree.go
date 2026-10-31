package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/update"
)

type verifyTreeRelease struct {
	Release   string `json:"release"`
	Manifest  string `json:"manifest"`
	Artifacts int    `json:"artifacts"`
	Bytes     int64  `json:"bytes"`
	Yanked    bool   `json:"yanked,omitempty"`
}

type verifyTreeSummary struct {
	Dir       string              `json:"dir"`
	Latest    string              `json:"latest,omitempty"`
	Releases  []verifyTreeRelease `json:"releases"`
	Channels  int                 `json:"channels"`
	Artifacts int                 `json:"artifacts"`
	Bytes     int64               `json:"bytes"`
	SBOMs     int                 `json:"sboms"`
}

func newReleaseVerifyTreeCommand() *cobra.Command {
	var (
		dir, rootKey  string
		skipArtifacts bool
		asJSON        bool
	)
	cmd := &cobra.Command{
		Use:   "verify-tree",
		Short: "Verify a complete local release tree before publishing",
		Long: "Verify the signed index, every release manifest, every channel, artifact hash and size, " +
			"and every SBOM reference. The tree must be append-only and must not be published if this command fails.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" {
				return errors.New("--dir is required")
			}
			if rootKey == "" {
				return errors.New("--root-key is required")
			}
			anchor, err := update.ParseTrustAnchor(rootKey)
			if err != nil {
				return err
			}
			summary, err := verifyReleaseTree(dir, update.NewVerifier(anchor), skipArtifacts)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Tree:      %s\n", summary.Dir)
			fmt.Fprintf(cmd.OutOrStdout(), "Latest:    %s\n", displayLatest(summary.Latest))
			fmt.Fprintf(cmd.OutOrStdout(), "Releases:  %d\n", len(summary.Releases))
			fmt.Fprintf(cmd.OutOrStdout(), "Channels:  %d\n", summary.Channels)
			fmt.Fprintf(cmd.OutOrStdout(), "Artifacts: %d (%d bytes)\n", summary.Artifacts, summary.Bytes)
			fmt.Fprintf(cmd.OutOrStdout(), "SBOMs:     %d\n", summary.SBOMs)
			if skipArtifacts {
				fmt.Fprintln(cmd.OutOrStdout(), "Artifacts: not hashed (--skip-artifacts)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "release tree")
	cmd.Flags().StringVar(&rootKey, "root-key", "", "root public key or key file")
	cmd.Flags().BoolVar(&skipArtifacts, "skip-artifacts", false, "verify metadata only; do not hash large archives")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func verifyReleaseTree(dir string, verifier *update.Verifier, skipArtifacts bool) (*verifyTreeSummary, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("release tree is not a directory")
	}
	if err := rejectTreeSymlinks(root); err != nil {
		return nil, err
	}
	indexPath := defaultIndexPath(root)
	index, _, err := readReleaseIndex(indexPath, "manifests/index.json", verifier)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("signed release index is missing at %s; run `release index` first", indexPath)
		}
		return nil, err
	}
	summary := &verifyTreeSummary{Dir: root, Latest: index.Latest}
	manifestByName := make(map[string]string, len(index.Releases))
	manifestFiles := make(map[string]bool, len(index.Releases))
	releaseByName := make(map[string]update.IndexEntry, len(index.Releases))
	referenced := make(map[string]bool)
	for _, entry := range index.Releases {
		manifestRef := "manifests/" + filepath.ToSlash(entry.Manifest)
		manifestPath, err := releaseTreePath(root, manifestRef)
		if err != nil {
			return nil, fmt.Errorf("index entry %s: %w", entry.Release, err)
		}
		data, err := readReleaseDocument(manifestPath, update.MaxManifestBytes)
		if err != nil {
			return nil, fmt.Errorf("manifest for %s: %w", entry.Release, err)
		}
		if sum := update.ManifestSHA256(data); sum != entry.ManifestSHA256 {
			return nil, fmt.Errorf("manifest for %s does not match the index SHA-256", entry.Release)
		}
		manifest, err := verifier.VerifyManifest(data, entry.Release)
		if err != nil {
			return nil, fmt.Errorf("manifest for %s: %w", entry.Release, err)
		}
		if err := verifyDocumentSidecar(manifestPath, manifestRef, data, manifest.ReleaseKey); err != nil {
			return nil, fmt.Errorf("manifest for %s: %w", entry.Release, err)
		}
		if manifest.Level != entry.Level || manifest.PublishedAt != entry.PublishedAt {
			return nil, fmt.Errorf("manifest for %s does not match its index level or publication time", entry.Release)
		}
		manifestName := filepath.Base(manifestPath)
		if previous, exists := manifestByName[manifestName]; exists {
			return nil, fmt.Errorf("manifests for %s and %s share the file name %s", previous, entry.Release, manifestName)
		}
		manifestByName[manifestName] = entry.Release
		manifestFiles[filepath.ToSlash(manifestRef)] = true
		releaseByName[entry.Release] = entry
		record := verifyTreeRelease{
			Release: entry.Release, Manifest: filepath.ToSlash(entry.Manifest),
			Artifacts: len(manifest.Artifacts), Yanked: entry.Yanked,
		}
		for _, artifact := range manifest.Artifacts {
			artifactPath, err := artifactTreePath(root, manifest.Release, artifact)
			if err != nil {
				return nil, fmt.Errorf("%s artifact %s/%s: %w", entry.Release, artifact.OS, artifact.Arch, err)
			}
			relative, err := filepath.Rel(root, artifactPath)
			if err != nil {
				return nil, err
			}
			referenced[filepath.ToSlash(relative)] = true
			if skipArtifacts {
				continue
			}
			sum, size, err := hashReleaseFile(artifactPath, update.MaxArtifactBytes)
			if err != nil {
				return nil, fmt.Errorf("%s artifact %s/%s: %w", entry.Release, artifact.OS, artifact.Arch, err)
			}
			if size != artifact.Size {
				return nil, fmt.Errorf("%s artifact %s/%s has size %d, want %d", entry.Release, artifact.OS, artifact.Arch, size, artifact.Size)
			}
			if sum != artifact.SHA256 {
				return nil, fmt.Errorf("%s artifact %s/%s does not match its manifest SHA-256", entry.Release, artifact.OS, artifact.Arch)
			}
			record.Bytes += size
		}
		if manifest.SBOM != nil {
			sbomPath, err := sbomTreePath(root, *manifest.SBOM)
			if err != nil {
				return nil, fmt.Errorf("%s SBOM: %w", entry.Release, err)
			}
			relative, err := filepath.Rel(root, sbomPath)
			if err != nil {
				return nil, err
			}
			referenced[filepath.ToSlash(relative)] = true
			if !skipArtifacts {
				sum, size, err := hashReleaseFile(sbomPath, update.MaxSBOMBytes)
				if err != nil {
					return nil, fmt.Errorf("%s SBOM: %w", entry.Release, err)
				}
				if size != manifest.SBOM.Size || sum != manifest.SBOM.SHA256 {
					return nil, fmt.Errorf("%s SBOM does not match its manifest reference", entry.Release)
				}
				record.Bytes += size
			}
			summary.SBOMs++
		}
		summary.Releases = append(summary.Releases, record)
		summary.Artifacts += len(manifest.Artifacts)
		summary.Bytes += record.Bytes
	}

	channels, err := verifyReleaseChannels(root, verifier, manifestByName, releaseByName)
	if err != nil {
		return nil, err
	}
	summary.Channels = channels
	if err := verifyTreeInventory(root, referenced, manifestFiles); err != nil {
		return nil, err
	}
	return summary, nil
}

func rejectTreeSymlinks(root string) error {
	return filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			return fmt.Errorf("release tree contains a symlink at %s; publish a directory of regular files only", filepath.ToSlash(relative))
		}
		return nil
	})
}

// verifyTreeInventory rejects files that no signed manifest references. This
// keeps an accidental key, log, or editor file from being uploaded with a
// release tree.
func verifyTreeInventory(root string, referenced, manifestFiles map[string]bool) error {
	allowedTop := map[string]bool{"channels": true, "manifests": true, "daemon": true, "sbom": true}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !allowedTop[entry.Name()] {
			return fmt.Errorf("unexpected top-level entry %s in the release tree", entry.Name())
		}
	}
	for _, dir := range []string{"daemon", "sbom"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file", current)
			}
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if !referenced[relative] {
				return fmt.Errorf("%s is not referenced by any signed release manifest", relative)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	manifestsDir := filepath.Join(root, "manifests")
	err = filepath.WalkDir(manifestsDir, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", current)
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "manifests/index.json" || relative == "manifests/index.json.sig" {
			return nil
		}
		if strings.HasSuffix(relative, ".sig") {
			if manifestFiles[strings.TrimSuffix(relative, ".sig")] {
				return nil
			}
			return fmt.Errorf("%s has no matching manifest", relative)
		}
		if !manifestFiles[relative] {
			return fmt.Errorf("%s is not in the signed release index", relative)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func verifyReleaseChannels(root string, verifier *update.Verifier, manifests map[string]string, releases map[string]update.IndexEntry) (int, error) {
	channelsDir := filepath.Join(root, "channels")
	entries, err := os.ReadDir(channelsDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	documents := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			return 0, fmt.Errorf("channels/%s is a directory; channel documents must be regular files", name)
		}
		if strings.HasSuffix(name, ".json.sig") {
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			return 0, fmt.Errorf("channels/%s is not a JSON channel document", name)
		}
		documents[name] = true
		channelName := strings.TrimSuffix(name, ".json")
		channelPath := filepath.Join(channelsDir, name)
		data, err := readReleaseDocument(channelPath, update.MaxChannelBytes)
		if err != nil {
			return 0, fmt.Errorf("channel %s: %w", channelName, err)
		}
		channel, err := verifier.VerifyChannel(data, channelName)
		if err != nil {
			return 0, fmt.Errorf("channel %s: %w", channelName, err)
		}
		if err := verifyDocumentSidecar(channelPath, "channels/"+name, data, channel.ReleaseKey); err != nil {
			return 0, fmt.Errorf("channel %s: %w", channelName, err)
		}
		manifestName, err := channelManifestFile(channel.Manifest)
		if err != nil {
			return 0, fmt.Errorf("channel %s: %w", channelName, err)
		}
		release, ok := manifests[manifestName]
		if !ok {
			return 0, fmt.Errorf("channel %s points at %s, which is not in the signed index", channelName, manifestName)
		}
		if release != channel.Release {
			return 0, fmt.Errorf("channel %s points at the manifest for %s, not %s", channelName, release, channel.Release)
		}
		entry := releases[channel.Release]
		if entry.Yanked {
			return 0, fmt.Errorf("channel %s offers yanked release %s", channelName, channel.Release)
		}
		count++
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json.sig") {
			continue
		}
		if document := strings.TrimSuffix(name, ".sig"); !documents[document] {
			return 0, fmt.Errorf("channels/%s has no matching channel document", name)
		}
	}
	return count, nil
}

func readReleaseDocument(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() <= 0 || info.Size() > limit {
		return nil, fmt.Errorf("%s has an invalid size %d", path, info.Size())
	}
	return os.ReadFile(path)
}

func hashReleaseFile(filePath string, limit int64) (string, int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s is not a regular file", filePath)
	}
	if info.Size() <= 0 || info.Size() > limit {
		return "", 0, fmt.Errorf("%s has an invalid size %d", filePath, info.Size())
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, io.LimitReader(file, limit+1))
	if err != nil {
		return "", 0, err
	}
	if size != info.Size() {
		return "", 0, fmt.Errorf("%s changed while it was being verified", filePath)
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func releaseTreePath(root, reference string) (string, error) {
	if err := validateTreeReference(reference); err != nil {
		return "", err
	}
	for _, part := range strings.Split(filepath.ToSlash(reference), "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid reference %q", reference)
		}
	}
	target := filepath.Join(root, filepath.FromSlash(reference))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("reference %q escapes the release tree", reference)
	}
	return target, nil
}

func artifactTreePath(root, release string, artifact update.Artifact) (string, error) {
	raw := artifact.URL
	if parsed, err := url.Parse(raw); err == nil && parsed.IsAbs() {
		raw = parsed.Path
	}
	parts := splitURLPath(raw)
	wantPlatform := artifact.OS + "-" + artifact.Arch
	wantVersion := "v" + release
	for i := 0; i+3 < len(parts); i++ {
		if parts[i] != "daemon" || parts[i+1] != wantPlatform || parts[i+2] != wantVersion {
			continue
		}
		if i+4 != len(parts) {
			return "", fmt.Errorf("artifact URL does not name one archive")
		}
		return releaseTreePath(root, strings.Join(parts[i:], "/"))
	}
	return "", fmt.Errorf("artifact URL does not contain daemon/%s/%s/", wantPlatform, wantVersion)
}

func sbomTreePath(root string, reference update.FileRef) (string, error) {
	raw := reference.URL
	if parsed, err := url.Parse(raw); err == nil && parsed.IsAbs() {
		raw = parsed.Path
	}
	parts := splitURLPath(raw)
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "sbom" {
			continue
		}
		if i+2 != len(parts) || parts[i+1] == "" {
			return "", errors.New("SBOM URL does not name one file")
		}
		return releaseTreePath(root, strings.Join(parts[i:], "/"))
	}
	return "", fmt.Errorf("SBOM URL %q is not under sbom/", reference.URL)
}

func channelManifestFile(reference string) (string, error) {
	raw := reference
	if parsed, err := url.Parse(reference); err == nil && parsed.IsAbs() {
		raw = parsed.Path
	}
	parts := splitURLPath(raw)
	if len(parts) == 0 {
		return "", errors.New("channel manifest reference is empty")
	}
	name := parts[len(parts)-1]
	if !strings.HasSuffix(name, ".json") {
		return "", errors.New("channel manifest reference does not name a JSON manifest")
	}
	return name, nil
}

func splitURLPath(raw string) []string {
	trimmed := strings.TrimPrefix(raw, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil
		}
	}
	return parts
}

func validateTreeReference(reference string) error {
	if reference == "" || strings.ContainsAny(reference, "\x00\\") {
		return errors.New("reference is empty or contains unsupported characters")
	}
	if path.IsAbs(reference) || strings.HasPrefix(reference, "/") || filepath.IsAbs(reference) {
		return errors.New("reference must be relative")
	}
	return nil
}
