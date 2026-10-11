package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

// asWorker is a run attached in sess-1 (the lead, on P0, host inst) with a worker on P1 in a worktree of
// its own, its phase started; w runs commands as the worker's session, lead as the lead's.
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
	lead.set(func(x *state.State) {
		if err := state.Assign(x, pl, state.Worker{Phase: "P1", Ref: state.WorkerRef(run.Ref, "P1"), Session: "wsess"}, lead.now); err != nil {
			t.Fatal(err)
		}
	})
	p, _ := state.OpenProject(lead.env["BATON_DIR"], nil)
	sub, err := p.NewWorker(run.Ref, "P1", "wsess", "inst", t.TempDir())
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
	w = &session{t: t, now: lead.now, env: map[string]string{"BATON_DIR": lead.env["BATON_DIR"], "BATON_HOST": "1", "BATON_ROLE": "worker",
		"BATON_INSTANCE": state.WorkerInstance("inst", "P1"), "BATON_PHASE": "P1", "CLAUDE_CODE_SESSION_ID": "wsess"}}
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
			w.mustFail("not run — this session is a worker: it works on P1 alone, in a worktree of its own, while the lead session runs the plan in the human's terminal, "+
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
	for _, want := range []string{"baton: proposal P1.d1 recorded. Nobody watches this session, so baton puts it to the human through the lead session",
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
	if !w.logged("proposed", map[string]any{"id": "P1.d1", "relayed": true}) || strings.Contains(w.events(), `"deadline"`) {
		t.Errorf("the worker's events:\n%s", w.events())
	}
	for _, args := range [][]string{{"propose", "x", "--because", "y", "--undo", "z"}, {"done", "P1"}, {"blocked", "x", "--tried", "y"}} {
		w.mustFail("your proposal P1.d1 (use a fake clock) waits for the human's answer, which baton gets through the lead session", args...)
	}
	if got := relays(lead); got != "P1/proposal/P1.d1" {
		t.Errorf("queued again: %q", got)
	}

	// One that looks outward-facing has no deadline.
	w, _ = asWorker(t)
	if out := w.must("", "propose", "push the branch", "--because", "CI needs it", "--undo", "delete it"); !strings.Contains(out, "It has no deadline: 'push' looks outward-facing or irreversible") {
		t.Errorf("untimed: %s", out)
	}
	if ws := w.state(); ws.PendingProposal().Question != "" || ws.PendingProposal().Untimed == "" {
		t.Errorf("the lead words the question, as it asks it: %+v", ws.PendingProposal())
	}
}

// A worker's block stops its phase alone, and is put to the human by the lead session.
func TestAWorkersBlockGoesThroughTheLead(t *testing.T) {
	w, lead := asWorker(t)
	out := w.must("", "blocked", "need the signing key", "--tried", "the keychain")
	if !strings.Contains(out, "baton: recorded. baton puts it to the human through the lead session; it stops only P1, while the lead session and any other workers carry on") {
		t.Errorf("blocked: %s", out)
	}
	if got := relays(lead); got != "P1/blocked" {
		t.Errorf("the run's relay queue: %q", got)
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
		{"done", false, "baton: P1 done. baton lands your work on the run's branch: it rebases your branch onto it and merges it under the lead session."},
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
