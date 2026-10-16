package state

import (
	"os"

	"golang.org/x/sys/unix"
)

// syncFile flushes f's data to the disk before writeAtomic renames it into place. On macOS, File.Sync
// asks for F_FULLFSYNC, which also flushes the drive's own cache, for every file on it: milliseconds a
// call, paid on every write of a run's state, and felt by every other process writing to the disk. A
// plain fsync hands the data to the drive, which is what keeps a reader from ever seeing a renamed file
// without its data; a power cut losing the last write of a small state file is a risk baton takes, as it
// does on Linux.
func syncFile(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) { serr = unix.Fsync(int(fd)) }); err != nil {
		return err
	}
	return serr
}
