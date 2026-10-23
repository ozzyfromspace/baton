package decide

import (
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Each stage a rerun runs whole is named with its last time, where known, and why, in words.
func TestWholeStagesSayWhy(t *testing.T) {
	m := time.Minute
	for _, c := range []struct {
		stage state.WholeStage
		want  string
	}{
		{state.WholeStage{Name: "browser", Why: lane.UnnarrowedNoIDs, Took: 78 * m}, "`browser` (1h18m last time; baton read no failure ids from it)"},
		{state.WholeStage{Name: "load", Why: lane.UnnarrowedMany, Took: 4 * m}, "`load` (4m last time; it had more than 100 failures)"},
		{state.WholeStage{Name: "api", Why: lane.UnnarrowedWhole, Took: 30 * time.Second}, "`api` (30s last time; a file or package of it failed with no test named)"},
		{state.WholeStage{Name: "mixed", Why: lane.UnnarrowedRunners, Took: 12 * m}, "`mixed` (12m last time; its failures came from more than one runner)"},
		{state.WholeStage{Name: "e2e", Why: state.WholeNotOnRecord}, "`e2e` (a run of it showed a failure and never ended, so baton has no record of it)"},
		{state.WholeStage{Name: "docs", Why: state.WholeSkipped("j3")}, "`docs` (skipped in job j3)"},
		{state.WholeStage{Name: "unit", Why: state.WholeBroughtBack, Took: 2 * m}, "`unit` (2m last time; brought back by --stages)"},
		{state.WholeStage{Name: "bare"}, "`bare`"},
	} {
		if got := wholeStages([]state.WholeStage{c.stage}); got != c.want {
			t.Errorf("%s:\ngot  %s\nwant %s", c.stage.Name, got, c.want)
		}
	}
}

// A rerun that would run a long red stage whole is questioned once: why it runs whole, its last time and
// the phase's cost, where the failures may be named, the rerun by name, and --why. The human is told the
// reason given, or that none was.
func TestWholeRerunWords(t *testing.T) {
	m := time.Minute
	of := state.Job{ID: "j4", Phase: "P11", Command: "./qa.sh"}
	browser := state.WholeStage{Name: "browser", Why: lane.UnnarrowedNoIDs, Took: 78 * m}
	api := state.WholeStage{Name: "api", Why: lane.UnnarrowedWhole, Took: 12 * m}
	cost := state.Cost{Runs: 3, Gates: 2*time.Hour + 40*m, Phase: 3 * time.Hour, Of: "./qa.sh", Command: 3, CommandTime: 2*time.Hour + 40*m}
	for _, c := range []struct {
		name, got, want string
	}{
		{"one stage", WholeRerunQuestioned("P11", of, []state.WholeStage{browser}, []string{"/r/jobs/j4.log"}, cost),
			"not queued, this once. This gate spends 1h18m of the human's time; to you it is one tool call. " +
				"P11 so far: 3 gate runs, 2h40m of gates in its 3h00m; all of them `./qa.sh`.\n" +
				"--failed reruns what failed in job j4 (`./qa.sh`), and would run this stage whole: `browser` (1h18m last time; baton read no failure ids from it).\n" +
				"The log of the run it went red in may name what failed (/r/jobs/j4.log, or a log the stage writes of its own): " +
				"if it does, rerun those by name with `baton verify --replace --notes \"…\" -- <a command naming them>`, " +
				"which makes that command P11's gate in place of j4, and the human is told. " +
				"To run it whole anyway, give the same call again with `--why \"…\"`: baton tells the human your reason."},
		{"two stages, a first run", WholeRerunQuestioned("P11", of, []state.WholeStage{browser, api}, []string{"/r/jobs/j2.log", "/r/jobs/j4.log"}, state.Cost{Runs: 1, Of: "./qa.sh", Command: 1}),
			"not queued, this once. This gate spends 1h30m of the human's time; to you it is one tool call.\n" +
				"--failed reruns what failed in job j4 (`./qa.sh`), and would run these stages whole: " +
				"`browser` (1h18m last time; baton read no failure ids from it); `api` (12m last time; a file or package of it failed with no test named).\n" +
				"The log of the run they went red in may name what failed (/r/jobs/j2.log and /r/jobs/j4.log, or a log the stage writes of its own): " +
				"if it does, rerun those by name with `baton verify --replace --notes \"…\" -- <a command naming them>`, " +
				"which makes that command P11's gate in place of j4, and the human is told. " +
				"To run them whole anyway, give the same call again with `--why \"…\"`: baton tells the human your reason."},
		{"said", WholeRerunSaid("P11", []state.WholeStage{browser}, cost, "the fix touches every page"),
			"P11 reruns `browser` of `./qa.sh` whole (1h18m last time, run 4): the fix touches every page"},
		{"unexplained", WholeRerunUnexplained("P11", []state.WholeStage{browser, api}, cost),
			"P11 reran `browser` and `api` of `./qa.sh` whole (1h30m last time, run 4, 2h40m so far) without saying why"},
		{"unexplained, its first run in the phase", WholeRerunUnexplained("P11", []state.WholeStage{browser}, state.Cost{Of: "./qa.sh"}),
			"P11 reran `browser` of `./qa.sh` whole (1h18m last time) without saying why"},
	} {
		if c.got != c.want {
			t.Errorf("%s:\ngot\n%s\nwant\n%s", c.name, c.got, c.want)
		}
	}
}
