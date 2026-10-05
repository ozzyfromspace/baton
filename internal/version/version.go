// Package version holds the build's version string, stamped at link time:
//
//	go build -ldflags "-X github.com/ozzyfromspace/baton/internal/version.Version=v0.1.0"
//
// and who made baton, which every build carries as it is.
package version

// Version is "dev" for local builds and the release tag for published binaries.
var Version = "dev"

// Who made baton and where it lives, printed by baton --version and baton --help.
const (
	Author    = "Oswald Chisala"
	AuthorURL = "https://github.com/ozzyfromspace"
	Homepage  = "https://github.com/ozzyfromspace/baton"
	Copyright = "Copyright (c) 2026 " + Author
	License   = "MIT License"
)

// Credit is the attribution line: "Copyright (c) 2026 Oswald Chisala · MIT License".
func Credit() string { return Copyright + " · " + License }
