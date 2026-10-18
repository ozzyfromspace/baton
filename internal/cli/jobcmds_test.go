package cli

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/loop"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// gitSession is a hosted session in a git repository, with the test plan attached and owned, P0 under
// way, and a baton home of its own.
func gitSession(t *testing.T) (*session, string) {
	s, planFile, root := newRepoSession(t)
	s.env["BATON_HOME"] = t.TempDir()
	s.attach(planFile)
	s.set(func(x *state.State) { state.Claim(x, "inst", 1, s.now) })
	return s, root
}

func TestVerifyAndAwaitRefusals(t *testing.T) {
	const notes = "--notes=the parser is done; this checks it"
	for _, c := range []struct {
		name  string
		setup func(s *session, root string)
		args  []string
		want  string
		why   string // the refused event's, if one is logged
	}{
		{"no command", nil, []string{"verify", "--notes", "x"}, `usage: baton verify "<command>" [--here] [--qa "<why a full check now>"] [--replace] --notes`, ""},
		{"not hosted", func(s *session, _ string) { delete(s.env, "BATON_HOST") }, []string{"verify", "make test", notes}, "not queued — the baton host runs jobs, and this session is not hosted", ""},
		{"no notes", nil, []string{"verify", "make test"}, `not queued — say where P0 stands with --notes "<what is done, what is left, what this job checks>"`, "no notes"},
		{"blank notes", nil, []string{"await", "gh run watch", "--notes", " "}, "not queued — say where P0 stands", "no notes"},
		{"await takes no --here", nil, []string{"await", "--here", "gh run watch", notes}, `unknown flag --here`, ""},
		{"fresh work, here", func(s *session, root string) { gittest.Write(t, root, "src/new.go", "package src\n") },
			[]string{"verify", "--here", "make e2e", notes}, "and 1 file changed in P0 is not committed, so the gate would not see it", "uncommitted work"},
		{"fresh work", func(s *session, root string) { gittest.Write(t, root, "src/new.go", "package src\n") },
			[]string{"verify", "make test", notes}, "and 1 file changed in P0 is not committed, so the gate would not see it:\n    src/new.go\nCommit them (by path), then verify again", "uncommitted work"},
		{"blocked", func(s *session, _ string) { s.set(func(x *state.State) { state.SetBlocked(x, "need a key", s.now) }) },
			[]string{"verify", "make test", notes}, "not queued — the run is waiting on the human (blocked: need a key", "held for the human"},
		{"a proposal not asked", func(s *session, _ string) {
			s.must("", "propose", "drop the cache", "--because", "it is stale", "--undo", "rebuild it")
		}, []string{"await", "gh run watch", notes}, "not queued — put your proposal d1 to the human first", "proposal not asked"},
		{"a phase not started", func(s *session, _ string) { s.set(func(x *state.State) { x.BoundaryOwed = true }) },
			[]string{"verify", "make test", notes}, "not queued — P0 has not started yet: end your turn now", "phase not started"},
		{"no phase in hand", func(s *session, _ string) { s.set(func(x *state.State) { x.Current = "" }) },
			[]string{"verify", "make test", notes}, "not queued — no phase is in hand: every phase left waits for a job to finish", "no phase in hand"},
		{"a plan complete", func(s *session, _ string) { s.set(func(x *state.State) { x.Mode = state.ModeComplete }) },
			[]string{"await", "gh run watch", notes}, "not queued — no plan is running here (mode: complete)", "no run"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, root := gitSession(t)
			if c.setup != nil {
				c.setup(s, root)
			}
			if why := s.fails(c.args...); !strings.Contains(why, c.want) {
				t.Errorf("refusal %q, want it to mention %q", why, c.want)
			}
			if c.why != "" && !s.logged("refused", map[string]any{"command": c.args[0], "why": c.why}) {
				t.Errorf("no refused event for %q: %s", c.why, s.events())
			}
			if st := s.state(); len(st.Jobs) != 0 {
				t.Errorf("a refused %s queued %+v", c.args[0], st.Jobs)
			}
		})
	}
}

