package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sidenor/toget/internal/config"
)

func TestResolveUsesExplicitHome(t *testing.T) {
	dir := t.TempDir()
	paths, err := config.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Home != dir {
		t.Fatalf("home = %q, want %q", paths.Home, dir)
	}
	if paths.DB != filepath.Join(dir, "toget.db") ||
		paths.VaultKey != filepath.Join(dir, "vault.key") ||
		paths.APIToken != filepath.Join(dir, "api-token") {
		t.Fatalf("unexpected paths: %#v", paths)
	}
}

func TestResolveUsesTOGET_HOME(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TOGET_HOME", dir)
	paths, err := config.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Home != dir {
		t.Fatalf("home = %q, want %q", paths.Home, dir)
	}
}

func TestEnsureCreatesPrivateHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nested", "home")
	paths, err := config.Resolve(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("home mode = %o, want 700", mode)
	}
}

func TestLoadCreatesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	paths, err := config.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != config.DefaultListen {
		t.Fatalf("listen = %q, want %q", cfg.Listen, config.DefaultListen)
	}
	info, err := os.Stat(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("config mode = %o, want 600", mode)
	}

	if err := config.Save(paths, config.Config{Listen: "127.0.0.1:9999"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Listen != "127.0.0.1:9999" {
		t.Fatalf("listen = %q", loaded.Listen)
	}
}

func TestLoadRejectsMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	paths, err := config.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Config, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(paths); err == nil {
		t.Fatal("malformed config was accepted")
	}
}

func TestAPITokenIsCreatedOnceAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-token")
	token, err := config.LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 {
		t.Fatalf("token length = %d, want 43", len(token))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("token mode = %o, want 600", mode)
	}
	again, err := config.LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if again != token {
		t.Fatalf("token changed: %q != %q", again, token)
	}
}

func TestShortAPITokenIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-token")
	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ReadToken(path); err == nil {
		t.Fatal("short token was accepted")
	}
}
