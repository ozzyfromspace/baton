package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Each kind of look-in the advisor is asked about is held from the session while the host asks, from every
// path that tells it, and once released goes with the advisor's words, taken once; a hold stands once only.
func TestALookInHeldForTheAdvisorIsToldOnceReleased(t *testing.T) {
	until := t0.Add(11 * time.Minute)
	for _, c := range []struct {
		name string
		kind string
		set  func(st *State)
		due  func(st State) bool // whether it is to be told now
		take func(st *State) bool
	}{
		{"a first failure", LookFailing, func(st *State) {
			st.Jobs[0].Failing = &Failing{At: t0.Add(8 * time.Minute), Line: "--- FAIL: TestLogin"}
		}, func(st State) bool { return len(st.UntoldFailures()) == 1 && st.LookInsDue() }, func(st *State) bool {
			f, _ := TakeLookIns(st, t0.Add(10*time.Minute))
			return len(f) == 1
		}},
		{"a look-in left", LookSlow, func(st *State) {
			AddLookIn(st, LookIn{Kind: LookSlow, Job: "j3", Text: "slow", At: t0})
		}, func(st State) bool { return len(st.LeftLookIns()) == 1 && st.LookInsDue() }, func(st *State) bool {
			_, left := TakeLookIns(st, t0.Add(10*time.Minute))
			return len(left) == 1 && len(st.LookIns) == 0
		}},
		{"a red gate's result", LookEnv, func(st *State) {
			st.Current = "P3"
			st.Phases["P3"].Status = PhaseActive
			st.Jobs[0].Status, st.Jobs[0].Ended = JobFailed, t0.Add(9*time.Minute)
		}, func(st State) bool { return len(st.Due()) == 1 }, func(st *State) bool {
			return len(TakeDue(st, t0.Add(10*time.Minute))) == 1
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := lookRun(Job{ID: "j3", Phase: "P3", Kind: JobVerify, Command: "make e2e", Status: JobRunning, Started: t0})
			c.set(&st)
			if !c.due(st) {
				t.Fatal("not due before any hold")
			}
			if !HoldForAdvice(&st, c.kind, "j3", until) || HoldForAdvice(&st, c.kind, "j3", until) {
				t.Fatalf("held %+v", st.Advice)
			}
			if c.due(st) || !st.Advising(c.kind, "j3") || !st.AdviceHeld() || len(st.Holds()) != 1 {
				t.Fatalf("due while held: %+v", st.Advice)
			}
			if c.take(&st) {
				t.Fatal("taken while held")
			}
			if !st.Advising(c.kind, "j3") {
				t.Fatalf("a take before its deadline lapsed it: %+v", st.Advice)
			}
			if !ReleaseAdvice(&st, c.kind, "j3", "Advisor (Haiku): rerun TestLogin.") || ReleaseAdvice(&st, c.kind, "j3", "again") {
				t.Fatalf("released %+v", st.Advice)
			}
			if !c.due(st) || st.AdviceHeld() || !st.Advised(c.kind, "j3") {
				t.Fatalf("not due once released: %+v", st.Advice)
			}
			if !c.take(&st) {
				t.Fatal("not taken once released")
			}
			if got := TakeAdvice(&st, c.kind, "j3"); got != "Advisor (Haiku): rerun TestLogin." {
				t.Errorf("advice %q", got)
			}
			if TakeAdvice(&st, c.kind, "j3") != "" || st.Advised(c.kind, "j3") || HoldForAdvice(&st, c.kind, "j3", until) {
				t.Errorf("advice twice, or held once told: %+v", st.Advice)
			}
		})
	}
}

// Only a look-in still to be told is held: not one told already, about a job that ended, or not left at all.
func TestOnlyALookInToBeToldIsHeld(t *testing.T) {
	for _, c := range []struct {
		name string
		kind string
		set  func(st *State)
	}{
		{"no failure", LookFailing, func(*State) {}},
		{"a failure told", LookFailing, func(st *State) {
			st.Jobs[0].Failing, st.Jobs[0].FailTold = &Failing{Line: "FAIL"}, t0
		}},
		{"a failure of a job that ended", LookFailing, func(st *State) {
			st.Jobs[0].Failing, st.Jobs[0].Status = &Failing{Line: "FAIL"}, JobFailed
		}},
		{"no look-in left", LookSlow, func(*State) {}},
		{"a result not in", LookEnv, func(*State) {}},
		{"a result given", LookEnv, func(st *State) { st.Jobs[0].Status, st.Jobs[0].Delivered = JobFailed, t0 }},
		{"no such job", LookEnv, func(st *State) { st.Jobs = nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := lookRun(Job{ID: "j3", Phase: "P3", Kind: JobVerify, Status: JobRunning, Started: t0})
			c.set(&st)
			if HoldForAdvice(&st, c.kind, "j3", t0) || len(st.Advice) != 0 {
				t.Errorf("held %+v", st.Advice)
			}
		})
	}
}

// A hold past its deadline by AdviceGrace is released by the next take, with no words, so a session never
// waits on a host that stopped looking; advice on a look-in that can no longer be told is forgotten.
func TestAHoldPastItsDeadlineLapses(t *testing.T) {
	st := lookRun(Job{ID: "j3", Phase: "P3", Kind: JobVerify, Status: JobRunning, Started: t0, Failing: &Failing{Line: "FAIL"}})
	until := t0.Add(time.Minute)
	HoldForAdvice(&st, LookFailing, "j3", until)
	if f, _ := TakeLookIns(&st, until.Add(AdviceGrace-time.Second)); len(f) != 0 {
		t.Fatalf("taken within the grace: %+v", f)
	}
	if f, _ := TakeLookIns(&st, until.Add(AdviceGrace)); len(f) != 1 || TakeAdvice(&st, LookFailing, "j3") != "" {
		t.Fatalf("not taken past the grace: %+v %+v", f, st.Advice)
	}

	st = lookRun(Job{ID: "j3", Phase: "P3", Kind: JobVerify, Status: JobRunning, Started: t0})
	AddLookIn(&st, LookIn{Kind: LookSlow, Job: "j3", Text: "slow"})
	HoldForAdvice(&st, LookSlow, "j3", until)
	ReleaseAdvice(&st, LookSlow, "j3", "Advisor (Haiku): stop it.")
	st.Jobs[0].Status = JobPassed
	if _, left := TakeLookIns(&st, t0); len(left) != 0 || st.Advice != nil || st.LookIns != nil {
		t.Errorf("an ended job's look-in: left %+v, advice %+v, look-ins %+v", left, st.Advice, st.LookIns)
	}
}

// A hold reads back as written, and a state with none writes none.
func TestAdviceRoundTrip(t *testing.T) {
	st := New()
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), `"advice"`) {
		t.Fatalf("an empty state wrote advice: %s", b)
	}
	st.Advice = []Advice{{Kind: LookSlow, Job: "j3", Until: t0}, {Kind: LookEnv, Job: "j4", Until: t0, Released: true, Text: "Advisor (Haiku): a port."}}
	b, _ = json.Marshal(st)
	var back State
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Advice) != 2 || back.Advice[1] != st.Advice[1] || !back.Advice[0].Until.Equal(t0) {
		t.Errorf("read back %+v", back.Advice)
	}
}
