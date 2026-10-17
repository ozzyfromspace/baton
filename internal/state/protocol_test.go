package state

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// The records the gate protocol, look-ins, facts, the record file and the risk ledger keep read back as
// written, and a state that has none of them writes none of their keys, as before.
func TestTheGateRecordsRoundTrip(t *testing.T) {
	st := New()
	st.Phases["P3"] = &PhaseState{Status: PhaseDone, LandCheck: "go test -run TestLanding ./internal/loop", Deviation: "none"}
	st.Jobs = []Job{{
		ID: "P3.j2", Phase: "P3", Kind: JobVerify, Command: "sh scripts/gate.sh", Status: JobFailed, Exit: 1,
		Reruns: "P3.j1", Ran: t0.Add(31 * time.Minute),
		Stages: []lane.Stage{
			{Name: "lint", Status: lane.StageCarried, Commit: "72b7740", From: "P3.j1"},
			{Name: "unit", Status: lane.StageFailed, Exit: 1, Started: t0.Add(32 * time.Minute), Ended: t0.Add(34 * time.Minute),
				Commit: "5d1e2aa", Failures: []string{"TestA"}, KeepGoing: true},
		},
		Failing:  &Failing{At: t0.Add(33 * time.Minute), Line: "--- FAIL: TestA (0.01s)", Stage: "unit"},
		FailTold: t0.Add(33*time.Minute + time.Second),
		LookedIn: []string{"first_failure", "past_history"},
	}}
	st.Facts = []Fact{{Text: "the lane's database is on port 5433", Phase: "P5", At: t0}}
	st.Recorded = []string{"P0", "P3"}
	st.Risk = &Risk{Total: 17, Since: t0, Commit: "72b7740", Reviewed: t0.Add(time.Hour), Verdict: "targeted", Held: t0.Add(2 * time.Hour),
		Phases: []PhaseRisk{{Phase: "P3", Points: 17, Deviation: "none",
			Factors: []RiskFactor{{Name: "lines", Points: 9, Detail: "412 lines changed"}, {Name: "no_check", Points: 8}}}}}
	st.Run.GatesQuestioned = []string{"repeat\x00make e2e\x0072b7740"}

	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back State
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, st) {
		t.Errorf("read back\n%+v\nwant\n%+v", back, st)
	}

	plain := New()
	plain.Phases["P3"] = &PhaseState{Status: PhaseDone}
	plain.Jobs = []Job{{ID: "j1", Phase: "P3", Status: JobPassed}}
	b, err = json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"land_check", "deviation", "reruns", `"ran"`, "stages", "failing", "fail_told", "looked_in",
		"facts", "recorded", "risk", "gates_questioned"} {
		if strings.Contains(string(b), key) {
			t.Errorf("a state with no such record writes %s: %s", key, b)
		}
	}
}

func TestJobFileID(t *testing.T) {
	cases := []struct{ name, want string }{
		{"j3.log", "j3"},
		{"j3.stages", "j3"},
		{"P3.j12.log", "P3.j12"},
		{"P3.j12.stages", "P3.j12"},
		{".log", ""},
		{"j3", ""},
		{"j3.txt", ""},
		{".j3.log.tmp-1", ""},
	}
	for _, c := range cases {
		if got := JobFileID(c.name); got != c.want {
			t.Errorf("JobFileID(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
