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
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// Every lock baton takes on its files (a run's, the project's, a bridge's) is held for a read-modify-write
// of a small file and nothing else: commands, process and transcript reads, network calls and copies are
// done before the lock or after it. So that a hold that breaks this, or a waiter that gives up, can say
// who held the lock, the holder writes into the lock file, once it has held it for holderAfter, its pid,
// where it took the lock from and when (best effort, as is all of this). A waiter that times out logs them
// (lock_timeout), and a holder that kept the lock longer than lockSlow logs where it took it from
// (lock_slow).

// lockTimeout bounds how long any process waits for another's update; updates take milliseconds. A test
// may shorten it.
var lockTimeout = 5 * time.Second

// lockSlow is how long a hold may last before its holder logs it (lock_slow).
const lockSlow = time.Second

// holderAfter is how long a lock is held before its holder records itself in the lock file. A hold as
// short as an update should be writes nothing: v0.8.5 wrote the record and cleared it on every hold, a
// mere read of a run's state included, and on macOS those two writes took most of the time spent under
// the lock. A waiter gives up only after lockTimeout, long after a holder that keeps it waiting has
// recorded itself.
const holderAfter = 100 * time.Millisecond

// afterHold runs record after d, in a goroutine of its own, unless the returned stop is called first (as
// time.AfterFunc); a test may take it over.
var afterHold = func(d time.Duration, record func()) (stop func() bool) { return time.AfterFunc(d, record).Stop }

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
	// The lock is let go however this ends, a panic in the clock or the log included: deferred before
	// anything else runs under it.
	var held time.Duration
	defer func() {
		if held >= lockSlow { // logged once the lock is let go
			w.log("lock_slow", map[string]any{"lock": lockName(path), "caller": caller, "held_for": held.Round(time.Millisecond).String()})
		}
	}()
	defer l.Unlock()
	start := w.now()
	h := holdOn(path, lockHolder{PID: os.Getpid(), Caller: caller, Since: start})
	defer func() {
		h.letGo()
		held = w.now().Sub(start)
	}()
	return fn()
}

// A holding is a lock's holder, which records itself in the lock file once it has held the lock for
// holderAfter, and clears its record as it lets the lock go.
type holding struct {
	path    string
	stop    func() bool
	mu      sync.Mutex
	gone    bool // the lock was let go: nothing is recorded any more
	written bool // the record is in the lock file
}

// holdOn starts the holding of the lock at path by h.
func holdOn(path string, h lockHolder) *holding {
	r := &holding{path: path}
	r.stop = afterHold(holderAfter, func() {
		defer func() { recover() }() // a goroutine of its own: nothing above it would catch a panic
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.gone {
			writeHolder(path, h)
			r.written = true
		}
	})
	return r
}

// letGo ends the holding, before the lock is let go: no record is written from now on, and one written is
// cleared.
func (r *holding) letGo() {
	r.stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gone = true
	if r.written {
		clearHolder(r.path)
	}
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
