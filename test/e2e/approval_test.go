//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Approving a plan in plan mode runs it, with no command from anyone: baton attaches the plan, compacts
// the planning conversation away, and P0 starts from a brief. Before the plan reaches the human, baton
// turns back the model's first ExitPlanMode once with its request to flag competing instructions, and
// the model puts the plan to them after. The test plays the human: it switches to plan mode, asks for a
// plan, and approves it.
func TestAnApprovedPlanRunsByItself(t *testing.T) {
	dir := NewProject(t)
	PlansHere(t, dir)
	s := Start(t, dir, "--model", "haiku")
	s.Trust()
	s.WaitQuiet(3*time.Second, 40*time.Second)
	if !s.PlanMode() {
		t.Fatal("could not switch to plan mode")
	}
	s.AutoApprove()
	s.Type(`Plan this tiny task. Write the plan to your plan file with exactly these two headings and nothing else as headings: ` +
		`"## P0 — Say hello" (step: run echo hello) and "## P1 — Say goodbye" (step: run echo goodbye). ` +
		`Do not explore the repository. Then call ExitPlanMode.`)
	waitSequence(t, s, 8*time.Minute,
		kind("directives_asked"),
		func(e map[string]any) bool {
			return e["kind"] == "attached" && e["by"] == "approval" && e["phases"] == 2.0
		},
		func(e map[string]any) bool {
			return e["kind"] == "compact_queued" && e["reason"] == "boundary" && e["next"] == "P0"
		},
		func(e map[string]any) bool { return e["kind"] == "phase_started" && e["phase"] == "P0" }, // from the compaction's brief
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
		kind("plan_complete"),
	)
}

// A plan with one phase heading is a plan baton runs: approved, it is attached, the planning conversation
// is compacted away, and its one phase runs to the plan's end.
func TestAnApprovedOnePhasePlanRunsByItself(t *testing.T) {
	dir := NewProject(t)
	PlansHere(t, dir)
	s := Start(t, dir, "--model", "haiku")
	s.Trust()
	s.WaitQuiet(3*time.Second, 40*time.Second)
	if !s.PlanMode() {
		t.Fatal("could not switch to plan mode")
	}
	s.AutoApprove()
	s.Type(`Plan this tiny task. Write the plan to your plan file with exactly this one heading and nothing else as a heading: ` +
		`"## P0 — Say hello" (step: run echo hello). Do not explore the repository. Then call ExitPlanMode.`)
	waitSequence(t, s, 8*time.Minute,
		kind("directives_asked"),
		func(e map[string]any) bool {
			return e["kind"] == "attached" && e["by"] == "approval" && e["phases"] == 1.0
		},
		func(e map[string]any) bool {
			return e["kind"] == "compact_queued" && e["reason"] == "boundary" && e["next"] == "P0"
		},
		func(e map[string]any) bool { return e["kind"] == "phase_started" && e["phase"] == "P0" },
		func(e map[string]any) bool { return e["kind"] == "phase_done" && e["phase"] == "P0" },
		kind("plan_complete"),
	)
	none(t, s, "plan_not_attached")
}

// Two baton terminals in one project each run a plan of their own, at the same time.
func TestTwoTerminalsRunTwoPlans(t *testing.T) {
	dir := NewProject(t)
	writePlan(t, dir, "a.md", "# Plan A\n\n## P0 — Say alpha\nRun echo alpha.\n\n## P1 — Say beta\nRun echo beta.\n")
	writePlan(t, dir, "b.md", "# Plan B\n\n## P0 — Say gamma\nRun echo gamma.\n\n## P1 — Say delta\nRun echo delta.\n")
	start := func(file string) *Session {
		s := Start(t, dir, "--model", "haiku",
			"Run exactly this one command with the Bash tool: baton attach "+file+" --suggested\nThen do the phase it names, report it with baton done, and end your turn.")
		s.Trust()
		return s
	}
	a := start("a.md")
	b := start("b.md")
	for _, c := range []struct {
		s     *Session
		title string
	}{{a, "a.md"}, {b, "b.md"}} {
		s := c.s
		if !s.Until(c.title+" completes", 8*time.Minute, func() bool {
			for _, e := range s.OwnEvents() {
				if e["kind"] == "plan_complete" {
					return true
				}
			}
			return false
		}) {
			t.Fatalf("%s did not complete: %s", c.title, eventLog(s))
		}
	}
	own := func(s *Session) string {
		for _, e := range s.OwnEvents() {
			if p, _ := e["plan"].(string); e["kind"] == "attached" {
				return p
			}
		}
		return ""
	}
	if pa, pb := own(a), own(b); pa == "" || pb == "" || pa == pb {
		t.Fatalf("the sessions' runs: %q and %q", pa, pb)
	}
}

// writePlan writes a plan document into the project and commits it, as a plan kept in the repository.
func writePlan(t *testing.T, dir, name, doc string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o644)
	Git(t, dir, "add", name)
	Git(t, dir, "commit", "-q", "-m", "docs: "+name)
}

