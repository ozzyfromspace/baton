//go:build !windows

package state

import "os"

// rename moves oldpath over newpath for writeAtomic (see rename_windows.go for Windows).
func rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }
