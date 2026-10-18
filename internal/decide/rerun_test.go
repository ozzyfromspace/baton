package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// A red gate that recorded its stages is cleared by --failed, and every word about it says so: its result,
// done's refusal, and a narrower check that passed without clearing it. One with no stages is told as
// before.
func TestAStagedRedGateNamesFailed(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	stages := []lane.Stage{{Name: "unit", Status: lane.StagePassed}, {Name: "e2e", Status: lane.StageFailed, Failures: []string{"TestA"}}}
	red := state.Job{ID: "j4", Phase: "P3", Kind: state.JobVerify, Command: "make e2e", Commit: "3f2a9c1b04de55aa", Status: state.JobFailed, Exit: 1,
		Started: t0, Ended: t0.Add(time.Hour), Stages: stages}
	rule := "As it recorded its stages, `baton verify --failed` runs `make e2e` again on what failed alone, with the stages that passed carried, " +
		"and clears it if that passes: the whole gate need not run again."
	narrow := state.Job{ID: "j5", Phase: "P3", Kind: state.JobVerify, Command: "go test -run TestA ./e2e", Status: state.JobPassed}
	for _, c := range []struct {
		name, got string
		want      []string
	}{
		{"its result", JobResult(red, Log{}, nil, ""), []string{"Fix what failed (quick checks in place help), commit, and rerun what failed with `baton verify --failed --notes \"…\"`: a red gate stands", rule}},
		{"done's refusal", RedRefusal(red, nil), []string{"Fix what failed, commit, and rerun what failed with `baton verify --failed --notes \"…\"`; `baton done P3` then waits for it.", rule}},
		{"a narrower check passed", JobResult(narrow, Log{}, []state.Job{red}, ""), []string{"It does not clear P3's gate, job j4 (`make e2e`), which is red", rule}},
		{"a narrower check queued", RedGateStands(red, narrow), []string{"so it does not clear it, whatever its result", rule}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.got, w) {
				t.Errorf("%s lacks %q:\n%s", c.name, w, c.got)
			}
		}
	}
	red.Stages = nil
	for _, got := range []string{JobResult(red, Log{}, nil, ""), RedRefusal(red, nil)} {
		if strings.Contains(got, "--failed") || !strings.Contains(got, "verify again with") {
			t.Errorf("an unstaged gate:\n%s", got)
		}
	}
	if got := RedRefusal(state.Job{ID: "j6", Phase: "P3", Kind: state.JobAwait, Command: "gh run watch 7", Status: state.JobFailed, Stages: stages}, nil); strings.Contains(got, "--failed") {
		t.Errorf("a wait outside:\n%s", got)
	}
}

// As a rerun is queued, the model is told what it carries, with the commit each carried stage passed on
// and the files changed since, what it narrows, with the ids, and what runs whole.
func TestRerunQueued(t *testing.T) {
	v := state.RerunView{Of: "j4", Commit: "9a9a9a9a9a9a9a9a",
		Carried: []state.CarriedStage{
			{Stage: lane.Stage{Name: "unit", Commit: "72b7740c1d2e3f4a", From: "j4"}, Changed: []string{"a.go", "b.go"}, More: 3, Known: true},
			{Stage: lane.Stage{Name: "lint", Commit: "72b7740c1d2e3f4a", From: "j2"}, Known: true},
			{Stage: lane.Stage{Name: "docs", Commit: "9a9a9a9a9a9a9a9a", From: "j3"}},
			{Stage: lane.Stage{Name: "vet", Commit: "11"}},
		},
		Narrowed: []lane.Stage{{Name: "e2e", Failures: []string{"TestA", "TestB/sub"}}},
		Whole:    []string{"perf"},
	}
	got := RerunQueued("P3", v)
	want := "it reruns what failed in job j4:\n" +
		"- carried, not run again: `unit`, passed in j4 on 72b7740c1d2e (changed since: a.go, b.go and 3 more); `lint`, passed in j2 on 72b7740c1d2e (no file changed since); " +
		"`docs`, passed in j3 on 9a9a9a9a9a9a (this commit); `vet`, passed on 11\n" +
		"- narrowed to what failed there: `e2e` (TestA, TestB/sub)\n" +
		"- run whole: `perf`\n" +
		"A stage j4's record does not name runs whole too. If it passes, it clears j4; if it fails, it is P3's red gate from then on, " +
		"and `baton verify --failed` reruns what failed in it. To run a carried stage again, add --stages <name>,<name>."
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	var ids []string
	for i := range maxIDs + 2 {
		ids = append(ids, "T"+string(rune('a'+i)))
	}
	if got := narrowed([]lane.Stage{{Name: "e2e", Failures: ids}}); !strings.HasSuffix(got, ", Tt and 2 more)") {
		t.Errorf("many failures: %s", got)
	}
}