func TestVerifyQueuesAGateOnHead(t *testing.T) {
	s, root := gitSession(t)
	gittest.Write(t, root, "src/parse.go", "package src\n")
	gittest.Commit(t, root, "parse", "src/parse.go")
	head := gittest.Git(t, root, "rev-parse", "HEAD")
	orphan := filepath.Join(s.store().Dir, "jobs", "j9.log") // a log no job on record has
	os.MkdirAll(filepath.Dir(orphan), 0o755)
	os.WriteFile(orphan, []byte("old\n"), 0o644)

	out := s.must("", "verify", "make", "test", "--notes", "the parser is done")
	for _, want := range []string{"baton: queued j1 — `make test` (P0), on " + head[:12] + ", in a lane of its own. baton starts it within seconds; its output goes to " + s.store().JobLog("j1"),
		"Keep working on what does not depend on its result", "commit your work and end your turn: if the job runs past 5m (or is known to take that long) and another phase can go on, " +
			"baton sets P0 aside for it, and P0 resumes from your notes once the job ends", "If the job runs past 3h00m, baton stops it", "What you commit from now on is not in this check."} {
		if !strings.Contains(out, want) {
			t.Errorf("verify says:\n%s\nwant %q", out, want)
		}
	}
	j := s.state().Job("j1")
	if j == nil || j.Phase != "P0" || j.Kind != state.JobVerify || j.Command != "make test" || j.Commit != head || j.Notes != "the parser is done" || j.Status != state.JobQueued || !j.Queued.Equal(s.now) {
		t.Fatalf("job: %+v", j)
	}
	if !s.logged("job_queued", map[string]any{"job": "j1", "phase": "P0", "type": "verify", "command": "make test", "commit": head}) {
		t.Errorf("events: %s", s.events())
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Error("the log of a job no longer on record is still there")
	}

	// An await runs beside the gate: neither stops the other.
	out = s.must("", "await", "gh run watch 7 --exit-status", "--notes", "pushed; CI checks it")
	if !strings.Contains(out, "baton: queued j2 — `gh run watch 7 --exit-status` (P0), run from the working tree.") ||
		!strings.Contains(out, "baton: j2 runs beside j1 (`make test`), a gate: neither stops the other, and `baton done P0` waits for both, and is refused while either is red.") ||
		strings.Contains(out, "replaces") {
		t.Errorf("await says:\n%s", out)
	}
	if strings.Contains(out, "not in this check") {
		t.Errorf("await speaks of commits:\n%s", out)
	}
	st := s.state()
	if j1, j2 := st.Job("j1"), st.Job("j2"); j1.Status != state.JobQueued || j2.Kind != state.JobAwait || j2.Commit != "" || j2.Status != state.JobQueued {
		t.Errorf("jobs: %+v, %+v", *j1, *j2)
	}
	if !s.logged("job_queued", map[string]any{"job": "j2", "type": "await", "beside": "j1"}) || s.logged("job_queued", map[string]any{"job": "j2", "replaced": "j1"}) {
		t.Errorf("events: %s", s.events())
	}
	if want := []string{"baton: job j1 queued for P0 — `make test` on " + head[:12] + ", in a lane",
		"baton: job j2 queued for P0 — `gh run watch 7 --exit-status`, from the working tree; it runs beside j1, a gate"}; !slices.Equal(st.Run.Announce, want) {
		t.Errorf("announced %q, want %q", st.Run.Announce, want)
	}
}

// Within a kind, the same check queued again replaces the one that runs; another is refused, naming the
// one that runs and the ways forward, and replaces it only with --replace.
func TestAnotherGateReplacesOnlyWhenAsked(t *testing.T) {
	s, root := gitSession(t)
	gittest.Write(t, root, "src/parse.go", "package src\n")
	gittest.Commit(t, root, "parse", "src/parse.go")
	s.must("", "verify", "make", "test", "--notes", "the parser is done")
	s.must("", "await", "gh run watch 7 --exit-status", "--notes", "pushed; CI checks it")

	// The same gate again, on a newer commit: it replaces j1 by itself, and leaves the wait alone.
	gittest.Write(t, root, "src/lex.go", "package src\n")
	gittest.Commit(t, root, "lex", "src/lex.go")
	out := s.must("", "verify", "make", "test", "--notes", "the lexer too")
	if !strings.Contains(out, "baton: it replaces j1, the same check queued before, which baton stops.") || !strings.Contains(out, "j3 runs beside j2 (`gh run watch 7 --exit-status`), a wait on something outside") {
		t.Errorf("verify again says:\n%s", out)
	}
	if st := s.state(); st.Job("j1").Status != state.JobInterrupted || st.Job("j2").Status != state.JobQueued || st.Job("j3").Status != state.JobQueued {
		t.Errorf("jobs: %+v", st.Jobs)
	}

	// Another gate is refused, and nothing changes.
	errs := s.fails("verify", "make", "e2e", "--notes", "e2e as well")
	for _, want := range []string{"not queued — P0's job j3 (`make test` on", "a phase runs one gate at a time (one gate beside one wait on something outside",
		"`make e2e` is not the same check, so baton does not stop j3 unasked", "run it again with --replace", "fold both checks into one command", "queue this one once j3's result comes"} {
		if !strings.Contains(errs, want) {
			t.Errorf("another gate refused with:\n%s\nwant %q", errs, want)
		}
	}
	if st := s.state(); len(st.Jobs) != 3 || st.Job("j3").Status != state.JobQueued {
		t.Errorf("jobs after the refusal: %+v", st.Jobs)
	}
	if !s.logged("refused", map[string]any{"command": "verify", "why": "another check runs", "phase": "P0", "running": "j3", "asked": "make e2e"}) {
		t.Errorf("events: %s", s.events())
	}
	// So is another wait.
	if errs := s.fails("await", "gh run watch 8 --exit-status", "--notes", "the other run"); !strings.Contains(errs, "a phase runs one wait on something outside at a time") {
		t.Errorf("another wait refused with:\n%s", errs)
	}

	// With --replace, it replaces the gate that runs, and still leaves the wait alone.
	out = s.must("", "verify", "--replace", "make", "e2e", "--notes", "e2e instead")
	if !strings.Contains(out, "baton: it replaces j3 (`make test`), which baton stops, as --replace asked.") {
		t.Errorf("verify --replace says:\n%s", out)
	}
	st := s.state()
	if st.Job("j3").Status != state.JobInterrupted || st.Job("j3").Reason != "replaced by j4" || st.Job("j4").Command != "make e2e" || st.Job("j2").Status != state.JobQueued {
		t.Errorf("jobs: %+v", st.Jobs)
	}
	if !s.logged("job_queued", map[string]any{"job": "j4", "replaced": "j3", "replace": true, "beside": "j2"}) {
		t.Errorf("events: %s", s.events())
	}
}

