// Package config resolves the local data directory and node settings.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// DefaultListen is the loopback address of the local API and web UI.
	DefaultListen = "127.0.0.1:7718"
	// DefaultNodeListen is the LAN-facing node transport address.
	DefaultNodeListen = "0.0.0.0:7717"
	// NodeListenOff disables the LAN node transport.
	NodeListenOff = "off"

	// DefaultAgentTimeoutSeconds bounds one Agent runtime invocation.
	DefaultAgentTimeoutSeconds = 900
	// DefaultAgentHistoryLimit is the number of recent events given to an
	// Agent when it catches up.
	DefaultAgentHistoryLimit = 200
)

// AgentRuntimeConfig describes how to start an external Agent runtime. The
// command receives a JSON RunRequest on stdin and writes a JSON RunResult (or
// plain reply text) on stdout.
type AgentRuntimeConfig struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Workdir string   `json:"workdir,omitempty"`
	Model   string   `json:"model,omitempty"`
}

// Config is stored in config.json inside the data directory.
type Config struct {
	Listen string `json:"listen"`
	// NodeListen is the HTTPS listener for other Toget nodes. Use "off" to
	// disable the transport entirely.
	NodeListen string `json:"node_listen,omitempty"`
	// NodeAdvertise overrides the host:port placed in invitation codes and
	// room home addresses when the bind address is not reachable by peers.
	NodeAdvertise string `json:"node_advertise,omitempty"`
	// NodeRelay is a pinned relay URL, "auto" to follow the configured
	// directory's default relay, or empty/"off" to disable relay fallback.
	NodeRelay string `json:"node_relay,omitempty"`
	// NodeDirectory is a pinned public directory URL. It publishes this
	// node's signed manifest and resolves "auto" relay discovery.
	NodeDirectory string `json:"node_directory,omitempty"`
	// AgentRuntimes maps an Agent's runtime name to a local command. The
	// built-in echo and claude-code runtimes do not need an entry unless they
	// should be overridden.
	AgentRuntimes map[string]AgentRuntimeConfig `json:"agent_runtimes,omitempty"`
	// AgentTimeoutSeconds bounds one runtime invocation. Zero uses the
	// default.
	AgentTimeoutSeconds int `json:"agent_timeout_seconds,omitempty"`
	// AgentHistoryLimit is the number of recent events handed to an Agent.
	AgentHistoryLimit int `json:"agent_history_limit,omitempty"`
}

// Paths describes the files a local node owns.
type Paths struct {
	Home           string
	DB             string
	VaultKey       string
	APIToken       string
	Config         string
	WebCache       string
	DownloadsDir   string
	AttachmentsDir string
}

// DefaultHome returns the per-user data directory.
func DefaultHome() (string, error) {
	if home := os.Getenv("TOGET_HOME"); home != "" {
		return filepath.Abs(home)
	}

	switch runtime.GOOS {
	case "windows":
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, "Toget"), nil
		}
	case "darwin":
		if dir, err := os.UserHomeDir(); err == nil {
			return filepath.Join(dir, "Library", "Application Support", "Toget"), nil
		}
	default:
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return filepath.Join(dir, "toget"), nil
		}
		if dir, err := os.UserHomeDir(); err == nil {
			return filepath.Join(dir, ".local", "share", "toget"), nil
		}
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}
	return filepath.Join(dir, "toget"), nil
}

// Resolve returns the file paths for a data directory.
func Resolve(home string) (Paths, error) {
	if home == "" {
		var err error
		home, err = DefaultHome()
		if err != nil {
			return Paths{}, err
		}
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home: %w", err)
	}
	return Paths{
		Home:           abs,
		DB:             filepath.Join(abs, "toget.db"),
		VaultKey:       filepath.Join(abs, "vault.key"),
		APIToken:       filepath.Join(abs, "api-token"),
		Config:         filepath.Join(abs, "config.json"),
		WebCache:       filepath.Join(abs, "web"),
		DownloadsDir:   filepath.Join(abs, "downloads"),
		AttachmentsDir: filepath.Join(abs, "attachments"),
	}, nil
}

// Ensure creates the data directory with owner-only permissions.
func Ensure(paths Paths) error {
	if err := os.MkdirAll(paths.Home, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if err := os.Chmod(paths.Home, 0o700); err != nil {
		return fmt.Errorf("secure data directory: %w", err)
	}
	return nil
}

// Load reads config.json, creating it with defaults when missing.
func Load(paths Paths) (Config, error) {
	cfg := Config{Listen: DefaultListen, NodeListen: DefaultNodeListen}
	data, err := os.ReadFile(paths.Config)
	if errors.Is(err, os.ErrNotExist) {
		cfg.applyDefaults()
		if err := Save(paths, cfg); err != nil {
			return Config{}, err
		}
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.AgentTimeoutSeconds <= 0 {
		c.AgentTimeoutSeconds = DefaultAgentTimeoutSeconds
	}
	if c.AgentHistoryLimit <= 0 {
		c.AgentHistoryLimit = DefaultAgentHistoryLimit
	}
	if c.AgentRuntimes == nil {
		c.AgentRuntimes = map[string]AgentRuntimeConfig{
			"claude-code": {Command: "claude"},
		}
	}
}

// Save writes config.json atomically.
func Save(paths Paths, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	tmp := paths.Config + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, paths.Config); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// LoadOrCreateToken returns the local API token, creating it when missing.
func LoadOrCreateToken(path string) (string, error) {
	token, err := ReadToken(path)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate API token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create token directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ReadToken(path)
		}
		return "", fmt.Errorf("create API token: %w", err)
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		return "", fmt.Errorf("write API token: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close API token: %w", err)
	}
	return token, nil
}

// ReadToken reads the local API token.
func ReadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 {
		return "", errors.New("API token file is invalid")
	}
	return token, nil
}
