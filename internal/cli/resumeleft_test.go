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
