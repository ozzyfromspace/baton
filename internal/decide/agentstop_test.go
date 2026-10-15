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
			[]string{"baton: you stopped the agent on P3 again. Restart it? Unless you answer by 12:10, baton takes it down", "with no deadline"},
			[]string{RestartIt, LeaveStopped, "Pause baton"}, "Let baton decide"},
		{"taken down", state.Relay{Phase: "P3", Kind: state.RelayStopped, Count: 2, Persist: AgentPersistent},
			[]string{"What should become of P3?", "baton will not choose for you", "P3 and the phases that need it wait for your answer"},
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
			if c.r.Persistent() && strings.Contains(q.Text, "12:10") {
				t.Errorf("the question with no deadline names one: %s", q.Text)
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
