package state

import (
	"testing"
	"time"
)

// Where baton's mod runs, only a dialog that is not a phase agent's holds the lead.
func TestLeadOpen(t *testing.T) {
	for _, c := range []struct {
		name string
		q    DialogQueue
		want bool
	}{
		{"none", nil, false},
		{"the lead's", DialogQueue{{Tool: "Bash"}}, true},
		{"an agent of the model's own", DialogQueue{{Tool: "Bash", Agent: "sub"}}, true},
		{"a phase agent's", DialogQueue{{Tool: "Bash", Agent: "a1", Phase: "P1"}}, false},
		{"a phase agent's, the lead's behind it", DialogQueue{{Tool: "Bash", Agent: "a1", Phase: "P1"}, {Tool: "Bash"}}, true},
	} {
		if got := c.q.LeadOpen(); got != c.want {
			t.Errorf("%s: LeadOpen %v, want %v", c.name, got, c.want)
		}
	}
}

// An Agent call waiting on its permission prompt may still ask for a worktree however long it waits; an
// Agent prompt of another agent's keeps it open no longer than AgentCallWait (spike 36: a permission
// request names the agent that asked).
func TestAnAgentCallIsKeptOpenOnlyByItsOwnAgentsPrompt(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name   string
		caller string
		dialog *Dialog
		at     time.Duration
		open   bool
	}{
		{"just made", "", nil, time.Second, true},
		{"made long enough ago", "", nil, AgentCallWait, false},
		{"the lead's, its prompt open", "", &Dialog{Tool: "Agent"}, time.Hour, true},
		{"the lead's, a phase agent's Agent prompt open", "", &Dialog{Tool: "Agent", Agent: "a1", Phase: "P1"}, time.Hour, false},
		{"a phase agent's, its prompt open", "a1", &Dialog{Tool: "Agent", Agent: "a1", Phase: "P1"}, time.Hour, true},
		{"a phase agent's, the lead's Agent prompt open", "a1", &Dialog{Tool: "Agent"}, time.Hour, false},
		{"a phase agent's, another's Agent prompt open", "a1", &Dialog{Tool: "Agent", Agent: "a2", Phase: "P2"}, time.Hour, false},
		{"a phase agent's, its prompt for another tool open", "a1", &Dialog{Tool: "Bash", Agent: "a1", Phase: "P1"}, time.Hour, false},
	} {
		var r Runtime
		r.AgentCalled("toolu_1", c.caller, t0)
		if c.dialog != nil {
			r.Dialogs.Open(*c.dialog)
		}
		if got := r.AgentCallsOpen(t0.Add(c.at)); got != c.open {
			t.Errorf("%s: open %v, want %v", c.name, got, c.open)
		}
	}
}
