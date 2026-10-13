package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/state"
)

// asWorker is a run attached in sess-1 (the lead, on P0, host inst) with a worker on P1 in a worktree of
// its own, its phase started; w runs commands as the worker's phase agent (the lead's session, in the
// worktree), lead as the lead's.
func asWorker(t *testing.T) (w, lead *session) {
	t.Helper()
	lead = attached(t)
	run := lead.store()
	pl, err := run.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	pl.Phases[1].After = []string{} // P1 can go ahead beside P0
	run.SavePlan(pl)
	tree := t.TempDir()
	lead.set(func(x *state.State) {
		if err := state.Assign(x, pl, state.Worker{Phase: "P1", Ref: state.WorkerRef(run.Ref, "P1"), Worktree: tree}, lead.now); err != nil {
			t.Fatal(err)
		}
	})
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
	w = &session{t: t, now: lead.now, env: maps.Clone(lead.env), dir: sub.Root}
	return w, lead
}

// relays lists the run's relay queue as phase/kind/decision.
func relays(lead *session) string {
	var out []string
	for _, r := range lead.state().Relay {
		out = append(out, strings.TrimSuffix(r.Phase+"/"+r.Kind+"/"+r.Decision, "/"))
	}
	return strings.Join(out, " ")
}

// What runs the plan is the lead session's: a worker is refused it, and carries on with its phase.
func TestAWorkerIsRefusedTheLeadsCommands(t *testing.T) {
	for _, args := range [][]string{{"attach", "plan.md", "--suggested"}, {"run"}, {"pause"}, {"drop"}, {"exit"}} {
		t.Run(args[0], func(t *testing.T) {
			w, lead := asWorker(t)
			w.mustFail("not run — you are a worker: you work on P1 alone, in a worktree of your own, while the lead session runs the plan in the human's terminal, "+
				"and baton "+args[0]+" belongs to the lead session. Carry on with P1", args...)
			if !w.logged("refused", map[string]any{"command": args[0], "why": "worker"}) {
				t.Errorf("the worker's events:\n%s", w.events())
			}
			if ws, ls := w.state(), lead.state(); ws.Mode != state.ModeRunning || ws.Current != "P1" || ls.Mode != state.ModeRunning || ls.Current != "P0" {
				t.Errorf("the worker's run %s on %s; the lead's %s on %s", ws.Mode, ws.Current, ls.Mode, ls.Current)
			}
		})
	}
}

