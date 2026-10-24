package decide

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/state"
)

// A whole run after one that lost a stage to its environment is questioned once (v0.8.17,
// cli/envagain.go): the stages, their lines, and the ways out a whole run would skip.

// GateEnvAgain is the kind of question (gate_questioned's type) put to a whole run of a gate whose last
// whole run lost a stage to its environment.
const GateEnvAgain = "env_again"

// envAgainAbout is what GateAnswered says of GateEnvAgain.
const envAgainAbout = "its last whole run lost a stage to its environment"

// EnvAgain is baton's question, once, about a whole run of a gate whose last whole run, last, lost stages to
// its environment (lost): each stage with its line, and the ways out, as its result gave them.
func EnvAgain(last state.Job, lost []state.EnvLoss) string {
	clauses := make([]string, len(lost))
	var machine, other int
	for i, l := range lost {
		to := l.Kind
		if killed(l) {
			to, machine = "the machine", machine+1
		} else {
			other++
		}
		clauses[i] = fmt.Sprintf("`%s` to %s (`%s`)", l.Stage, to, ticked(l.Line))
	}
	s := fmt.Sprintf("Its last whole run, job %s, lost %s.", last.ID, andList(clauses))
	if machine > 0 {
		s += " " + roomFor(machine)
	}
	if other > 0 {
		s += fmt.Sprintf(" Fix the environment before a whole run, rerun %s alone with `baton verify --failed`, and record what %s with `baton env`.",
			plural(other, "it", "them"), plural(other, "it needs", "they need"))
	}
	return s
}
