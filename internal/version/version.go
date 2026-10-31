// Package version holds the app, protocol, and schema versions.
package version

import (
	"runtime"
	"runtime/debug"
)

const (
	// Protocol is the node protocol version.
	Protocol = 1
	// Schema is the local database schema version. Keep it in sync with
	// storage.CurrentSchemaVersion; a test enforces the match.
	Schema = 10
)

// App is the user-visible application version. It is a variable so release
// builds can set it with:
//
//	-ldflags "-X github.com/sidenor/toget/internal/version.App=1.2.3"
var App = "0.1.0-dev"

// Commit, BuildTime, and ReleaseRootKey are set at build time with -ldflags.
// ReleaseRootKey is the official release trust anchor: a base64url Ed25519
// public key or a path to a release key file. It is empty in development.
var (
	Commit         = "dev"
	BuildTime      = "unknown"
	ReleaseRootKey = ""
)

// Info describes a running binary.
type Info struct {
	App       string `json:"app"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	Protocol  int    `json:"protocol"`
	Schema    int    `json:"schema"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Current returns the version of this binary.
func Current() Info {
	commit, buildTime := Commit, BuildTime
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if commit == "dev" && len(setting.Value) >= 7 {
					commit = setting.Value[:7]
				}
			case "vcs.time":
				if buildTime == "unknown" && setting.Value != "" {
					buildTime = setting.Value
				}
			}
		}
	}
	return Info{
		App:       App,
		Commit:    commit,
		BuildTime: buildTime,
		Protocol:  Protocol,
		Schema:    Schema,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}