// A worker's proposal is put to the human by the lead session: it is queued in the run, once, and the
// worker hears what happens next. Until it is answered, the worker cannot move past it.
func TestAWorkersProposalGoesThroughTheLead(t *testing.T) {
	w, lead := asWorker(t)
	out := w.must("", "propose", "use a fake clock", "--because", "the real one drifts", "--undo", "revert the commit")
	for _, want := range []string{"baton: proposal P1.d1 recorded. baton puts it to the human, and gives you their answer here",
		"If nobody answers within 5 minutes of being asked, baton picks Go ahead for them", "Until then, do not do what you proposed"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	ws := w.state()
	p := ws.PendingProposal()
	if p == nil || p.ID != "P1.d1" || !p.Relayed || p.Question != "" || !p.Deadline.IsZero() || p.Asked {
		t.Fatalf("the worker's proposal: %+v", p)
	}
	if got := relays(lead); got != "P1/proposal/P1.d1" || !lead.logged("relay_queued", map[string]any{"phase": "P1", "type": "proposal", "decision": "P1.d1"}) {
		t.Errorf("the run's relay queue %q; events:\n%s", got, lead.events())
	}
	if r := lead.state().Relay[0]; r.What != "use a fake clock" || r.Because != "the real one drifts" || r.Untimed != "" {
		t.Errorf("queued %+v: the lead words its question from it", r)
	}
	if !w.logged("proposed", map[string]any{"id": "P1.d1", "relayed": true}) || strings.Contains(w.events(), `"deadline"`) {
		t.Errorf("the worker's events:\n%s", w.events())
	}
	for _, args := range [][]string{{"propose", "x", "--because", "y", "--undo", "z"}, {"done", "P1"}, {"blocked", "x", "--tried", "y"}} {
		w.mustFail("your proposal P1.d1 (use a fake clock) waits for the human's answer, which baton gives you here", args...)
	}
	if got := relays(lead); got != "P1/proposal/P1.d1" {
		t.Errorf("queued again: %q", got)
	}

	// One that looks outward-facing has no deadline.
	w, lead2 := asWorker(t)
	if out := w.must("", "propose", "push the branch", "--because", "CI needs it", "--undo", "delete it"); !strings.Contains(out, "It has no deadline: 'push' looks outward-facing or irreversible") {
		t.Errorf("untimed: %s", out)
	}
	if ws := w.state(); ws.PendingProposal().Question != "" || ws.PendingProposal().Untimed == "" {
		t.Errorf("the lead words the question, as it asks it: %+v", ws.PendingProposal())
	}
	if ws, r := w.state(), lead2.state().Relay[0]; r.Untimed == "" || r.Untimed != ws.PendingProposal().Untimed {
		t.Errorf("queued %+v without why it has no deadline", r)
	}
}

// A worker's block stops its phase alone, and is put to the human by the lead session.
func TestAWorkersBlockGoesThroughTheLead(t *testing.T) {
	w, lead := asWorker(t)
	out := w.must("", "blocked", "need the signing key", "--tried", "the keychain")
	if !strings.Contains(out, "baton: recorded. baton puts it to the human; it stops only P1, while the lead session and any other workers carry on") {
		t.Errorf("blocked: %s", out)
	}
	if got := relays(lead); got != "P1/blocked" || lead.state().Relay[0].Because != "need the signing key" || !lead.state().Relay[0].Since.Equal(w.state().Blocked.Since) {
		t.Errorf("the run's relay queue: %q, %+v", got, lead.state().Relay)
	}
	w.mustFail("only they can clear that", "blocked", "again", "--tried", "y")
	if st := lead.state(); st.Blocked != nil || st.Mode != state.ModeRunning || relays(lead) != "P1/blocked" {
		t.Errorf("the lead's run: blocked %+v, mode %s, relays %q", st.Blocked, st.Mode, relays(lead))
	}
}

// A worker's phase done is the run's to land, whatever the plan file says meanwhile.
func TestAWorkerDoneHandsItsWorkToBaton(t *testing.T) {
	for _, c := range []struct {
		name      string
		verifying bool
		want      string
	}{
		{"done", false, "baton: P1 done. baton lands your work on the run's branch: it merges the run's branch into yours if that has moved on, then fast-forwards the run's branch to yours under the lead session."},
		{"verifying", true, "baton: P1 is finished, and verifying: job P1.j1 ("},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, lead := asWorker(t)
			if c.verifying {
				w.set(func(x *state.State) {
					state.QueueJob(x, state.Job{Phase: "P1", Kind: state.JobVerify, Command: "make test"}, w.now)
				})
			}
			pl, _ := w.store().LoadPlan()
			doc, _ := os.ReadFile(pl.File)
			os.WriteFile(pl.File, append(doc, "\n## R2 — Added\n\nMore.\n"...), 0o644)
			out := w.must("", "done", "P1", "--notes", "Fake clock in clock.go.")
			if !strings.Contains(out, c.want) || strings.Contains(out, "plan is complete") || strings.Contains(out, "Next phase") {
				t.Errorf("done: %s", out)
			}
			st := w.state()
			if want := map[bool]string{false: state.ModeComplete, true: state.ModeRunning}[c.verifying]; st.Mode != want {
				t.Errorf("the worker's run is %s, want %s", st.Mode, want)
			}
			if only, _ := w.store().LoadPlan(); len(only.Phases) != 1 {
				t.Errorf("the worker followed the plan file: %+v", only.Phases)
			}
			if handoff, _ := os.ReadFile(lead.store().HandoffPath()); !strings.Contains(string(handoff), "## P1") || !strings.Contains(string(handoff), "Fake clock in clock.go.") {
				t.Errorf("the run's notes: %s", handoff)
			}
		})
	}
}