// A rerun's result says what it carried and narrowed; a passing one, that it clears the red gate, and,
// when files changed since a carried stage passed, to check them.
func TestARerunsResult(t *testing.T) {
	v := &state.RerunView{Of: "j4", Commit: "9a9a9a9a9a9a9a9a",
		Carried:  []state.CarriedStage{{Stage: lane.Stage{Name: "unit", Commit: "72b7740c1d2e3f4a", From: "j4"}, Changed: []string{"a.go"}, Known: true}},
		Narrowed: []lane.Stage{{Name: "e2e", Failures: []string{"TestA"}}},
	}
	j := state.Job{ID: "j7", Phase: "P3", Kind: state.JobVerify, Command: "make e2e", Commit: "9a9a9a9a9a9a9a9a", Reruns: "j4", Status: state.JobPassed}
	got := JobResult(j, Log{Rerun: v}, nil, "")
	for _, w := range []string{"[baton] Job j7 (P3) passed after 0s: `make e2e` on 9a9a9a9a9a9a.\nIt reran what failed in job j4 (verify --failed):\n" +
		"- carried, not run again: `unit`, passed in j4 on 72b7740c1d2e (changed since: a.go)\n- narrowed to what failed there: `e2e` (TestA)\n" +
		"As it passed, it clears j4. The stages it carried passed on the commits named: if a file changed since is theirs, check it in place.\n\n" +
		"It checked 9a9a9a9a9a9a"} {
		if !strings.Contains(got, w) {
			t.Errorf("lacks %q:\n%s", w, got)
		}
	}
	j.Status, j.Exit, j.Stages = state.JobFailed, 1, []lane.Stage{{Name: "e2e", Status: lane.StageFailed}}
	got = JobResult(j, Log{Rerun: v}, nil, "")
	if !strings.Contains(got, "It reran what failed in job j4") || strings.Contains(got, "it clears j4") || !strings.Contains(got, "rerun what failed with `baton verify --failed") {
		t.Errorf("a red rerun:\n%s", got)
	}
	if got := JobResult(j, Log{Rerun: &state.RerunView{Of: "j4"}}, nil, ""); !strings.Contains(got, "- it carried nothing and narrowed nothing: every stage ran whole") {
		t.Errorf("a rerun that carried nothing:\n%s", got)
	}
	j.Reruns = ""
	if got := JobResult(j, Log{Rerun: v}, nil, ""); strings.Contains(got, "It reran") {
		t.Errorf("a job that reran nothing:\n%s", got)
	}
}

