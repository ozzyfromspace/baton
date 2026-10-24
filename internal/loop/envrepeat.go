package loop

import (
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A gate that keeps losing a stage to the machine (v0.8.17, state/envrepeat.go): in a real run the
// out-of-memory killer of Docker's VM took a QA set's build in three of four whole runs, and only the human
// could have given the VM more memory. So when a second whole run of a phase's gate loses a stage to the same
// kind as an earlier one, the human is pushed, once per phase and kind (env_repeated): information, not a
// question, as the model works around it meanwhile.

// envRepeat reads each whole run of the loop's run that ended red or was stopped and is not read yet
// (state.EnvUnread) for the stages its environment took (state.Store.EnvLost), before the lock; records
// them, and pushes the human the repeats that makes due (state.ReadEnv, decide.EnvRepeated), with a line in
// the session (the lead's, for a phase agent's run, which nobody watches), each logged.
func (l *Loop) envRepeat(v host.View, st state.State) {
	unread := state.EnvUnread(st)
	if len(unread) == 0 {
		return
	}
	lost := make(map[string][]state.EnvLoss, len(unread))
	for _, j := range unread {
		lost[j.ID] = l.Store.EnvLost(j)
	}
	agent := l.Store.Worker() != ""
	var due []state.EnvRepeat
	var lines []string
	_, err := l.Store.Update(func(st *state.State) error {
		due, lines = nil, nil
		for _, j := range unread {
			for _, r := range state.ReadEnv(st, j.ID, lost[j.ID], v.Now) {
				text := decide.EnvRepeated(r, agent)
				due, lines = append(due, r), append(lines, "baton: "+text)
				st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "env_repeated", Text: text, At: v.Now})
			}
		}
		if !agent {
			st.Run.Announce = append(st.Run.Announce, lines...)
		}
		return nil
	})
	if err != nil {
		l.logf("loop: reading what the environment took of gates: %v", err)
		return
	}
	for _, r := range due {
		l.Store.Event("env_repeated", map[string]any{"phase": r.Phase, "env_kind": r.Kind, "command": r.Command, "stages": r.Stages, "jobs": r.Jobs})
	}
	if agent && len(lines) > 0 {
		if up, err := l.Store.ParentRun(); err == nil {
			up.Update(func(st *state.State) error { st.Run.Announce = append(st.Run.Announce, lines...); return nil })
		}
	}
}
