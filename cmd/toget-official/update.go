package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/update"
	"github.com/sidenor/toget/internal/version"
)

func newReleaseCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Generate release keys, manifests, and channels",
		Long: "Generate and sign immutable release manifests and channel pointers. " +
			"Private release keys are never uploaded; only manifests, channels, and artifacts are served.",
	}
	cmd.AddCommand(newReleaseKeygenCommand())
	cmd.AddCommand(newReleaseCertifyCommand())
	cmd.AddCommand(newReleaseRootPublicCommand())
	cmd.AddCommand(newReleaseManifestCommand())
	cmd.AddCommand(newReleaseChannelCommand())
	cmd.AddCommand(newReleaseVerifyCommand())
	cmd.AddCommand(newReleaseVerifyTreeCommand())
	cmd.AddCommand(newReleaseIndexCommand())
	cmd.AddCommand(newReleaseLatestCommand())
	cmd.AddCommand(newReleaseYankCommand())
	cmd.AddCommand(newReleaseSBOMCommand())
	cmd.AddCommand(newReleaseBuildCommand())
	return cmd
}

func newReleaseKeygenCommand() *cobra.Command {
	var kind, out string
	var force bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Create a root or release signing key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if kind == "" {
				return errors.New("--kind is required (root or release)")
			}
			if out == "" {
				return errors.New("--out is required")
			}
			key, err := update.GenerateSigningKey(kind)
			if err != nil {
				return err
			}
			if dir := filepath.Dir(out); dir != "." {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return err
				}
			}
			if err := key.Save(out, force); err != nil {
				return err
			}
			fmt.Printf("Kind:       %s\n", key.Kind)
			fmt.Printf("Key ID:     %s\n", key.KeyID)
			fmt.Printf("Public key: %s\n", base64.RawURLEncoding.EncodeToString(key.Public))
			fmt.Printf("Key file:   %s (mode 0600)\n", out)
			if key.Kind == update.KindRoot {
				fmt.Println("Keep the root key offline; only publish its public key.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "key kind: root or release")
	cmd.Flags().StringVar(&out, "out", "", "private key output path")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing key file")
	return cmd
}

func newReleaseCertifyCommand() *cobra.Command {
	var rootKey, releaseKey, out string
	var validFor time.Duration
	var force bool
	cmd := &cobra.Command{
		Use:   "certify",
		Short: "Certify a release key with the offline root key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if rootKey == "" || releaseKey == "" || out == "" {
				return errors.New("--root-key, --release-key, and --out are required")
			}
			if validFor <= 0 {
				return errors.New("--valid-for must be positive")
			}
			root, err := update.LoadSigningKey(rootKey)
			if err != nil {
				return err
			}
			if root.Kind != update.KindRoot {
				return fmt.Errorf("%s is a %s key, not a root key", rootKey, root.Kind)
			}
			release, err := update.LoadSigningKey(releaseKey)
			if err != nil {
				return err
			}
			if release.Kind != update.KindRelease {
				return fmt.Errorf("%s is a %s key, not a release key", releaseKey, release.Kind)
			}
			now := time.Now().UTC()
			cert, err := update.CertifyRelease(root, release.Public, now.Add(-5*time.Minute), now.Add(validFor))
			if err != nil {
				return err
			}
			if err := writeReleaseDocument(out, cert, force); err != nil {
				return err
			}
			fmt.Printf("Release key: %s\n", cert.KeyID)
			fmt.Printf("Root key:    %s\n", cert.Root)
			fmt.Printf("Valid:       %s to %s\n", cert.NotBefore, cert.NotAfter)
			fmt.Printf("Certificate: %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&rootKey, "root-key", "", "root private key file")
	cmd.Flags().StringVar(&releaseKey, "release-key", "", "release private key file")
	cmd.Flags().StringVar(&out, "out", "", "certificate output path")
	cmd.Flags().DurationVar(&validFor, "valid-for", 90*24*time.Hour, "certificate lifetime")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing certificate")
	return cmd
}

func newReleaseRootPublicCommand() *cobra.Command {
	var rootKey string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "root-public",
		Short: "Print the root public key to embed in Toget builds",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if rootKey == "" {
				return errors.New("--root-key is required")
			}
			key, err := update.LoadSigningKey(rootKey)
			if err != nil {
				return err
			}
			if key.Kind != update.KindRoot {
				return fmt.Errorf("%s is a %s key, not a root key", rootKey, key.Kind)
			}
			public := base64.RawURLEncoding.EncodeToString(key.Public)
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"key_id": key.KeyID, "public_key": public})
			}
			fmt.Printf("Key ID:     %s\n", key.KeyID)
			fmt.Printf("Public key: %s\n", public)
			return nil
		},
	}
	cmd.Flags().StringVar(&rootKey, "root-key", "", "root private key file")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newReleaseManifestCommand() *cobra.Command {
	var (
		dir, release, baseURL, executable string
		protocol, schema, minProtocol     int
		minApp, level, changelog, sbom    string
		out, keyPath, certPath, rootKey   string
		publishedAt, moduleDir            string
		force, noSBOM, noSig              bool
	)
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "Hash artifacts and write a signed release manifest",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" || release == "" {
				return errors.New("--dir and --release are required")
			}
			if baseURL == "" {
				return errors.New("--base-url is required so artifact URLs resolve from the manifest URL")
			}
			if keyPath == "" || certPath == "" || rootKey == "" {
				return errors.New("--key, --cert, and --root-key are required")
			}
			if sbom != "" && noSBOM {
				return errors.New("--sbom and --no-sbom are mutually exclusive")
			}
			key, err := update.LoadSigningKey(keyPath)
			if err != nil {
				return err
			}
			if key.Kind != update.KindRelease {
				return fmt.Errorf("%s is a %s key, not a release key", keyPath, key.Kind)
			}
			cert, err := loadReleaseCertificate(certPath)
			if err != nil {
				return err
			}
			if err := verifyCertificate(cert, rootKey); err != nil {
				return err
			}
			published := time.Now().UTC()
			if publishedAt != "" {
				parsed, err := time.Parse(time.RFC3339, publishedAt)
				if err != nil {
					return fmt.Errorf("--published-at: %w", err)
				}
				published = parsed.UTC()
			}
			if out == "" {
				out = filepath.Join(dir, "manifests", "v"+release+".json")
			}
			if !force {
				if _, err := os.Stat(out); err == nil {
					return fmt.Errorf("%s already exists; published release manifests are immutable (use --force only for an unpublished release)", out)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			if sbom == "" && !noSBOM {
				sbom, err = ensureReleaseSBOM(cmd.Context(), dir, release, moduleDir, published, force)
				if err != nil {
					return err
				}
				fmt.Printf("SBOM:     %s\n", sbom)
			}
			options := update.BuildManifestOptions{
				Dir:         dir,
				Release:     release,
				BaseURL:     baseURL,
				Executable:  executable,
				Protocol:    protocol,
				Schema:      schema,
				MinApp:      minApp,
				MinProtocol: minProtocol,
				Level:       level,
				Changelog:   changelog,
				PublishedAt: published,
				SBOMPath:    sbom,
				Key:         key,
				Cert:        cert,
			}
			manifest, err := update.BuildManifest(options)
			if err != nil {
				return err
			}
			if err := writeSignedReleaseDocument(dir, out, manifest, key, force, noSig); err != nil {
				return err
			}
			fmt.Printf("Release:  %s\n", manifest.Release)
			fmt.Printf("Artifacts: %d\n", len(manifest.Artifacts))
			fmt.Printf("Manifest: %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "release tree containing daemon/<os>-<arch>/v<release>/")
	cmd.Flags().StringVar(&release, "release", "", "canonical SemVer release")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "public base URL of the release tree")
	cmd.Flags().StringVar(&executable, "executable", "", "executable path inside archives (default toget; toget.exe on Windows)")
	cmd.Flags().IntVar(&protocol, "protocol", version.Protocol, "protocol version")
	cmd.Flags().IntVar(&schema, "schema", version.Schema, "database schema version")
	cmd.Flags().IntVar(&minProtocol, "min-protocol", 0, "minimum supported protocol version")
	cmd.Flags().StringVar(&minApp, "min-app", "", "minimum app version allowed to update")
	cmd.Flags().StringVar(&level, "level", update.LevelRecommended, "release level: recommended, security, or forced")
	cmd.Flags().StringVar(&changelog, "changelog", "", "release notes")
	cmd.Flags().StringVar(&sbom, "sbom", "", "SPDX SBOM file (default: generate <dir>/sbom/toget-<release>.spdx.json)")
	cmd.Flags().BoolVar(&noSBOM, "no-sbom", false, "publish without an SBOM")
	cmd.Flags().StringVar(&moduleDir, "module-dir", ".", "Go module directory for automatic SBOM generation")
	cmd.Flags().BoolVar(&noSig, "no-sig", false, "do not write a .sig sidecar")
	cmd.Flags().StringVar(&out, "out", "", "manifest output path (default <dir>/manifests/v<release>.json)")
	cmd.Flags().StringVar(&keyPath, "key", "", "release private key file")
	cmd.Flags().StringVar(&certPath, "cert", "", "release key certificate")
	cmd.Flags().StringVar(&rootKey, "root-key", "", "root public key used to verify the certificate chain")
	cmd.Flags().StringVar(&publishedAt, "published-at", "", "RFC3339 publication time (default now)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing manifest")
	return cmd
}

func newReleaseChannelCommand() *cobra.Command {
	var (
		dir, channel, release, manifestRef, baseURL string
		out, keyPath, certPath, rootKey             string
		publishedAt                                 string
		rollout                                     int
		force, noSig                                bool
	)
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "Write a signed channel pointer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if release == "" {
				return errors.New("--release is required")
			}
			if keyPath == "" || certPath == "" || rootKey == "" {
				return errors.New("--key, --cert, and --root-key are required")
			}
			if channel == "" {
				channel = "stable"
			}
			ref := manifestRef
			if ref == "" && baseURL != "" {
				ref = strings.TrimSuffix(baseURL, "/") + "/manifests/v" + release + ".json"
			}
			if ref == "" {
				return errors.New("--manifest-ref or --base-url is required")
			}
			if out == "" {
				if dir == "" {
					return errors.New("--out is required when --dir is empty")
				}
				out = filepath.Join(dir, "channels", channel+".json")
			}
			key, err := update.LoadSigningKey(keyPath)
			if err != nil {
				return err
			}
			if key.Kind != update.KindRelease {
				return fmt.Errorf("%s is a %s key, not a release key", keyPath, key.Kind)
			}
			cert, err := loadReleaseCertificate(certPath)
			if err != nil {
				return err
			}
			if err := verifyCertificate(cert, rootKey); err != nil {
				return err
			}
			options := update.BuildChannelOptions{
				Channel:     channel,
				Release:     release,
				ManifestRef: ref,
				Rollout:     rollout,
				Key:         key,
				Cert:        cert,
			}
			if publishedAt != "" {
				parsed, err := time.Parse(time.RFC3339, publishedAt)
				if err != nil {
					return fmt.Errorf("--published-at: %w", err)
				}
				options.PublishedAt = parsed
			}
			pointer, err := update.BuildChannel(options)
			if err != nil {
				return err
			}
			if err := writeSignedReleaseDocument(dir, out, pointer, key, force, noSig); err != nil {
				return err
			}
			fmt.Printf("Channel: %s\n", pointer.Channel)
			fmt.Printf("Release: %s\n", pointer.Release)
			fmt.Printf("Rollout: %d%%\n", pointer.Rollout)
			fmt.Printf("Channel file: %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "release tree (for the default channel path)")
	cmd.Flags().StringVar(&channel, "channel", "stable", "channel name")
	cmd.Flags().StringVar(&release, "release", "", "canonical SemVer release")
	cmd.Flags().StringVar(&manifestRef, "manifest-ref", "", "signed manifest URL as seen by clients")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "public base URL used to build the manifest reference")
	cmd.Flags().StringVar(&out, "out", "", "channel output path")
	cmd.Flags().StringVar(&keyPath, "key", "", "release private key file")
	cmd.Flags().StringVar(&certPath, "cert", "", "release key certificate")
	cmd.Flags().StringVar(&rootKey, "root-key", "", "root public key used to verify the certificate chain")
	cmd.Flags().StringVar(&publishedAt, "published-at", "", "RFC3339 publication time (default now)")
	cmd.Flags().IntVar(&rollout, "rollout", 100, "rollout percentage (1-100)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing channel pointer")
	cmd.Flags().BoolVar(&noSig, "no-sig", false, "do not write a .sig sidecar")
	return cmd
}