// A job's command may be given as its words after --, as a phase agent gives it (Claude Code refuses an
// isolated agent's command that hands a program text starting with a shell or git): baton runs those
// words as they were given.
func TestAJobsCommandMayBeItsWords(t *testing.T) {
	s, root := gitSession(t)
	gittest.Write(t, root, "check.sh", "exit 0\n")
	gittest.Commit(t, root, "check", "check.sh")

	words := []string{"sh", "-c", "sh check.sh && echo it's fine"}
	s.must("", "verify", "--notes", "checked by hand", "--", words[0], words[1], words[2])
	if j := s.state().Job("j1"); j == nil || j.Command != lane.Join(words) || j.Notes != "checked by hand" {
		t.Fatalf("job: %+v, want the command %q", j, lane.Join(words))
	}
	s.must("", "await", "--notes", "pushed", "--", "gh", "run", "watch", "--exit-status")
	if j := s.state().Job("j2"); j == nil || j.Command != "gh run watch --exit-status" {
		t.Fatalf("job: %+v", j)
	}
	s.mustFail("-- <the command's words>", "verify", "--notes", "n", "--")
}

// Before the plan's QA phase, baton asks once of each gate whether it is a focused check or a full one
// the QA phase runs anyway, and the model decides: the job runs either way. So it asks of each await, as
// a wait on CI is often a full check in disguise. A full check run early on purpose is flagged (--qa), and
// the human is told. With aggregate_qa off, no QA phase ahead, or in the QA phase itself, nothing is asked.
func TestAGateBeforeTheQAPhaseIsAskedAbout(t *testing.T) {
	qaPlan := func(s *session, phase int) {
		pl, err := s.store().LoadPlan()
		if err != nil {
			t.Fatal(err)
		}
		if phase < 0 {
			phase += len(pl.Phases)
		}
		pl.Phases[phase].Type = plan.TypeQA
		if err := s.store().WritePlan(pl); err != nil {
			t.Fatal(err)
		}
	}
	asks := map[string]string{
		"verify": "Is this a focused check of what P0 changed, or a full suite, lint or end-to-end run that",
		"await":  "Is what this waits on a focused check of what P0 changed, or a full check in disguise",
	}
	const last, none, first = -1, 99, 0
	for _, c := range []struct {
		name    string
		qa      int // the phase made a QA phase: last, first (P0, the phase in hand), or none
		off     bool
		args    []string
		asks    bool
		flagged string
	}{
		{"a QA phase ahead", last, false, []string{"verify", "go test ./src", "--notes", "n"}, true, ""},
		{"flagged as early QA", last, false, []string{"verify", "make test", "--qa", "the parser touches every package", "--notes", "n"}, false, "the parser touches every package"},
		{"aggregate_qa off", last, true, []string{"verify", "make test", "--notes", "n"}, false, ""},
		{"no QA phase ahead", none, false, []string{"verify", "make test", "--notes", "n"}, false, ""},
		{"in the QA phase", first, false, []string{"verify", "make test", "--notes", "n"}, false, ""},
		{"await, a QA phase ahead", last, false, []string{"await", "gh run watch", "--notes", "n"}, true, ""},
		{"await, flagged as early QA", last, false, []string{"await", "gh run watch", "--qa", "CI runs on three systems", "--notes", "n"}, false, "CI runs on three systems"},
		{"await, aggregate_qa off", last, true, []string{"await", "gh run watch", "--notes", "n"}, false, ""},
		{"await, no QA phase ahead", none, false, []string{"await", "gh run watch", "--notes", "n"}, false, ""},
		{"await, in the QA phase", first, false, []string{"await", "gh run watch", "--notes", "n"}, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := gitSession(t)
			if c.qa != none {
				qaPlan(s, c.qa)
			}
			if c.off {
				s.env["BATON_AGGREGATE_QA"] = "0"
			}
			out := s.must("", c.args...)
			for kind, ask := range asks {
				if got, want := strings.Contains(out, ask), c.asks && kind == c.args[0]; got != want {
					t.Errorf("asked as %s %v, want %v:\n%s", kind, got, want, out)
				}
			}
			j := s.state().Job("j1")
			if j == nil || j.Kind != c.args[0] || j.QA != c.flagged {
				t.Fatalf("job %+v, want a %s queued, flagged %q", j, c.args[0], c.flagged)
			}
			if c.flagged != "" {
				if !s.logged("qa_early", map[string]any{"job": "j1", "phase": "P0", "why": c.flagged}) || !strings.Contains(strings.Join(s.state().Run.Announce, "\n"), "runs a full check ahead of its QA phase (job j1)") {
					t.Errorf("not flagged: %s", s.events())
				}
				if status := s.must("", "status"); !strings.Contains(status, "(a full check ahead of QA: "+c.flagged+")") {
					t.Errorf("status:\n%s", status)
				}
			}
		})
	}
}

