package loop

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// lines is a file of n numbered lines, the first one first, or replaced by first when it is set, and the
// last one replaced by last when it is set: two branches that change one end each merge cleanly.
func lines(n int, first, last string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		switch {
		case i == 1 && first != "":
			b.WriteString(first + "\n")
		case i == n && last != "":
			b.WriteString(last + "\n")
		default:
			b.WriteString("line " + string(rune('0'+i)) + "\n")
		}
	}
	return b.String()
}

// qaCrew is a landingCrew whose plan, when qa says so, ends with P4, a QA phase after all the others, and
// whose main has shared.txt and NOTES.md, which a phase and the lead can each change at one end. The
// merge queue sizes its verifications again with aggregate_qa as aggregate says.
func qaCrew(t *testing.T, qa, aggregate bool) *crew {
	t.Helper()
	phases := []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}},
		{ID: "P2", Title: "two", After: []string{}}, {ID: "P3", Title: "three", After: []string{"P1"}}}
	if qa {
		phases = append(phases, plan.Phase{ID: "P4", Title: "QA", After: []string{"P0", "P1", "P2", "P3"}, Type: plan.TypeQA})
	}
	x := newCrewOn(t, 1, plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: phases})
	for k, v := range map[string]string{"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.invalid", "GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.invalid"} {
		t.Setenv(k, v)
	}
	x.c.Fleet.AggregateQA = aggregate
	gittest.Write(t, x.repo, "shared.txt", lines(9, "", ""))
	gittest.Write(t, x.repo, "NOTES.md", lines(9, "", ""))
	gittest.Commit(t, x.repo, "shared files")
	x.atWork("P1")
	return x
}

// A phase agent's landing that the run's branch moved on from since its gate passed is verified again,
// unless what came since shares no file with it, Markdown aside, a QA phase not started yet follows it,
// and aggregate_qa is on: then it lands over those commits, unverified, and the lead is told the QA
// phase checks them together. What came is the lead's commits, whose files git says, or another phase's
// landing, whose files are on record.
func TestALandingsVerificationAgainIsSizedToTheChange(t *testing.T) {
	type came struct {
		files   map[string]string
		landing []string // the files of the landing on record that brought the commit; nil: the lead's
	}
	for _, c := range []struct {
		name          string
		qa, aggregate bool
		qaStatus      string // P4's status, when not pending
		came          []came
		p1            map[string]string // the files P1 changes
		skipped       bool
		why           string // what reverify_skipped's why says
	}{
		{name: "the lead's commit, no file shared", qa: true, aggregate: true,
			came: []came{{files: map[string]string{"lead.txt": "lead\n"}}}, p1: map[string]string{"one.txt": "one\n"},
			skipped: true, why: "the 1 file changed on main since its gate share none with the 1 it changes"},
		{name: "the lead's commits, no file shared", qa: true, aggregate: true,
			came: []came{{files: map[string]string{"lead.txt": "lead\n"}}, {files: map[string]string{"lead2.txt": "lead\n", "shared.txt": lines(9, "the lead's", "")}}},
			p1:   map[string]string{"one.txt": "one\n"}, skipped: true, why: "the 3 files changed on main since its gate share none with the 1 it changes"},
		{name: "the lead's commit, a file shared", qa: true, aggregate: true,
			came: []came{{files: map[string]string{"shared.txt": lines(9, "the lead's", "")}}},
			p1:   map[string]string{"one.txt": "one\n", "shared.txt": lines(9, "", "P1's")}},
		{name: "a landing, no file shared", qa: true, aggregate: true,
			came: []came{{files: map[string]string{"two.txt": "two\n"}, landing: []string{"two.txt"}}}, p1: map[string]string{"one.txt": "one\n"},
			skipped: true, why: "the 1 file changed on main since its gate share none with the 1 it changes"},
		{name: "a landing, a file shared on record", qa: true, aggregate: true,
			came: []came{{files: map[string]string{"two.txt": "two\n"}, landing: []string{"two.txt", "one.txt"}}}, p1: map[string]string{"one.txt": "one\n"}},
		{name: "only Markdown shared", qa: true, aggregate: true,
			came:    []came{{files: map[string]string{"NOTES.md": lines(9, "the lead's", ""), "lead.txt": "lead\n"}}},
			p1:      map[string]string{"one.txt": "one\n", "NOTES.md": lines(9, "", "P1's")},
			skipped: true, why: "the 2 files changed on main since its gate share only Markdown (NOTES.md) with the 2 it changes"},
		{name: "no QA phase after it", aggregate: true,
			came: []came{{files: map[string]string{"lead.txt": "lead\n"}}}, p1: map[string]string{"one.txt": "one\n"}},
		{name: "aggregate_qa off", qa: true,
			came: []came{{files: map[string]string{"lead.txt": "lead\n"}}}, p1: map[string]string{"one.txt": "one\n"}},
		{name: "the QA phase under way", qa: true, aggregate: true, qaStatus: state.PhaseActive,
			came: []came{{files: map[string]string{"lead.txt": "lead\n"}}}, p1: map[string]string{"one.txt": "one\n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := qaCrew(t, c.qa, c.aggregate)
			if c.qaStatus != "" {
				x.lead.Update(func(st *state.State) error { st.Phases["P4"].Status = c.qaStatus; return nil })
			}
			var since []string
			for _, m := range c.came {
				commit := x.leadCommits(m.files)
				since = append(since, commit)
				if m.landing != nil {
					x.lead.Update(func(st *state.State) error {
						st.Landed = append(st.Landed, state.Landing{Phase: "P2", Commits: []string{commit}, Files: m.landing, Head: commit, At: x.now, Told: true})
						return nil
					})
				}
			}
			gate := x.finish("P1", c.p1, true)
			x.tick(time.Second) // merged
			x.tick(time.Second) // landed, or verified again
			merged := gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1")

			if !c.skipped {
				sst, _ := x.sub("P1").Load()
				j := sst.Latest("P1")
				if x.worker("P1").Status != state.WorkerLanding || j == nil || !j.Requeue || j.Commit != merged || !x.has("reverify_queued", `"phase":"P1"`) || x.has("reverify_skipped") {
					t.Fatalf("not verified again: worker %+v, job %+v\n%s", x.worker("P1"), j, x.events())
				}
				return
			}
			st := x.state()
			if x.worker("P1").Status != state.WorkerLanded || gitx.Head(x.repo) != merged || x.has("reverify_queued") {
				t.Fatalf("not landed without its verification again: worker %+v\n%s", x.worker("P1"), x.events())
			}
			l := st.Landed[len(st.Landed)-1]
			if l.Phase != "P1" || l.QA != "P4" || l.Verified != gate || !slices.Equal(l.Unverified, since) {
				t.Errorf("landing %+v; want it over %v, its gate on %s, P4 checking them", l, since, gate)
			}
			if !x.logged("reverify_skipped", map[string]any{"phase": "P1", "qa": "P4", "why": c.why, "verified": gate}) || !x.logged("landed", map[string]any{"phase": "P1", "qa": "P4"}) {
				t.Errorf("events:\n%s", x.events())
			}
			if want := "which change none of its files, Markdown aside: not verified again, as P4, the QA phase ahead, checks them together"; !strings.Contains(x.announced(), want) {
				t.Errorf("announced %q, want %q", x.announced(), want)
			}
			told := decide.Landed([]state.Landing{l}, "main")
			if want := "P1's gate passed on " + short(gate) + ", before "; !strings.Contains(told, want) ||
				!strings.Contains(told, "which change none of its files, Markdown aside; P4, the QA phase ahead, checks them together.") || strings.Contains(told, "your next gate") {
				t.Errorf("the lead is told %q", told)
			}
		})
	}
}

