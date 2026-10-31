package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

type goListModule struct {
	Path    string        `json:"Path"`
	Version string        `json:"Version"`
	Main    bool          `json:"Main"`
	Replace *goListModule `json:"Replace"`
}

type goListPackage struct {
	ImportPath string        `json:"ImportPath"`
	Standard   bool          `json:"Standard"`
	Module     *goListModule `json:"Module"`
}

// CollectGoModules lists the modules actually used by the given Go package
// patterns. It is intended to run in a release checkout.
func CollectGoModules(ctx context.Context, dir string, patterns []string) ([]Module, string, error) {
	if len(patterns) == 0 {
		patterns = []string{"./cmd/toget"}
	}
	env := cleanGoEnv()
	args := append([]string{"list", "-deps", "-json"}, patterns...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start go list: %w", err)
	}
	modules, err := decodeGoList(stdout)
	if err != nil {
		_ = cmd.Wait()
		return nil, "", fmt.Errorf("go list: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return nil, "", fmt.Errorf("go list: %w: %s", err, message)
		}
		return nil, "", fmt.Errorf("go list: %w", err)
	}
	if len(modules) == 0 {
		return nil, "", errors.New("go list returned no modules")
	}
	goVersion, err := goEnv(ctx, dir, env, "GOVERSION")
	if err != nil {
		return nil, "", err
	}
	return modules, goVersion, nil
}

func goEnv(ctx context.Context, dir string, env []string, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", name)
	cmd.Dir = dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return "", fmt.Errorf("go env %s: %w: %s", name, err, message)
		}
		return "", fmt.Errorf("go env %s: %w", name, err)
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", fmt.Errorf("go env %s returned an empty value", name)
	}
	return value, nil
}

// cleanGoEnv keeps the user's toolchain paths but removes flags that would
// make an SBOM or build depend on ambient configuration.
func cleanGoEnv() []string {
	environ := os.Environ()
	env := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch name {
		case "GOFLAGS", "GOOS", "GOARCH", "CGO_ENABLED", "GO111MODULE", "GOWORK", "SOURCE_DATE_EPOCH":
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "GOFLAGS=", "GO111MODULE=on", "GOWORK=off", "CGO_ENABLED=0")
	return env
}

func decodeGoList(reader io.Reader) ([]Module, error) {
	decoder := json.NewDecoder(reader)
	modules := make(map[string]Module)
	mainPath := ""
	for {
		var pkg goListPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if pkg.Standard || pkg.Module == nil || pkg.Module.Path == "" {
			continue
		}
		module := *pkg.Module
		comment := ""
		if module.Replace != nil && module.Replace.Path != "" {
			comment = "replaces " + module.Path
			if module.Version != "" {
				comment += "@" + module.Version
			}
			module = *module.Replace
		}
		if module.Main {
			if mainPath != "" && mainPath != module.Path {
				return nil, fmt.Errorf("go list returned multiple main modules: %s and %s", mainPath, module.Path)
			}
			mainPath = module.Path
		}
		version := module.Version
		if module.Main {
			version = ""
		}
		key := module.Path + "@" + version
		if existing, ok := modules[key]; ok {
			if comment != "" && existing.Comment == "" {
				existing.Comment = comment
				modules[key] = existing
			}
			continue
		}
		modules[key] = Module{Path: module.Path, Version: version, Main: module.Main, Comment: comment}
	}
	if mainPath == "" {
		return nil, errors.New("go list did not report a main module")
	}
	result := make([]Module, 0, len(modules))
	for _, module := range modules {
		result = append(result, module)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Main != result[j].Main {
			return result[i].Main
		}
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		return result[i].Version < result[j].Version
	})
	return result, nil
}