// A gate that needs the checkout runs there, on HEAD, with no lane (--here): baton says what it holds
// until the gate ends.
func TestVerifyHereQueuesAGateInTheCheckout(t *testing.T) {
	s, root := gitSession(t)
	head := gittest.Git(t, root, "rev-parse", "HEAD")
	out := s.must("", "verify", "--here", "make e2e", "--notes", "the server is up; this checks the pages")
	for _, want := range []string{"baton: queued j1 — `make e2e` (P0), on " + head[:12] + ", in the working tree, with no lane.",
		"The gate reads this working tree as it runs, so until it ends baton refuses edits to it, takes up no other phase here, and lands no worker's work in this checkout",
		"If it runs past 3h00m, baton stops it"} {
		if !strings.Contains(out, want) {
			t.Errorf("verify --here says:\n%s\nwant %q", out, want)
		}
	}
	if strings.Contains(out, "sets P0 aside") || strings.Contains(out, "worker's worktree") {
		t.Errorf("verify --here promises what it does not do:\n%s", out)
	}
	if want := "they would change what it checks. A worker's work that finishes meanwhile lands once the gate ends. Meanwhile,"; !strings.Contains(out, want) {
		t.Errorf("verify --here with nothing landing says:\n%s\nwant %q", out, want)
	}
	j := s.state().Job("j1")
	if j == nil || !j.Here || j.Kind != state.JobVerify || j.Commit != head || j.Command != "make e2e" || j.Status != state.JobQueued {
		t.Fatalf("job: %+v", j)
	}
	if !s.logged("job_queued", map[string]any{"job": "j1", "type": "verify", "commit": head, "here": true}) {
		t.Errorf("events: %s", s.events())
	}
	if want := []string{"baton: job j1 queued for P0 — `make e2e` on " + head[:12] + ", in the working tree (no lane): edits there wait until it ends"}; !slices.Equal(s.state().Run.Announce, want) {
		t.Errorf("announced %q, want %q", s.state().Run.Announce, want)
	}
	// A plain verify queued over it (another check, so --replace) is a lane gate again.
	s.must("", "verify", "--replace", "make test", "--notes", "x")
	if j := s.state().Job("j2"); j == nil || j.Here {
		t.Errorf("job: %+v", j)
	}
}

// A gate in the lead's checkout holds the landings that wait there, this run's and other sessions':
// verify --here names them, and says they land once it ends.
func TestVerifyHereNamesTheLandingsItHolds(t *testing.T) {
	s, planFile, _ := newRepoSession(t)
	s.env["BATON_HOME"] = t.TempDir()
	s.attach(planFile)
	landing := func(phase string) state.Worker {
		return state.Worker{Phase: phase, Ref: "r/" + phase, Status: state.WorkerLanding, Merge: &state.Merge{Queued: s.now}}
	}
	s.set(func(x *state.State) {
		state.Claim(x, "inst", 1, s.now)
		x.Workers = []state.Worker{landing("P1"), {Phase: "P2", Ref: "r/P2", Status: state.WorkerWorking}, landing("P3")}
	})
	other := &session{t: t, now: s.now, env: map[string]string{"BATON_DIR": s.env["BATON_DIR"], "BATON_HOST": "1",
		"BATON_INSTANCE": "inst-2", "CLAUDE_CODE_SESSION_ID": "sess-2"}}
	other.attach(planFile)
	p, err := state.OpenProject(s.env["BATON_DIR"], func() time.Time { return s.now })
	if err != nil {
		t.Fatal(err)
	}
	run2, err := p.Bind(state.Binding{Session: "sess-2"}) // the session helper's store is sess-1's
	if err != nil {
		t.Fatal(err)
	}
	run2.Update(func(x *state.State) error {
		state.Claim(x, "inst-2", 1, s.now)
		x.Workers = []state.Worker{landing("P1")}
		return nil
	})

	out := s.must("", "verify", "--here", "make e2e", "--notes", "the server is up; this checks the pages")
	if want := "they would change what it checks. P1, P3 and 1 phase of another baton session wait to land: " +
		"their work lands once the gate ends, as does any worker's work that finishes meanwhile. Meanwhile,"; !strings.Contains(out, want) {
		t.Errorf("verify --here says:\n%s\nwant %q", out, want)
	}
}

// A gate whose command goes into the checkout runs on the checkout's files from a lane, while landings and
// edits change them: verify refuses it, and says how to run it there. --here runs it there, so the lead
// may name its checkout; a worker never may.
func TestVerifyRefusesAGateNamingTheCheckout(t *testing.T) {
	for _, c := range []struct {
		name    string
		command func(root, rel string) string
		here    bool
		refused bool
	}{
		{"cd into it", func(root, _ string) string { return "cd " + root + " && make e2e" }, false, true},
		{"a script in it", func(root, _ string) string { return root + "/scripts/gate.sh" }, false, true},
		{"quoted", func(root, _ string) string { return `make -C "` + root + `" e2e` }, false, true},
		{"from home", func(_, rel string) string { return "cd ~/" + rel + "; make e2e" }, false, true},
		{"from $HOME", func(_, rel string) string { return "make -C $HOME/" + rel }, false, true},
		{"a sibling", func(root, _ string) string { return "cd " + root + "-other && make e2e" }, false, false},
		{"elsewhere", func(string, string) string { return "make e2e" }, false, false},
		{"here", func(root, _ string) string { return "cd " + root + " && make e2e" }, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, root := gitSession(t)
			s.env["HOME"] = filepath.Dir(root)
			args := []string{"verify", c.command(root, filepath.Base(root)), "--notes", "x"}
			if c.here {
				args = append(args, "--here")
			}
			if !c.refused {
				s.must("", args...)
				return
			}
			why := s.fails(args...)
			for _, want := range []string{"names the checkout (" + root + "), but verify runs a gate in a lane",
				"`baton verify --here \"<command>\" --notes \"…\"` runs it on HEAD from the checkout's root", "commit an executable .baton-lane script"} {
				if !strings.Contains(why, want) {
					t.Errorf("refusal %q, want it to mention %q", why, want)
				}
			}
			if !s.logged("refused", map[string]any{"command": "verify", "why": "names the checkout"}) {
				t.Errorf("events: %s", s.events())
			}
			if len(s.state().Jobs) != 0 {
				t.Errorf("queued %+v", s.state().Jobs)
			}
		})
	}
}