// An approved plan is moved out of Claude Code's way as baton attaches it (spike 38): the session's plan-mode
// file is renamed in its folder to a name from the plan's title, so the compaction at the boundary attaches
// no copy of it (plan_file_reference), and the run follows the plan at its new path to the end.
func TestAnApprovedPlanIsMovedOutOfClaudeCodesWay(t *testing.T) {
	dir := NewProject(t)
	PlansHere(t, dir)
	s := Start(t, dir, "--model", "haiku")
	s.Trust()
	s.WaitQuiet(3*time.Second, 40*time.Second)
	if !s.PlanMode() {
		t.Fatal("could not switch to plan mode")
	}
	s.AutoApprove()
	s.Type(`Plan this tiny task. Write the plan to your plan file with the title "# Greeting plan" and exactly these two headings and nothing else as headings: ` +
		`"## P0 — Say hello" (step: run echo hello) and "## P1 — Say goodbye" (step: run echo goodbye). ` +
		`Do not explore the repository. Then call ExitPlanMode.`)
	waitSequence(t, s, 8*time.Minute,
		func(e map[string]any) bool { return e["kind"] == "attached" && e["by"] == "approval" },
		is("compact_queued", "reason", "boundary", "next", "P0"),
		is("phase_started", "phase", "P0"),
		is("phase_done", "phase", "P0"),
		kind("plan_complete"),
	)
	evs := s.Events()
	i := first(evs, is("plan_moved", "by", "approval"))
	if i < 0 {
		t.Fatalf("the plan was not moved:\n%s", eventLog(s))
	}
	from, _ := evs[i]["from"].(string)
	to, _ := evs[i]["to"].(string)
	if filepath.Dir(from) != filepath.Dir(to) || filepath.Base(filepath.Dir(to)) != "plans" || from == to {
		t.Errorf("moved from %q to %q", from, to)
	}
	if _, err := os.Stat(from); err == nil {
		t.Errorf("the plan-mode file %s is still there", from)
	}
	if _, err := os.Stat(to); err != nil {
		t.Errorf("the moved plan: %v", err)
	}
	if a := evs[first(evs, is("attached", "by", "approval"))]; a["plan"] != to {
		t.Errorf("attached %v, the plan moved to %s", a["plan"], to)
	}
	// After the boundary's compaction, Claude Code attached no copy of the plan.
	boundaries, refs := planReferences(t, leadTranscript(t, dir))
	if boundaries == 0 {
		t.Fatalf("no compaction in the lead's transcript")
	}
	if len(refs) > 0 {
		t.Errorf("plan_file_reference after the last compaction: %v", refs)
	}
}

// leadTranscript is the lead's transcript of the one run in dir, as its hooks last marked it.
func leadTranscript(t *testing.T, dir string) string {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(dir, ".baton", "runs", "*", "state.json"))
	for _, p := range paths {
		var st struct {
			Run struct {
				Proven *struct {
					Path string `json:"path"`
				} `json:"proven"`
			} `json:"run"`
		}
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &st) == nil && st.Run.Proven != nil && st.Run.Proven.Path != "" {
			return st.Run.Proven.Path
		}
	}
	t.Fatalf("no run in %s marks the lead's transcript", dir)
	return ""
}

// planReferences reads a transcript: how many compact boundaries it holds, and the plan files Claude Code
// attached (plan_file_reference) after the last of them.
func planReferences(t *testing.T, path string) (boundaries int, refs []string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		var e struct {
			Type       string `json:"type"`
			Subtype    string `json:"subtype"`
			Attachment struct {
				Type string `json:"type"`
				Path string `json:"planFilePath"`
			} `json:"attachment"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		switch {
		case e.Type == "system" && e.Subtype == "compact_boundary":
			boundaries, refs = boundaries+1, nil
		case e.Type == "attachment" && e.Attachment.Type == "plan_file_reference":
			refs = append(refs, e.Attachment.Path)
		}
	}
	return boundaries, refs
}

// holdPlan's P0 hands baton a gate that runs for minutes and reports the phase done, so the run is under
// way, its lead idle, while the human plans something else.
const holdPlan = `# Hold plan

## P0 — Mark the work
1. Write the single word ok into a new file named marker, and commit it: git add marker, then git commit -m 'feat: the marker'.
2. Hand the project's gate to baton, exactly: baton verify --notes 'marker committed; the gate runs' -- sh gate.sh
3. Report P0 done, exactly: baton done P0 --notes 'marker committed; the gate runs'
4. End your turn. Do not run the gate yourself, and do not check on it: baton gives you its result.

## P1 — Say goodbye
After: P0

Run echo goodbye, then report P1 done.
`

// A plan approved while a run is under way gets baton's question, whose first option is Wait for me: a
// stray Enter holds the run rather than setting its progress aside (Start the new plan from P0 is second),
// and with no word from the human the hold lapses into the default, Keep the current run, third.
func TestAStrayEnterOnThePlanQuestionHoldsTheRun(t *testing.T) {
	dir := NewGateProject(t, 420)
	PlansHere(t, dir)
	Attach(t, dir, holdPlan)
	s := StartEnv(t, dir, []string{"BATON_HOLD_LAPSE=30s"}, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.")
	s.Trust()
	s.AutoApprove() // Enter on every dialog: the plan's approval, and baton's question about it
	waitSequence(t, s, 4*time.Minute, is("phase_verifying", "phase", "P0"))
	s.WaitQuiet(5*time.Second, 2*time.Minute)
	if !s.PlanMode() {
		t.Fatal("could not switch to plan mode")
	}
	s.Type(`Plan this tiny task, apart from the plan baton runs. Write the plan to your plan file with exactly these two headings and nothing else as headings: ` +
		`"## P0 — Say hello" (step: run echo hello) and "## P1 — Say goodbye" (step: run echo goodbye). ` +
		`Do not explore the repository. Then call ExitPlanMode.`)
	waitSequence(t, s, 10*time.Minute,
		is("plan_question_asked", "same", "false"),
		is("dialog_open", "dialog", "plan_question"),
		is("plan_question_held", "answer", "Wait for me", "by", "human"),
		is("plan_question_resolved", "answer", "Keep the current run", "by", "timeout"),
	)
	evs := s.Events()
	if i := first(evs, is("attached", "by", "approval")); i >= 0 {
		t.Errorf("the approved plan was attached: %v", evs[i])
	}
	none(t, s, "revised")
}