func newReleaseVerifyCommand() *cobra.Command {
	var (
		channelURL, rootKey, channel string
		current                      string
		protocol, schema             int
		osName, archName             string
		force, allowDowngrade        bool
		asJSON                       bool
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Fetch and verify a channel exactly as a client would",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if channelURL == "" || rootKey == "" {
				return errors.New("--channel-url and --root-key are required")
			}
			if (osName == "") != (archName == "") {
				return errors.New("--os and --arch must be provided together")
			}
			if current == "" {
				current = version.App
			}
			anchor, err := update.ParseTrustAnchor(rootKey)
			if err != nil {
				return err
			}
			client := &update.Client{
				Verifier:   update.NewVerifier(anchor),
				ChannelURL: channelURL,
				Channel:    channel,
				OS:         osName,
				Arch:       archName,
			}
			result, err := client.Check(cmd.Context(), update.Current{App: current, Protocol: protocol, Schema: schema}, "release-verify-rollout-id", update.CheckOptions{
				Force: force, AllowDowngrade: allowDowngrade,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(result)
			}
			release := ""
			if result.Manifest != nil {
				release = result.Manifest.Release
			}
			fmt.Printf("Channel:  %s\n", channel)
			fmt.Printf("Release:  %s\n", release)
			fmt.Printf("Current:  %s\n", current)
			fmt.Printf("Available: %v (%s)\n", result.Available, result.Reason)
			fmt.Printf("Manifest: %s\n", result.ManifestURL)
			if result.Artifact != nil {
				fmt.Printf("Artifact: %s/%s · %d bytes\n", result.Artifact.OS, result.Artifact.Arch, result.Artifact.Size)
				fmt.Printf("SHA-256:  %s\n", result.Artifact.SHA256)
			}
			if result.SBOM != nil {
				fmt.Printf("SBOM:     %s\n", result.SBOM.URL)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&channelURL, "channel-url", "", "signed channel document URL")
	cmd.Flags().StringVar(&rootKey, "root-key", "", "release root public key or key file")
	cmd.Flags().StringVar(&channel, "channel", "stable", "expected channel name")
	cmd.Flags().StringVar(&current, "current", "", "current app version (default build version)")
	cmd.Flags().IntVar(&protocol, "protocol", version.Protocol, "current protocol version")
	cmd.Flags().IntVar(&schema, "schema", version.Schema, "current database schema version")
	cmd.Flags().StringVar(&osName, "os", "", "override artifact operating system")
	cmd.Flags().StringVar(&archName, "arch", "", "override artifact architecture")
	cmd.Flags().BoolVar(&force, "force", false, "ignore staged rollout")
	cmd.Flags().BoolVar(&allowDowngrade, "allow-downgrade", false, "allow a release older than --current")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func loadReleaseCertificate(path string) (update.ReleaseKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return update.ReleaseKey{}, fmt.Errorf("read certificate: %w", err)
	}
	var cert update.ReleaseKey
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cert); err != nil {
		return update.ReleaseKey{}, fmt.Errorf("parse certificate: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return update.ReleaseKey{}, errors.New("certificate contains trailing data")
	}
	return cert, nil
}

func verifyCertificate(cert update.ReleaseKey, rootKey string) error {
	if rootKey == "" {
		return errors.New("a root public key is required to verify the release certificate")
	}
	anchor, err := update.ParseTrustAnchor(rootKey)
	if err != nil {
		return err
	}
	if _, err := update.NewVerifier(anchor).VerifyReleaseKey(cert); err != nil {
		return fmt.Errorf("release certificate: %w", err)
	}
	return nil
}

func writeReleaseDocument(path string, value any, force bool) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o644); err != nil {
		return err
	}
	return nil
}

