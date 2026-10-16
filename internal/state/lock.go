package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// Every lock baton takes on its files (a run's, the project's, a bridge's) is held for a read-modify-write
// of a small file and nothing else: commands, process and transcript reads, network calls and copies are
// done before the lock or after it. So that a hold that breaks this, or a waiter that gives up, can say
// who held the lock, the holder writes into the lock file, while it holds it, its pid, where it took the
// lock from and when (best effort, as is all of this). A waiter that times out logs them (lock_timeout),
// and a holder that kept the lock longer than lockSlow logs where it took it from (lock_slow).

// lockTimeout bounds how long any process waits for another's update; updates take milliseconds. A test
// may shorten it.
var lockTimeout = 5 * time.Second

// lockSlow is how long a hold may last before its holder logs it (lock_slow).
const lockSlow = time.Second

// lockRetry is how often a waiter tries the lock again.
const lockRetry = 20 * time.Millisecond

// A LockWatch is how a lock's holder and its waiters keep time and log what they learn of each other.
type LockWatch struct {
	Now   func() time.Time                         // nil: time.Now
	Event func(kind string, fields map[string]any) // nil: nothing is logged
}

func (w LockWatch) now() time.Time {
	if w.Now == nil {
		return time.Now()
	}
	return w.Now()
}

func (w LockWatch) log(kind string, fields map[string]any) {
	if w.Event != nil {
		w.Event(kind, fields)
	}
}

// lockHolder is what the process holding a lock writes into its file.
type lockHolder struct {
	PID    int       `json:"pid"`
	Caller string    `json:"caller"`
	Since  time.Time `json:"since"`
}

// withLockFile runs fn holding an exclusive lock on path, waiting up to lockTimeout for another holder.
func withLockFile(path string, w LockWatch, fn func() error) error {
	caller := lockCaller()
	l := flock.New(path)
	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()
	if ok, err := l.TryLockContext(ctx, lockRetry); err != nil || !ok {
		return lockTimedOut(path, w, caller, err)
	}
	start := w.now()
	writeHolder(path, lockHolder{PID: os.Getpid(), Caller: caller, Since: start})
	var held time.Duration
	defer func() {
		if held >= lockSlow { // logged once the lock is let go
			w.log("lock_slow", map[string]any{"lock": lockName(path), "caller": caller, "held_for": held.Round(time.Millisecond).String()})
		}
	}()
	defer l.Unlock()
	defer func() {
		held = w.now().Sub(start)
		clearHolder(path)
	}()
	return fn()
}

// lockTimedOut logs, and returns as an error, that the waiter at caller gave up on the lock at path, naming
// the holder its file records, if it records one.
func lockTimedOut(path string, w LockWatch, caller string, err error) error {
	fields := map[string]any{"lock": lockName(path), "waiter": caller}
	h, known := readHolder(path)
	if !known {
		w.log("lock_timeout", fields)
		return fmt.Errorf("baton state is locked by another process (%v)", err)
	}
	held := w.now().Sub(h.Since).Round(time.Millisecond)
	fields["holder"], fields["caller"], fields["held_for"] = h.PID, h.Caller, held.String()
	w.log("lock_timeout", fields)
	return fmt.Errorf("baton state is locked by another process (pid %d, from %s, for %s)", h.PID, h.Caller, held)
}

// holderAt is where the holder's record starts in a lock file: past its first byte, the one byte a lock on
// Windows (gofrs/flock's LockFileEx) bars other handles from, so that it is written and read the same way
// on every platform. A record is one line; what follows its newline is left from a longer one before it.
const holderAt = 1

// writeHolder records h in the lock file at path; best effort.
func writeHolder(path string, h lockHolder) {
	if b, err := json.Marshal(h); err == nil {
		writeAt(path, append(b, '\n'))
	}
}

// clearHolder leaves the lock file at path with an empty record: nobody holds it now, so a waiter that reads
// it learns nothing stale. Best effort.
func clearHolder(path string) { writeAt(path, []byte("\n")) }

func writeAt(path string, b []byte) {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	f.WriteAt(b, holderAt)
	f.Close()
}

// readHolder reads what the holder of the lock at path recorded, if it did.
func readHolder(path string) (lockHolder, bool) {
	f, err := os.Open(path)
	if err != nil {
		return lockHolder{}, false
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.ReadAt(buf, holderAt)
	line, _, _ := bytes.Cut(buf[:n], []byte("\n"))
	var h lockHolder
	if json.Unmarshal(line, &h) != nil || h.PID == 0 {
		return lockHolder{}, false
	}
	return h, true
}

// lockName names the lock at path for a log: its directory and file, as in "<run>/lock".
func lockName(path string) string {
	return filepath.ToSlash(filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path)))
}

// lockCaller is where the lock is being taken from: the first function on the stack past the ones that only
// take a lock for their caller (lockPlumbing), as "pkg.Func (file.go:line)".
func lockCaller() string {
	pcs := make([]uintptr, 16)
	n := runtime.Callers(3, pcs) // past runtime.Callers, lockCaller and withLockFile
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if name := f.Function[strings.LastIndex(f.Function, "/")+1:]; !lockPlumbing(name) {
			return fmt.Sprintf("%s (%s:%d)", name, filepath.Base(f.File), f.Line)
		}
		if !more {
			return "unknown"
		}
	}
}

// lockPlumbing reports whether the function name (package-qualified, as "state.(*Store).Update") only takes a
// lock for its caller: the lock's own helpers here, and the update and locked wrappers of other packages.
func lockPlumbing(name string) bool {
	switch name {
	case "state.withLockFile", "state.Locked", "state.(*Store).withLock", "state.(*Store).Update", "state.(*Store).Load",
		"state.(*Store).SavePlan", "state.(*Project).withLock":
		return true
	}
	return strings.HasSuffix(name, ").update") || strings.HasSuffix(name, ").locked")
}
