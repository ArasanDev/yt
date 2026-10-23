// Package version carries the product build version.
package version

// Version is the human-facing build version. Release builds can override it:
//
//	go build -ldflags "-X yt/internal/version.Version=v0.3.0" ./cmd/yt
var Version = "dev"