// The branch checked out when a plan is attached is the run's: workers branch off it.
func TestAttachRecordsTheRunsBranch(t *testing.T) {
	gittest.Isolate(t)
	s, planFile := newSession(t)
	gittest.Git(t, filepath.Dir(planFile), "init", "-q", "-b", "trunk")
	s.attach(planFile)
	if st := s.state(); st.RunBranch != "trunk" || !s.logged("attached", map[string]any{"branch": "trunk"}) {
		t.Errorf("run branch %q\n%s", st.RunBranch, s.events())
	}
}

// baton status lists the phase agents' questions: those baton puts to the human, and those that wait for
// them, which /baton run asks them again. An agent's own status says baton puts its proposal to the human.
func TestStatusListsTheAgentsQuestions(t *testing.T) {
	w, lead := asWorker(t)
	w.must("", "blocked", "need the signing key", "--tried", "the keychain")
	if out := lead.must("", "status"); !strings.Contains(out, "questions from phase agents (baton puts them to you, one at a time):\n  ? P1     blocked: need the signing key — next") {
		t.Errorf("status:\n%s", out)
	}
	lead.set(func(st *state.State) {
		r := st.FrontRelay()
		state.IssueRelay(r, "baton: the agent on P1 is blocked: need the signing key.", 3, "inst", lead.now.Add(5*time.Minute), lead.now)
	})
	if out := lead.must("", "status"); !strings.Contains(out, "? P1     blocked: need the signing key — asked as soon as no other dialog is open") {
		t.Errorf("status:\n%s", out)
	}
	lead.set(func(st *state.State) { st.FrontRelay().Asked = true })
	if out := lead.must("", "status"); !strings.Contains(out, "? P1     blocked: need the signing key — asked; if you do not answer, it waits for you") {
		t.Errorf("status:\n%s", out)
	}
	lead.set(func(st *state.State) { state.AnswerRelay(st.FrontRelay(), state.ByHuman, "Wait for me", lead.now) })
	out := lead.must("", "status")
	if strings.Contains(out, "questions from phase agents") || !strings.Contains(out, "waiting for you (/baton run asks you again):\n  ! P1     blocked: need the signing key") {
		t.Errorf("status:\n%s", out)
	}
	w.set(func(st *state.State) { state.Resume(st) }) // settled meanwhile
	if out := lead.must("", "status"); strings.Contains(out, "waiting for you (/baton run") {
		t.Errorf("status:\n%s", out)
	}

	w, _ = asWorker(t)
	w.must("", "propose", "use a fake clock", "--because", "the real one drifts", "--undo", "revert the commit")
	if out := w.must("", "status"); !strings.Contains(out, "proposal P1.d1: use a fake clock\n  because: the real one drifts\n  baton puts it to you in the lead's terminal, and gives this agent your answer") {
		t.Errorf("the agent's status:\n%s", out)
	}
}

// /baton run asks the human again each question of a phase agent that waits for them: it goes back in
// the queue, afresh. One settled meanwhile, or not applied yet, is not asked again.
func TestRunAsksAgainWhatWaitsForTheHuman(t *testing.T) {
	w, lead := asWorker(t)
	w.must("", "blocked", "need the signing key", "--tried", "the keychain")
	lead.set(func(st *state.State) {
		r := st.FrontRelay()
		state.IssueRelay(r, "baton: the agent on P1 is blocked: need the signing key.", 3, "inst", lead.now.Add(5*time.Minute), lead.now)
		state.AnswerRelay(r, state.ByTimeout, "Wait for me", lead.now)
	})
	if out := lead.must("", "run"); strings.Contains(out, "asks them its question again") {
		t.Fatalf("asked again before its answer was applied:\n%s", out)
	}
	lead.set(func(st *state.State) { st.Relay[0].Applied = lead.now })
	out := lead.must("", "run")
	if !strings.Contains(out, "baton: the agent on P1 waits for the human, and baton's mod asks them its question again; nothing of it is yours to do") {
		t.Fatalf("run:\n%s", out)
	}
	if r := lead.state().Relay[0]; r.Answered != nil || !r.Issued.IsZero() || r.Question != "" {
		t.Errorf("relay %+v: not asked afresh", r)
	}
	if !lead.logged("relay_again", map[string]any{"phase": "P1", "type": "blocked"}) {
		t.Errorf("events:\n%s", lead.events())
	}
	lead.set(func(st *state.State) {
		r := st.FrontRelay()
		state.IssueRelay(r, "q", 4, "inst", lead.now.Add(5*time.Minute), lead.now)
		state.AnswerRelay(r, state.ByHuman, "Wait for me", lead.now)
		r.Applied = lead.now
	})
	w.set(func(st *state.State) { state.Resume(st) })
	if out := lead.must("", "run"); strings.Contains(out, "asks them its question again") {
		t.Errorf("asked again once settled:\n%s", out)
	}
}

