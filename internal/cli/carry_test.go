package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/elevate"
	"github.com/ozzyfromspace/baton/internal/hostcheck"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/statusline"
)

// A session with no plan of its own that runs a plan whose run another session left with no host (Claude
// Code moved the conversation to its background service, say) carries that run on, where it stood; with a
// live owner, with several such runs, or with another plan, it attaches afresh.
func TestRunCarriesALeftRunOn(t *testing.T) {
	for _, c := range []struct {
		name   string
		live   bool // the first session's host still drives its run
		second bool // another session left a run of the same plan too
		other  bool // the plan run is another file
		cmd    string
		carry  bool
	}{
		{name: "one left with no host", cmd: "run", carry: true},
		{name: "one left, by attach", cmd: "attach", carry: true},
		{name: "one a live host drives", live: true, cmd: "run"},
		{name: "two left with no host", second: true, cmd: "run"},
		{name: "another plan file", other: true, cmd: "run"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, planFile := newSession(t)
			s.attach(planFile)
			s.must("", "done", "P0", "--notes", "P0's notes")
			s.startNext()
			if c.second {
				older := &session{t: t, now: s.now, env: map[string]string{"BATON_DIR": s.env["BATON_DIR"], "BATON_HOST": "1",
					"BATON_INSTANCE": "inst-0", "CLAUDE_CODE_SESSION_ID": "sess-0"}}
				older.attach(planFile)
			}
			if !c.live {
				s.now = s.now.Add(state.OwnerTTL + time.Minute) // its host is gone
			}
			bg := &session{t: t, now: s.now, env: map[string]string{"BATON_DIR": s.env["BATON_DIR"], "BATON_HOST": "1",
				"BATON_INSTANCE": "inst-bg", "CLAUDE_CODE_SESSION_ID": "bg-sess"}}
			file := planFile
			if c.other {
				file = filepath.Join(filepath.Dir(planFile), "other.md")
				b, _ := os.ReadFile(planFile)
				os.WriteFile(file, b, 0o644)
			}
			var out string
			if c.cmd == "attach" {
				out = bg.must(bg.must("", "attach", file, "--suggest"), "attach", file, "--spec", "-")
			} else {
				out = bg.must("", "run", file)
			}
			p, _ := state.OpenProject(s.env["BATON_DIR"], func() time.Time { return s.now })
			if !c.carry {
				if strings.Contains(out, "carried on") || p.RunOf("bg-sess") == "sess-1" {
					t.Fatalf("carried a run on: %s", out)
				}
				return
			}
			if !strings.Contains(out, "carried on the run of") || !strings.Contains(out, "that session sess-1 left with no baton host: 1 of ") ||
				!strings.Contains(out, "at P1") || !strings.Contains(out, "go on with P1") {
				t.Fatalf("%s:\n%s", c.cmd, out)
			}
			if p.RunOf("bg-sess") != "sess-1" || p.HostRun("inst-bg") != "sess-1" {
				t.Fatalf("bg-sess belongs to %q, its host drives %q", p.RunOf("bg-sess"), p.HostRun("inst-bg"))
			}
			st := s.state()
			if st.Owner == nil || st.Owner.Instance != "inst-bg" || st.Phases["P0"].Status != state.PhaseDone || st.Current != "P1" {
				t.Fatalf("the run did not go on where it stood: owner %+v, current %s", st.Owner, st.Current)
			}
			if ev := s.events(); !strings.Contains(ev, `"run_carried"`) || !strings.Contains(ev, `"session":"bg-sess"`) {
				t.Fatalf("not logged:\n%s", ev)
			}
			if again := bg.must("", "run", file); strings.Contains(again, "carried on") {
				t.Errorf("carried on twice: %s", again)
			}
		})
	}
}

// A session that only inherited baton's environment says so where it is asked (version, status), gives
// no session to baton that it could not resume (start), and binds no run to its inherited host.
func TestInheritedSessionSaysSo(t *testing.T) {
	s, planFile := newSession(t)
	s.attach(planFile)
	home := t.TempDir()
	bg := &session{t: t, now: s.now, env: map[string]string{"BATON_DIR": s.env["BATON_DIR"], "BATON_HOST": "1", "BATON_HOME": home,
		"BATON_INSTANCE": "inst", "CLAUDE_CODE_SESSION_ID": "bg-sess", "CLAUDE_PID": "1"},
		host: func() hostcheck.Status { return hostcheck.Inherited }}
	way := decide.Inherited("bg-sess")
	if out := bg.must("", "version"); !strings.Contains(out, "this session: not hosted (inherited)") || !strings.Contains(out, way) {
		t.Errorf("version:\n%s", out)
	}
	if out := bg.must("", "status"); !strings.Contains(out, "no plan attached here") || !strings.Contains(out, way) {
		t.Errorf("status:\n%s", out)
	}
	if out := bg.must("", "start"); out != way+"\n" {
		t.Errorf("start: %q", out)
	}
	if _, ok := elevate.FindSession(home, "bg-sess"); ok {
		t.Error("start recorded the session for a relaunch nothing would make")
	}
	if out := bg.must(bg.must("", "attach", planFile, "--suggest"), "attach", planFile, "--spec", "-"); !strings.Contains(out, "baton --resume bg-sess") {
		t.Errorf("attach:\n%s", out)
	}
	p, _ := state.OpenProject(s.env["BATON_DIR"], func() time.Time { return s.now })
	if p.HostRun("inst") != "sess-1" {
		t.Errorf("the inherited host's link moved to %q", p.HostRun("inst"))
	}
	if st := s.state(); st.Owner == nil || st.Owner.Instance != "inst" {
		t.Errorf("the first session's run lost its owner: %+v", st.Owner)
	}
	bgRun, _ := p.Lookup("bg-sess", "")
	if st, _ := bgRun.Load(); st.Owner != nil {
		t.Errorf("the inherited session's run was claimed: %+v", st.Owner)
	}
	if out := bg.must("", "status"); !strings.Contains(out, "(not hosted (inherited))") {
		t.Errorf("status with a plan:\n%s", out)
	}
	// Its status line records nothing in the run of the host it inherited, whose lead's context the
	// valves read, and shows none of it.
	in := `{"session_id":"bg-sess","context_window":{"used_percentage":41,"context_window_size":1000000,` +
		`"current_usage":{"input_tokens":2,"cache_read_input_tokens":410000,"output_tokens":90}}}`
	bg.env["HOME"] = t.TempDir() // no status line of the user's after it
	if out := bg.must(in, "statusline"); out != statusline.Inherited() {
		t.Errorf("status line %q", out)
	}
	if st := s.state(); st.Run.Context != nil {
		t.Errorf("the inherited session's context was recorded in the host's run: %+v", st.Run.Context)
	}
}
