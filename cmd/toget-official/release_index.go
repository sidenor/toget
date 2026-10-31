package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/update"
)

func newReleaseIndexCommand() *cobra.Command {
	var (
		dir, out, keyPath, certPath, rootKey, publishedAt string
		force, noSig                                      bool
	)
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Verify release manifests and write a signed release index",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" {
				return errors.New("--dir is required")
			}
			if keyPath == "" || certPath == "" || rootKey == "" {
				return errors.New("--key, --cert, and --root-key are required")
			}
			key, cert, verifier, err := loadReleaseSigner(keyPath, certPath, rootKey)
			if err != nil {
				return err
			}
			entries, err := collectReleaseEntries(dir, verifier)
			if err != nil {
				return err
			}
			if out == "" {
				out = defaultIndexPath(dir)
			}
			published := time.Now().UTC()
			if publishedAt != "" {
				parsed, err := time.Parse(time.RFC3339, publishedAt)
				if err != nil {
					return fmt.Errorf("--published-at: %w", err)
				}
				published = parsed.UTC()
			}
			if existing, _, err := readReleaseIndex(out, releaseIndexRef(dir, out), verifier); err == nil {
				byRelease := make(map[string]update.IndexEntry, len(existing.Releases))
				for _, entry := range existing.Releases {
					byRelease[entry.Release] = entry
				}
				for i := range entries {
					old, ok := byRelease[entries[i].Release]
					if !ok {
						continue
					}
					if old.ManifestSHA256 != entries[i].ManifestSHA256 {
						return fmt.Errorf("manifest for published release %s changed; refusing to rebuild the index", entries[i].Release)
					}
					entries[i].Yanked = old.Yanked
					entries[i].YankedAt = old.YankedAt
					entries[i].YankReason = old.YankReason
					delete(byRelease, entries[i].Release)
				}
				for release := range byRelease {
					fmt.Fprintf(os.Stderr, "toget-official: release %s is in the index but its manifest is missing; dropping it\n", release)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			index, err := update.BuildReleaseIndex(update.BuildReleaseIndexOptions{
				Entries: entries, PublishedAt: published, Key: key, Cert: cert,
			})
			if err != nil {
				return err
			}
			if err := writeSignedReleaseDocument(dir, out, index, key, force, noSig); err != nil {
				return err
			}
			fmt.Printf("Releases: %d\n", len(index.Releases))
			fmt.Printf("Latest:   %s\n", displayLatest(index.Latest))
			fmt.Printf("Index:    %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "release tree containing manifests/")
	cmd.Flags().StringVar(&out, "out", "", "index output path (default <dir>/manifests/index.json)")
	cmd.Flags().StringVar(&keyPath, "key", "", "release private key file")
	cmd.Flags().StringVar(&certPath, "cert", "", "release key certificate")
	cmd.Flags().StringVar(&rootKey, "root-key", "", "root key used to verify manifests and the certificate")
	cmd.Flags().StringVar(&publishedAt, "published-at", "", "RFC3339 publication time (default now)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing index")
	cmd.Flags().BoolVar(&noSig, "no-sig", false, "do not write a .sig sidecar")
	return cmd
}

func newReleaseLatestCommand() *cobra.Command {
	var (
		dir, indexPath, rootKey string
		asJSON, all             bool
	)
	cmd := &cobra.Command{
		Use:   "latest",
		Short: "Print the newest non-yanked release from a signed index",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if indexPath == "" {
				if dir == "" {
					return errors.New("--dir or --index is required")
				}
				indexPath = defaultIndexPath(dir)
			}
			if rootKey == "" {
				return errors.New("--root-key is required")
			}
			anchor, err := update.ParseTrustAnchor(rootKey)
			if err != nil {
				return err
			}
			verifier := update.NewVerifier(anchor)
			index, _, err := readReleaseIndex(indexPath, releaseIndexRef(dir, indexPath), verifier)
			if err != nil {
				return err
			}
			if all {
				if asJSON {
					return json.NewEncoder(os.Stdout).Encode(index)
				}
				for _, entry := range index.Releases {
					status := ""
					if entry.Yanked {
						status = " (yanked)"
					}
					fmt.Printf("%s\t%s\t%s%s\n", entry.Release, entry.Level, entry.Manifest, status)
				}
				return nil
			}
			entry := index.LatestEntry()
			if entry == nil {
				return errors.New("every release in the index is yanked")
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(entry)
			}
			fmt.Printf("Release:   %s\n", entry.Release)
			fmt.Printf("Manifest:  manifests/%s\n", entry.Manifest)
			fmt.Printf("SHA-256:   %s\n", entry.ManifestSHA256)
			fmt.Printf("Level:     %s\n", entry.Level)
			fmt.Printf("Published: %s\n", entry.PublishedAt)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "release tree (reads <dir>/manifests/index.json)")
	cmd.Flags().StringVar(&indexPath, "index", "", "signed release index path")
	cmd.Flags().StringVar(&rootKey, "root-key", "", "root key used to verify the index")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().BoolVar(&all, "all", false, "list every release instead of the latest")
	return cmd
}

func newReleaseYankCommand() *cobra.Command {
	var (
		dir, indexPath, release, reason   string
		keyPath, certPath, rootKey        string
		replaceWith, baseURL, publishedAt string
		rollout                           int
		dryRun, force                     bool
	)
	cmd := &cobra.Command{
		Use:   "yank",
		Short: "Mark a release yanked and repoint channels that still offer it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" || release == "" {
				return errors.New("--dir and --release are required")
			}
			if keyPath == "" || certPath == "" || rootKey == "" {
				return errors.New("--key, --cert, and --root-key are required")
			}
			if err := validateReleaseVersion(release); err != nil {
				return err
			}
			reason, err := update.NormalizeYankReason(reason)
			if err != nil {
				return err
			}
			if reason == "" {
				return errors.New("--reason is required so the yank is auditable")
			}
			if replaceWith != "" {
				if err := validateReleaseVersion(replaceWith); err != nil {
					return err
				}
				if replaceWith == release {
					return errors.New("--replace-with must name a different release")
				}
			}
			key, cert, verifier, err := loadReleaseSigner(keyPath, certPath, rootKey)
			if err != nil {
				return err
			}
			if indexPath == "" {
				indexPath = defaultIndexPath(dir)
			}
			index, _, err := readReleaseIndex(indexPath, releaseIndexRef(dir, indexPath), verifier)
			if err != nil {
				return err
			}
			var target *update.IndexEntry
			for i := range index.Releases {
				if index.Releases[i].Release == release {
					target = &index.Releases[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("release %s is not in the index", release)
			}
			if target.Yanked && !force {
				return fmt.Errorf("release %s is already yanked (use --force to update the reason)", release)
			}
			var replacement *update.IndexEntry
			if replaceWith != "" {
				for i := range index.Releases {
					if index.Releases[i].Release == replaceWith {
						replacement = &index.Releases[i]
						break
					}
				}
				if replacement == nil {
					return fmt.Errorf("replacement release %s is not in the index", replaceWith)
				}
				if replacement.Yanked {
					return fmt.Errorf("replacement release %s is yanked", replaceWith)
				}
			}
			channels, err := releaseChannelsForRelease(dir, release, verifier)
			if err != nil {
				return err
			}
			if len(channels) > 0 && replaceWith == "" {
				names := make([]string, 0, len(channels))
				for _, channel := range channels {
					names = append(names, channel.channel.Channel)
				}
				return fmt.Errorf("channels still offer %s: %s; pass --replace-with <release>", release, strings.Join(names, ", "))
			}
			published := time.Now().UTC()
			if publishedAt != "" {
				parsed, err := time.Parse(time.RFC3339, publishedAt)
				if err != nil {
					return fmt.Errorf("--published-at: %w", err)
				}
				published = parsed.UTC()
			}
			target.Yanked = true
			target.YankedAt = formatIndexTime(published)
			target.YankReason = reason
			updated, err := update.BuildReleaseIndex(update.BuildReleaseIndexOptions{
				Entries: index.Releases, PublishedAt: published, Key: key, Cert: cert,
			})
			if err != nil {
				return err
			}
			type channelUpdate struct {
				path string
				doc  *update.Channel
			}
			updates := make([]channelUpdate, 0, len(channels))
			for _, current := range channels {
				manifestRef := ""
				if baseURL != "" {
					manifestRef = strings.TrimSuffix(baseURL, "/") + "/manifests/v" + replaceWith + ".json"
				} else {
					oldName := "v" + release + ".json"
					newName := "v" + replaceWith + ".json"
					if !strings.HasSuffix(current.channel.Manifest, oldName) {
						return fmt.Errorf("channel %s manifest reference %q cannot be rewritten; pass --base-url", current.channel.Channel, current.channel.Manifest)
					}
					manifestRef = strings.TrimSuffix(current.channel.Manifest, oldName) + newName
				}
				doc, err := update.BuildChannel(update.BuildChannelOptions{
					Channel: current.channel.Channel, Release: replaceWith, ManifestRef: manifestRef,
					Rollout: rollout, PublishedAt: published, Key: key, Cert: cert,
				})
				if err != nil {
					return err
				}
				updates = append(updates, channelUpdate{path: current.path, doc: doc})
			}
			if dryRun {
				fmt.Printf("Would yank %s: %s\n", release, reason)
				fmt.Printf("Latest would become: %s\n", displayLatest(updated.Latest))
				for _, update := range updates {
					fmt.Printf("Would repoint channel %s to %s\n", update.doc.Channel, update.doc.Release)
				}
				return nil
			}
			// Write the index first: a failed channel rewrite leaves a visible
			// yank that the operator can finish, not a channel offering a
			// release the catalog still recommends.
			if err := writeSignedReleaseDocument(dir, indexPath, updated, key, true, false); err != nil {
				return err
			}
			for _, change := range updates {
				if err := writeSignedReleaseDocument(dir, change.path, change.doc, key, true, false); err != nil {
					return fmt.Errorf("index was yanked but channel %s was not updated: %w", change.doc.Channel, err)
				}
			}
			fmt.Printf("Yanked:  %s\n", release)
			fmt.Printf("Reason:  %s\n", reason)
			fmt.Printf("Latest:  %s\n", displayLatest(updated.Latest))
			for _, change := range updates {
				fmt.Printf("Channel %s -> %s\n", change.doc.Channel, change.doc.Release)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "release tree")
	cmd.Flags().StringVar(&indexPath, "index", "", "signed index path (default <dir>/manifests/index.json)")
	cmd.Flags().StringVar(&release, "release", "", "release to yank")
	cmd.Flags().StringVar(&reason, "reason", "", "why the release is being yanked")
	cmd.Flags().StringVar(&keyPath, "key", "", "release private key file")
	cmd.Flags().StringVar(&certPath, "cert", "", "release key certificate")
	cmd.Flags().StringVar(&rootKey, "root-key", "", "root key used to verify the index and channels")
	cmd.Flags().StringVar(&replaceWith, "replace-with", "", "release channels should offer instead")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "public base URL used to rewrite channel manifest references")
	cmd.Flags().StringVar(&publishedAt, "published-at", "", "RFC3339 publication time (default now)")
	cmd.Flags().IntVar(&rollout, "rollout", 100, "rollout percentage for repointed channels")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print changes without writing")
	cmd.Flags().BoolVar(&force, "force", false, "allow updating an existing yank")
	return cmd
}

type verifiedChannelFile struct {
	path    string
	channel *update.Channel
}

func defaultIndexPath(dir string) string {
	return filepath.Join(dir, "manifests", "index.json")
}

func loadReleaseSigner(keyPath, certPath, rootKey string) (*update.SigningKey, update.ReleaseKey, *update.Verifier, error) {
	key, err := update.LoadSigningKey(keyPath)
	if err != nil {
		return nil, update.ReleaseKey{}, nil, err
	}
	if key.Kind != update.KindRelease {
		return nil, update.ReleaseKey{}, nil, fmt.Errorf("%s is a %s key, not a release key", keyPath, key.Kind)
	}
	cert, err := loadReleaseCertificate(certPath)
	if err != nil {
		return nil, update.ReleaseKey{}, nil, err
	}
	anchor, err := update.ParseTrustAnchor(rootKey)
	if err != nil {
		return nil, update.ReleaseKey{}, nil, err
	}
	verifier := update.NewVerifier(anchor)
	if _, err := verifier.VerifyReleaseKey(cert); err != nil {
		return nil, update.ReleaseKey{}, nil, fmt.Errorf("release certificate: %w", err)
	}
	return key, cert, verifier, nil
}

func collectReleaseEntries(dir string, verifier *update.Verifier) ([]update.IndexEntry, error) {
	manifestsDir := filepath.Join(dir, "manifests")
	entries, err := os.ReadDir(manifestsDir)
	if err != nil {
		return nil, fmt.Errorf("manifests directory: %w", err)
	}
	result := make([]update.IndexEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "v") || !strings.HasSuffix(name, ".json") {
			continue
		}
		release := strings.TrimSuffix(strings.TrimPrefix(name, "v"), ".json")
		if err := validateReleaseVersion(release); err != nil {
			return nil, fmt.Errorf("manifest %s: %w", name, err)
		}
		path := filepath.Join(manifestsDir, name)
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("manifest %s is not a regular file", name)
		}
		if info.Size() > update.MaxManifestBytes {
			return nil, fmt.Errorf("manifest %s is too large", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		manifest, err := verifier.VerifyManifest(data, release)
		if err != nil {
			return nil, fmt.Errorf("manifest %s: %w", name, err)
		}
		if err := verifyDocumentSidecar(path, "manifests/"+name, data, manifest.ReleaseKey); err != nil {
			return nil, fmt.Errorf("manifest %s: %w", name, err)
		}
		result = append(result, update.IndexEntry{
			Release:        manifest.Release,
			Manifest:       name,
			ManifestSHA256: update.ManifestSHA256(data),
			Level:          manifest.Level,
			PublishedAt:    manifest.PublishedAt,
		})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no release manifests found in %s", manifestsDir)
	}
	return result, nil
}

func readReleaseIndex(path, document string, verifier *update.Verifier) (*update.ReleaseIndex, []byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > update.MaxManifestBytes {
		return nil, nil, fmt.Errorf("%s is too large", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	index, err := verifier.VerifyReleaseIndex(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := verifyDocumentSidecar(path, document, data, index.ReleaseKey); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return index, data, nil
}

func releaseIndexRef(dir, path string) string {
	if dir != "" {
		if ref, err := releaseDocumentRef(dir, path); err == nil {
			return ref
		}
	}
	if filepath.Base(filepath.Dir(path)) == "manifests" {
		return "manifests/" + filepath.Base(path)
	}
	return filepath.Base(path)
}

func verifyDocumentSidecar(path, document string, data []byte, cert update.ReleaseKey) error {
	sidecarPath := path + ".sig"
	sidecarData, err := os.ReadFile(sidecarPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var signature update.DetachedSignature
	decoder := json.NewDecoder(bytes.NewReader(sidecarData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&signature); err != nil {
		return fmt.Errorf("parse %s: %w", sidecarPath, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s contains trailing data", sidecarPath)
	}
	if err := update.VerifyDetachedSignature(document, data, cert, signature); err != nil {
		return fmt.Errorf("%s: %w", sidecarPath, err)
	}
	return nil
}

func releaseChannelsForRelease(dir, release string, verifier *update.Verifier) ([]verifiedChannelFile, error) {
	channelsDir := filepath.Join(dir, "channels")
	entries, err := os.ReadDir(channelsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []verifiedChannelFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(channelsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		channel, err := verifier.VerifyChannel(data, "")
		if err != nil {
			return nil, fmt.Errorf("channel %s: %w", name, err)
		}
		if err := verifyDocumentSidecar(path, "channels/"+name, data, channel.ReleaseKey); err != nil {
			return nil, fmt.Errorf("channel %s: %w", name, err)
		}
		if channel.Release == release {
			result = append(result, verifiedChannelFile{path: path, channel: channel})
		}
	}
	return result, nil
}

func formatIndexTime(value time.Time) string {
	return value.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func displayLatest(release string) string {
	if release == "" {
		return "(none; every release is yanked)"
	}
	return release
}
