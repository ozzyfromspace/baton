package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// boardSession is a hosted session whose baton config is its own, with phase agents on.
func boardSession(t *testing.T) (*session, string) {
	s, planFile := newSession(t)
	s.env["BATON_HOME"], s.env["BATON_WORKERS"] = t.TempDir(), "2"
	return s, planFile
}

// With no plan, baton board says so, and the band is empty, so the mod draws none. It binds no run.
func TestBoardWithNoPlan(t *testing.T) {
	s, _ := boardSession(t)
	if out := s.must("", "board", "--band"); out != "" {
		t.Errorf("band %q", out)
	}
	if out := s.must("", "board"); out != decide.BoardNoPlan+"\n" {
		t.Errorf("board %q", out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(s.must("", "board", "--json")), &got); err != nil || got["mode"] != state.ModeIdle {
		t.Errorf("json %v (%v)", got, err)
	}
	if runs, _ := os.ReadDir(filepath.Join(s.env["BATON_DIR"], "runs")); len(runs) != 0 {
		t.Errorf("baton board made a run: %v", runs)
	}
}

func TestBoard(t *testing.T) {
	s, planFile := boardSession(t)
	s.attach(planFile)
	s.set(func(x *state.State) { state.Claim(x, "inst", 1, s.now) })
	s.now = s.now.Add(12 * time.Minute)
	const band = "◆ baton · step 1/3 · P0 ▶ 12m · P1 waits on P0 · lead: idle"
	if out := s.must("", "board", "--band"); out != band+"\n" {
		t.Errorf("band %q, want %q", out, band)
	}
	if out := s.must("", "board", "--band", "--width", "40"); out != "◆ baton · step 1/3 · +2 · lead: idle\n" {
		t.Errorf("band at 40 columns %q", out)
	}
	board := strings.Join([]string{
		`◆ baton · "The widget campaign" · step 1 of 3`,
		"step 1  ▶ P0  The overlay — the lead works on it 12m",
		"step 2  · P1  The one-liners — waits on P0",
		"step 3  · R1  The roster can grow — waits on P1",
		"lead: its turn over, with P0 in hand",
	}, "\n") + "\n"
	if out := s.must("", "board"); out != board {
		t.Errorf("board:\n%s\nwant\n%s", out, board)
	}
	var got struct {
		Mode  string
		Step  int
		Steps []struct{ Phases []struct{ ID string } }
	}
	if err := json.Unmarshal([]byte(s.must("", "board", "--json")), &got); err != nil || got.Mode != "running" || got.Step != 1 || len(got.Steps) != 3 {
		t.Errorf("json %+v (%v)", got, err)
	}
	s.mustFail("--width takes a number", "board", "--width", "wide")

	// Phases go one at a time in the lead when the config runs no phase agents, or the host decided so.
	s.env["BATON_WORKERS"] = "0"
	if out := s.must("", "board", "--band"); !strings.Contains(out, " · step 1/3 · one at a time · ") {
		t.Errorf("serial by config: band %q", out)
	}
	if out := s.must("", "board"); !strings.HasSuffix(out, "\n"+decide.BoardSerial(decide.SerialConfig(false))+"\n") {
		t.Errorf("serial by config: board\n%s", out)
	}
	s.env["BATON_WORKERS"] = "2"
	s.set(func(x *state.State) {
		x.Run.Serial = &state.Serial{Instance: "inst", Why: decide.ModMissing, At: s.now}
	})
	if out := s.must("", "board"); !strings.HasSuffix(out, "\n"+decide.BoardSerial(decide.ModMissing)+"\n") {
		t.Errorf("serial by the host: board\n%s", out)
	}
	s.set(func(x *state.State) { x.Run.Serial.Instance = "inst-gone" }) // another host's decision
	if out := s.must("", "board"); strings.Contains(out, "one at a time") {
		t.Errorf("another host's serial: board\n%s", out)
	}
}

// In a phase agent's worktree, baton board shows the run its phase is of.
func TestBoardInAPhaseAgent(t *testing.T) {
	s, planFile := boardSession(t)
	s.attach(planFile)
	lead := s.store()
	p, err := state.OpenProject(s.env["BATON_DIR"], func() time.Time { return s.now })
	if err != nil {
		t.Fatal(err)
	}
	w, err := p.NewWorker(lead.Ref, "P1", "inst", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.dir = w.Root
	if out := s.must("", "board", "--band"); !strings.HasPrefix(out, "◆ baton · step 1/3 · P0 ▶") {
		t.Errorf("band in the phase agent %q", out)
	}
}
