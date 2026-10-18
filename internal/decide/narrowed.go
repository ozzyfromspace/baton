package decide

import (
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton says of a narrowed rerun (v0.8.11): `baton verify --failed -- <command>` on a red gate built
// from no stages runs the model's command, which names every test the gate's log says failed
// (lane.Unnamed), and stands in for the gate (Job.Reruns, Job.Narrows): a pass clears it, as a staged
// --failed does, and the QA phase runs the whole set.

// NarrowedQueued tells the model, as it queues command for phase, a narrowed rerun of gate of naming ids,
// what it stands in for, and what it does not run: what a gate stopped before its end never reached, and
// what a gate that runs its tests through something else (make, a script) may never have reached after its
// first failing command. qa is the QA phase ahead ("P7 (QA)"), "" for none.
func NarrowedQueued(phase string, of state.Job, ids []string, command, qa string) string {
	s := fmt.Sprintf("it reruns only what failed in job %s (%s) and stands in for it: if it passes, it clears %s; if it fails, it is %s's red gate from then on.",
		of.ID, idNames(ids), of.ID, phase)
	unreached := ""
	switch {
	case !of.Red():
		unreached = fmt.Sprintf(" Job %s did not run to its end, so the tests it never reached are unchecked, and this rerun does not run them.", of.ID)
	case firstWord(command) != firstWord(of.Command):
		unreached = fmt.Sprintf(" If `%s` stops at its first failing command, what came after it never ran, and this rerun does not run it either.", of.Command)
	}
	switch {
	case qa != "":
		return s + unreached + fmt.Sprintf(" %s runs the whole set.", qa)
	case unreached != "":
		return s + unreached + fmt.Sprintf(" Verify `%s` whole before the phase is done if that matters.", of.Command)
	}
	return s
}

// narrowedRan is what the result of narrowed rerun j says of what it reran, and, once it passed, that it
// cleared the gate it stands in for; qa is the QA phase ahead, "" for none. "" for any other job.
func narrowedRan(j state.Job, qa string) string {
	if j.Reruns == "" || len(j.Narrows) == 0 {
		return ""
	}
	s := fmt.Sprintf("It reran only what failed in job %s (verify --failed): %s.\n", j.Reruns, idNames(j.Narrows))
	if j.Status == state.JobPassed {
		s += fmt.Sprintf("As it passed, it stands in for %s and clears it.", j.Reruns)
		if qa != "" {
			s += fmt.Sprintf(" %s runs the whole set.", qa)
		}
		s += "\n"
	}
	return s + "\n"
}

// idNames names ids, at most maxIDs of them: "TestA, TestB and 3 more".
func idNames(ids []string) string {
	shown := ids
	if len(shown) > maxIDs {
		shown = append(shown[:maxIDs:maxIDs], fmt.Sprintf("%d more", len(ids)-maxIDs))
	}
	return andList(shown)
}

// testIDs are the ids of fs.
func testIDs(fs []lane.FailedTest) []string {
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}
	return ids
}

// narrowOffer is the command that reruns only the tests fs, as `baton verify --failed` takes it, "" when
// one of them no runner of baton's named (a ::baton fail line's).
func narrowOffer(fs []lane.FailedTest) string {
	for _, f := range fs {
		if f.Runner == "" {
			return ""
		}
	}
	cmds := lane.Reruns(fs)
	if len(cmds) == 0 {
		return ""
	}
	return fmt.Sprintf("`baton verify --failed --notes \"…\" -- %s`", strings.Join(cmds, " && "))
}

// NarrowCommandless turns down --failed with no command for red gate of, built from no stages, whose log
// says fs failed: a narrowed rerun runs a command that names each of them, offered where baton has one.
func NarrowCommandless(of state.Job, fs []lane.FailedTest) string {
	s := fmt.Sprintf("not queued — job %s (%s) recorded no stages, so --failed reruns what failed in it by a command you give that names each test that failed (%s), "+
		"and that command stands in for %s", of.ID, Ran(of), idNames(testIDs(fs)), of.ID)
	if offer := narrowOffer(fs); offer != "" {
		return s + ": " + offer + ", inside the gate's own way of running it (make, docker compose) where it needs one."
	}
	return s + ": `baton verify --failed --notes \"…\" -- <command>`."
}

// NarrowUnnamed turns down command as a narrowed rerun of red gate of, whose log says fs failed: it names
// none of left.
func NarrowUnnamed(of state.Job, command string, left, fs []lane.FailedTest) string {
	s := fmt.Sprintf("not queued — `%s` does not name %s, which failed in job %s, so it cannot stand in for %s: "+
		"a narrowed rerun names every test that failed, by its id, its test name or its file:line.", command, idNames(testIDs(left)), of.ID, of.ID)
	if offer := narrowOffer(fs); offer != "" {
		s += " This one does: " + offer + "."
	}
	return s
}

// NarrowWhole turns down --failed given red gate of's own command, which runs the whole gate rather than
// what failed in it (fs).
func NarrowWhole(of state.Job, fs []lane.FailedTest) string {
	s := fmt.Sprintf("not queued — `%s` is job %s's whole command, and --failed with a command reruns only what failed in it (%s). "+
		"To run the whole gate again, verify it without --failed (baton questions a long repeat once).", of.Command, of.ID, idNames(testIDs(fs)))
	if offer := narrowOffer(fs); offer != "" {
		s += " To rerun only what failed: " + offer + "."
	}
	return s
}

// NarrowTimedOut turns down a narrowed rerun of gate of, which ran out of time: what it never reached is
// unchecked, so a rerun of what failed in it cannot stand in for it.
func NarrowTimedOut(of state.Job) string {
	return fmt.Sprintf("not queued — job %s (%s) ran out of time, so the tests it never reached are unchecked, and a rerun of what failed in it cannot stand in for it. "+
		"Fix what failed, then verify `%s` again; or, if a narrower check is %s's gate from now on, verify it with --replace (a decision without the human, and they are told).",
		of.ID, Ran(of), of.Command, of.Phase)
}

// NarrowTooMany turns down a narrowed rerun of gate of, whose log says more tests failed than baton reads
// by name (lane.MaxFailures): it cannot tell that a command names them all.
func NarrowTooMany(of state.Job) string {
	return fmt.Sprintf("not queued — job %s (%s) recorded no stages, and its log names more failed tests than baton reads by name (over %d), "+
		"so baton cannot tell that a narrower command reruns them all. %s", of.ID, Ran(of), lane.MaxFailures, byNameInstead(of))
}

// NarrowNoStages turns down --stages names on red gate of, which recorded no stages to bring back.
func NarrowNoStages(of state.Job, names []string) string {
	return fmt.Sprintf("not queued — --stages names %s, and job %s recorded no stages: a rerun of it runs the command you give with --failed, whole.", stageNames(names), of.ID)
}

// byNameInstead is how the model clears red gate of when baton cannot check a narrowed rerun of it: the
// failures rerun by name, made the phase's gate with --replace, or the gate again once the fix is in.
func byNameInstead(of state.Job) string {
	return fmt.Sprintf("Rerun the failures by name and make that %s's gate: `baton verify --replace --notes \"…\" -- <command>` (a decision without the human, and they are told); "+
		"or fix what failed, then verify again with `%s`.", of.Phase, of.Command)
}
