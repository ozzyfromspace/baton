//go:build !windows

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// served is what a reply in a transcript was served (docs/research/tiers.md): its model and effort.
type served struct {
	At            time.Time
	Model, Effort string
}

// servedIn is every reply of the conversation's own in the transcript at path (agent: a subagent's own
// transcript, whose every entry is a sidechain's), with what it was served.
func servedIn(t *testing.T, path string, agent bool) []served {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading a transcript: %v", err)
	}
	var out []served
	for _, line := range strings.Split(string(b), "\n") {
		var e struct {
			Type      string `json:"type"`
			Sidechain bool   `json:"isSidechain"`
			Error     bool   `json:"isApiErrorMessage"`
			Timestamp string `json:"timestamp"`
			Effort    string `json:"effort"`
			Message   struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &e) != nil || e.Type != "assistant" || e.Sidechain != agent || e.Error || e.Message.Model == "" || e.Message.Model == "<synthetic>" {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
		out = append(out, served{At: at, Model: e.Message.Model, Effort: e.Effort})
	}
	return out
}

// efforts are the efforts of the replies in [from, to), in order, each once in a row.
func efforts(replies []served, from, to time.Time) []string {
	var out []string
	for _, r := range replies {
		if r.At.Before(from) || !to.IsZero() && !r.At.Before(to) {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != r.Effort {
			out = append(out, r.Effort)
		}
	}
	return out
}

// sessionTranscript is the transcript of the lead's session of the one run in dir, from the session id
// its hooks recorded: Claude Code keeps it under ~/.claude/projects, in the folder of its directory.
func sessionTranscript(t *testing.T, dir string) string {
	t.Helper()
	sid := fmt.Sprint(runState(t, dir)["run"].(map[string]any)["session_id"])
	home, _ := os.UserHomeDir()
	paths, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sid+".jsonl"))
	if len(paths) != 1 {
		t.Fatalf("the lead's transcript of session %s: %v", sid, paths)
	}
	return paths[0]
}

// at is when the first event cond matches was logged, or the zero time.
func at(events []map[string]any, cond func(map[string]any) bool) time.Time {
	if i := first(events, cond); i >= 0 {
		return eventTime(events[i])
	}
	return time.Time{}
}

// tierPlan is three phases in a row, each on its own effort: down from high to low, then up to medium.
const tierPlan = `# Tiers

## P0 — First
Model: haiku
Effort: high

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Second
Model: haiku
Effort: low

Run echo one, then report P1 done: baton done P1 --notes 'said one'

## P2 — Third
Model: haiku
Effort: medium

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// In the lead, with no phase agents, each phase runs on its own tier: at each boundary whose phase names
// another effort, baton restarts the lead in place on it, in the same conversation, and the restarted
// session compacts and starts the phase. The tiers go down a step and up again. Every reply of each phase,
// in the lead's transcript, was served that phase's effort.
func TestEachPhaseRunsOnItsTierInTheLead(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, tierPlan)
	s := Start(t, dir, "--model", "haiku", "--effort", "high", "Start the attached plan, and follow baton's instructions.")
	s.Trust()
	if !s.Until("the plan to complete", 10*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 }) {
		logEvents(t, "the run", s.Events())
		t.Fatal("the plan did not complete")
	}
	ev := s.Events()
	for _, want := range []func(map[string]any) bool{
		is("tier_restart", "phase", "P1", "to", "haiku/low", "why", "its boundary"),
		is("restarted", "phase", "P1", "tier", "haiku/low"),
		is("tier_restart", "phase", "P2", "to", "haiku/medium", "why", "its boundary"),
		is("restarted", "phase", "P2", "tier", "haiku/medium"),
	} {
		if first(ev, want) < 0 {
			logEvents(t, "the run", ev)
			t.Fatal("a restart on a phase's tier is missing")
		}
	}
	if first(ev, is("tier_restart", "phase", "P0")) >= 0 {
		t.Errorf("restarted for P0, whose tier the lead was launched on")
	}
	replies := servedIn(t, sessionTranscript(t, dir), false)
	for _, c := range []struct {
		phase, effort string
		next          func(map[string]any) bool
	}{
		{"P0", "high", is("phase_done", "phase", "P0")},
		{"P1", "low", is("phase_done", "phase", "P1")},
		{"P2", "medium", is("phase_done", "phase", "P2")},
	} {
		from := at(ev, is("phase_started", "phase", c.phase))
		if c.phase == "P0" {
			from = time.Time{}
		}
		got := efforts(replies, from, at(ev, c.next))
		if len(got) != 1 || got[0] != c.effort {
			t.Errorf("%s's replies were served %v, want only %s", c.phase, got, c.effort)
		}
	}
	for _, r := range replies {
		if !strings.Contains(r.Model, "haiku") {
			t.Errorf("a reply on %s", r.Model)
		}
	}
}

// agentTierPlan is a lead phase on the lead's tier, and two phases beside it, each on another effort.
const agentTierPlan = `# Agent tiers

