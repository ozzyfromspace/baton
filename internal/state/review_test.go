package state

import (
	"slices"
	"testing"
	"time"
)

// A suspect is due a review when it first has a signal that can call for one (any but idle), then only on a
// kind of signal no review has seen, or once a KEEP's time has come; never while one is in flight, once a
// stop was decided, or after MaxReviews.
func TestWhenASuspectIsDueAReview(t *testing.T) {
	now := t0
	sus := func(whys ...string) Suspect {
		var s Suspect
		for _, w := range whys {
			s.Signals = append(s.Signals, Signal{Why: w})
		}
		return s
	}
	for _, c := range []struct {
		name string
		r    *TaskReview
		s    Suspect
		due  bool
	}{
		{"a first signal", nil, sus(SignalQuiet), true},
		{"idle alone", nil, sus(SignalIdle), false},
		{"idle beside another", nil, sus(SignalIdle, SignalPhaseOver), true},
		{"the same kinds again", &TaskReview{Reviews: 1, Seen: []string{SignalQuiet}, Verdict: ReviewUnsure}, sus(SignalQuiet, SignalIdle), false},
		{"a new kind", &TaskReview{Reviews: 1, Seen: []string{SignalQuiet}, Verdict: ReviewUnsure}, sus(SignalQuiet, SignalLong), true},
		{"a KEEP not due yet", &TaskReview{Reviews: 1, Seen: []string{SignalQuiet}, Verdict: ReviewKeep, Again: now.Add(time.Minute)}, sus(SignalQuiet), false},
		{"a KEEP due", &TaskReview{Reviews: 1, Seen: []string{SignalQuiet}, Verdict: ReviewKeep, Again: now}, sus(SignalQuiet), true},
		{"a new kind before a KEEP is due", &TaskReview{Reviews: 1, Seen: []string{SignalQuiet}, Verdict: ReviewKeep, Again: now.Add(time.Hour)}, sus(SignalQuiet, SignalPastEnd), true},
		{"in flight", &TaskReview{Reviews: 1, Seen: []string{SignalQuiet}, Asking: now}, sus(SignalQuiet, SignalLong), false},
		{"a stop decided", &TaskReview{Reviews: 1, Seen: []string{SignalQuiet}, Stop: &TaskStop{At: now}}, sus(SignalQuiet, SignalLong), false},
		{"reviewed MaxReviews times", &TaskReview{Reviews: MaxReviews, Seen: []string{SignalQuiet}, Verdict: ReviewKeep, Again: now}, sus(SignalQuiet, SignalLong), false},
	} {
		if due, _ := c.r.Due(c.s, now); due != c.due {
			t.Errorf("%s: due %v, want %v", c.name, due, c.due)
		}
	}
}

// A review asked is in flight and counted; its verdict lands only on the review asked: a STOP starts the
// task's stop, a KEEP names when to look again; and a record goes once its task is gone, but a stop under way.
func TestAReviewsRecord(t *testing.T) {
	var r Runtime
	if !r.AskReview("b1", []string{SignalQuiet}, t0) || r.AskReview("b1", []string{SignalLong}, t0) {
		t.Fatal("a second review asked while one is in flight")
	}
	if r.Reviewed("b1", t0.Add(time.Second), ReviewStop, "x", "sonnet", time.Time{}, t0) {
		t.Fatal("a verdict landed on a review not asked")
	}
	if !r.Reviewed("b1", t0, ReviewKeep, "a build", "sonnet", t0.Add(time.Hour), t0.Add(time.Minute)) {
		t.Fatal("the verdict did not land")
	}
	rv := r.Reviews["b1"]
	if rv.Reviews != 1 || !slices.Equal(rv.Seen, []string{SignalQuiet}) || rv.Verdict != ReviewKeep || !rv.Again.Equal(t0.Add(time.Hour)) || !rv.Asking.IsZero() {
		t.Fatalf("%+v", rv)
	}
	r.AskReview("b1", []string{SignalQuiet, SignalLong}, t0.Add(2*time.Hour))
	r.Reviewed("b1", t0.Add(2*time.Hour), ReviewStop, "it waits on nothing", "sonnet", time.Time{}, t0.Add(2*time.Hour))
	if rv.Stop == nil || rv.Stop.Reason != "it waits on nothing" || rv.Reviews != 2 || !slices.Equal(rv.Seen, []string{SignalQuiet, SignalLong}) {
		t.Fatalf("%+v", rv)
	}
	r.ReviewOf("b2").Long = true
	r.ForgetReviews(func(string) bool { return false })
	if r.Reviews["b1"] == nil || r.Reviews["b2"] != nil {
		t.Fatalf("forgot %v", r.Reviews)
	}
	rv.Stop.Ended = t0
	if r.ForgetReviews(func(string) bool { return false }); r.Reviews != nil {
		t.Fatalf("kept %v", r.Reviews)
	}
}

// A plan attached, a phase boundary and a run resumed each owe a look at the session's background work.
func TestALookAtBackgroundWorkIsOwed(t *testing.T) {
	st := Attach(threePhases(), t0, Origin{})
	if st.Run.Audit == nil || !slices.Equal(st.Run.Audit.Why, []string{AuditAttach}) {
		t.Fatalf("attach: %+v", st.Run.Audit)
	}
	st.Run.Audit = nil
	st.OweBoundary(t0)
	st.Run.Audit = nil
	Pause(&st)
	if err := Resume(&st); err != nil || st.Run.Audit == nil || !slices.Equal(st.Run.Audit.Why, []string{AuditResume}) {
		t.Fatalf("resume: %+v, %v", st.Run.Audit, err)
	}
	st = AttachAtBoundary(st, threePhases(), t0)
	if !slices.Equal(st.Run.Audit.Why, []string{AuditAttach, AuditBoundary}) {
		t.Fatalf("attach at a boundary: %+v", st.Run.Audit)
	}
}
