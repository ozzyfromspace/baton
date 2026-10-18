package decide

import (
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// As a narrowed rerun is queued, the model is told what it stands in for and what it does not run; the QA
// phase ahead runs the whole set.
func TestANarrowedRerunsWords(t *testing.T) {
	red := state.Job{ID: "j1", Phase: "P3", Command: "make test", Status: state.JobFailed}
	direct := state.Job{ID: "j1", Phase: "P3", Command: "go test ./...", Status: state.JobFailed}
	stopped := state.Job{ID: "j1", Phase: "P3", Command: "go test ./...", Status: state.JobInterrupted}
	const opens = "it reruns only what failed in job j1 (TestA and TestB) and stands in for it: if it passes, it clears j1; if it fails, it is P3's red gate from then on."
	const cmd = "go test -run '^(TestA|TestB)$' ./src"
	ids := []string{"TestA", "TestB"}
	for _, c := range []struct{ got, want string }{
		{NarrowedQueued("P3", direct, ids, cmd, ""), opens},
		{NarrowedQueued("P3", direct, ids, cmd, "P7 (QA)"), opens + " P7 (QA) runs the whole set."},
		{NarrowedQueued("P3", red, ids, cmd, "P7 (QA)"), opens + " If `make test` stops at its first failing command, what came after it never ran, " +
			"and this rerun does not run it either. P7 (QA) runs the whole set."},
		{NarrowedQueued("P3", red, ids, cmd, ""), opens + " If `make test` stops at its first failing command, what came after it never ran, " +
			"and this rerun does not run it either. Verify `make test` whole before the phase is done if that matters."},
		{NarrowedQueued("P3", stopped, ids, cmd, "P7 (QA)"), opens + " Job j1 did not run to its end, so the tests it never reached are unchecked, " +
			"and this rerun does not run them. P7 (QA) runs the whole set."},
	} {
		if c.got != c.want {
			t.Errorf("got\n%s\nwant\n%s", c.got, c.want)
		}
	}
}

// A narrowed rerun's result names what it reran, and, passed, that it cleared the gate it stands in for;
// a staged rerun's words are left to rerunRan, and a narrowed one's never say every stage ran whole.
func TestANarrowedRerunsResult(t *testing.T) {
	j := state.Job{ID: "j2", Phase: "P3", Kind: state.JobVerify, Command: "go test -run '^TestA$' ./src", Commit: "abcdef0123",
		Reruns: "j1", Narrows: []string{"TestA"}, Status: state.JobPassed}
	got := JobResult(j, Log{Rerun: &state.RerunView{Of: "j1"}}, nil, "P7 (QA)")
	if want := "It reran only what failed in job j1 (verify --failed): TestA.\nAs it passed, it stands in for j1 and clears it. P7 (QA) runs the whole set.\n\n"; !strings.Contains(got, want) ||
		strings.Contains(got, "every stage ran whole") {
		t.Errorf("result:\n%s\nwant %q", got, want)
	}
	j.Status = state.JobFailed
	if got := narrowedRan(j, "P7 (QA)"); got != "It reran only what failed in job j1 (verify --failed): TestA.\n\n" {
		t.Errorf("a red one's:\n%q", got)
	}
	j.Narrows = nil
	if got := narrowedRan(j, ""); got != "" {
		t.Errorf("a staged rerun's: %q", got)
	}
}
