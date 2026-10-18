package risk

import (
	"context"
	"slices"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Record is what baton has of a phase as it scores it.
type Record struct {
	Phase string
	Plan  plan.Plan
	Doc   []byte // the plan document, for the paths other phases' sections name; nil: none
	// Lead is the run's state; Sub the sub-run of the phase agent that worked on the phase (nil for a phase
	// of the lead's own), where its record is kept.
	Lead state.State
	Sub  *state.State
	// Root is the lead's checkout, where git reads the phase's commits.
	Root string
}

// Gather reads the facts of rec's phase: its record, and from git what its own commits changed. A phase
// agent's commits are those its landing brought (state.Landing.Commits), a lead phase's those of the
// checkout from where the phase started until it was done, less any landing's. Git that cannot say leaves
// the git factors out (Facts.GitKnown). It runs git, so it is called before any lock is taken.
func Gather(ctx context.Context, rec Record) Facts {
	own := &rec.Lead
	if rec.Sub != nil {
		own = rec.Sub
	}
	f := Facts{Phase: rec.Phase, ChecksKnown: true}
	f.Files, f.GitKnown = files(ctx, rec)
	f.Named = named(rec)
	f.DespiteRed = own.FinishedDespiteRed(rec.Phase) || rec.Lead.FinishedDespiteRed(rec.Phase)
	for _, d := range own.Decisions {
		if d.Phase == rec.Phase {
			f.Decisions++
		}
	}
	f.Conflicts = rec.Lead.LandingsReturned(rec.Phase)
	f.Checks = own.Checks(rec.Phase)
	for _, j := range own.Jobs {
		if j.Phase == rec.Phase && !j.Requeue { // the merge queue's verification again is not the phase's own check
			f.Jobs++
		}
	}
	if ps := own.Phases[rec.Phase]; ps != nil {
		f.Deviation = ps.Deviation
	}
	return f
}

// files is what rec's phase's own commits changed, and whether git could say.
func files(ctx context.Context, rec Record) ([]gitx.FileStat, bool) {
	if rec.Root == "" || !gitx.Usable(rec.Root) {
		return nil, false
	}
	var commits []string
	if l := landing(rec.Lead, rec.Phase); l != nil {
		commits = l.Commits
	} else {
		ps := rec.Lead.Phases[rec.Phase]
		if ps == nil || ps.StartHead == "" {
			return nil, false
		}
		to := "HEAD"
		if rec.Lead.RunBranch != "" && gitx.RevParse(rec.Root, "refs/heads/"+rec.Lead.RunBranch) != "" {
			to = "refs/heads/" + rec.Lead.RunBranch
		}
		mine, err := gitx.CommitsUntil(ctx, rec.Root, ps.StartHead, to, ps.DoneAt)
		if err != nil {
			return nil, false
		}
		landed := map[string]bool{}
		for _, l := range rec.Lead.Landed {
			for _, c := range l.Commits {
				landed[c] = true
			}
		}
		for _, c := range mine {
			if !landed[c] {
				commits = append(commits, c)
			}
		}
	}
	stats, err := gitx.Stats(ctx, rec.Root, commits)
	if err != nil {
		return nil, false
	}
	return stats, true
}

// landing is the latest landing of phase on record, or nil.
func landing(st state.State, phase string) *state.Landing {
	for i := len(st.Landed) - 1; i >= 0; i-- {
		if st.Landed[i].Phase == phase {
			return &st.Landed[i]
		}
	}
	return nil
}

// named is the paths the sections of rec's plan name, but for its phase's own.
func named(rec Record) []string {
	if len(rec.Doc) == 0 {
		return nil
	}
	var out []string
	for i, ph := range rec.Plan.Phases {
		if ph.ID == rec.Phase {
			continue
		}
		section, err := rec.Plan.Section(rec.Doc, i)
		if err != nil {
			continue
		}
		for _, p := range plan.Paths(section) {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}
