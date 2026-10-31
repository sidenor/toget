package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// writeServiceDefinition atomically installs a service definition. Existing
// identical content is left alone; a different definition is only replaced
// with force.
func writeServiceDefinition(path string, content []byte, force bool) (bool, error) {
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if bytes.Equal(existing, content) {
			return false, nil
		}
		if !force {
			return false, fmt.Errorf("%s already exists with different contents (use --force to replace it)", path)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return false, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return false, err
	}
	if _, err := tmp.Write(content); err != nil {
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	return true, nil
}

func runServiceCommand(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func currentUsername() string {
	if value := os.Getenv("USER"); value != "" {
		return value
	}
	if value := os.Getenv("LOGNAME"); value != "" {
		return value
	}
	return "$USER"
}
