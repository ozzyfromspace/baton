package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// demoDoc is the plan file of a plannedCrew: standing rules, then P0 (the lead's), P1 and P2, which can
// start at once.
const demoDoc = `# Demo

## Standing rules

- Commit by path.

## P0 — zero

After: none

Lay the ground.

## P1 — one

After: none

Write the parser.

## P2 — two

After: none

Write the printer.

## Verification

Run the tests.
`

// plannedCrew is a crew with one agent's room whose plan is demoDoc, in a file of its own, and that file.
func plannedCrew(t *testing.T) (*crew, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "demo.md")
	if err := os.WriteFile(file, []byte(demoDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	pl, err := plan.Build(file, []byte(demoDoc), plan.Spec{Title: "Demo", RulesAnchor: "## Standing rules", EndAnchor: "## Verification", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", Anchor: "## P0 — zero"}, {ID: "P1", Title: "one", Anchor: "## P1 — one"}, {ID: "P2", Title: "two", Anchor: "## P2 — two"}}})
	if err != nil {
		t.Fatal(err)
	}
	return newCrewOn(t, 1, pl), file
}

// follow changes the crew's plan file by edit, and has the lead follow the change, as its stop does
// (hooks' planDrift).
func (x *crew) follow(file string, edit func(string) string) {
	x.t.Helper()
	if err := os.WriteFile(file, []byte(edit(demoDoc)), 0o644); err != nil {
		x.t.Fatal(err)
	}
	adopted, changed, err := plan.Recheck(x.pl, state.Remaining(x.state(), x.pl))
	if !changed || err != nil {
		x.t.Fatalf("the lead does not follow the change: changed %v, %v", changed, err)
	}
	if err := x.lead.SavePlan(adopted); err != nil {
		x.t.Fatal(err)
	}
	x.pl = adopted
}

// swap is an edit of a plan file: old becomes new.
func swap(old, new string) func(string) string {
	return func(doc string) string { return strings.Replace(doc, old, new, 1) }
}

// planMail is the messages about the plan that the sub-run on phase has for its agent.
func (x *crew) planMail(phase string) []state.Message {
	st, err := x.sub(phase).Load()
	if err != nil {
		x.t.Fatal(err)
	}
	var out []state.Message
	for _, m := range st.Inbox {
		if m.Kind == state.MessagePlan {
			out = append(out, m)
		}
	}
	return out
}

// The human changes the plan while P1's agent is at work: the lead follows it, and the next step passes it
// on to P1's sub-run, whose plan is the new one narrowed to P1. Its agent is told the new text of what
// changed for it, once (a step after changes nothing), and nothing of a change to another phase.
func TestAPlanChangeReachesTheAgentAtWork(t *testing.T) {
	section, rules := swap("Write the parser.", "Write the parser, and its tests."), swap("- Commit by path.", "- Commit by path.\n- Never push.")
	for _, c := range []struct {
		name           string
		edit           func(string) string // nil: the plan file is not changed
		rules, section bool                // what P1's agent is told the new text of
	}{
		{"its section changed", section, false, true},
		{"the rules changed", rules, true, false},
		{"the rules and its section changed", func(d string) string { return rules(section(d)) }, true, true},
		{"another phase's section changed", swap("Write the printer.", "Write the printer, in color."), false, false},
		{"no change", nil, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			x, file := plannedCrew(t)
			x.atWork("P1")
			if c.edit != nil {
				x.follow(file, c.edit)
			}
			for range 3 { // a step after the first passes nothing on again
				x.tick(time.Second)
			}
			if got, _ := x.sub("P1").LoadPlan(); got.SHA256 != x.pl.SHA256 || len(got.Phases) != 1 || got.Phases[0].ID != "P1" {
				t.Fatalf("P1's plan is %s with %+v, the lead's %s", short(got.SHA256), got.Phases, short(x.pl.SHA256))
			}
			mail := x.planMail("P1")
			told := c.rules || c.section
			if !told {
				if len(mail) > 0 || x.logged("plan_told", map[string]any{"phase": "P1"}) {
					t.Fatalf("P1's agent is told %+v:\n%s", mail, x.events())
				}
				return
			}
			doc, _ := os.ReadFile(file)
			only, _ := x.pl.Only("P1")
			wantRules, wantSection := "", ""
			if c.rules {
				wantRules = only.Rules(doc)
			}
			if c.section {
				wantSection, _ = only.Section(doc, 0)
			}
			want := decide.PlanChanged("P1", wantRules, wantSection)
			if len(mail) != 1 || mail[0].Text != want || mail[0].Phase != "P1" {
				t.Fatalf("P1's agent has %+v, want one message:\n%s", mail, want)
			}
			if !x.logged("plan_told", map[string]any{"phase": "P1", "rules": c.rules, "section": c.section}) {
				t.Errorf("events:\n%s", x.events())
			}
			if line := decide.PlanTold("P1", c.rules, c.section); !strings.Contains(strings.Join(x.state().Run.Announce, "\n"), line) {
				t.Errorf("announced %q, want %q", x.state().Run.Announce, line)
			}
			if said := x.endTurn("P1"); !strings.Contains(said, want) {
				t.Errorf("P1's stop gave it %q, want the message", said)
			}
			x.tick(time.Second)
			if mail := x.planMail("P1"); len(mail) > 0 {
				t.Errorf("told again: %+v", mail)
			}
		})
	}
}

// A fresh agent on a phase is told nothing of a change its brief reads already: one that ended before it
// was told is replaced by one briefed from the new text.
func TestAFreshAgentReadsThePlanAsItIs(t *testing.T) {
	x, file := plannedCrew(t)
	x.atWork("P1")
	x.modOff = true // nothing reaches the agent
	x.follow(file, swap("Write the parser.", "Write the parser, and its tests."))
	x.tick(time.Second)
	if len(x.planMail("P1")) != 1 {
		t.Fatalf("P1's agent has %+v", x.planMail("P1"))
	}
	x.modOff = false
	x.c.setEntry(x.lead, x.worker("P1"), func(e *state.Worker) { e.Status, e.Since, e.Why = state.WorkerSuspended, x.now, "its agent is gone" })
	x.atWork("P1")
	if a := x.agent("P1"); a.id == "ap11" || !strings.Contains(x.briefs[a.id], "Write the parser, and its tests.") {
		t.Fatalf("P1's agent %s was given:\n%s", a.id, x.briefs[a.id])
	}
	if mail := x.planMail("P1"); len(mail) > 0 {
		t.Errorf("the fresh agent is told what its brief says: %+v", mail)
	}
}
