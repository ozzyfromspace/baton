package decide

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton does about a stopped agent: the human's answer as they chose; nobody's from the record.
func TestAgentStopAction(t *testing.T) {
	first := state.Relay{Phase: "P3", Kind: state.RelayStopped, Count: 1}
	second := state.Relay{Phase: "P3", Kind: state.RelayStopped, Count: 2}
	third := state.Relay{Phase: "P3", Kind: state.RelayStopped, Count: 3}
	down := state.Relay{Phase: "P3", Kind: state.RelayStopped, Count: 2, Persist: AgentPersistent}
	restarted := state.Relay{Phase: "P3", Kind: state.RelayRestarted}
	human := func(a string) state.Resolution { return state.Resolution{By: state.ByHuman, Answer: a} }
	timeout := state.Resolution{By: state.ByTimeout, Answer: RelayDefault(first)}
	declined := state.Resolution{By: state.ByDeclined}
	for _, c := range []struct {
		name      string
		r         state.Relay
		a         state.Resolution
		satisfied bool
		want      string
	}{
		{"first, restart it", first, human(RestartIt), false, StopRestart},
		{"first, leave it stopped", first, human(LeaveStopped), false, StopLeave},
		{"first, pause", first, human("Pause baton"), false, StopPause},
		{"first, own words", first, human("hold on"), false, StopLeave},
		{"first, restart it with its work done", first, human(RestartIt), true, StopRestart},
		{"first, nobody, not done", first, timeout, false, StopRestart},
		{"first, nobody, done", first, timeout, true, StopRelease},
		{"first, its dialog went away, not done", first, declined, false, StopRestart},
		{"second, nobody, not done", second, timeout, false, StopTakeDown},
		{"second, nobody, done", second, timeout, true, StopRelease},
		{"second, restart it", second, human(RestartIt), false, StopRestart},
		{"third uncounted afresh, nobody", third, timeout, false, StopTakeDown},
		{"taken down, nobody", down, state.Resolution{By: state.ByTimeout, Answer: RelayDefault(down)}, false, StopLater},
		{"taken down, went away", down, declined, false, StopLater},
		{"taken down, nobody, done meanwhile", down, timeout, true, StopLater},
		{"taken down, restart it", down, human(RestartIt), false, StopRestart},
		{"taken down, leave it stopped", down, human(LeaveStopped), false, StopLeave},
		{"restarted, nobody", restarted, state.Resolution{By: state.ByTimeout, Answer: RelayDefault(restarted)}, false, StopKeep},
		{"restarted, keep it going", restarted, human(KeepItGoing), false, StopKeep},
		{"restarted, stop it", restarted, human(StopIt), false, StopLeave},
		{"restarted, pause", restarted, human("Pause baton"), false, StopPause},
		{"restarted, own words", restarted, human("use the staging db"), false, StopKeep},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := AgentStopAction(c.r, c.a, c.satisfied); got != c.want {
				t.Errorf("AgentStopAction = %s, want %s", got, c.want)
			}
		})
	}
}

// The questions about a stopped agent: three options each, and words that say what happens with no answer.
func TestAgentQuestions(t *testing.T) {
	due := time.Date(2026, 10, 15, 12, 10, 0, 0, time.Local)
	for _, c := range []struct {
		name    string
		r       state.Relay
		want    []string
		options []string
		def     string
	}{
		{"first", state.Relay{Phase: "P3", Kind: state.RelayStopped, Count: 1, What: "Stopped agents."},
			[]string{"baton: you stopped the agent on P3 (Stopped agents). Restart it? Unless you answer by 12:10, baton restarts it, or lets it go if its work is done."},
			[]string{RestartIt, LeaveStopped, "Pause baton"}, "Let baton decide"},
		{"second", state.Relay{Phase: "P3", Kind: state.RelayStopped, Count: 2},
			[]string{"baton: you stopped the agent on P3 again. Restart it? Unless you answer by 12:10, baton takes it down", "asks you what to do with it while the rest of the run goes on"},
			[]string{RestartIt, LeaveStopped, "Pause baton"}, "Let baton decide"},
		{"taken down", state.Relay{Phase: "P3", Kind: state.RelayStopped, Count: 2, Persist: AgentPersistent},
			[]string{"What should become of P3?", "While the rest of the run can go on, baton will not choose for you", "P3 and the phases that need it wait for your answer",
				"Once nothing else can move, baton takes P3 back from its agent, not before 12:10, and Claude does it in this session"},
			[]string{RestartIt, LeaveStopped, "Pause baton"}, "Ask me later"},
		{"restarted", state.Relay{Phase: "P3", Kind: state.RelayRestarted},
			[]string{"so baton restarted it", "Keep it going? Unless you answer by 12:10, it carries on."},
			[]string{StopIt, "Pause baton", KeepItGoing}, KeepItGoing},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := RelayQuestion(c.r, due)
			for _, w := range c.want {
				if !strings.Contains(q.Text, w) {
					t.Errorf("the question lacks %q:\n%s", w, q.Text)
				}
			}
			var labels []string
			for _, o := range q.Options {
				labels = append(labels, o.Label)
			}
			if !slices.Equal(labels, c.options) {
				t.Errorf("options %q, want %q", labels, c.options)
			}
			if got := RelayDefault(c.r); got != c.def {
				t.Errorf("default %q, want %q", got, c.def)
			}
			if c.r.Persistent() && strings.Contains(q.Text, "Unless you answer by") {
				t.Errorf("the question with no deadline names one for the human's answer: %s", q.Text)
			}
		})
	}
}

