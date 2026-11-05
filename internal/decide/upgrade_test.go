package decide

import (
	"strings"
	"testing"
	"time"
)

// What keeps a restart on a newer baton away is said with what names each part: a task by its
// description, else its id, with its age and phase when known; agents by phase; wakeups, monitors and the
// gate's reason. A task no longer needed is the human's to stop, said only when there is one.
func TestUpgradeWaitsNamesWhatHoldsTheRestart(t *testing.T) {
	for _, c := range []struct {
		name    string
		h       UpgradeHeld
		want    []string
		without []string
	}{
		{"a stale shell", UpgradeHeld{Tasks: []UpgradeTask{{Type: "shell", Description: "Wait for the tests", ID: "b1", Age: 22*time.Hour + 5*time.Minute, Phase: "P23"}}},
			[]string{`the background shell "Wait for the tests" (running 22h05m, started in P23)`, "can be stopped", "baton v0.8.21 is installed", "for 10 minutes"}, nil},
		{"a subagent named by its id", UpgradeHeld{Tasks: []UpgradeTask{{Type: "subagent", ID: "a7"}}},
			[]string{`the subagent "a7"`}, []string{"running", "("}},
		{"agents, wakeups and monitors", UpgradeHeld{Agents: []string{"P40", "P60"}, Wakeups: 1, Monitors: 2},
			[]string{"the phase agents on P40 and P60, 1 wakeup (a cron or a /loop) and 2 monitors"}, []string{"can be stopped"}},
		{"a dialog", UpgradeHeld{Agents: []string{"P2"}, Gate: "a dialog is open"},
			[]string{"the phase agent on P2; also, a dialog is open."}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := UpgradeWaits("v0.8.21", 10*time.Minute, c.h)
			for _, w := range c.want {
				if !strings.Contains(s, w) {
					t.Errorf("%q\nwant %q in it", s, w)
				}
			}
			for _, w := range c.without {
				if strings.Contains(s, w) {
					t.Errorf("%q\nwant no %q in it", s, w)
				}
			}
		})
	}
}

// A hold given up names the agents it waited for, and says the run goes on.
func TestUpgradeGivenUpSaysTheRunGoesOn(t *testing.T) {
	s := UpgradeGivenUp("v0.8.21", 10*time.Minute, []string{"P40"})
	for _, w := range []string{"baton v0.8.21", "For 10 minutes", "the phase agent on P40", "the run goes on"} {
		if !strings.Contains(s, w) {
			t.Errorf("%q\nwant %q in it", s, w)
		}
	}
}