func TestRerunAndStopWords(t *testing.T) {
	stages := []lane.Stage{{Name: "unit", Status: lane.StagePassed}, {Name: "e2e", Status: lane.StageFailed}}
	red := state.Job{ID: "j4", Phase: "P3", Command: "make e2e", Commit: "3f2a9c1b04de55aa", Status: state.JobFailed, Exit: 1, Stages: stages}
	running := state.Job{ID: "j8", Phase: "P3", Command: "make e2e", Commit: "4f2a9c1b04de55aa", Status: state.JobRunning}
	stopped := state.Job{ID: "j8", Phase: "P3", Command: "make e2e", Commit: "4f2a9c1b04de55aa", Status: state.JobRunning}
	for _, c := range []struct{ got, want string }{
		{NoRedGate("P3", nil), "not queued — --failed reruns what failed in P3's red gate, and P3 has none: no gate of P3 went red since its last pass, " +
			"or was stopped after it showed a failure (`baton stop`). To check the phase, queue its gate itself: `baton verify --notes \"…\" -- <command>`."},
		{NoRedGate("P3", &running), "not queued — --failed reruns what failed in P3's red gate, and P3 has none: no gate of P3 went red since its last pass, " +
			"or was stopped after it showed a failure (`baton stop`). Its gate, job j8 (`make e2e` on 4f2a9c1b04de), is running and has shown no failure: baton gives you its result when it ends."},
		{NoStages("P3", red), "not queued — job j4 (`make e2e` on 3f2a9c1b04de), the gate --failed would rerun for P3, recorded no stages, and baton reads no failed test in its log, " +
			"so it cannot tell that a narrower command reruns what failed. Rerun the failures by name and make that P3's gate: " +
			"`baton verify --replace --notes \"…\" -- <command>` (a decision without the human, and they are told); or fix what failed, then verify again with `make e2e`. " +
			"For the gates to come, build the gate from stages: a committed script whose steps each run as `baton stage <name> -- <command>` " +
			"(a failed stage stops the ones after it, and --failed then reruns only what failed)."},
		{UnknownStages(red, []string{"tow"}, stages), "not queued — --stages names `tow`, which job j4 did not record: its stages are `unit`, `e2e`."},
		{FailedCommand(red, "make test"), "not queued — --failed reruns job j4's own command, `make e2e`, and `make test` is another. Leave the command out to rerun what failed in j4; " +
			"to make `make test` P3's gate instead, verify it with --replace."},
		{RerunChanged("P3"), "not queued — P3's jobs changed as baton looked: run it again."},
		{StopReason(""), "stopped with baton stop"},
		{StopReason(" e2e failed. "), "stopped with baton stop (e2e failed)"},
		{StopUnknown("j9", nil), "not stopped — this run has no job j9 (a phase agent's jobs are its own to stop). None of its jobs is queued or running."},
		{StopUnknown("j9", []state.Job{running}), "not stopped — this run has no job j9 (a phase agent's jobs are its own to stop). Its jobs queued or running: j8 (`make e2e`, P3)."},
		{StopEnded(red), "not stopped — job j4 (`make e2e`, P3) has ended: it failed (exit 1) after 0s. To rerun only what failed, fix it, commit, and `baton verify --failed --notes \"…\"`."},
		{Stopped(stopped, 15*time.Second, &red, true, true), "stopped job j8 (`make e2e` on 4f2a9c1b04de, P3): it counts as interrupted, so it checked nothing, " +
			"and its runner ends what it runs, and brings its lane down, within 15s. P3's gate is still job j4 (`make e2e`), red, as it was. " +
			"P3 waited on it after `baton done`, so it reopens as unverified. " +
			"To rerun only what failed, fix it, commit, and `baton verify --failed --notes \"…\"`: the stages that passed are carried."},
		{Stopped(stopped, 15*time.Second, nil, false, false), "stopped job j8 (`make e2e` on 4f2a9c1b04de, P3): it counts as interrupted, so it checked nothing, " +
			"and its runner ends what it runs, and brings its lane down, within 15s."},
		{Stopped(state.Job{ID: "j9", Phase: "P3", Command: "make lint", Status: state.JobQueued}, 15*time.Second, nil, false, false),
			"stopped job j9 (`make lint`, P3) before it started: it counts as interrupted, so it checked nothing."},
		{StoppedLine(stopped, "e2e failed"), "baton: job j8 (`make e2e`, P3) stopped with baton stop (e2e failed) → its runner ends it"},
	} {
		if c.got != c.want {
			t.Errorf("got  %q\nwant %q", c.got, c.want)
		}
	}
}