// writeSignedReleaseDocument writes a signed document and, unless noSig is
// set, a .sig sidecar over the exact bytes. The pair is preflighted so an
// existing immutable document is never partially replaced.
func writeSignedReleaseDocument(dir, path string, value any, key *update.SigningKey, force, noSig bool) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path == "-" {
		if noSig {
			_, err := os.Stdout.Write(data)
			return err
		}
		ref := filepath.Base(path)
		signature, err := update.BuildDetachedSignature(ref, data, key)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(signature)
	}
	var sidecar []byte
	if !noSig {
		ref, err := releaseDocumentRef(dir, path)
		if err != nil {
			return err
		}
		signature, err := update.BuildDetachedSignature(ref, data, key)
		if err != nil {
			return err
		}
		sidecar, err = json.MarshalIndent(signature, "", "  ")
		if err != nil {
			return err
		}
		sidecar = append(sidecar, '\n')
	}
	sidecarPath := path + ".sig"
	if !force {
		for _, candidate := range []string{path, sidecarPath} {
			if _, err := os.Stat(candidate); err == nil {
				return fmt.Errorf("%s already exists (use --force to overwrite)", candidate)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	} else if noSig {
		if err := os.Remove(sidecarPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := writeReleaseBytesAtomic(path, data); err != nil {
		return err
	}
	if sidecar != nil {
		if err := writeReleaseBytesAtomic(sidecarPath, sidecar); err != nil {
			return err
		}
	}
	return nil
}

func releaseDocumentRef(dir, path string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return filepath.Base(path), nil
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return filepath.ToSlash(rel), nil
	}
	return filepath.Base(path), nil
}

func writeReleaseBytesAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".toget-release-*")
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

func startUpdateService(dir, listen, certFile, keyFile string, insecure bool) (func(context.Context) error, string, error) {
	service, err := update.NewService(dir)
	if err != nil {
		return nil, "", err
	}
	if listen == "" {
		listen = "127.0.0.1:7721"
	}
	if !isLoopbackListen(listen) && !insecure && (certFile == "" || keyFile == "") {
		service.Close()
		return nil, "", errors.New("a non-loopback update listener requires --update-tls-cert and --update-tls-key (or --update-insecure-http)")
	}
	if (certFile == "") != (keyFile == "") {
		service.Close()
		return nil, "", errors.New("--update-tls-cert and --update-tls-key must be provided together")
	}
	var certificate tls.Certificate
	if certFile != "" {
		certificate, err = tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			service.Close()
			return nil, "", fmt.Errorf("load update TLS certificate: %w", err)
		}
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		service.Close()
		return nil, "", fmt.Errorf("update listener: %w", err)
	}
	server := &http.Server{
		Handler:           service.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	scheme := "http"
	if certFile != "" {
		scheme = "https"
		server.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
		}
	}
	go func() {
		var serveErr error
		if certFile != "" {
			// The certificate is preloaded above so an unreadable or invalid
			// pair fails before the caller prints a listening address.
			serveErr = server.ServeTLS(listener, "", "")
		} else {
			serveErr = server.Serve(listener)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "toget-official: update service: %v\n", serveErr)
		}
	}()
	shutdown := func(shutdownCtx context.Context) error {
		err := server.Shutdown(shutdownCtx)
		closeErr := service.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	address := listener.Addr().String()
	if scheme == "https" {
		address = "https://" + address
	} else {
		address = "http://" + address
	}
	return shutdown, address, nil
}

func isLoopbackListen(value string) bool {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
