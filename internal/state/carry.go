package state

import (
	"fmt"
	"path/filepath"
	"slices"
)

// A session can be left with no host while its run is under way: Claude Code moved the conversation to
// its background service under a new session id, say, and the host that drove the run is gone. Running
// the same plan in a session with no plan of its own then carries that run on, rather than starting the
// plan again from its first phase.

// Orphans lists the runs other than except that have plan file attached and unfinished, and that no live
// host owns: those a session with no plan of its own may carry on (Carry).
func (p *Project) Orphans(file, except string) []RunInfo {
	if file == "" {
		return nil
	}
	file = planPath(file)
	now := p.Now()
	var out []RunInfo
	for _, r := range p.Runs() {
		if r.ID != except && r.File != "" && planPath(r.File) == file && r.HasPlan() && r.State.Mode != ModeComplete && !r.Live(now) {
			out = append(out, r)
		}
	}
	return out
}

// Carry links session to run, so that the session goes on with that run where it stood: its phases done,
// its notes and decisions, the phase in hand. The run must be one Orphans lists for plan file, and the
// session must have no plan of its own: a run of its own with a plan attached keeps it. The session's
// next Bind then makes its host the run's driver.
func (p *Project) Carry(session, run, file string) error {
	if !validID(session) || isWorker(run) {
		return fmt.Errorf("cannot carry run %q on in session %q", run, session)
	}
	file = planPath(file)
	return p.withLock(func() error {
		if !p.runExists(run) {
			return fmt.Errorf("no run %q here", run)
		}
		cur := p.RunOf(session)
		if cur == run {
			return nil
		}
		if cur != "" {
			if mine := p.info(cur, p.refDir(cur, ""), nil); mine.HasPlan() {
				return fmt.Errorf("session %s has a plan of its own, in run %s", session, cur)
			}
		}
		r := p.info(run, p.refDir(run, ""), nil)
		if r.File == "" || planPath(r.File) != file || !r.HasPlan() || r.State.Mode == ModeComplete || r.Live(p.Now()) {
			return fmt.Errorf("run %s is not one of %s to carry on (%s, owned: %v)", run, file, r.State.Mode, r.Live(p.Now()))
		}
		return p.writeLink(run, sessionsDir, session)
	})
}

// planPath is plan file as a carry compares it: absolute, and through any symlink as far as it resolves, so
// that one file named two ways (/var and /private/var on macOS, a project reached through a link) is one
// plan.
func planPath(file string) string {
	if abs, err := filepath.Abs(file); err == nil {
		file = abs
	}
	if real, err := filepath.EvalSymlinks(file); err == nil {
		return real
	}
	return file
}

// Left is the sessions that belonged to run r before session: those that left it.
func (r RunInfo) Left(session string) []string {
	return slices.DeleteFunc(slices.Clone(r.Sessions), func(s string) bool { return s == session })
}
