package decide

import (
	"strings"
	"testing"
)

// A model's message to a phase agent is refused with what to do instead of coordinating by message: leave the
// shared part to that phase and say so in the notes, or build on it once it has landed (baton sync). A stop is
// refused with the agent left to baton.
func TestARefusedMessageToAPhaseAgentSaysWhatToDoInstead(t *testing.T) {
	msg := AgentIsBatons("SendMessage", "P62")
	for _, want := range []string{"SendMessage is for baton alone with the agent on P62", "Phases do not coordinate by message",
		"leave the shared part to P62 and say so in your phase's notes", "once P62 has landed", "`baton sync`", "Go on with the rest of your own work"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q\nwant %q in it", msg, want)
		}
	}
	stop := AgentIsBatons("TaskStop", "P62")
	if !strings.Contains(stop, "Leave the agent and P62 to baton") || strings.Contains(stop, "coordinate") {
		t.Errorf("a stop's refusal: %q", stop)
	}
}
