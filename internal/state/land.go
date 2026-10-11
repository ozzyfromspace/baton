package state

import (
	"context"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// A worker's work lands in the project's checkout while the lead session works there (the merge queue,
// package loop). The project's landing lock keeps a landing and the lead's tool calls apart: the host
// holds it while it lands, and only when no tool call of a lead in the checkout is running
// (Runtime.InFlight), and each tool call of a lead waits for it before it runs. A tool call recorded
// before the host took the lock holds the landing back; one recorded after waits for the landing.

const landLock = "land.lock"

// HoldLanding takes the landing lock of the project whose .baton is dir, if nobody holds it, and returns
// how to let it go. ok is false when someone holds it.
func HoldLanding(dir string) (release func(), ok bool, err error) {
	l := flock.New(filepath.Join(dir, landLock))
	if ok, err = l.TryLock(); err != nil || !ok {
		return func() {}, false, err
	}
	return func() { l.Unlock() }, true, nil
}

// WaitLanding waits, for up to wait, until no landing holds the lock of the project whose .baton is dir.
// It reports how long it waited, and an error if it gave up.
func WaitLanding(dir string, wait time.Duration) (time.Duration, error) {
	start := time.Now()
	l := flock.New(filepath.Join(dir, landLock))
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	ok, err := l.TryRLockContext(ctx, 20*time.Millisecond)
	if ok {
		l.Unlock()
	}
	return time.Since(start), err
}
