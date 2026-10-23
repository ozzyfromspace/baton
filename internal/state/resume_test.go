package state

import (
	"testing"
	"time"
)

// A try after the timer fired backs off 2, 5, 10, then 15 minutes, and never gives up.
func TestResumeBackoff(t *testing.T) {
	for n, want := range map[int]time.Duration{0: 2 * time.Minute, 1: 2 * time.Minute, 2: 5 * time.Minute, 3: 10 * time.Minute, 4: 15 * time.Minute, 40: 15 * time.Minute} {
		if got := ResumeBackoff(n); got != want {
			t.Errorf("try %d: %s, want %s", n, got, want)
		}
	}
}

// When the timer would fire for a hit: its reset known, two minutes after it, afresh; unknown, a backoff
// after the hit only when the timer fired within ten minutes before it.
func TestResumeFor(t *testing.T) {
	hit := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	week := hit.Add(3 * 24 * time.Hour)
	for _, c := range []struct {
		name   string
		u      *Usage
		resets time.Time
		resume time.Time
		tries  int
	}{
		{"a weekly reset days away", nil, week, week.Add(ResumeGrace), 0},
		{"a reset of its own after the timer fired", &Usage{Fired: hit.Add(-time.Minute), Tries: 2}, week, week.Add(ResumeGrace), 0},
		{"unknown, never fired: the old retry", nil, time.Time{}, time.Time{}, 0},
		{"unknown, just after the timer fired", &Usage{Fired: hit.Add(-time.Minute)}, time.Time{}, hit.Add(2 * time.Minute), 1},
		{"unknown, the third try", &Usage{Fired: hit.Add(-3 * time.Minute), Tries: 2}, time.Time{}, hit.Add(10 * time.Minute), 3},
		{"unknown, ten minutes on", &Usage{Fired: hit.Add(-RefireWithin)}, time.Time{}, hit.Add(2 * time.Minute), 1},
		{"unknown, long after it fired", &Usage{Fired: hit.Add(-RefireWithin - time.Second), Tries: 3}, time.Time{}, time.Time{}, 0},
	} {
		resume, tries := ResumeFor(c.u, hit, c.resets)
		if !resume.Equal(c.resume) || tries != c.tries {
			t.Errorf("%s: %s try %d, want %s try %d", c.name, resume, tries, c.resume, c.tries)
		}
	}
}

// A run works on its own while it runs and the lead's stopped turn was not the human's; a phase agent's
// hit does not ask who started the lead's turn.
func TestAutonomous(t *testing.T) {
	for _, c := range []struct {
		name, mode, turnBy, by string
		want                   bool
	}{
		{"baton's turn", ModeRunning, "baton", "lead", true},
		{"no turn yet", ModeRunning, "", "lead", true},
		{"the human's turn", ModeRunning, "human", "lead", false},
		{"an agent's, the human at the lead", ModeRunning, "human", "P3", true},
		{"paused", ModePaused, "baton", "lead", false},
		{"complete", ModeComplete, "baton", "P3", false},
	} {
		st := New()
		st.Mode, st.Run.TurnBy = c.mode, c.turnBy
		if got := Autonomous(st, c.by); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// The timer's life: armed, cancelled by a pause, armed again by /baton run at the end of its hold, fired
// once its time came, and fired at once when a reply lifts the hold.
func TestTheResumeTimer(t *testing.T) {
	hit := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	resume := hit.Add(3 * time.Hour)
	st := New()
	st.Mode = ModeRunning
	if CancelResume(&st, CancelPause, hit) || Rearm(&st) || FireResume(&st, resume) || st.Usage.Waits() {
		t.Fatalf("a timer with none armed: %+v", st.Usage)
	}
	Limit(&st, "lead", hit, resume)
	if !ArmResume(&st, resume, 0) || ArmResume(&st, resume, 0) || !st.Usage.ResumeAt.Equal(resume) || !st.Usage.Waits() {
		t.Fatalf("armed: %+v", st.Usage)
	}
	if err := Pause(&st); err != nil {
		t.Fatal(err)
	}
	if u := st.Usage; !u.ResumeAt.IsZero() || u.Cancelled != CancelPause || !u.CancelSaid.IsZero() || !u.Waits() {
		t.Fatalf("paused: %+v", u)
	}
	if Rearm(&st) {
		t.Fatal("armed again while paused")
	}
	if err := Resume(&st); err != nil {
		t.Fatal(err)
	}
	if !Rearm(&st) || !st.Usage.ResumeAt.Equal(resume) || st.Usage.Cancelled != "" {
		t.Fatalf("armed again: %+v", st.Usage)
	}
	if FireResume(&st, resume.Add(-time.Second)) {
		t.Fatal("fired early")
	}
	if !FireResume(&st, resume.Add(time.Minute)) || FireResume(&st, resume.Add(2*time.Minute)) {
		t.Fatalf("fired: %+v", st.Usage)
	}
	if u := st.Usage; !u.ResumeAt.IsZero() || !u.Fired.Equal(resume.Add(time.Minute)) || u.Waits() || !u.FiredFor(hit) || u.FiredFor(u.Fired) {
		t.Fatalf("fired: %+v", u)
	}

	again := resume.Add(2 * time.Minute)
	Limit(&st, "P3", again, again.Add(time.Hour))
	ArmResume(&st, again.Add(time.Hour), 0)
	reply := again.Add(10 * time.Minute)
	if !LiftLimit(&st, reply) || !st.Usage.ResumeAt.Equal(reply) || !FireResume(&st, reply) {
		t.Fatalf("lifted: %+v", st.Usage)
	}
}
