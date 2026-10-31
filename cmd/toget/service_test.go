//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateServiceName(t *testing.T) {
	for _, name := range []string{"toget", "toget-node", "a1"} {
		if err := validateServiceName(name); err != nil {
			t.Errorf("validateServiceName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "Toget", "1toget", "toget_node", "toget/node", strings.Repeat("a", 33)} {
		if err := validateServiceName(name); err == nil {
			t.Errorf("validateServiceName(%q) = nil, want error", name)
		}
	}
}

func TestSystemdUnitEscapesPathsAndScopes(t *testing.T) {
	unit := systemdUnit(serviceOptions{
		Name:       "toget",
		Home:       "/srv/Toget $100%",
		Executable: "/opt/Toget 100%/toget",
		Scope:      "user",
	})
	for _, want := range []string{
		`Environment="TOGET_HOME=/srv/Toget $$100%%"`,
		`ExecStart="/opt/Toget 100%%/toget" serve`,
		`WorkingDirectory="/srv/Toget $$100%%"`,
		"WantedBy=default.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("user unit does not contain %q:\n%s", want, unit)
		}
	}

	system := systemdUnit(serviceOptions{
		Name: "toget", Home: "/var/lib/toget", Executable: "/usr/local/bin/toget", Scope: "system",
	})
	if !strings.Contains(system, "WantedBy=multi-user.target") {
		t.Errorf("system unit has the wrong target:\n%s", system)
	}
}

func TestSystemdUnitPath(t *testing.T) {
	dir := t.TempDir()
	path, err := systemdUnitPath(serviceOptions{Name: "toget", UnitDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "toget.service"); path != want {
		t.Errorf("unit path = %q, want %q", path, want)
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err = systemdUnitPath(serviceOptions{Name: "custom", Scope: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd", "user", "custom.service"); path != want {
		t.Errorf("user unit path = %q, want %q", path, want)
	}

	path, err = systemdUnitPath(serviceOptions{Name: "custom", Scope: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "/etc/systemd/system/custom.service"; path != want {
		t.Errorf("system unit path = %q, want %q", path, want)
	}
}

func TestWriteServiceDefinitionIsIdempotentAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "systemd", "user", "toget.service")
	first := []byte("[Unit]\nDescription=one\n")
	changed, err := writeServiceDefinition(path, first, false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first write did not report a change")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", info.Mode().Perm())
	}
	changed, err = writeServiceDefinition(path, first, false)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("identical definition reported a change")
	}
	if _, err := writeServiceDefinition(path, []byte("different"), false); err == nil {
		t.Error("different definition was replaced without force")
	}
	changed, err = writeServiceDefinition(path, []byte("different"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("forced write did not report a change")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "different" {
		t.Errorf("content = %q", data)
	}
}

func TestNormalizeServiceOptions(t *testing.T) {
	oldHome, oldArgs := homeFlag, os.Args
	t.Cleanup(func() {
		homeFlag, os.Args = oldHome, oldArgs
	})
	home := t.TempDir()
	t.Setenv("TOGET_HOME", home)
	homeFlag = ""
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	opts, err := normalizeServiceOptions(serviceOptions{
		Name: "toget", Executable: executable, Scope: "system",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Home != home {
		t.Errorf("home = %q, want %q", opts.Home, home)
	}
	if opts.Scope != "system" || opts.Executable == "" {
		t.Errorf("normalized options = %+v", opts)
	}
	if _, err := normalizeServiceOptions(serviceOptions{Name: "Bad", Executable: executable}, true); err == nil {
		t.Error("invalid name was accepted")
	}
	if _, err := normalizeServiceOptions(serviceOptions{Name: "toget", Executable: executable, Scope: "global"}, true); err == nil {
		t.Error("invalid scope was accepted")
	}
	t.Setenv("TOGET_HOME", home+"\nbad")
	if _, err := normalizeServiceOptions(serviceOptions{Name: "toget", Executable: executable}, true); err == nil {
		t.Error("control character in home was accepted")
	}
}

func TestServiceExecutablePreservesSymlink(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "current-toget")
	if err := os.Symlink(executable, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{link}
	got, err := serviceExecutable("")
	if err != nil {
		t.Fatal(err)
	}
	if got != link {
		t.Errorf("serviceExecutable() = %q, want symlink %q", got, link)
	}
}

func TestUninstallMissingDefinitionIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := uninstallService(context.Background(), serviceOptions{
		Name: "toget", Scope: "user", UnitDir: dir,
	}); err != nil {
		t.Fatalf("uninstall missing definition: %v", err)
	}
}

func TestServiceStatusMissingDefinition(t *testing.T) {
	err := serviceStatus(context.Background(), serviceOptions{
		Name: "toget", Scope: "user", UnitDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("status error = %v, want not installed", err)
	}
}