// A worker's gates check its own work: one naming the lead's checkout is refused, --here or not, and
// --here runs in the worker's worktree, which no landing touches.
func TestAWorkersGateHere(t *testing.T) {
	lead, root := gitSession(t)
	tree := filepath.Join(t.TempDir(), "P1")
	gittest.Git(t, root, "worktree", "add", "-q", "-b", "baton/sess-1/P1", tree)
	run := lead.store()
	pl, _ := run.LoadPlan()
	p, _ := state.OpenProject(lead.env["BATON_DIR"], nil)
	sub, err := p.NewWorker(run.Ref, "P1", "inst", tree)
	if err != nil {
		t.Fatal(err)
	}
	only, _ := pl.Only("P1")
	sub.SavePlan(only)
	sub.Update(func(x *state.State) error {
		*x = state.AttachWorker(*x, "P1")
		state.Start(x, lead.now, state.Origin{})
		return nil
	})
	w := &session{t: t, now: lead.now, env: maps.Clone(lead.env), dir: sub.Root} // the phase agent, in its worktree

	for _, here := range []bool{false, true} {
		args := []string{"verify", "cd " + root + " && make e2e", "--notes", "x"}
		if here {
			args = append(args, "--here")
		}
		why := w.fails(args...)
		if want := "names the lead session's checkout (" + root + "), which a worker never touches: your gates check your own work. " +
			"Leave the checkout's path out: verify runs the command on your commit in a lane of its own, or, with --here, in your worktree (" + w.store().Root + ")."; !strings.Contains(why, want) {
			t.Errorf("refusal %q, want %q", why, want)
		}
	}
	out := w.must("", "verify", "--here", "make e2e", "--notes", "x")
	if want := "until it ends baton refuses edits to it, and takes up nothing else here: they would change what it checks. Meanwhile,"; !strings.Contains(out, want) {
		t.Errorf("verify --here says:\n%s\nwant %q", out, want)
	}
	if j := w.state().Job("P1.j1"); j == nil || !j.Here || j.Commit != gittest.Git(t, tree, "rev-parse", "HEAD") {
		t.Errorf("job: %+v", j)
	}
}

// namesCheckout matches the checkout's path whole, or a path inside it, in any of the forms a shell takes
// it in.
func TestNamesCheckout(t *testing.T) {
	const co, home = "/w/proj", "/w"
	for _, c := range []struct {
		command string
		want    bool
	}{
		{"cd /w/proj && make", true},
		{"/w/proj/gate.sh", true},
		{"'/w/proj'", true},
		{"make -C /w/proj", true},
		{"cd ~/proj; make", true},
		{"cd ${HOME}/proj/web; make", true},
		{"cd /w/projects && make", false},
		{"cd /w/proj.bak && make", false},
		{"cd /x/w/proj && make", false},
		{"cd /mnt//w/proj && make", false},
		{"cd ~/projx", false},
		{"make e2e", false},
	} {
		if got := namesCheckout(c.command, co, home); got != c.want {
			t.Errorf("namesCheckout(%q) = %v, want %v", c.command, got, c.want)
		}
	}
	if namesCheckout("cd / && make", "", home) {
		t.Error("an empty checkout named")
	}
}

// Work already uncommitted when the phase started is not the phase's, so it does not hold a gate up; but
// the gate does not see it either, and verify says so.
func TestVerifyNotesWorkFromBeforeThePhase(t *testing.T) {
	s, planFile, root := newRepoSession(t)
	s.env["BATON_HOME"] = t.TempDir()
	gittest.Write(t, root, "scratch.txt", "the human's\n")
	s.attach(planFile)
	if out := s.must("", "verify", "make test", "--notes", "x"); !strings.Contains(out, "1 uncommitted file was already there when P0 started; the gate checks HEAD, without it.") {
		t.Errorf("verify says:\n%s", out)
	}
	if out := s.must("", "verify", "--here", "--replace", "make test", "--notes", "x"); !strings.Contains(out, "1 uncommitted file was already there when P0 started; the gate runs with it, as it is in the working tree.") {
		t.Errorf("verify --here says:\n%s", out)
	}
}

func TestAwaitWorksWithoutGit(t *testing.T) {
	s := attached(t)
	s.env["BATON_HOME"] = t.TempDir()
	if why := s.fails("verify", "make test", "--notes", "x"); !strings.Contains(why, "this project does not use git. Run the gate in place instead") {
		t.Errorf("verify without git: %s", why)
	}
	s.must("", "await", "make test", "--notes", "x")
	if j := s.state().Job("j1"); j == nil || j.Kind != state.JobAwait || j.Commit != "" {
		t.Fatalf("job: %+v", j)
	}
}

// proc is a process standing in for a job's runner.
type proc struct {
	pid  int
	done chan error
}

func sleeper(t *testing.T) proc {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stands in for a runner with a POSIX process")
	}
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := proc{pid: cmd.Process.Pid, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })
	return p
}