// Every session's status line records the account's five-hour usage in the run (a worker's, in the run it
// works for), where the host reads it before it starts a worker. The same reading is written once in a
// while only, and a reading past its reset, or none, changes nothing.
func TestTheStatusLineRecordsTheAccountsUsageInTheRun(t *testing.T) {
	w, lead := asWorker(t)
	home := t.TempDir()
	w.env["HOME"], lead.env["HOME"] = home, home
	line := func(s *session, used float64, resets time.Time) {
		t.Helper()
		in := fmt.Sprintf(`{"session_id":%q,"rate_limits":{"five_hour":{"used_percentage":%v,"resets_at":%d}}}`, s.env["CLAUDE_CODE_SESSION_ID"], used, resets.Unix())
		s.must(in, "statusline")
	}
	usage := func() state.Usage {
		t.Helper()
		u := lead.state().Usage
		if u == nil {
			t.Fatal("no usage recorded in the run")
		}
		return *u
	}
	resets := lead.now.Add(2 * time.Hour).Truncate(time.Second)
	first := w.now
	line(w, 42.5, resets)
	if u := usage(); u.UsedPct != 42.5 || !u.Resets.Equal(resets) || !u.At.Equal(first) {
		t.Fatalf("usage %+v", u)
	}
	if w.state().Usage != nil {
		t.Error("the worker's sub-run recorded the run's usage")
	}

	w.now = w.now.Add(time.Minute)
	line(w, 42.5, resets)
	if u := usage(); !u.At.Equal(first) {
		t.Errorf("the same reading was written again a minute on: %+v", u)
	}
	w.now = w.now.Add(usageFresh)
	line(w, 42.5, resets)
	if u := usage(); !u.At.Equal(w.now) {
		t.Errorf("a reading %s old was not written again: %+v", usageFresh, u)
	}
	w.now = w.now.Add(time.Second)
	resets = resets.Add(5 * time.Hour) // the window rolled over, at the same use
	line(w, 42.5, resets)
	if u := usage(); !u.Resets.Equal(resets) || !u.At.Equal(w.now) {
		t.Errorf("a new reset was not recorded: %+v", u)
	}

	// The lead's reading goes into its own run; what the run's host recorded about a limit stays.
	lead.set(func(x *state.State) { x.Usage.Limited = lead.now.Add(time.Hour) })
	lead.now = w.now.Add(time.Second)
	line(lead, 81, resets)
	if u := usage(); u.UsedPct != 81 || !u.At.Equal(lead.now) || !u.Limited.Equal(first.Add(time.Hour)) {
		t.Errorf("usage %+v", u)
	}

	lead.now = lead.now.Add(time.Minute)
	line(lead, 3, lead.now.Add(-time.Second)) // the window reset since
	lead.must(fmt.Sprintf(`{"session_id":%q}`, "sess-1"), "statusline")
	if u := usage(); u.UsedPct != 81 {
		t.Errorf("a reading past its reset, or none, was recorded: %+v", u)
	}
}

// The status line is the lead's: a usage reading that holds new phase agents back is on baton's band (the
// phase it holds, and on the board why), never on the status line. It records the reading all the same.
func TestTheStatusLineLeavesTheUsageReadingToTheBand(t *testing.T) {
	s := attached(t)
	s.env["HOME"], s.env["BATON_WORKER_USAGE_MAX"] = t.TempDir(), "80"
	in := fmt.Sprintf(`{"session_id":"sess-1","rate_limits":{"five_hour":{"used_percentage":81,"resets_at":%d}}}`, s.now.Add(time.Hour).Unix())
	if out := s.must(in, "statusline"); strings.Contains(out, "5h ") || strings.Contains(out, "81%") {
		t.Errorf("status line %q", out)
	}
	if u := s.state().Usage; u == nil || u.UsedPct != 81 {
		t.Errorf("usage recorded: %+v", u)
	}
}

