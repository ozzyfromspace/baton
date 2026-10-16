//go:build !darwin

package state

import "os"

// syncFile flushes f's data to the disk before writeAtomic renames it into place: File.Sync, which is
// fsync on Linux and FlushFileBuffers on Windows (see sync_darwin.go for macOS).
func syncFile(f *os.File) error { return f.Sync() }
