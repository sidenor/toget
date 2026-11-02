package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/sidenor/toget/internal/directory"
	"github.com/sidenor/toget/internal/directoryclient"
	"github.com/sidenor/toget/internal/node"
)

const (
	hostManifestTTL     = 7 * 24 * time.Hour
	hostPublishInterval = 6 * time.Hour
	hostPublishTimeout  = 30 * time.Second
)

// directoryHandler returns the directory API mounted under /directory on the
// account host origin, or nil when the directory is disabled.
func directoryHandler(service *directory.Service) http.Handler {
	if service == nil {
		return nil
	}
	return service.Handler()
}

// directoryEndpoint returns the public origin and optional pinned certificate
// fingerprint this account host should advertise in the directory.
func (h *accountHost) directoryEndpoint() (string, string) {
	if h == nil {
		return "", ""
	}
	if h.publicURL != "" {
		return h.publicURL, ""
	}
	if h.pinnedURL != "" {
		base, fingerprint, err := node.ParsePinnedURL(h.pinnedURL)
		if err == nil {
			return base, fingerprint
		}
	}
	return "", ""
}

// startHostPublisher registers the account host in the embedded directory and
// optionally an external directory, then refreshes the short-lived host
// manifest periodically. Publication failures are retried on the next tick
// instead of taking the account host down.
func startHostPublisher(parent context.Context, host *accountHost, embedded *directory.Service, external string, logger *slog.Logger) {
	if host == nil || (embedded == nil && external == "") {
		return
	}
	go func() {
		publish := func() {
			rawURL, fingerprint := host.directoryEndpoint()
			if rawURL == "" {
				logger.Warn("account host has no public endpoint to publish")
				return
			}
			manifest, err := host.service.SignHostManifest(rawURL, fingerprint, hostManifestTTL)
			if err != nil {
				logger.Warn("could not sign host manifest", "error", err)
				return
			}
			if embedded != nil {
				if err := embedded.PublishHost(host.service.Manifest(), manifest); err != nil {
					logger.Warn("could not publish host record to the embedded directory", "error", err)
				}
			}
			if external != "" {
				ctx, cancel := context.WithTimeout(parent, hostPublishTimeout)
				err := directoryclient.PublishHost(ctx, external, host.service.Manifest(), manifest)
				cancel()
				if err != nil {
					logger.Warn("could not publish host record to the external directory", "url", external, "error", err)
				}
			}
		}
		publish()
		ticker := time.NewTicker(hostPublishInterval)
		defer ticker.Stop()
		for {
			select {
			case <-parent.Done():
				return
			case <-ticker.C:
				publish()
			}
		}
	}()
}