// section is the block of baton status out that starts with the line header: it, and the indented lines
// under it.
func section(out, header string) string {
	var b []string
	for _, l := range strings.Split(out, "\n") {
		switch {
		case len(b) == 0 && l == header:
			b = append(b, l)
		case len(b) > 0 && strings.HasPrefix(l, "  "):
			b = append(b, l)
		case len(b) > 0:
			return strings.Join(b, "\n")
		}
	}
	return strings.Join(b, "\n")
}

// baton status shows each of the run's workers: how it stands, its branch, session and worktree, what of
// it waits on the human and where to answer it; the merge queue its finished work joins, and what that
// waits on; a branch that keeps commits that never landed; the account's usage; and the decisions every
// worker made without the human, beside the lead's.
func TestBatonStatusShowsTheWorkers(t *testing.T) {
	w, lead := asWorker(t)
	lead.env["HOME"], lead.env["BATON_WORKER_USAGE_MAX"] = t.TempDir(), "80"
	root := lead.store().Checkout
	gittest.Isolate(t)
	gittest.Init(t, root)
	gittest.Write(t, root, "a.txt", "a\n")
	gittest.Commit(t, root, "first")
	gittest.Git(t, root, "branch", "baton/sess-1/R1")
	now := lead.now
	at := func(d time.Duration) string { return now.Add(d).Local().Format("15:04") }
	w.must("", "note", "used a fake clock", "--undo", "revert the commit")
	lead.now = now.Add(time.Minute)
	lead.must("", "note", "skipped the flaky test", "--undo", "run it again")
	lead.now = now
	tree := lead.state().Workers[0].Worktree
	lead.set(func(x *state.State) {
		x.RunBranch = "main"
		p1 := &x.Workers[0]
		p1.Status, p1.Assigned, p1.Branch, p1.AgentID = state.WorkerWorking, now.Add(-14*time.Minute), "baton/sess-1/P1", "a1b2c3"
		x.Workers = append(x.Workers,
			state.Worker{Phase: "R1", Ref: "sess-1/R1", Branch: "baton/sess-1/R1", Status: state.WorkerFailed,
				Why: "its agent never started", Unlanded: 2},
			state.Worker{Phase: "R1", Ref: "sess-1/R1", Branch: "baton/sess-1/R1", Worktree: "/wt/R1", Status: state.WorkerLanding,
				Assigned: now.Add(-time.Hour), Generation: x.Generation,
				Merge: &state.Merge{Queued: now.Add(-5 * time.Minute), Deferred: "the lead changed a.txt too", DeferredAt: now.Add(-2 * time.Minute)}})
		x.Phases["R1"] = &state.PhaseState{Status: state.PhaseWorking, Worker: "sess-1/R1"}
		x.Usage = &state.Usage{UsedPct: 62, Resets: now.Add(2 * time.Hour), At: now.Add(-time.Minute)}
	})
	out := lead.must("", "status")
	for _, want := range []string{
		"workers (each an agent in a worktree of its own; the agent panel below the prompt shows them):\n" +
			"  P1     working 14m · branch baton/sess-1/P1 · agent a1b2c3\n" +
			"         worktree: " + tree + "\n" +
			"  R1     finished, waits to land · branch baton/sess-1/R1\n" +
			"         worktree: /wt/R1",
		"merge queue (first finished, first landed on main):\n" +
			"  1. R1     finished " + at(-5*time.Minute) + " · waits to land: the lead changed a.txt too (since " + at(-2*time.Minute) + ")",
		"decisions made without you (this run, oldest first):\n" +
			"  P1.d1 P1       " + decide.Stamp(now) + "  noted: used a fake clock\n" +
			"       undo: revert the commit\n" +
			"  d1   P0       " + decide.Stamp(now.Add(time.Minute)) + "  noted: skipped the flaky test\n" +
			"       undo: run it again",
	} {
		header, _, _ := strings.Cut(want, "\n")
		if got := section(out, header); got != want {
			t.Errorf("got:\n%s\nwant:\n%s\nin:\n%s", got, want, out)
		}
	}
	for id, want := range map[string]string{"P1": "  ⚙ P1       The one-liners  — needs no other phase  (worker: working 14m)",
		"R1": "  ⚙ R1       The roster can grow  — after P1  (worker: finished, waits to land)"} {
		if got := phaseLines(out, id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	if want := "usage: 62% of the five-hour allowance, which resets at " + at(2*time.Hour) + " (read " + at(-time.Minute) + ") · new workers start below 80%"; !strings.Contains(out, want+"\n") {
		t.Errorf("no %q in:\n%s", want, out)
	}
	lead.set(func(x *state.State) { x.Usage.Limited, x.Usage.Hit, x.Usage.By = now, now.Add(-time.Hour), "lead" })
	if out := lead.must("", "status"); strings.Contains(out, "usage limit") {
		t.Errorf("a limit that is over is shown:\n%s", out)
	}

	// A worker that ended keeping commits that never landed is named while its branch is there, and while
	// no later worker took its phase up on the same branch; a suspended one says what resumes it; a usage
	// limit says until when.
	lead.set(func(x *state.State) {
		x.Workers = x.Workers[:2]
		x.Phases["R1"] = &state.PhaseState{Status: state.PhasePending}
		p1 := &x.Workers[0]
		p1.Status, p1.Why, p1.AgentID = state.WorkerSuspended, "baton exited", ""
		x.Usage.Limited, x.Usage.Hit, x.Usage.By = now.Add(time.Hour), now.Add(-10*time.Minute), "P1"
	})
	out = lead.must("", "status")
	for _, want := range []string{
		"workers (each an agent in a worktree of its own; the agent panel below the prompt shows them):\n" +
			"  P1     suspended · branch baton/sess-1/P1\n" +
			"         worktree: " + tree + "\n" +
			"         baton exited; a fresh agent takes it up as soon as one may start (with the lead's baton session)\n" +
			"  R1     ended: its agent never started · its branch baton/sess-1/R1 keeps 2 commits that never landed",
		"usage limit: the worker on P1 hit it at " + at(-10*time.Minute) + " → no worker starts, and baton types nothing into the run's sessions, until " + at(time.Hour),
	} {
		header, _, _ := strings.Cut(want, "\n")
		if got := section(out, header); got != want {
			t.Errorf("got:\n%s\nwant:\n%s\nin:\n%s", got, want, out)
		}
	}
	if strings.Contains(out, "merge queue") {
		t.Errorf("a merge queue with nothing in it:\n%s", out)
	}
	gittest.Git(t, root, "branch", "-D", "baton/sess-1/R1")
	if out := lead.must("", "status"); strings.Contains(out, "never landed") {
		t.Errorf("a branch that is gone is named:\n%s", out)
	}

	// With no workers, the reading is not shown.
	lead.env["BATON_WORKERS"] = "0"
	if out := lead.must("", "status"); strings.Contains(out, "usage: ") {
		t.Errorf("usage shown with no workers:\n%s", out)
	}
}

// baton status says how many workers may run at once, what the machine reads now, and what holds back a
// phase that waits for a worker.
func TestBatonStatusShowsTheMachine(t *testing.T) {
	linux := machine.Reading{CPUs: 8, MemTotal: 16 << 30, MemAvailablePct: 40, Load1: 2.54, Known: true}
	small := machine.Reading{CPUs: 4, MemTotal: 4 << 30, MemAvailablePct: 30, Load1: 1, Known: true}
	unknown := machine.Reading{CPUs: 8}
	for _, c := range []struct {
		name string
		env  map[string]string
		m    *machine.Reading
		hold bool
		want string // the lines, or "" for none
	}{
		{"auto, on a Mac", nil, nil, false, "workers: auto, up to 6 on this machine (14 CPUs, 48 GiB) · load 3.1 · memory pressure normal (45% free)"},
		{"auto, on Linux", nil, &linux, false, "workers: auto, up to 3 on this machine (8 CPUs, 16 GiB) · load 2.5 · 40% memory free"},
		{"auto, on a machine too small for one", nil, &small, false, "workers: auto, none on this machine (4 CPUs, 4 GiB) · load 1.0 · 30% memory free"},
		{"auto, with no live readings", nil, &unknown, false, "workers: auto, none on this machine (8 CPUs, memory unknown)"},
		{"a number", map[string]string{"BATON_WORKERS": "2"}, nil, false, "workers: up to 2 · load 3.1 · memory pressure normal (45% free)"},
		{"held back", nil, nil, true, "workers: auto, up to 6 on this machine (14 CPUs, 48 GiB) · load 3.1 · memory pressure normal (45% free)\n" +
			"  held: P1 waits for a worker since {since}: the machine is busy (load 15.0 on 14 CPUs)"},
		{"workers off", map[string]string{"BATON_WORKERS": "0"}, nil, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := attached(t)
			s.env["HOME"], s.env["BATON_HOME"] = t.TempDir(), t.TempDir()
			for k, v := range c.env {
				s.env[k] = v
			}
			s.machine = c.m
			if c.hold {
				s.set(func(x *state.State) {
					x.Hold = &state.Hold{Phase: "P1", Kind: state.HoldBusy, Why: "the machine is busy (load 15.0 on 14 CPUs)", Since: s.now.Add(-3 * time.Minute)}
				})
			}
			out := s.must("", "status")
			want := strings.ReplaceAll(c.want, "{since}", s.now.Add(-3*time.Minute).Local().Format("15:04"))
			first, _, _ := strings.Cut(want, "\n")
			if got := section(out, first); want != "" && got != want || want == "" && strings.Contains(out, "workers:") {
				t.Errorf("got:\n%s\nwant:\n%s\nin:\n%s", got, want, out)
			}
		})
	}
}

// Nobody watches a worker's session: its note is announced in the lead's.
func TestAWorkersNoteIsAnnouncedInTheLead(t *testing.T) {
	w, lead := asWorker(t)
	w.must("", "note", "used a fake clock", "--undo", "revert the commit")
	if ann := strings.Join(lead.state().Run.Announce, "\n"); ann != "baton: the worker on P1 noted — used a fake clock (P1.d1)" {
		t.Errorf("the lead's session is told %q", ann)
	}
	lead.must("", "note", "skipped the flaky test", "--undo", "run it again")
	if ann := lead.state().Run.Announce; len(ann) != 1 {
		t.Errorf("the lead's own note is announced by its hooks, not here: %q", ann)
	}
}

// Attaching a plan says how many workers will work beside the session, and warns when the mode they start
// in would stop them at permission prompts.
func TestAttachSaysHowManyWorkersRun(t *testing.T) {
	for _, c := range []struct {
		name         string
		env          map[string]string
		want, absent string
	}{
		{"auto by default: as many as the plan allows, in the default mode", nil,
			"baton: tell the human: up to 1 worker works beside this session on the phases that can go ahead, each in a worktree of its own, " +
				"and baton lands their work on this branch (the plan's widest step is 2 phases; this machine allows 6).", ""},
		{"the default mode stops them at prompts", nil, "Workers work in this session's permission mode (default as it started)", ""},
		{"a mode with no prompts", map[string]string{"BATON_WORKER_MODE": "auto"}, "(workers: 2).", "permission mode"},
		{"none", map[string]string{"BATON_WORKERS": "0"}, "", "worker"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, planFile := newSession(t)
			s.env["HOME"], s.env["BATON_HOME"] = t.TempDir(), t.TempDir()
			for k, v := range c.env {
				s.env[k] = v
			}
			if c.env["BATON_WORKER_MODE"] != "" {
				s.env["BATON_WORKERS"] = "2"
			}
			doc, _ := os.ReadFile(planFile)
			os.WriteFile(planFile, []byte(strings.Replace(string(doc), "Small fixes.", "After: none\n\nSmall fixes.", 1)), 0o644)
			out := s.must(s.must("", "attach", planFile, "--suggest"), "attach", planFile, "--spec", "-")
			if c.want != "" && !strings.Contains(out, c.want) || c.absent != "" && strings.Contains(out, c.absent) {
				t.Errorf("attach said:\n%s", out)
			}
		})
	}
}