// stopped fails unless the process was told to stop (SIGTERM).
func (p proc) stopped(t *testing.T, by string) {
	t.Helper()
	select {
	case err := <-p.done:
		if err == nil || !strings.Contains(err.Error(), "terminated") {
			t.Errorf("the runner ended with %v, not SIGTERM", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not stop the job's runner", by)
	}
}

func (p proc) running() bool {
	select {
	case err := <-p.done:
		p.done <- err
		return false
	case <-time.After(200 * time.Millisecond):
		return true
	}
}

func TestDropStopsTheJobs(t *testing.T) {
	s := attached(t)
	s.env["BATON_HOME"] = t.TempDir()
	runner := sleeper(t)
	s.set(func(x *state.State) {
		x.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobPassed},
			{ID: "j2", Phase: "P0", Command: "make e2e", Status: state.JobRunning, PID: runner.pid, Heartbeat: s.now}}
		x.JobSeq = 2
	})
	logs := filepath.Join(s.store().Dir, "jobs")
	os.MkdirAll(logs, 0o755)
	os.WriteFile(filepath.Join(logs, "j1.log"), []byte("passed\n"), 0o644)

	out := s.must("", "drop")
	if !strings.Contains(out, "baton: it stopped job j2 (`make e2e`, P0), and removed the logs of the run's jobs.") {
		t.Errorf("drop says:\n%s", out)
	}
	runner.stopped(t, "drop")
	if _, err := os.Stat(logs); err == nil {
		t.Error("the jobs' logs are still there")
	}
	if st := s.state(); len(st.Jobs) != 0 || st.JobSeq != 2 {
		t.Errorf("after drop: jobs %+v, seq %d", st.Jobs, st.JobSeq)
	}
	if ev := s.events(); !strings.Contains(ev, `"kind":"dropped"`) || !strings.Contains(ev, `"stopped":["j2"]`) {
		t.Errorf("events: %s", ev)
	}
}

// A runner not heard from for a while may be gone, and its pid another process's: drop does not signal it.
func TestStopRunnersSparesARunnerNotHeardFrom(t *testing.T) {
	runner := sleeper(t)
	now := time.Now()
	stopRunners([]state.Job{{ID: "j1", PID: runner.pid, Heartbeat: now.Add(-time.Hour)}}, now)
	if !runner.running() {
		t.Fatal("a runner not heard from for an hour was signalled")
	}
	stopRunners([]state.Job{{ID: "j1", PID: runner.pid, Spawned: now}}, now)
	runner.stopped(t, "stopping a runner just started")
}

func TestTheJobsStopWhenTheSessionEnds(t *testing.T) {
	s := attached(t)
	runner := sleeper(t)
	s.set(func(x *state.State) {
		x.Jobs = []state.Job{{ID: "j1", Phase: "P0", Command: "make test", Status: state.JobRunning, PID: runner.pid, Heartbeat: s.now}}
	})
	io, out, _ := testIO("", s.env)
	io.Now = func() time.Time { return s.now }
	endJobs(io, s.store(), "another-host")
	if j := s.state().Job("j1"); j.Status != state.JobRunning {
		t.Fatalf("a host that does not drive the run stopped its job: %+v", *j)
	}
	endJobs(io, s.store(), "inst")
	runner.stopped(t, "the session's end")
	if j := s.state().Job("j1"); j.Status != state.JobInterrupted || j.Reason != "the baton session ended" {
		t.Errorf("job: %+v", *j)
	}
	if !strings.Contains(out.String(), "baton: stopped job j1 (`make test`, P0), as the session ended.") || !s.logged("jobs_stopped", map[string]any{"why": "the session ended"}) {
		t.Errorf("said %q; events %s", out, s.events())
	}
}

func TestStatusListsTheJobs(t *testing.T) {
	s, root := gitSession(t)
	head := gittest.Git(t, root, "rev-parse", "HEAD")
	s.set(func(x *state.State) {
		x.Jobs = []state.Job{
			{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make test", Commit: head, Status: state.JobFailed, Exit: 2, Lane: 1, Started: s.now.Add(-time.Hour), Ended: s.now.Add(-48 * time.Minute)},
			{ID: "j2", Phase: "P0", Kind: state.JobAwait, Command: "gh run watch", Status: state.JobInterrupted, Exit: -1, Started: s.now.Add(-5 * time.Minute), Ended: s.now.Add(-5*time.Minute + 30*time.Second), Reason: "replaced by j3"},
			{ID: "j3", Phase: "P0", Kind: state.JobVerify, Command: "make test", Commit: head, Status: state.JobRunning, Lane: 1, Log: "/elsewhere/j3.log", Started: s.now.Add(-2*time.Hour - 5*time.Minute)},
			{ID: "j4", Phase: "P0", Kind: state.JobAwait, Command: "sleep 1", Status: state.JobQueued},
		}
	})
	out := s.must("", "status")
	for _, want := range []string{
		"jobs (newest first):\n  j4   P0       await  queued  `sleep 1`\n       log: " + s.store().JobLog("j4") + "\n" +
			"  j3   P0       verify running 2h05m, lane 1  `make test` on " + head[:12] + "\n       log: /elsewhere/j3.log\n" +
			"  j2   P0       await  was interrupted after 30s: replaced by j3  `gh run watch`\n",
		"  j1   P0       verify failed (exit 2) after 12m, lane 1  `make test` on " + head[:12] + "\n",
		"lanes: this project has no .baton-lane, so a gate runs with sh -c in a bare checkout of its commit, one at a time",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status:\n%s\nwant %q", out, want)
		}
	}
}

