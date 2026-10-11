package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// withWorkers is a run whose host, instance inst, runs a worker on P1; the worker on R1 is suspended and
// the one on P5 has landed.
func withWorkers(t *testing.T) *session {
	s := attached(t)
	s.set(func(x *state.State) {
		x.Workers = []state.Worker{
			{Phase: "P1", Ref: "sess-1/P1", Instance: state.WorkerInstance("inst", "P1"), Status: state.WorkerWorking, Since: s.now.Add(-14 * time.Minute)},
			{Phase: "R1", Ref: "sess-1/R1", Instance: state.WorkerInstance("inst", "R1"), Status: state.WorkerSuspended, Since: s.now.Add(-time.Hour)},
			{Phase: "P5", Ref: "sess-1/P5", Instance: state.WorkerInstance("inst", "P5"), Status: state.WorkerLanded, Since: s.now},
		}
	})
	return s
}

// fromShell makes the session's commands run from a plain shell in its project.
func (s *session) fromShell() {
	s.t.Chdir(filepath.Dir(s.env["BATON_DIR"]))
	s.env = map[string]string{"HOME": s.t.TempDir()}
}

func TestViewListsTheWorkers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		shell   bool
		workers bool
		want    []string
		not     []string
	}{
		{"from a shell", true, true, []string{
			"Workers:",
			"  P1 — The one-liners  working 14m  baton view P1\n",
			"  R1 — The roster can grow  suspended 1h00m, not running: it resumes with its run's next host\n",
			"Ctrl-] leaves",
		}, []string{"P5", "another terminal"}},
		{"in a hosted session", false, true, []string{
			"  P1 — The one-liners  working 14m  baton view P1\n",
			"run it in another terminal, in this project",
		}, nil},
		{"with none", true, false, []string{"No worker is working in this project.\n"}, []string{"Workers:"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := attached(t)
			if tc.workers {
				s = withWorkers(t)
			}
			if tc.shell {
				s.fromShell()
			}
			out := s.must("", "view")
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("no %q in:\n%s", w, out)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(out, w) {
					t.Errorf("%q in:\n%s", w, out)
				}
			}
		})
	}
}

func TestViewRefusesWhatItCannotShow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shell bool
		arg   string
		want  string
	}{
		{"inside a hosted session", false, "P1", "Run baton view P1 in another terminal, in this project."},
		{"an unknown phase", true, "P9", "no worker is working on P9. The workers here: P1, R1 (baton view lists them)"},
		{"a landed worker", true, "P5", "no worker is working on P5"},
		{"a suspended worker", true, "R1", "R1's worker is not running: its host has ended."},
		{"no terminal", true, "P1", "baton view needs a terminal"},
		{"two phases", true, "P1 R1", "view takes one phase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := withWorkers(t)
			if tc.shell {
				s.fromShell()
			}
			s.mustFail(tc.want, append([]string{"view"}, strings.Fields(tc.arg)...)...)
		})
	}
}

func TestPickViewable(t *testing.T) {
	running := func(run, phase string) viewable {
		return viewable{w: state.Worker{Phase: phase, Ref: state.WorkerRef(run, phase)}, host: "inst-" + run}
	}
	for _, tc := range []struct {
		name string
		ws   []viewable
		arg  string
		want string // the ref picked, or the error
	}{
		{"by phase", []viewable{running("a", "P1"), running("a", "P2")}, "P2", "a/P2"},
		{"by ref", []viewable{running("a", "P1"), running("b", "P1")}, "b/P1", "b/P1"},
		{"a phase two runs have", []viewable{running("a", "P1"), running("b", "P1")}, "P1",
			"several runs have a worker on P1; name the one you mean: baton view a/P1, or baton view b/P1"},
		{"no workers at all", nil, "P1", "no worker is working on P1: this project has no workers"},
		{"not running", []viewable{{w: state.Worker{Phase: "P1", Ref: "a/P1"}}}, "P1", "P1's worker is not running"},
		{"the one running, beside a run that ended", []viewable{{w: state.Worker{Phase: "P1", Ref: "a/P1"}}, running("b", "P1")}, "P1", "b/P1"},
		{"the running two, beside a run that ended", []viewable{running("a", "P1"), {w: state.Worker{Phase: "P1", Ref: "b/P1"}}, running("c", "P1")}, "P1",
			"several runs have a worker on P1; name the one you mean: baton view a/P1, or baton view c/P1"},
		{"none of several running", []viewable{{w: state.Worker{Phase: "P1", Ref: "a/P1"}}, {w: state.Worker{Phase: "P1", Ref: "b/P1"}}}, "P1", "P1's worker is not running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := pickViewable(tc.ws, tc.arg)
			got := v.w.Ref
			if err != nil {
				got = err.Error()
			}
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A worker whose run's host is gone, or whose instance is not that host's, is not running.
func TestAWorkerRunsOnlyUnderItsRunsLiveHost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		later   time.Duration // how long after the host's last heartbeat
		inst    string        // the worker's instance
		running bool
	}{
		{"the host is live", 0, state.WorkerInstance("inst", "P1"), true},
		{"the host is gone", 2 * state.OwnerTTL, state.WorkerInstance("inst", "P1"), false},
		{"another host's worker", 0, state.WorkerInstance("old", "P1"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := withWorkers(t)
			s.set(func(x *state.State) { x.Workers[0].Instance = tc.inst })
			p, err := state.OpenProject(s.env["BATON_DIR"], func() time.Time { return s.now })
			if err != nil {
				t.Fatal(err)
			}
			ws := viewables(p, s.now.Add(tc.later))
			if len(ws) != 2 || ws[0].w.Phase != "P1" || (ws[0].host == "inst") != tc.running {
				t.Errorf("got %+v", ws)
			}
		})
	}
}

// The list names a worker by its phase alone unless another worker on that phase is running too: a run
// that ended keeps its workers suspended, and its P1 is no reason to name the running P1 by its ref.
func TestViewNamesAWorkerByItsPhaseWhileItIsTheOnlyOneRunning(t *testing.T) {
	worker := func(run, phase, host string) viewable {
		return viewable{w: state.Worker{Phase: phase, Ref: state.WorkerRef(run, phase), Status: state.WorkerWorking}, host: host}
	}
	for _, tc := range []struct {
		name string
		ws   []viewable
		want string
	}{
		{"beside a run that ended", []viewable{worker("a", "P1", ""), worker("b", "P1", "inst-b")}, "  baton view P1\n"},
		{"beside another running", []viewable{worker("a", "P1", "inst-a"), worker("b", "P1", "inst-b")}, "  baton view b/P1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			io, out, _ := testIO("", nil)
			printViewables(io, tc.ws)
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("no %q in:\n%s", tc.want, out)
			}
		})
	}
}