## P0 — Lead's
After: none
Model: haiku
Effort: high

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Low
After: none
Model: haiku
Effort: low

Run echo one, then report P1 done: baton done P1 --notes 'said one'

## P2 — Max
After: none
Model: haiku
Effort: max

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// With phase agents, the lead takes the phase on its own tier, and the phases on other tiers go to phase
// agents, each spawned as the type of its effort: every reply of each agent was served its phase's effort,
// and the lead never restarts.
func TestEachPhaseAgentRunsOnItsTier(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, agentTierPlan)
	s := StartAgents(t, dir, []string{"BATON_WORKERS=2"}, "--effort", "high")
	if !s.Until("the plan to complete", 12*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 }) {
		t.Fatal("the plan did not complete")
	}
	ev := s.Events()
	for _, want := range []func(map[string]any) bool{
		is("worker_spawned", "phase", "P1", "tier", "haiku/low"),
		is("worker_spawned", "phase", "P2", "tier", "haiku/max"),
	} {
		if first(ev, want) < 0 {
			t.Fatal("a phase agent was not spawned on its tier")
		}
	}
	if first(ev, is("tier_restart")) >= 0 {
		t.Errorf("the lead restarted, with agents to take the other tiers")
	}
	for phase, want := range map[string]string{"P1": "low", "P2": "max"} {
		if first(s.WorkerEvents(phase), is("tier_served", "asked", "haiku/"+want, "fits", "true")) < 0 {
			t.Errorf("%s: no tier_served that fits", phase)
		}
		paths, _ := filepath.Glob(filepath.Join(dir, ".baton", "runs", "*", "workers", phase, "state.json"))
		if len(paths) != 1 {
			t.Fatalf("%s's sub-run: %v", phase, paths)
		}
		var st struct {
			Run struct {
				Transcript string `json:"transcript"`
			} `json:"run"`
		}
		json.Unmarshal([]byte(readFile(filepath.Dir(paths[0]), "state.json")), &st)
		if got := efforts(servedIn(t, st.Run.Transcript, true), time.Time{}, time.Time{}); len(got) != 1 || got[0] != want {
			t.Errorf("%s's agent was served %v, want only %s", phase, got, want)
		}
	}
}

// raisePlan is one phase whose gate fails the first time it runs, after 20 seconds (gate.sh counts its
// runs in a file outside the project), on a low tier: the lead ends its turn while it runs, as it does for
// any gate it hands to baton.
const raisePlan = `# Raise

## P0 — Gate
Model: haiku
Effort: low

Commit nothing new. Run this gate with baton: baton verify "sh gate.sh" --notes 'checking'
Then end your turn: baton wakes you with its result.
When its result is red, run the same gate again the same way, with: --why 'the gate is flaky', and end your turn again.
When it passes, report P0 done: baton done P0 --notes 'the gate passed'
`

