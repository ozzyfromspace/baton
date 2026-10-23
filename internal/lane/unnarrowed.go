package lane

import "strconv"

// Why a red stage's rerun cannot run only what failed in it (Stage.Unnarrowed), so that it runs whole.
const (
	// UnnarrowedNoIDs: no fail line, and no runner's line baton reads, named a test that failed.
	UnnarrowedNoIDs = "no failure ids"
	// UnnarrowedWhole: a file or package failed with no test of it named (it did not build or load, a test
	// timed out), and only a run of all of it reruns that.
	UnnarrowedWhole = "a file or package failed with no test named"
	// UnnarrowedRunners: the failures came from more than one runner's lines, and no one runner's narrowing
	// runs them all.
	UnnarrowedRunners = "failures from more than one runner"
)

// UnnarrowedMany: more tests failed than a stage records (MaxFailures).
var UnnarrowedMany = "more than " + strconv.Itoa(MaxFailures) + " failures"
