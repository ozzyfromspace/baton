package state

// WriteFile replaces path with data so that readers never see a partial file: it writes a temporary file
// beside path, named after it with a leading dot (".<name>.tmp-…"), and renames it over path.
func WriteFile(path string, data []byte) error { return writeAtomic(path, data) }

// Locked runs fn holding an exclusive lock on path, waiting for another process's as long as an update of
// a run's state does.
func Locked(path string, fn func() error) error { return withLockFile(path, fn) }