// A phase that gave a land check with its done is verified again by it, in its gate's place, where its
// gate ran (in a lane, or in its worktree); once it passes, the phase lands.
func TestALandingIsVerifiedAgainByItsLandCheck(t *testing.T) {
	for _, here := range []bool{false, true} {
		t.Run(map[bool]string{false: "in a lane", true: "here"}[here], func(t *testing.T) {
			x := landingCrew(t)
			x.leadCommits(map[string]string{"lead.txt": "lead\n"})
			x.finish("P1", map[string]string{"one.txt": "one\n"}, true)
			const check = "go test -run TestOne ./one"
			sub := x.sub("P1")
			sub.Update(func(st *state.State) error {
				st.Latest("P1").Here, st.Phases["P1"].LandCheck = here, check
				return nil
			})
			x.tick(time.Second) // merged
			x.tick(time.Second) // verified again
			merged := gitx.RevParse(x.repo, "refs/heads/baton/sess-1/P1")
			sst, _ := sub.Load()
			j := sst.Latest("P1")
			if j == nil || j.Command != check || j.Here != here || !j.Requeue || j.Commit != merged || x.worker("P1").Merge.Reverify != j.ID {
				t.Fatalf("job %+v", j)
			}
			if !x.logged("reverify_queued", map[string]any{"phase": "P1", "job": j.ID, "command": check, "land_check": true}) ||
				!strings.Contains(x.announced(), "verifying it again with the run's branch merged in, by its land check (job "+j.ID+", `"+check+"`)") {
				t.Errorf("events:\n%s\nannounced %q", x.events(), x.announced())
			}
			sub.Update(func(st *state.State) error {
				j := st.Job(j.ID)
				j.Status, j.Started, j.Ended = state.JobPassed, x.now, x.now
				return nil
			})
			x.tick(time.Second) // the worker's own loop settles its job
			x.tick(time.Second)
			if gitx.Head(x.repo) != merged || x.worker("P1").Status != state.WorkerLanded {
				t.Fatalf("not landed once its land check passed\n%s", x.events())
			}
		})
	}
}

// A file both sides hold is shared, unless it is Markdown (.md or .markdown, in any case), which is listed
// apart.
func TestCommonFilesSetMarkdownAside(t *testing.T) {
	for _, c := range []struct {
		a, b             []string
		shared, markdown []string
	}{
		{[]string{"a.go", "b.go"}, []string{"c.go"}, nil, nil},
		{[]string{"a.go", "README.md"}, []string{"a.go", "README.md"}, []string{"a.go"}, []string{"README.md"}},
		{[]string{"docs/x.MD", "y.markdown"}, []string{"docs/x.MD", "y.markdown", "z.mdx"}, nil, []string{"docs/x.MD", "y.markdown"}},
		{[]string{"z.mdx"}, []string{"z.mdx"}, []string{"z.mdx"}, nil},
	} {
		shared, markdown := common(c.a, c.b)
		if !slices.Equal(shared, c.shared) || !slices.Equal(markdown, c.markdown) {
			t.Errorf("common(%v, %v) = %v, %v; want %v, %v", c.a, c.b, shared, markdown, c.shared, c.markdown)
		}
	}
}