// A phase agent shares the lead's session and its environment, so the baton command in one is its phase's
// by where it runs: inside the phase's worktree it is the phase's (its decisions, its done), and refused
// what belongs to the lead; anywhere else it is the lead's.
func TestTheBatonCommandInAPhaseAgentIsItsPhases(t *testing.T) {
	w, lead := asWorker(t)
	tree := w.store().Root
	os.MkdirAll(filepath.Join(tree, "pkg"), 0o755)
	t.Chdir(filepath.Join(tree, "pkg"))
	lead.mustFail("baton pause belongs to the lead session. Carry on with P1", "pause")
	lead.must("", "note", "used a fake clock", "--undo", "revert the commit")
	lead.must("", "done", "P1", "--notes", "P1 is done.")
	if ws, ls := w.state(), lead.state(); ws.Mode != state.ModeComplete || len(ws.Decisions) != 1 || ws.Decisions[0].ID != "P1.d1" ||
		len(ls.Decisions) != 0 || ls.Mode != state.ModeRunning || ls.Current != "P0" {
		t.Errorf("the phase's run %s %+v; the lead's %s on %s %+v", ws.Mode, ws.Decisions, ls.Mode, ls.Current, ls.Decisions)
	}
	t.Chdir(filepath.Dir(lead.env["BATON_DIR"]))
	lead.must("", "note", "the lead's own", "--undo", "nothing")
	if ls := lead.state(); len(ls.Decisions) != 1 || ls.Decisions[0].ID != "d1" {
		t.Errorf("the lead's decisions %+v", ls.Decisions)
	}
}