// A phase whose gate goes red is raised a step (escalate_after 1 here), and the lead, once idle, restarts
// in place on the raised tier, told why: its replies after the restart were served the raised effort.
func TestARedPhaseIsRaisedAndTheLeadRestartsOnIt(t *testing.T) {
	dir := NewProject(t)
	count := filepath.Join(t.TempDir(), "runs")
	writeFile(t, dir, "gate.sh", fmt.Sprintf("n=$(cat %q 2>/dev/null || echo 0); n=$((n+1)); echo $n >%q\n"+
		"if [ \"$n\" -lt 2 ]; then sleep 20; echo 'FAIL: TestGate (run '$n')'; exit 1; fi\necho ok\n", count, count))
	Git(t, dir, "add", "gate.sh")
	Git(t, dir, "commit", "-q", "-m", "test: the gate")
	Attach(t, dir, raisePlan)
	s := StartEnv(t, dir, []string{"BATON_ESCALATE_AFTER=1"}, "--model", "haiku", "--effort", "low", "Start the attached plan, and follow baton's instructions.")
	s.Trust()
	s.ApprovePermissions()
	if !s.Until("the plan to complete", 12*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 }) {
		logEvents(t, "the run", s.Events())
		t.Fatal("the plan did not complete")
	}
	ev := s.Events()
	raised := at(ev, is("tier_raised", "phase", "P0", "from", "haiku/low", "to", "haiku/medium"))
	restart := at(ev, is("tier_restart", "phase", "P0", "to", "haiku/medium", "why", "mid-phase"))
	if raised.IsZero() || restart.IsZero() || restart.Before(raised) {
		logEvents(t, "the run", ev)
		t.Fatalf("raised %v, restarted %v", raised, restart)
	}
	replies := servedIn(t, sessionTranscript(t, dir), false)
	if got := efforts(replies, time.Time{}, raised); len(got) == 0 || got[0] != "low" {
		t.Errorf("before the raise: %v", got)
	}
	if got := efforts(replies, restart, time.Time{}); len(got) != 1 || got[0] != "medium" {
		t.Errorf("after the restart: %v, want only medium", got)
	}
}

// afterAllPlan is a lead phase and an agent's beside it, then a phase that waits for both on another tier,
// which is not a QA phase: a release, say.
const afterAllPlan = `# After all

## P0 — Lead's
After: none
Model: haiku
Effort: high

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Beside
After: none
Model: haiku
Effort: low

Run echo one, then report P1 done: baton done P1 --notes 'said one'

## P2 — Release
After: all
Model: haiku
Effort: medium

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// A phase that waits for every phase before it and is not a QA phase stays the lead's whatever its tier,
// even with agents to take it: the lead restarts on its tier at its boundary, and its replies are served
// that tier.
func TestAPhaseAfterAllStaysTheLeadsOnItsTier(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, afterAllPlan)
	s := StartAgents(t, dir, []string{"BATON_WORKERS=2"}, "--effort", "high")
	if !s.Until("the plan to complete", 12*time.Minute, func() bool { return first(s.Events(), is("plan_complete")) >= 0 }) {
		logEvents(t, "the run", s.Events())
		t.Fatal("the plan did not complete")
	}
	ev := s.Events()
	if first(ev, is("worker_spawned", "phase", "P2")) >= 0 {
		t.Errorf("the phase after all went to a phase agent")
	}
	if first(ev, is("worker_spawned", "phase", "P1", "tier", "haiku/low")) < 0 {
		t.Errorf("P1 did not go to an agent on its tier")
	}
	if first(ev, is("tier_restart", "phase", "P2", "to", "haiku/medium", "why", "its boundary")) < 0 || first(ev, is("tier_given_up")) >= 0 {
		logEvents(t, "the run", ev)
		t.Fatal("the lead did not restart on P2's tier at its boundary")
	}
	replies := servedIn(t, sessionTranscript(t, dir), false)
	if got := efforts(replies, at(ev, is("phase_started", "phase", "P2")), at(ev, is("phase_done", "phase", "P2"))); len(got) != 1 || got[0] != "medium" {
		t.Errorf("P2's replies were served %v, want only medium", got)
	}
}
