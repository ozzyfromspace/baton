package cli

import (
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// withWorker adds to lead's run (asWorker's) a worker on phase, working in a worktree of its own, and
// returns a session that runs commands as its agent.
func withWorker(t *testing.T, lead *session, phase string) *session {
	t.Helper()
	run := lead.store()
	pl, _ := run.LoadPlan()
	pl.Phases[pl.Index(phase)].After = []string{}
	run.SavePlan(pl)
	tree := t.TempDir()
	lead.set(func(x *state.State) {
		if err := state.Assign(x, pl, state.Worker{Phase: phase, Ref: state.WorkerRef(run.Ref, phase), Worktree: tree}, lead.now); err != nil {
			t.Fatal(err)
		}
	})
	p, _ := state.OpenProject(lead.env["BATON_DIR"], nil)
	sub, err := p.NewWorker(run.Ref, phase, "inst", tree)
	if err != nil {
		t.Fatal(err)
	}
	only, _ := pl.Only(phase)
	sub.SavePlan(only)
	sub.Update(func(x *state.State) error {
		*x = state.AttachWorker(*x, phase)
		state.Start(x, lead.now, state.Origin{})
		return nil
	})
	return &session{t: t, now: lead.now, env: maps.Clone(lead.env), dir: sub.Root}
}

// working marks the workers on phases as their agents at work.
func working(lead *session, phases ...string) {
	lead.set(func(x *state.State) {
		for i := range x.Workers {
			for _, ph := range phases {
				if x.Workers[i].Phase == ph {
					x.Workers[i].Status = state.WorkerWorking
				}
			}
		}
	})
}

// factMail is the facts left for the agent of w's sub-run.
func factMail(w *session) []string {
	var out []string
	for _, m := range w.state().Inbox {
		if m.Kind == state.MessageFact {
			out = append(out, m.Text)
		}
	}
	return out
}

// A fact the lead records is the run's, with the lead's phase: on record once, announced, logged, and left
// for each phase agent at work; the same words again record nothing.
func TestTheLeadRecordsAnEnvironmentFact(t *testing.T) {
	w, lead := asWorker(t)
	working(lead, "P1")
	out := lead.must("", "env", "leave host port 5432 free:", "the dev stack holds it")
	const text = "leave host port 5432 free: the dev stack holds it"
	if !strings.Contains(out, "baton: recorded, as the run's environment fact 1: "+text+". Every later brief in this run lists it, and P1, at work now, is told.") {
		t.Errorf("said: %s", out)
	}
	st := lead.state()
	if len(st.Facts) != 1 || st.Facts[0].Text != text || st.Facts[0].Phase != "P0" || !st.Facts[0].At.Equal(lead.now) {
		t.Fatalf("facts %+v", st.Facts)
	}
	if !strings.Contains(strings.Join(st.Run.Announce, "\n"), "baton: P0 recorded what the gates' environment needs — "+text+" → every later brief lists it; P1 told") {
		t.Errorf("announced %q", st.Run.Announce)
	}
	if !lead.logged("env_fact", map[string]any{"text": text, "phase": "P0", "by": "lead", "n": float64(1)}) {
		t.Errorf("events:\n%s", lead.events())
	}
	if got := factMail(w); len(got) != 1 || got[0] != decide.FactTold(st.Facts[0]) {
		t.Errorf("P1's mail %q", got)
	}
	if ws := w.state(); len(ws.Facts) != 0 {
		t.Errorf("the worker's sub-run keeps facts of its own: %+v", ws.Facts)
	}

	out = lead.must("", "env", "Leave host port 5432   FREE: the dev stack holds it")
	if !strings.Contains(out, "baton: already on record, from P0: "+text+". Nothing new to record.") {
		t.Errorf("again: %s", out)
	}
	if st := lead.state(); len(st.Facts) != 1 || len(factMail(w)) != 1 {
		t.Errorf("recorded or told again: %+v, mail %q", st.Facts, factMail(w))
	}
}

// A phase agent's fact goes to the run it works for, with its phase, and to every other agent at work: not
// to itself, nor to one whose phase is done and waits to land.
func TestAPhaseAgentsFactIsTheRuns(t *testing.T) {
	w, lead := asWorker(t)
	r1 := withWorker(t, lead, "R1")
	working(lead, "P1", "R1")
	out := w.must("", "env", "run docker compose with -p $BATON_JOB, so lanes do not share containers")
	if !strings.Contains(out, "and R1, at work now, is told") {
		t.Errorf("said: %s", out)
	}
	st := lead.state()
	if len(st.Facts) != 1 || st.Facts[0].Phase != "P1" {
		t.Fatalf("the run's facts %+v", st.Facts)
	}
	if !lead.logged("env_fact", map[string]any{"phase": "P1", "by": "agent"}) {
		t.Errorf("the run's events:\n%s", lead.events())
	}
	if got := factMail(r1); len(got) != 1 || !strings.HasPrefix(got[0], "[baton] P1 found what this run's gates need") {
		t.Errorf("R1's mail %q", got)
	}
	if got := factMail(w); len(got) != 0 {
		t.Errorf("P1 is told its own fact: %q", got)
	}

	// R1's work done, it waits to land: it is told nothing more.
	lead.set(func(x *state.State) { x.WorkerOn("R1").Status = state.WorkerLanding })
	if out := lead.must("", "env", "start colima before the gate"); !strings.Contains(out, ", and P1, at work now, is told.") {
		t.Errorf("said: %s", out)
	}
	if got := factMail(r1); len(got) != 1 {
		t.Errorf("R1, landing, is told: %q", got)
	}
	if got := factMail(w); len(got) != 1 {
		t.Errorf("P1, at work, is told the lead's: %q", got)
	}
}

// baton env refuses no words, a fact past a line, one past the run's last, and a run with no plan; each
// refusal records nothing.
func TestEnvRefusals(t *testing.T) {
	s := attached(t)
	s.mustFail(decide.EnvUsage, "env")
	s.mustFail(decide.EnvUsage, "env", "  ")
	s.mustFail("unknown flag --port", "env", "--port", "5432")

	long := strings.Repeat("x", state.MaxFactLen+1)
	s.mustFail(decide.FactTooLong(len(long)), "env", long)
	if !s.logged("refused", map[string]any{"command": "env", "why": "too long"}) {
		t.Errorf("events:\n%s", s.events())
	}

	s.set(func(x *state.State) {
		for i := range state.MaxFacts {
			state.AddFact(x, "fact "+string(rune('a'+i%26))+strings.Repeat("!", i/26), "P0", s.now)
		}
	})
	s.mustFail(decide.FactsFull(), "env", "one more")
	if st := s.state(); len(st.Facts) != state.MaxFacts || !s.logged("refused", map[string]any{"command": "env", "why": "too many"}) {
		t.Errorf("%d facts; events:\n%s", len(st.Facts), s.events())
	}
	// One on record already is said so, even at the cap.
	if out := s.must("", "env", "fact a"); !strings.Contains(out, "already on record") {
		t.Errorf("known at the cap: %s", out)
	}

	bare, _ := newSession(t)
	bare.mustFail(state.ErrNoPlan.Error(), "env", "leave port 5432 free")
	if _, err := os.Stat(bare.env["BATON_DIR"]); err == nil {
		if st := bare.state(); len(st.Facts) != 0 {
			t.Errorf("a run with no plan records %+v", st.Facts)
		}
	}
}
