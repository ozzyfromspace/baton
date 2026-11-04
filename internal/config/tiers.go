package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ozzyfromspace/baton/internal/plan"
)

// The tiers a plan's phases run on (docs/research/tiers.md): which model families a phase may be given
// (models), and how baton raises the tier of a phase that goes badly (escalate, escalate_after).
const (
	// DefaultEscalateSteps is how many steps above its plan's tier a phase may be raised.
	DefaultEscalateSteps = 2
	MaxEscalateSteps     = 4
	// DefaultEscalateAfter is how many red runs of a phase's own gate raise its tier one step.
	DefaultEscalateAfter = 3
	MinEscalateAfter     = 2
	MaxEscalateAfter     = 10
)

// DefaultModels are the families a phase may run on unless models says otherwise: every family but Fable,
// which is metered on its own and can bill beyond a subscription's plan, so only the human may allow it.
var DefaultModels = []string{"haiku", "sonnet", "opus"}

// Tiers is what the settings say of the phases' tiers.
type Tiers struct {
	// Models are the families a phase may be given, in plan.Models' order: a phase that names another runs
	// on the highest of these below it, and escalation climbs only through these.
	Models []string
	// Steps is how many steps above its plan's tier baton may raise a phase (plan.Raise); 0 raises none.
	Steps int
	// After is how many red runs of a phase's own gate raise it one step.
	After int
}

// Tiering checks models, escalate and escalate_after (the environment's BATON_MODELS, BATON_ESCALATE and
// BATON_ESCALATE_AFTER win; the environment's escalate_after may go down to 1, for tests) and fills in the
// defaults. A value out of bounds is an error, with the defaults returned.
func (c Config) Tiering() (Tiers, error) {
	t := Tiers{Models: slices.Clone(DefaultModels), Steps: DefaultEscalateSteps, After: DefaultEscalateAfter}
	def := t
	models := c.Models
	if v := c.envModels; v != "" {
		models = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
	}
	if len(models) > 0 {
		var got []string
		for _, m := range models {
			f := plan.Family(m)
			if f == "" || plan.Family(m) != strings.ToLower(strings.TrimSpace(m)) {
				return def, fmt.Errorf("models: %q is not a model family: use any of %s", m, strings.Join(plan.Models, ", "))
			}
			got = append(got, f)
		}
		t.Models = nil
		for _, f := range plan.Models {
			if slices.Contains(got, f) {
				t.Models = append(t.Models, f)
			}
		}
	}
	steps, err := bounded(c.Escalate, c.envEscalate, "escalate", "BATON_ESCALATE", DefaultEscalateSteps, 0, MaxEscalateSteps, 0)
	if err != nil {
		return def, err
	}
	after, err := bounded(c.EscalateAfter, c.envEscalateAfter, "escalate_after", "BATON_ESCALATE_AFTER", DefaultEscalateAfter, MinEscalateAfter, MaxEscalateAfter, 1)
	if err != nil {
		return def, err
	}
	t.Steps, t.After = steps, after
	return t, nil
}

// bounded is a whole-number setting: the environment's value (which may go down to envMin) over the file's
// over def, checked against [min, max].
func bounded(file *int, env, name, envName string, def, min, max, envMin int) (int, error) {
	n := def
	if file != nil {
		if n = *file; n < min || n > max {
			return def, fmt.Errorf("%s %d is not valid: use a whole number from %d to %d", name, n, min, max)
		}
	}
	if env != "" {
		v, err := strconv.Atoi(strings.TrimSpace(env))
		if err != nil || v < envMin || v > max {
			return def, fmt.Errorf("%s %q is not valid: use a whole number from %d to %d", envName, env, envMin, max)
		}
		n = v
	}
	return n, nil
}

// Allows reports a family the human allows a phase to run on.
func (t Tiers) Allows(family string) bool { return slices.Contains(t.Models, family) }

// Env is the setting as the host gives it to the session's hooks: the families allowed, which the plan's
// check at ExitPlanMode reads (ModelsFromEnv).
func (t Tiers) Env() []string { return []string{"BATON_MODELS=" + strings.Join(t.Models, ",")} }

// ModelsFromEnv reads the families allowed as the host passed them down (BATON_MODELS); missing or not
// valid, the defaults.
func ModelsFromEnv(env func(string) string) []string {
	t, err := Config{envModels: env("BATON_MODELS")}.Tiering()
	if err != nil {
		return slices.Clone(DefaultModels)
	}
	return t.Models
}
