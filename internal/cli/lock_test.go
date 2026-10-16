package cli

import (
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/ozzyfromspace/baton/internal/hostcheck"
)

// A command reads whether its session is hosted (which reads the processes above it) before it takes the
// run's lock, never under it.
func TestCommandsAskWhetherHostedBeforeTheLock(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"propose", []string{"propose", "use the other port", "--because", "the first is taken", "--undo", "switch back"}},
		{"done", []string{"done", "P0", "--notes", "the overlay is in"}},
		{"blocked", []string{"blocked", "the key is gone", "--tried", "the vault"}},
		{"checkpoint", []string{"checkpoint", "--notes", "half the overlay"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := attached(t)
			lock := filepath.Join(s.store().Dir, "lock")
			var asked, locked int
			s.host = func() hostcheck.Status {
				asked++
				l := flock.New(lock)
				if ok, _ := l.TryLock(); ok {
					l.Unlock()
				} else {
					locked++
				}
				return hostcheck.Hosted
			}
			s.run("", c.args...)
			if asked == 0 || locked != 0 {
				t.Errorf("asked %d times, %d under the lock", asked, locked)
			}
		})
	}
}

// A plan attached over another keeps the record of the one it replaces, written once the lock is let go.
func TestAnAttachKeepsTheRecordAfterTheLock(t *testing.T) {
	s := attached(t)
	_, planFile := newSession(t)
	s.must("", "attach", planFile, "--suggested", "--replace")
	if got := s.store().Archives(); len(got) != 1 || got[0].Phases != 3 || !s.logged("plan_archived", map[string]any{"generation": float64(1)}) {
		t.Fatalf("archives %+v\n%s", got, s.events())
	}
	s.must("", "drop")
	if got := s.store().Archives(); len(got) != 2 || !s.logged("plan_archived", map[string]any{"generation": float64(2)}) {
		t.Fatalf("archives %+v\n%s", got, s.events())
	}
}
