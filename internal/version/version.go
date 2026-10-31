// Package version carries the product build version.
package version

import (
	"fmt"
	"runtime/debug"
)

// These values are overridden at build time. Release builds set all three:
//
//	go build -ldflags "\
//	  -X github.com/ArasanDev/yt/internal/version.Version=v0.1.0 \
//	  -X github.com/ArasanDev/yt/internal/version.Commit=$(git rev-parse --short HEAD) \
//	  -X github.com/ArasanDev/yt/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
//	  ./cmd/yt
//
// Version is the only value MCP clients see; keep it a plain version string.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Current returns the plain version. When a binary is built with
// `go install module@version`, Go records the module version in the build
// info but cannot inject ldflags, so fall back to that instead of reporting
// "dev" for an installed release.
func Current() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return Version
}

// String is the human-facing `yt --version` value. It falls back to the bare
// version when a development build was not stamped with commit metadata.
func String() string {
	v := Current()
	if Commit == "" || Commit == "none" {
		return v
	}
	return fmt.Sprintf("%s (%s, %s)", v, Commit, Date)
}
