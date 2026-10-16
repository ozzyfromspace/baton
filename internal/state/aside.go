package state

import (
	"os"
	"path/filepath"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/transcript"
)

// Claude Code attaches a session's plan-mode file, <plansDirectory>/<slug>.md, in full to the conversation
// after every compaction for as long as it exists (spike 38), on top of the brief baton gives at a phase
// boundary, which quotes what the phase needs of it. So a plan attached from that file is moved out of the
// way, in its folder, to a name of its own (plan.MoveAside), and the run follows it there: as baton attaches
// it, or, for a plan attached before baton did so, at the lead's next phase boundary (State.PlanLooked).
// A file another live run's plan is stays where it is.

// An Aside is what moving a run's plan file needs read before the run's lock (LookAside): the session's
// plan slug, from its transcript, which says whether the file is the session's plan-mode file, and whether
// another live run's plan is that file.
type Aside struct {
	File   string // the plan file looked at
	Slug   string // the session's plan slug (transcript.Slug); "" when not known, and nothing moves
	UsedBy string // the live run, other than the one looked for, whose plan File is
}

// LookAside reads what moving file, the plan of run, needs (Aside), with transcriptPath the session's
// transcript. It reads the transcript and the project's runs, so it is called before the run's lock.
func (p *Project) LookAside(run, file, transcriptPath string) Aside {
	a := Aside{File: file}
	if file == "" || transcriptPath == "" {
		return a
	}
	a.Slug, _ = transcript.Slug(transcriptPath)
	if a.PlanMode(file) {
		a.UsedBy = p.PlanUser(file, run)
	}
	return a
}

// PlanMode reports whether file is the file a looked at, and the session's plan-mode file: <slug>.md.
func (a Aside) PlanMode(file string) bool {
	return file != "" && file == a.File && a.Slug != "" && filepath.Base(file) == a.Slug+".md"
}

// Movable reports whether file may move out of Claude Code's way: it is the session's plan-mode file
// (PlanMode), and no other live run's plan.
func (a Aside) Movable(file string) bool { return a.PlanMode(file) && a.UsedBy == "" }

// Move moves pl's file out of Claude Code's way when a says it may (Movable): it is renamed in its folder
// to a name from pl's title (plan.MoveAside), and pl follows it there, with the path it had as its Origin.
// It reports whether it moved, and err when it tried and could not. It is called under the run's lock,
// before pl is written; Unmove puts it back if that fails.
func (a Aside) Move(pl *plan.Plan) (bool, error) {
	if !a.Movable(pl.File) {
		return false, nil
	}
	to, err := plan.MoveAside(pl.File, pl.Title)
	if err != nil {
		return false, err
	}
	pl.Origin, pl.File = pl.File, to
	return true, nil
}

// Unmove puts pl's file back where Move took it from, when what was to follow the move failed: unless a
// file is there again (Claude Code wrote another plan to it), which it never replaces.
func Unmove(pl *plan.Plan) {
	if pl.Origin == "" {
		return
	}
	if _, err := os.Lstat(pl.Origin); err == nil {
		return
	}
	if os.Rename(pl.File, pl.Origin) == nil {
		pl.File, pl.Origin = pl.Origin, ""
	}
}

// PlanUser is the live run other than except whose plan file is file, or was moved from it (Origin): one a
// host drives now, with a plan attached. "" when there is none.
func (p *Project) PlanUser(file, except string) string {
	file = planPath(file)
	now := p.Now()
	for _, r := range p.Runs() {
		if r.ID != except && r.planIs(file) && r.HasPlan() && r.Live(now) {
			return r.ID
		}
	}
	return ""
}
