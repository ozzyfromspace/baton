// Package version holds the build's version string, stamped at link time:
//
//	go build -ldflags "-X github.com/ozzyfromspace/baton/internal/version.Version=v0.1.0"
package version

// Version is "dev" for local builds and the release tag for published binaries.
var Version = "dev"
