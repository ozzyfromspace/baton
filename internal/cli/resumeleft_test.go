package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// baton status says when the resume timer resumes the run, or that it was cancelled; dropping the run
// clears the timer, logged.
func TestTheResumeTimerInStatusAndDrop(t *testing.T) {
	s, planFile := newSession(t)
	s.attach(planFile)
	now := s.now
	at := now.Add(3 * time.Hour)
	s.set(func(st *state.State) {
		state.Limit(st, "lead", now, at)
		state.ArmResume(st, at, 0)
	})
	if out := s.must("", "status"); !strings.Contains(out, "until "+at.Local().Format("15:04")+" · baton resumes the run at "+at.Local().Format("15:04")+" unless you pause or write") {
		t.Errorf("status:\n%s", out)
	}
	s.set(func(st *state.State) { state.CancelResume(st, state.CancelPause, now) })
	if out := s.must("", "status"); !strings.Contains(out, " · auto-resume cancelled (pause): /baton run resumes") {
		t.Errorf("status:\n%s", out)
	}
	s.set(func(st *state.State) { state.Rearm(st) })
	s.must("", "drop")
	logged := false
	for _, line := range strings.Split(s.events(), "\n") {
		logged = logged || strings.Contains(line, `"kind":"usage_wait_cancelled"`) && strings.Contains(line, `"by":"drop"`)
	}
	if !logged {
		t.Errorf("events:\n%s", s.events())
	}
	if st := s.state(); st.Usage != nil {
		t.Errorf("usage after drop: %+v", st.Usage)
	}
}

// baton status says the run winds down and when it goes on; baton done with nothing left to start says the
// lead waits for the reset; a drop cancels the going on, logged.
func TestTheWindDownInStatusDoneAndDrop(t *testing.T) {
	s, planFile := newSession(t)
	s.attach(planFile)
	resets := s.now.Add(5 * time.Hour)
	goesOn := resets.Add(state.ResumeGrace)
	s.set(func(st *state.State) {
		st.Usage = &state.Usage{WeeklyPct: 98.5, WeeklyResets: resets}
		state.WindDown(st, 2, s.now)
	})
	if out := s.must("", "status"); !strings.Contains(out, "weekly wind-down: 1.5% of weekly usage left → nothing new starts; what is under way finishes · baton goes on at "+goesOn.Local().Format("15:04")) {
		t.Errorf("status:\n%s", out)
	}
	if out := s.must("", "done", "P0", "--notes", "P0's notes"); !strings.Contains(out, "no new phase starts until the weekly reset, and baton goes on at "+goesOn.Local().Format("15:04")) {
		t.Errorf("done:\n%s", out)
	}
	if st := s.state(); st.Current != "" {
		t.Errorf("the lead took %s", st.Current)
	}
	s.must("", "drop")
	logged := false
	for _, line := range strings.Split(s.events(), "\n") {
		logged = logged || strings.Contains(line, `"kind":"usage_wait_cancelled"`) && strings.Contains(line, `"by":"drop"`) && strings.Contains(line, `"wind_down":true`)
	}
	if !logged {
		t.Errorf("events:\n%s", s.events())
	}
}
