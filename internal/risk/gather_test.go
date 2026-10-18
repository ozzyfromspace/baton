package risk

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

const gatherDoc = `# Demo

## P1 — the lead's
Work on ` + "`internal/a/a.go`" + `.

## P2 — the agent's
Work on ` + "`internal/b/b.go`" + `.

## P3 — later
Touches ` + "`a/a.go`" + ` and ` + "`internal/b/b_test.go`" + `.
`

// Gather reads a lead phase's own commits, from where it started until it was done, less what landed meanwhile
// and what came after; a phase agent's from its landing; and each phase's record where it was worked.
func TestGather(t *testing.T) {
	gittest.Isolate(t)
	dir := t.TempDir()
	git := func(args ...string) string { return gittest.Git(t, dir, args...) }
	git("init", "-q", "-b", "main")
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) {
		when := t0.Add(d).Format(time.RFC3339)
		t.Setenv("GIT_COMMITTER_DATE", when)
		t.Setenv("GIT_AUTHOR_DATE", when)
	}
	at(0)
	gittest.Write(t, dir, "README.md", "demo\n")
	gittest.Commit(t, dir, "init")
	start := git("rev-parse", "HEAD")

	// The agent's work, on a branch from the start, lands with main merged into it.
	git("checkout", "-q", "-b", "w")
	at(10 * time.Minute)
	gittest.Write(t, dir, "internal/b/b.go", "package b\n\nfunc B() {}\n")
	gittest.Write(t, dir, "internal/b/b_test.go", "package b\n")
	gittest.Commit(t, dir, "the agent's")
	agents := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	at(20 * time.Minute)
	gittest.Write(t, dir, "internal/a/a.go", "package a\n\nfunc A() {}\n\nfunc A2() {}\n")
	gittest.Commit(t, dir, "the lead's P1")
	git("checkout", "-q", "w")
	at(30 * time.Minute)
	git("-c", "user.name=test", "-c", "user.email=test@example.invalid", "merge", "-q", "--no-edit", "main")
	merge := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	git("merge", "-q", "--ff-only", "w")
	at(50 * time.Minute)
	gittest.Write(t, dir, "internal/a/a.go", "package a\n")
	gittest.Commit(t, dir, "the lead's next phase")

	pl := plan.Plan{Phases: []plan.Phase{{ID: "P1", Anchor: "## P1 — the lead's"}, {ID: "P2", Anchor: "## P2 — the agent's"}, {ID: "P3", Anchor: "## P3 — later"}}}
	lead := state.State{
		RunBranch: "main",
		Phases: map[string]*state.PhaseState{
			"P1": {Status: state.PhaseDone, StartHead: start, DoneAt: t0.Add(40 * time.Minute), Deviation: "moved A2 out"},
			"P2": {Status: state.PhaseDone, Worker: "run/P2", DoneAt: t0.Add(30 * time.Minute)},
		},
		Landed:    []state.Landing{{Phase: "P2", Commits: []string{agents, merge}, Head: merge, At: t0.Add(30 * time.Minute)}},
		Decisions: []state.Decision{{ID: "d1", Kind: state.Note, Phase: "P1"}, {ID: "d2", Kind: state.Note, Phase: "P0"}},
	}
	state.CountReturned(&lead, "P2")
	sub := state.State{
		Phases:    map[string]*state.PhaseState{"P2": {Status: state.PhaseDone, Deviation: "none"}},
		Decisions: []state.Decision{{ID: "P2.d1", Kind: state.Note, Phase: "P2"}, {ID: "P2.d2", Kind: state.Proposal, Phase: "P2"}},
		Jobs:      []state.Job{{ID: "P2.j1", Phase: "P2", Kind: state.JobVerify}, {ID: "P2.j2", Phase: "P2", Kind: state.JobVerify, Requeue: true}},
	}
	state.CountCheck(&sub, "P2")
	state.MarkDespiteRed(&sub, "P2")
	ctx := context.Background()

	p1 := Gather(ctx, Record{Phase: "P1", Plan: pl, Doc: []byte(gatherDoc), Lead: lead, Root: dir})
	want1 := Facts{Phase: "P1", GitKnown: true, ChecksKnown: true, Decisions: 1, Deviation: "moved A2 out",
		Files: []gitx.FileStat{{Path: "internal/a/a.go", Added: 5, Created: true}},
		Named: []string{"internal/b/b.go", "a/a.go", "internal/b/b_test.go"}}
	if !reflect.DeepEqual(p1, want1) {
		t.Errorf("the lead's phase:\n%+v\nwant\n%+v", p1, want1)
	}

	p2 := Gather(ctx, Record{Phase: "P2", Plan: pl, Doc: []byte(gatherDoc), Lead: lead, Sub: &sub, Root: dir})
	want2 := Facts{Phase: "P2", GitKnown: true, ChecksKnown: true, Decisions: 2, Conflicts: 1, Checks: 1, Jobs: 1, DespiteRed: true, Deviation: "none",
		Files: []gitx.FileStat{{Path: "internal/b/b.go", Added: 3, Created: true}, {Path: "internal/b/b_test.go", Added: 1, Created: true}},
		Named: []string{"internal/a/a.go", "a/a.go", "internal/b/b_test.go"}}
	if !reflect.DeepEqual(p2, want2) {
		t.Errorf("the agent's phase:\n%+v\nwant\n%+v", p2, want2)
	}

	// No git, or no commit to start from: the git factors are left out.
	for name, rec := range map[string]Record{
		"no checkout": {Phase: "P1", Plan: pl, Lead: lead},
		"no start":    {Phase: "P3", Plan: pl, Lead: lead, Root: dir},
	} {
		if f := Gather(ctx, rec); f.GitKnown || f.Files != nil {
			t.Errorf("%s: %+v", name, f)
		}
	}
}