// baton status says where each phase not done stands: for how long it has been active, why it is
// verifying or set aside, and the job it waits on; a phase set aside shows the notes it resumes from.
func TestStatusSaysWhereEachPhaseStands(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	running := state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make test", Status: state.JobRunning, Lane: 1, Started: now.Add(-3 * time.Minute)}
	failed := state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Command: "make test", Status: state.JobFailed, Exit: 1, Lane: 1, Started: now.Add(-20 * time.Minute), Ended: now.Add(-6 * time.Minute)}
	for _, c := range []struct {
		name string
		set  func(x *state.State)
		want string
	}{
		{"active", func(x *state.State) { x.Phases["P0"].StartedAt = now.Add(-12 * time.Minute) },
			"  ▶ P0       The overlay  (active 12m)"},
		{"active, with its gate running", func(x *state.State) {
			x.Phases["P0"].StartedAt = now.Add(-75 * time.Minute)
			x.Jobs = []state.Job{running}
		}, "  ▶ P0       The overlay  (active 1h15m — job j1 running 3m, lane 1)"},
		{"verifying", func(x *state.State) {
			x.Jobs = []state.Job{running}
			x.Phases["P0"].Status, x.Current = state.PhaseVerifying, "P1"
		}, "  ⧗ P0       The overlay  (verifying — job j1 running 3m, lane 1)"},
		{"parked while its job runs", func(x *state.State) {
			x.Jobs = []state.Job{running}
			x.Phases["P0"].Status, x.Current = state.PhaseParked, "P1"
			x.Phases["P0"].Park = &state.Park{Reason: state.ParkJob, Since: now, Job: "j1", Notes: "The modal closes;\nthe overlay test is the slow one."}
		}, "  ⏸ P0       The overlay  (parked while its job runs — job j1 running 3m, lane 1)\n" +
			"             notes: The modal closes; the overlay test is the slow one."},
		{"parked, its verification red", func(x *state.State) {
			x.Jobs = []state.Job{failed}
			x.Phases["P0"].Status, x.Current = state.PhaseParked, "P1"
			x.Phases["P0"].Park = &state.Park{Reason: state.ParkRed, Since: now, Job: "j1", Notes: strings.Repeat("all of it ", 20)}
		}, "  ⏸ P0       The overlay  (parked: its verification failed — job j1 failed (exit 1) after 14m, lane 1)\n" +
			"             notes: " + strings.Repeat("all of it ", 9) + "all of it…"},
		{"parked, its verification without a verdict", func(x *state.State) {
			x.Jobs = []state.Job{{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: state.JobLaneError, Reason: "up failed twice"}}
			x.Phases["P0"].Status, x.Current = state.PhaseParked, "P1"
			x.Phases["P0"].Park = &state.Park{Reason: state.ParkUnverified, Since: now, Job: "j1"}
		}, "  ⏸ P0       The overlay  (parked: its verification had no verdict — job j1 could not run: up failed twice)"},
		{"parked at a checkpoint", func(x *state.State) {
			x.Phases["P0"].Status, x.Current = state.PhaseParked, "P1"
			x.Phases["P0"].Park = &state.Park{Reason: state.ParkCheckpoint, Since: now, Notes: "halfway"}
		}, "  ⏸ P0       The overlay  (parked at a checkpoint)\n             notes: halfway"},
		{"to resume after the compaction", func(x *state.State) {
			x.Phases["P0"].Status, x.BoundaryOwed = state.PhaseParked, true
			x.Phases["P0"].Park = &state.Park{Reason: state.ParkRed, Since: now, Notes: "halfway"}
		}, "  → P0       The overlay  (resumes next, after compaction)\n             notes: halfway"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := gitSession(t)
			s.now = now
			s.set(c.set)
			if got := phaseLines(s.must("", "status"), "P0"); got != c.want {
				t.Errorf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

// phaseLines is a phase's line in baton status, with the notes line under it if there is one.
func phaseLines(status, id string) string {
	lines := strings.Split(status, "\n")
	for i, l := range lines {
		if f := strings.Fields(l); strings.HasPrefix(l, "  ") && len(f) > 1 && f[1] == id {
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "             notes: ") {
				return l + "\n" + lines[i+1]
			}
			return l
		}
	}
	return ""
}

// baton status names the job that holds each busy lane, and the model's own background work, apart from
// the Monitors it armed (watchers, not work).
func TestStatusShowsLanesAndBackgroundWork(t *testing.T) {
	s, root := gitSession(t)
	pool := lane.Pool{Home: s.env["BATON_HOME"], Root: s.store().Root}
	held, err := pool.TryAcquire(2)
	if err != nil || held == nil {
		t.Fatalf("lane: %v", err)
	}
	defer held.Release()
	if free, _ := pool.TryAcquire(2); free != nil {
		free.Release()
	}
	head := gittest.Git(t, root, "rev-parse", "HEAD")
	s.set(func(x *state.State) {
		x.Jobs = []state.Job{{ID: "j3", Phase: "P0", Kind: state.JobVerify, Command: "make test", Commit: head, Status: state.JobRunning, Lane: 1, Started: s.now.Add(-time.Minute)}}
		x.Run.Background = []state.Task{
			{ID: "b1", Type: "shell", Description: "npm test", Since: s.now.Add(-12 * time.Minute)},
			{ID: "m1", Type: "shell", Description: "watch the log"},
			{ID: "a1", Type: "subagent"},
		}
		x.Run.Monitors = []string{"m1"}
	})
	out := s.must("", "status")
	for _, want := range []string{
		"  lane 1  busy (j3)\n  lane 2  idle\n",
		"background work of Claude's own (each wakes Claude when it ends):\n  shell    npm test  (seen 12m ago)\n  subagent a1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status:\n%s\nwant %q", out, want)
		}
	}
	if strings.Contains(out, "watch the log") {
		t.Errorf("a Monitor is listed as work:\n%s", out)
	}
}

