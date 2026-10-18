//go:build windows

package state

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// renameFor is how long rename keeps trying while another reader holds the file open.
const renameFor = 2 * time.Second

// rename moves oldpath over newpath for writeAtomic. On Windows a rename over a file that another process
// or goroutine has open fails (access denied, or a sharing violation), and some files are read without
// their lock (Bridge.Peek), so it tries again for a moment, as a reader lets go within milliseconds.
func rename(oldpath, newpath string) error {
	deadline := time.Now().Add(renameFor)
	for wait := time.Millisecond; ; wait = min(2*wait, 50*time.Millisecond) {
		err := os.Rename(oldpath, newpath)
		if err == nil || !(errors.Is(err, fs.ErrPermission) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
	}
}
