package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFormatAccountID(t *testing.T) {
	if got := formatAccountID("68173501840479"); got != "68173501-840479" {
		t.Fatalf("formatAccountID = %q", got)
	}
	if got := formatAccountID("short"); got != "short" {
		t.Fatalf("short ID was changed: %q", got)
	}
}

func TestWriteSecretQR(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invite.png")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSecretQR("toget-invite:v1:test", path, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("written QR is not a PNG")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret QR mode = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteRecoveryFileDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recovery.togetkey")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeRecoveryFile(path, []byte("new"), false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("explicit existing path error = %v, want ErrExist", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "old" {
		t.Fatalf("existing file changed: %q", data)
	}
	written, err := writeRecoveryFile(path, []byte("new"), true)
	if err != nil {
		t.Fatal(err)
	}
	if written == path {
		t.Fatal("suffix mode overwrote the existing path")
	}
	if data, _ := os.ReadFile(written); string(data) != "new" {
		t.Fatalf("new file = %q", data)
	}
	info, err := os.Stat(written)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("recovery file mode = %o, want 600", info.Mode().Perm())
	}
	if data, _ := os.ReadFile(path); string(data) != "old" {
		t.Fatalf("existing file changed after suffix write: %q", data)
	}
}

func TestValidateLoopback(t *testing.T) {
	valid := []string{"127.0.0.1:7718", "localhost:7718", "[::1]:7718"}
	for _, listen := range valid {
		if err := validateLoopback(listen); err != nil {
			t.Errorf("validateLoopback(%q) = %v", listen, err)
		}
	}
	invalid := []string{"0.0.0.0:7718", ":7718", "192.168.1.2:7718", "not-an-address"}
	for _, listen := range invalid {
		if err := validateLoopback(listen); err == nil {
			t.Errorf("validateLoopback(%q) succeeded", listen)
		}
	}
}