// /baton run asks for a fresh phase agent on a phase that waits for the human because of its agent: one
// they stopped (held), or one whose last turn was refused, whose escalation it clears. The host starts it
// (the worker is suspended). With neither, the run is running, as before.
func TestBatonRunAsksForAFreshAgent(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(e *state.Worker, sub *state.State)
		why  string // "" for none
	}{
		{"held", func(e *state.Worker, _ *state.State) { e.Status = state.WorkerHeld }, "you stopped the agent before it"},
		{"refused", func(_ *state.Worker, sub *state.State) {
			sub.Run.Escalation = &state.Escalation{Kind: "refused", Reason: "P1 stopped"}
			sub.Run.LastError = &state.StopError{Error: state.ErrRefusal}
		}, "the model's safeguards declined the last turn of the agent before it"},
		// Its agent ended with the lead's claude since: a fresh agent, never a message into that conversation.
		{"refused, then suspended", func(e *state.Worker, sub *state.State) {
			e.Status, e.Why, e.Resume = state.WorkerSuspended, "the baton session ended", "sess-1"
			sub.Run.Escalation = &state.Escalation{Kind: "refused", Reason: "P1 stopped"}
		}, "the model's safeguards declined the last turn of the agent before it"},
		{"at work", func(*state.Worker, *state.State) {}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, lead := asWorker(t)
			p, _ := state.OpenProject(lead.env["BATON_DIR"], nil)
			sub, err := p.ExistingRun(state.WorkerRef(lead.store().Ref, "P1"), "")
			if err != nil {
				t.Fatal(err)
			}
			var e state.Worker
			sub.Update(func(x *state.State) error {
				lead.set(func(st *state.State) {
					st.Workers[0].Status, st.Workers[0].AgentID = state.WorkerWorking, "a1"
					c.set(&st.Workers[0], x)
					e = st.Workers[0]
				})
				return nil
			})
			out := lead.must("", "run")
			got := lead.state().Workers[0]
			sst, _ := sub.Load()
			if c.why == "" {
				if !strings.Contains(out, "is already running") || got.Status != state.WorkerWorking {
					t.Errorf("%s: %s; %+v", c.name, out, got)
				}
				return
			}
			if !strings.Contains(out, decide.AgentAfresh("P1", c.why)) || strings.Contains(out, "already running") {
				t.Errorf("out: %s", out)
			}
			if got.Status != state.WorkerSuspended || got.Why != "the human asked for a fresh agent: "+c.why || !got.Since.Equal(lead.now) || !got.Is(e) || got.Resume != "" {
				t.Errorf("the worker %+v", got)
			}
			if sst.Run.Escalation != nil || sst.Run.LastError != nil {
				t.Errorf("the sub-run still waits: %+v %+v", sst.Run.Escalation, sst.Run.LastError)
			}
		})
	}
}
