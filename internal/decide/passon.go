package decide

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton says of a stage whose failures a runner named that reads no variable baton could narrow it by
// (vitest, jest, Playwright; v0.8.13): its rerun gets that runner's own arguments for only those tests
// (lane.EnvFailedArgs), and runs fewer only if the stage's command passes them on.

// passesOn reports a stage s whose rerun narrows only through a command that passes lane.EnvFailedArgs on:
// it failed in tests its runner named, and that runner reads no variable that selects tests.
func passesOn(s lane.Stage) (lane.Preset, string, bool) {
	p, ok := lane.PresetFor(s.Runner)
	if !ok || p.Narrow != nil || p.Args == nil || len(s.Failures) == 0 {
		return lane.Preset{}, "", false
	}
	args := p.Args(s.Failures)
	return p, args, args != ""
}

// passOn is what the result of red gate j adds for each of its failed stages that passesOn: what
// `--failed` gives the stage, and that it runs whole again unless its command passes that on. "" for none.
func passOn(j state.Job) string {
	var b strings.Builder
	for _, s := range j.Stages {
		p, args, ok := passesOn(s)
		if s.Status != lane.StageFailed || !ok {
			continue
		}
		n := len(s.Failures)
		fmt.Fprintf(&b, " Stage `%s` failed in %d %s that %s named, after %s: `--failed` gives its rerun $%s, %s's own arguments for only those (`%s`), "+
			"and %s reads no variable that selects tests, so the stage runs only those if its command passes them on, in place of its own choice of tests "+
			"(`%s $%s` when it is set); otherwise it runs whole again. A change to the gate's script is a commit like any other.",
			s.Name, n, plural(n, "test", "tests"), s.Runner, Stopwatch(s.Took()), lane.EnvFailedArgs, s.Runner, clipWords(args, 120),
			s.Runner, p.Cmd, lane.EnvFailedArgs)
	}
	return b.String()
}

// clipWords is s, cut after the last whole word within max bytes with "…" when it is longer.
func clipWords(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if i := strings.LastIndexByte(s[:max], ' '); i > 0 {
		return s[:i] + " …"
	}
	return s[:max] + "…"
}