// How a phase whose agent was stopped stands, on the board and in the stall alarm.
func TestAgentStoppedStand(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		w      state.Worker
		asking bool
		want   string
	}{
		{state.Worker{Stops: 1}, true, "its agent was stopped; baton asks you whether to restart it"},
		{state.Worker{Stops: 1}, false, "its agent was stopped; /baton run starts a fresh one"},
		{state.Worker{Stops: 2, TakenDown: at}, false, "its agent was stopped twice; waiting for your answer"},
	} {
		if got := AgentStoppedStand(c.w, c.asking); got != c.want {
			t.Errorf("AgentStoppedStand(%+v, %v) = %q, want %q", c.w, c.asking, got, c.want)
		}
	}
}

// A twice-stopped phase baton took back for the lead: the human hears where its agent's work is, the
// decision says so with /baton pause as its undo, and the lead's brief says why the phase is its own and
// how to carry on from the agent's commits, by a merge.
func TestStoppedTaken(t *testing.T) {
	for _, c := range []struct {
		name  string
		t     Taken
		said  []string // in what the human is told, and in the decision
		brief []string // in the lead's brief
	}{
		{"commits and a snapshot", Taken{ID: "d4", Phase: "P3", Branch: "baton/run/P3", Unlanded: 2, Snapshot: "refs/baton/snapshots/P3-1"},
			[]string{"its agent's 2 commits stay on branch baton/run/P3", "what it had not committed is saved as refs/baton/snapshots/P3-1"},
			[]string{"Its 2 commits are on branch baton/run/P3, not on the run's branch: to carry on from them, merge that branch into the run's branch (git merge, never a rebase).",
				"git show refs/baton/snapshots/P3-1 shows it."}},
		{"one commit, its worktree kept", Taken{ID: "d4", Phase: "P3", Branch: "baton/run/P3", Unlanded: 1, Worktree: "/w/P3", Kept: "it is not a git worktree"},
			[]string{"its agent's 1 commit stays on branch baton/run/P3", "its worktree stays at /w/P3, as it is not a git worktree"},
			[]string{"Its 1 commit is on branch baton/run/P3", "to carry on from it", "Its worktree stays at /w/P3"}},
		{"nothing of its own", Taken{ID: "d4", Phase: "P3"}, nil, []string{"It left no commits that the run's branch lacks."}},
	} {
		t.Run(c.name, func(t *testing.T) {
			said := StoppedTaken(c.t)
			what, undo := StoppedTakenDecision(c.t)
			if !strings.HasPrefix(said, "baton: noted (d4) — nobody answered about the agent you stopped twice on P3") ||
				!strings.HasPrefix(what, "took P3 back from its agent for Claude to do in the lead's session") || !strings.HasPrefix(undo, "/baton pause") {
				t.Errorf("told %q\ndecision %q, undo %q", said, what, undo)
			}
			for _, w := range c.said {
				if !strings.Contains(said, w) || !strings.Contains(what, w) {
					t.Errorf("told %q, decision %q: lacks %q", said, what, w)
				}
			}
			b := StoppedTakenBrief(c.t)
			if !strings.HasPrefix(b, "[baton] The human stopped the phase agent on P3 twice") {
				t.Errorf("brief %q", b)
			}
			for _, w := range c.brief {
				if !strings.Contains(b, w) {
					t.Errorf("brief %q lacks %q", b, w)
				}
			}
		})
	}
	if got := StoppedTaken(Taken{Phase: "P3"}); !strings.HasPrefix(got, "baton: nobody answered") {
		t.Errorf("with no decision recorded: %q", got)
	}
}
