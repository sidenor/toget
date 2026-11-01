package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sidenor/toget/internal/config"
	"github.com/sidenor/toget/internal/hosted"
	"github.com/sidenor/toget/internal/identity"
	"github.com/sidenor/toget/internal/node"
	resetpage "github.com/sidenor/toget/web/reset"
)

type hostOptions struct {
	Listen          string
	PublicURL       string
	Name            string
	CertPath        string
	KeyPath         string
	DatabasePath    string
	ResetDir        string
	SMTPAddr        string
	SMTPUser        string
	SMTPPassword    string
	SMTPFrom        string
	SMTPFromName    string
	SMTPImplicitTLS bool
}

type accountHost struct {
	store       *hosted.Store
	service     *hosted.Service
	server      *http.Server
	listener    net.Listener
	pinnedURL   string
	publicURL   string
	fingerprint string
	cancel      context.CancelFunc
}

func startAccountHost(parent context.Context, paths config.Paths, options hostOptions, logger *slog.Logger) (*accountHost, error) {
	listen := strings.TrimSpace(options.Listen)
	if listen == "" {
		return nil, errors.New("account host listen address is empty")
	}
	name := strings.TrimSpace(options.Name)
	if name == "" {
		name = "Toget Official Accounts"
	}
	databasePath := options.DatabasePath
	if databasePath == "" {
		databasePath = filepath.Join(paths.Home, "host", "accounts.db")
	}
	certPath := options.CertPath
	keyPath := options.KeyPath
	if certPath == "" && keyPath == "" {
		certPath = filepath.Join(paths.Home, "host", "tls.crt")
		keyPath = filepath.Join(paths.Home, "host", "tls.key")
	}
	if certPath == "" || keyPath == "" {
		return nil, errors.New("both --host-tls-cert and --host-tls-key are required")
	}

	publicHost, err := accountHostAddress(listen, options.PublicURL)
	if err != nil {
		return nil, err
	}
	publicURL := "https://" + publicHost
	if strings.TrimSpace(options.PublicURL) != "" {
		parsed := options.PublicURL
		if !strings.Contains(parsed, "://") {
			parsed = "https://" + parsed
		}
		publicURL = strings.TrimSuffix(parsed, "/")
	}
	endpoints := []identity.Endpoint{{Transport: "https", Addr: publicHost}}
	material, err := hosted.LoadOrCreateTLS(certPath, keyPath, []string{
		hostOnly(publicHost), hostOnly(listen),
	})
	if err != nil {
		return nil, err
	}

	store, err := hosted.OpenStore(databasePath)
	if err != nil {
		return nil, err
	}
	cleanup := func() { store.Close() }
	_, err = store.EnsureHost(parent, name, endpoints)
	if err != nil {
		cleanup()
		return nil, err
	}

	var mailer hosted.Mailer
	if strings.TrimSpace(options.SMTPAddr) != "" {
		password := options.SMTPPassword
		if password == "" {
			password = os.Getenv("TOGET_HOST_SMTP_PASSWORD")
		}
		smtpMailer, err := hosted.NewSMTPMailer(hosted.SMTPConfig{
			Addr: options.SMTPAddr, Username: options.SMTPUser, Password: password,
			From: options.SMTPFrom, FromName: options.SMTPFromName,
			ImplicitTLS: options.SMTPImplicitTLS,
		})
		if err != nil {
			cleanup()
			return nil, err
		}
		mailer = smtpMailer
	}
	resetFS := resetpage.FS()
	if strings.TrimSpace(options.ResetDir) != "" {
		resetFS = os.DirFS(options.ResetDir)
	}

	service, err := hosted.New(hosted.Options{
		Store: store, Mailer: mailer, Logger: logger,
		PublicURL: publicURL, ResetFS: resetFS,
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	handler := service.Handler()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{material.Certificate},
		NextProtos:   []string{"h2", "http/1.1"},
	}
	rawListener, err := net.Listen("tcp", listen)
	if err != nil {
		cleanup()
		return nil, err
	}
	listener := tls.NewListener(rawListener, tlsConfig)
	ctx, cancel := context.WithCancel(parent)
	h := &accountHost{
		store: store, service: service, server: server, listener: listener,
		pinnedURL:   hosted.PinnedURL("https://"+publicHost, material.Fingerprint),
		publicURL:   publicURL,
		fingerprint: material.Fingerprint,
		cancel:      cancel,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("account host stopped", "error", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return h, nil
}

func (h *accountHost) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.cancel()
	err := h.server.Shutdown(ctx)
	if closeErr := h.listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) && err == nil {
		err = closeErr
	}
	if closeErr := h.store.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}

func accountHostAddress(listen, publicURL string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", fmt.Errorf("account host listen address: %w", err)
	}
	if strings.TrimSpace(publicURL) != "" {
		parsed := strings.TrimSpace(publicURL)
		if !strings.Contains(parsed, "://") {
			parsed = "https://" + parsed
		}
		parsedURL, err := url.Parse(parsed)
		if err != nil || parsedURL.Host == "" {
			return "", errors.New("account host public URL is invalid")
		}
		host = parsedURL.Hostname()
		if parsedURL.Port() != "" {
			port = parsedURL.Port()
		} else if parsedURL.Scheme == "https" {
			port = "443"
		}
	} else if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = node.OutboundIP()
	}
	if port == "" {
		return "", errors.New("account host port is empty")
	}
	if strings.Contains(host, ":") {
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), port), nil
}

func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.Trim(addr, "[]")
	}
	return strings.Trim(host, "[]")
}