func TestTheHostRefusesAnInvalidSetting(t *testing.T) {
	for name, want := range map[string]string{
		"BATON_VERIFY_TIMEOUT": `BATON_VERIFY_TIMEOUT "forever" is not valid: use a duration from 1s to 24h`,
		"BATON_SWITCH_AFTER":   `BATON_SWITCH_AFTER "forever" is not valid: use a duration from 1s to 2h`,
		"BATON_FOREGROUND_MAX": `BATON_FOREGROUND_MAX "forever" is not valid: use a duration from 1s to 10m`,
		"BATON_WORKERS":        `BATON_WORKERS "forever" is not valid: use "auto", or a number from 0 to 8`,
	} {
		t.Chdir(t.TempDir())
		io, _, errb := testIO("", map[string]string{"BATON_HOME": t.TempDir(), name: "forever", "BATON_CLAUDE": "/nonexistent/claude"})
		if code := runHostWith(nil, io, nil); code != 1 || !strings.Contains(errb.String(), want) {
			t.Errorf("%s: exit %d: %s", name, code, errb)
		}
	}
}

// The sessions the host runs read back the workers' settings the host started with: auto stays auto,
// whatever the file says, and the machine they read decides as it did for the host.
func TestTheWorkerSettingsReachTheSession(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(config.Path(root), []byte(`{"workers":3,"worker_usage_max":50}`), 0o644)
	for _, c := range []struct {
		n     int
		auto  bool
		usage float64
	}{{6, true, 0}, {2, false, 85.5}, {0, false, 0}} {
		env := map[string]string{}
		for _, kv := range workerEnv(c.n, c.auto, c.usage) {
			k, v, _ := strings.Cut(kv, "=")
			env[k] = v
		}
		cfg := config.Load(root, func(k string) string { return env[k] })
		n, auto, err := cfg.WorkerCap(func() machine.Reading { return roomy })
		usage, uerr := cfg.WorkerUsage()
		if n != c.n || auto != c.auto || usage != c.usage || err != nil || uerr != nil {
			t.Errorf("%+v: read back %d, auto %v, usage max %v (%v, %v)", c, n, auto, usage, err, uerr)
		}
	}
}

// claude moves a command to the background after foreground_max, unless the human capped that themselves
// or turned it off.
func TestClaudeBackgroundsLongCommands(t *testing.T) {
	for _, c := range []struct {
		name string
		env  []string
		d    time.Duration
		want []string
	}{
		{"set", []string{"PATH=/bin"}, 2 * time.Minute, []string{"CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS=120000"}},
		{"the human's own", []string{"CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS=30000"}, 2 * time.Minute, []string{"CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS=30000"}},
		{"off", []string{"PATH=/bin"}, 0, nil},
	} {
		var got []string
		for _, kv := range backgroundAfter(c.env, c.d) {
			if strings.HasPrefix(kv, "CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS=") {
				got = append(got, kv)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The test binary runs baton itself when CLI_TEST_MAIN is set: that is how the host's runners run here.
func TestMain(m *testing.M) {
	if os.Getenv("CLI_TEST_MAIN") == "1" {
		os.Exit(Main(os.Args[1:], StdIO()))
	}
	os.Exit(m.Run())
}

type keys struct{}

func (keys) Type(string, bool) error { return nil }
func (keys) ClearInput() error       { return nil }
func (keys) Quit() error             { return nil }

// The whole way through: the host's loop starts a detached runner (baton lane-run, here the test binary)
// for a queued job, which runs the gate in a lane and records how it ended.
func TestTheHostRunsAJobToItsEnd(t *testing.T) {
	s, env := laneProject(t, "sleep 1; echo gate output; exit 1")
	t.Setenv("CLI_TEST_MAIN", "1")
	p, err := state.OpenProject(env["BATON_DIR"], time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s, err = p.Bind(state.Binding{Session: "sess-1", Instance: "inst"})
	if err != nil {
		t.Fatal(err)
	}
	io, _, _ := testIO("", env)
	io.Now = time.Now
	exe, _ := os.Executable()
	l := &loop.Loop{Store: s, Timing: loop.DefaultTiming, Runners: newHostRunners(io, exe, p, "inst")}

	deadline := time.Now().Add(30 * time.Second)
	for {
		now := time.Now()
		s.Update(func(st *state.State) error { st.Owner.Heartbeat = now; return nil })
		l.Tick(host.View{Now: now, Store: s, Owner: true, LastOutput: now.Add(-time.Hour), LastHumanKey: now.Add(-time.Hour)}, keys{})
		if st, _ := s.Load(); st.Job("j1").Finished() {
			break
		}
		if now.After(deadline) {
			b, _ := os.ReadFile(filepath.Join(env["BATON_DIR"], "baton.log"))
			t.Fatalf("the job never finished; baton.log:\n%s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	st, _ := s.Load()
	j := st.Job("j1")
	if j.Status != state.JobFailed || j.Exit != 1 || j.Lane != 1 || j.Spawned.IsZero() || j.PID == os.Getpid() {
		t.Errorf("job: %+v", *j)
	}
	if log, _ := os.ReadFile(j.Log); !strings.Contains(string(log), "gate output") {
		t.Errorf("job log:\n%s", log)
	}
	ev := events(t, s)
	if ev["job_started"] == nil || ev["job_finished"] == nil || ev["job_finished"]["status"] != "failed" {
		t.Errorf("events: %v", ev)
	}
}
