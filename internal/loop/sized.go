package loop

import (
	"context"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A landing's verification again is sized to the change (v0.8.7). When the run's branch moved on since a
// phase agent's gate passed, the merge queue verifies the phase again with the run's branch merged in
// (merge.go). First it compares what came on the run's branch since that gate with what the phase
// changes: when they share no file, Markdown aside, a QA phase not started yet follows the phase, and
// aggregate_qa is on (Fleet.AggregateQA), the phase lands without being verified again, over those
// commits (Landing.Unverified, with Landing.QA), and the QA phase's full checks run them together.
// Otherwise it is verified again, with its land check if it gave one (`baton done --land-check`,
// PhaseState.LandCheck), and with its last passing gate if not.
//
// A file in common is no proof that the two changes meet, and none in common no proof that they don't (a
// call into a file that changed): the QA phase ahead is what checks them together. Without one, or with
// aggregate_qa off, every landing is verified again as before.

// skip is why a landing goes on without its verification again: qa, the QA phase ahead, checks it with
// what came since its gate, which shares no file with it (why: decide.NoSharedFiles). A zero skip is none.
type skip struct{ qa, why string }

// sized says whether worker e's phase, at tip (with the run's branch, at onto, merged in), may land
// without being verified again over since, the commits that came on the run's branch since its gate
// passed. Whatever git cannot tell verifies it again.
func (c *Coordinator) sized(ctx context.Context, s *state.Store, st state.State, e state.Worker, since []string, onto, tip string) skip {
	if c.Fleet == nil || !c.Fleet.AggregateQA {
		return skip{}
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return skip{}
	}
	qa := qaAhead(st, pl, e.Phase)
	if qa == "" {
		return skip{}
	}
	g := c.Fleet.gitx()
	landed, err := c.landedFiles(ctx, s, st, since)
	if err != nil {
		return skip{}
	}
	base, err := g.MergeBase(ctx, s.Checkout, onto, tip)
	if err != nil {
		return skip{}
	}
	mine, err := g.ChangedBetween(ctx, s.Checkout, base, tip)
	if err != nil {
		return skip{}
	}
	shared, markdown := common(landed, mine)
	if len(shared) > 0 {
		return skip{}
	}
	return skip{qa: qa, why: decide.NoSharedFiles(st.RunBranch, len(landed), len(mine), markdown)}
}

// qaAhead is the QA phase of pl that checks phase (plan.NextQA) while it has not started, so its full
// checks run after phase lands: "" when there is none. One under way may have run them already.
func qaAhead(st state.State, pl plan.Plan, phase string) string {
	qa := pl.NextQA(phase)
	if qa == nil {
		return ""
	}
	if ps := st.Phases[qa.ID]; ps == nil || ps.Status != state.PhasePending || ps.Worker != "" {
		return ""
	}
	return qa.ID
}

// landedFiles lists the files the commits since changed on the run's branch: those a landing that brought
// one of them changed (Landing.Files), of this run or of another in the checkout, and, for each of the
// rest (the checkout's own commits: the lead's, or the human's), those git says it changed.
func (c *Coordinator) landedFiles(ctx context.Context, s *state.Store, st state.State, since []string) ([]string, error) {
	came := map[string]bool{}
	for _, x := range since {
		came[x] = true
	}
	files, known := map[string]bool{}, map[string]bool{}
	add := func(ls []state.Landing) {
		for _, l := range ls {
			if len(l.Files) == 0 || !slices.ContainsFunc(l.Commits, func(x string) bool { return came[x] }) {
				continue // a landing recorded without its files has its commits read from git
			}
			for _, x := range l.Commits {
				known[x] = true
			}
			for _, f := range l.Files {
				files[f] = true
			}
		}
	}
	add(st.Landed)
	for _, r := range c.Project.Runs() {
		if r.ID != s.Ref {
			add(r.State.Landed)
		}
	}
	g := c.Fleet.gitx()
	for _, x := range since {
		if known[x] {
			continue
		}
		changed, err := g.ChangedBetween(ctx, s.Checkout, x+"^", x)
		if err != nil {
			return nil, err
		}
		for _, f := range changed {
			files[f] = true
		}
	}
	return slices.Sorted(maps.Keys(files)), nil
}

// common lists the paths both a and b hold (shared), Markdown files aside, which it lists apart.
func common(a, b []string) (shared, markdown []string) {
	in := map[string]bool{}
	for _, p := range a {
		in[p] = true
	}
	for _, p := range b {
		switch {
		case !in[p]:
		case isMarkdown(p):
			markdown = append(markdown, p)
		default:
			shared = append(shared, p)
		}
	}
	return shared, markdown
}

// isMarkdown reports a Markdown file, by its name.
func isMarkdown(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown":
		return true
	}
	return false
}
