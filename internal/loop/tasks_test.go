package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// taskRig is a running plan's run, the lead's, P0 done half an hour ago and P1 in hand, with the coordinator's
// reviewer on and its model call stubbed (answer), and a machine of its own: claude (pid 200) is the host's
// (pid 100) child, and each background shell put on it (process) claude's, with the CPU its tree used.
type taskRig struct {
	t   *testing.T
	now time.Time
	s   *state.Store
	c   *Coordinator
	dir string // the session's tasks folder

	mu      sync.Mutex
	answer  func(q Completion) (string, error)
	asked   []Completion
	procs   []procs.Proc
	cpu     map[int]uint64
	stopped [][]int
}

func newTaskRig(t *testing.T) *taskRig {
	t.Helper()
	r := &taskRig{t: t, now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC), dir: t.TempDir(), cpu: map[int]uint64{}}
	s, err := state.Open(filepath.Join(t.TempDir(), ".baton"), "inst", func() time.Time { return r.now })
	if err != nil {
		t.Fatal(err)
	}
	r.s = s
	pl := plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{{ID: "P0", Title: "zero"}, {ID: "P1", Title: "one", After: []string{"P0"}}}}
	if err := s.SavePlan(pl); err != nil {
		t.Fatal(err)
	}
	r.c = &Coordinator{Lead: &Loop{Store: s, Timing: DefaultTiming, Logf: func(string, ...any) {}}, Reviewer: Reviewer{On: true}}
	r.answer = func(Completion) (string, error) { return "UNSURE\nThe evidence does not say.", nil }
	r.c.tk.call = func(ctx context.Context, _ *state.Store, q Completion) (Answer, error) {
		r.mu.Lock()
		r.asked = append(r.asked, q)
		answer := r.answer
		r.mu.Unlock()
		text, err := answer(q)
		return Answer{Text: text, Took: 3 * time.Second}, err
	}
	r.c.Tasks = TaskProcs{Host: 100,
		List: func() ([]procs.Proc, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return append([]procs.Proc{{PID: 200, PPID: 100, PGID: 200, Comm: "claude"}}, r.procs...), nil
		},
		Usage: func(pgid int) (procs.Usage, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if cpu, ok := r.cpu[pgid]; ok {
				return procs.Usage{pgid: cpu, pgid + 1: 0}, nil
			}
			return procs.Usage{}, nil
		},
		Stop: func(pids []int, _ time.Duration) []int {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.stopped = append(r.stopped, pids)
			for _, pid := range pids {
				delete(r.cpu, pid)
				r.procs = slices.DeleteFunc(r.procs, func(p procs.Proc) bool { return p.PID == pid })
			}
			return nil
		}}
	s.Update(func(st *state.State) error {
		st.Mode, st.Current, st.Generation = state.ModeRunning, "P1", 1
		st.Phases["P0"] = &state.PhaseState{Status: state.PhaseDone, DoneAt: r.now.Add(-30 * time.Minute)}
		st.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive}
		st.Run.TasksDir = r.dir
		return nil
	})
	helloFrom(t, s)
	return r
}

// shell puts a background shell of the lead's model on record, as it started in phase age ago and as the last
// Stop listed it, its output last written quiet ago.
func (r *taskRig) shell(id, command, description, phase string, age, quiet time.Duration) string {
	r.t.Helper()
	out := filepath.Join(r.dir, id+".output")
	if err := os.WriteFile(out, []byte("started\nwaiting\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.still(id, quiet)
	r.s.Update(func(st *state.State) error {
		st.Run.Background = append(st.Run.Background, state.Task{ID: id, Type: state.TaskShell, Status: "running", Description: description, Command: command,
			Since: r.now.Add(-age)})
		st.Run.Start(id, state.BackgroundStart{Type: state.TaskShell, Tool: "Bash", Command: command, Description: description, Phase: phase,
			Generation: st.Generation, Output: out, At: r.now.Add(-age)})
		return nil
	})
	return out
}

// still marks task id's output as last written quiet ago.
func (r *taskRig) still(id string, quiet time.Duration) {
	at := r.now.Add(-quiet)
	os.Chtimes(filepath.Join(r.dir, id+".output"), at, at)
}

// process puts the shell running command on the machine, as claude starts it, with the CPU its tree used.
func (r *taskRig) process(pid int, command string, cpu uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	script := "source /tmp/snapshot.sh 2>/dev/null || true && setopt NO_EXTENDED_GLOB && eval '" + strings.ReplaceAll(command, "'", `'"'"'`) +
		"' < /dev/null && pwd -P >| /tmp/claude-cwd"
	r.procs = append(r.procs, procs.Proc{PID: pid, PPID: 200, PGID: pid, Comm: "zsh", Args: []string{"/bin/zsh", "-c", script}})
	r.cpu[pid] = cpu
}

// look runs the coordinator's look at background work after advance, and waits for its reviews.
func (r *taskRig) look(advance time.Duration) {
	r.now = r.now.Add(advance)
	r.c.taskStep(r.now, r.s)
	r.c.tk.wg.Wait()
}

// viewAt is the lead's view at now, its host the run's owner.
func viewAt(now time.Time) host.View { return host.View{Now: now, Owner: true} }

func (r *taskRig) state() state.State {
	st, _ := r.s.Load()
	return st
}

func (r *taskRig) reviews() []Completion {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked)
}

func (r *taskRig) count(kind string) int { return len(eventsOf(readEvents(r.s), kind)) }

func (r *taskRig) logged(kind string, fields map[string]any) bool {
	return eventIn(readEvents(r.s), kind, fields)
}

// stops lists the stop commands baton gave its mod, by the task each stops.
func (r *taskRig) stops() map[string]int {
	out := map[string]int{}
	rec, _ := bridge.Open(r.s.Dir, r.s.Now, nil).Peek()
	for _, o := range rec.Open {
		if o.Kind == bridge.Stop {
			out[o.Agent] = o.ID
		}
	}
	return out
}

// lookIns is what the lead's session is given now of baton's look-ins about background work.
func (r *taskRig) lookIns(s *state.Store) []state.LookIn {
	var out []state.LookIn
	s.Update(func(st *state.State) error {
		_, left := state.TakeLookIns(st, r.now)
		for _, li := range left {
			if li.Kind == state.LookTaskSuspect || li.Kind == state.LookTaskStopped {
				out = append(out, li)
			}
		}
		return nil
	})
	return out
}

const campaignLoop = `until grep -q "passed\|failed" /tmp/claude-501/p/s/tasks/b1.output; do sleep 5; done`

// The campaign's loop: a shell the lead's model started in P0, which is done, polling a task's output that
// has ended, quiet for 40 minutes. It is reviewed once, the reviewer given the evidence: its command, its
// description, where its phase stands, its output's end, its CPU, the task it waits on and the signals; and it
// is not reviewed again on the same signals.
func TestASuspectIsReviewedOnceWithItsEvidence(t *testing.T) {
	r := newTaskRig(t)
	r.shell("b8196fcr0", campaignLoop, "Wait for the P0 tests", "P0", 45*time.Minute, 40*time.Minute)
	r.process(4242, campaignLoop, 1000)
	r.s.Update(func(st *state.State) error {
		st.Run.Gone = map[string]time.Time{"b1": r.now.Add(-41 * time.Minute)}
		return nil
	})
	r.answer = func(Completion) (string, error) { return "KEEP 60\nIt may still be reading.", nil }
	r.look(0)
	r.look(time.Minute)
	got := r.reviews()
	if len(got) != 1 {
		t.Fatalf("%d reviews, want 1:\n%s", len(got), readEvents(r.s))
	}
	q := got[0]
	if q.Model != "sonnet" || q.MaxTokens != 8000 || !strings.Contains(q.System, "When in doubt, KEEP or UNSURE") {
		t.Errorf("asked %s for %d tokens, system %q", q.Model, q.MaxTokens, q.System)
	}
	for _, want := range []string{"task id b8196fcr0", `"Wait for the P0 tests"`, `phase P0 of the plan "Demo", which is now done`, "task b1, which ended 41m ago",
		"phase_over (30m), quiet (40m), waits_on_ended (41m)", "waiting\n</data>", `grep -q "passed\|failed"`} {
		if !strings.Contains(q.Prompt, want) {
			t.Errorf("want %q in the reviewer's prompt:\n%s", want, q.Prompt)
		}
	}
	if !r.logged("task_reviewed", map[string]any{"task": "b8196fcr0", "phase": "P0", "verdict": "keep", "model": "sonnet", "again_minutes": 60,
		"reason": "It may still be reading.", "owner": "the lead"}) {
		t.Errorf("task_reviewed:\n%s", readEvents(r.s))
	}
	// A second look reads the CPU its tree used meanwhile: under 1% of a core is a signal, but not one that
	// calls for a review by itself.
	r.look(time.Minute)
	if n := len(r.reviews()); n != 1 || len(r.stops()) != 0 || len(r.stopped) != 0 {
		t.Errorf("%d reviews, stops %v %v", n, r.stops(), r.stopped)
	}
}

// A KEEP holds the next review until the time it names, or until a kind of signal no review has seen; after
// MaxReviews a task is named once as long-running and left alone, never stopped.
func TestKeepHoldsTheNextReview(t *testing.T) {
	r := newTaskRig(t)
	r.shell("b2", "./build.sh", "A silent build ~20m", "P1", 40*time.Minute, 35*time.Minute)
	r.answer = func(Completion) (string, error) { return "KEEP 30\nA build that prints only at its end.", nil }
	r.look(0)
	r.look(10 * time.Minute) // no new signal, its KEEP not due
	if n := len(r.reviews()); n != 1 {
		t.Fatalf("%d reviews, want 1", n)
	}
	r.look(15 * time.Minute) // an hour old: long, a new kind, before its KEEP's time
	if n := len(r.reviews()); n != 2 {
		t.Fatalf("%d reviews, want 2:\n%s", n, readEvents(r.s))
	}
	r.look(29 * time.Minute)
	r.look(2 * time.Minute) // its KEEP's time came
	if n := len(r.reviews()); n != 3 {
		t.Fatalf("%d reviews, want 3", n)
	}
	for range 3 {
		r.look(4 * time.Hour)
	}
	if n := len(r.reviews()); n != 3 || r.count("background_long") != 1 || !r.logged("background_long", map[string]any{"task": "b2", "reviewed": true}) {
		t.Fatalf("%d reviews, %d background_long:\n%s", n, r.count("background_long"), readEvents(r.s))
	}
	if len(r.stops()) != 0 || len(r.stopped) != 0 || r.count("background_stopped") != 0 {
		t.Error("a task stopped with no STOP verdict")
	}
	if said := strings.Join(r.state().Run.Announce, "\n"); !strings.Contains(said, `"A silent build ~20m"`) || !strings.Contains(said, "reviewed 3 times and kept") {
		t.Errorf("told %q", said)
	}
}

// STOP sends one stop through baton's mod (TaskStop with the task's id); once Claude Code has ended the
// task, the stop is said with the reviewer's reason, and the owner is told what was stopped and why, and that
// it may start it again.
func TestAStopVerdictStopsTheTaskThroughTheMod(t *testing.T) {
	r := newTaskRig(t)
	out := r.shell("b8196fcr0", campaignLoop, "Wait for the P0 tests", "P0", 45*time.Minute, 40*time.Minute)
	r.process(4242, campaignLoop, 1000)
	r.answer = func(Completion) (string, error) { return "STOP\nIt polls the output of b1, which has ended.", nil }
	r.look(0)
	stops := r.stops()
	if len(stops) != 1 || stops["b8196fcr0"] == 0 {
		t.Fatalf("stop commands %v", stops)
	}
	r.look(20 * time.Second)
	if len(r.stops()) != 1 || r.count("background_stopped") != 0 || len(r.stopped) != 0 {
		t.Fatalf("stops %v, stopped %v before TaskStop had its time", r.stops(), r.stopped)
	}
	f, _ := os.OpenFile(out, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("[killed]\n")
	f.Close()
	r.look(10 * time.Second)
	if !r.logged("background_stopped", map[string]any{"task": "b8196fcr0", "phase": "P0", "via": "task_stop", "reason": "It polls the output of b1, which has ended.",
		"model": "sonnet"}) || len(r.stopped) != 0 {
		t.Fatalf("background_stopped:\n%s", readEvents(r.s))
	}
	if said := strings.Join(r.state().Run.Announce, "\n"); !strings.Contains(said, `baton stopped the background shell "Wait for the P0 tests" (running 45m, started in P0) on the finding of its reviewer (sonnet): It polls the output of b1, which has ended.`) {
		t.Errorf("told %q", said)
	}
	looks := r.lookIns(r.s)
	if len(looks) != 1 || looks[0].Kind != state.LookTaskStopped || !strings.Contains(looks[0].Text, "start it again") || !strings.Contains(looks[0].Text, out) {
		t.Errorf("look-ins %+v", looks)
	}
	r.look(time.Minute)
	if r.count("background_stopped") != 1 || len(r.reviews()) != 1 {
		t.Errorf("said %d times, reviewed %d times", r.count("background_stopped"), len(r.reviews()))
	}
}

// A task TaskStop did not end within a minute has its shell's process group ended by baton: each process of
// its tree, nothing outside it; its stop command is taken back, and the owner told why Claude Code says it
// failed.
func TestAStopTheModCouldNotMakeEndsTheProcessGroup(t *testing.T) {
	r := newTaskRig(t)
	r.shell("b9", "until test -f /tmp/never; do sleep 2; done", "Wait for a file", "P0", 2*time.Hour, 2*time.Hour)
	r.process(5150, "until test -f /tmp/never; do sleep 2; done", 10)
	r.process(6160, "npm run dev", 9999) // another shell of claude's, never touched
	r.answer = func(Completion) (string, error) { return "STOP\nNothing writes /tmp/never.", nil }
	r.look(0)
	cmd := r.stops()["b9"]
	r.look(taskStopWait)
	if !slices.EqualFunc(r.stopped, [][]int{{5150, 5151}}, slices.Equal) {
		t.Fatalf("stopped %v", r.stopped)
	}
	if _, open := r.stops()["b9"]; open || cmd == 0 {
		t.Errorf("its stop command %d still open", cmd)
	}
	if !r.logged("background_stopped", map[string]any{"task": "b9", "via": "process"}) {
		t.Errorf("background_stopped:\n%s", readEvents(r.s))
	}
	if looks := r.lookIns(r.s); len(looks) != 1 || !strings.Contains(looks[0].Text, "exit code 144") {
		t.Errorf("look-ins %+v", looks)
	}
}

// UNSURE, an answer that is no verdict, and none at all (an error, no mod, the reviewer off) each ask the
// task's owner to look and tell the human, once a task, and stop nothing.
func TestNoVerdictAsksTheOwnerAndStopsNothing(t *testing.T) {
	for _, c := range []struct {
		name    string
		set     func(r *taskRig)
		verdict string
		why     string
	}{
		{"unsure", func(r *taskRig) {}, state.ReviewUnsure, ""},
		{"no verdict", func(r *taskRig) {
			r.answer = func(Completion) (string, error) { return "It is hard to say; perhaps stop it.", nil }
		}, state.ReviewUnsure, ""},
		{"an API error", func(r *taskRig) {
			r.answer = func(Completion) (string, error) { return "", &Unanswered{Why: "api-error", Detail: "overloaded"} }
		}, state.ReviewNone, "api-error"},
		{"a failure", func(r *taskRig) { r.answer = func(Completion) (string, error) { return "", errors.New("boom") } }, state.ReviewNone, "failed"},
		{"no mod", func(r *taskRig) {
			r.c.tk.call = nil
			say(r.t, r.s, bridge.HelloInfo{Instance: "another", Version: "0.8.0", Protocol: bridge.Protocol})
		}, state.ReviewNone, NoMod},
		{"the reviewer off", func(r *taskRig) { r.c.Reviewer.On = false }, state.ReviewNone, reviewOff},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newTaskRig(t)
			r.shell("b3", "tail -f server.log", "Follow the log", "P0", 50*time.Minute, 45*time.Minute)
			r.process(7000, "tail -f server.log", 0)
			c.set(r)
			r.look(0)
			r.look(20 * time.Minute) // long: a new kind, reviewed again
			fields := map[string]any{"task": "b3", "verdict": c.verdict}
			if c.why != "" {
				fields["why"] = c.why
			}
			if !r.logged("task_reviewed", fields) || r.count("task_reviewed") != 2 {
				t.Fatalf("task_reviewed %v:\n%s", fields, readEvents(r.s))
			}
			if r.count("background_suspect") != 1 || !r.logged("background_suspect", map[string]any{"task": "b3", "owner": "the lead"}) {
				t.Errorf("background_suspect:\n%s", readEvents(r.s))
			}
			looks := r.lookIns(r.s)
			if len(looks) != 1 || looks[0].Kind != state.LookTaskSuspect || !strings.Contains(looks[0].Text, "stop it with TaskStop (task id b3)") {
				t.Errorf("look-ins %+v", looks)
			}
			if said := strings.Join(r.state().Run.Announce, "\n"); !strings.Contains(said, "baton stopped nothing, and asked the lead to look") {
				t.Errorf("told %q", said)
			}
			if len(r.stops()) != 0 || len(r.stopped) != 0 || r.state().Run.Reviews["b3"].Stop != nil {
				t.Errorf("stopped with no STOP verdict: %v %v", r.stops(), r.stopped)
			}
		})
	}
}

// A review whose call no host in memory stands for (the host that asked it restarted) counts as no verdict;
// one still in flight holds the next.
func TestAReviewAnEarlierHostAskedLapses(t *testing.T) {
	r := newTaskRig(t)
	r.shell("b4", "sleep 100000", "", "P0", 50*time.Minute, 45*time.Minute)
	r.s.Update(func(st *state.State) error {
		st.Run.AskReview("b4", []string{state.SignalPhaseOver, state.SignalQuiet}, r.now.Add(-time.Minute))
		return nil
	})
	r.look(0)
	if !r.logged("task_reviewed", map[string]any{"task": "b4", "verdict": state.ReviewNone, "why": reviewRestart}) || len(r.reviews()) != 0 {
		t.Fatalf("task_reviewed:\n%s", readEvents(r.s))
	}
	if rv := r.state().Run.Reviews["b4"]; rv == nil || !rv.Asking.IsZero() || !rv.Asked || rv.Reviews != 1 {
		t.Errorf("%+v", rv)
	}
}

// Subagents, workflows and monitors are never reviewed or stopped: once they have run an hour they are named
// once, and left alone.
func TestASubagentIsNeverReviewed(t *testing.T) {
	r := newTaskRig(t)
	r.s.Update(func(st *state.State) error {
		st.Run.Background = append(st.Run.Background, state.Task{ID: "a1", Type: state.TaskSubagent, Status: "running", Description: "Study the API",
			AgentType: "general-purpose", Since: r.now.Add(-3 * time.Hour)})
		st.Run.Start("a1", state.BackgroundStart{Type: state.TaskSubagent, Tool: "Agent", Description: "Study the API", Phase: "P0", Generation: 1, At: r.now.Add(-3 * time.Hour)})
		return nil
	})
	r.look(0)
	r.look(time.Hour)
	if len(r.reviews()) != 0 || r.count("task_reviewed") != 0 || r.count("background_long") != 1 || !r.logged("background_long", map[string]any{"task": "a1", "type": "subagent"}) {
		t.Fatalf("%d reviews:\n%s", len(r.reviews()), readEvents(r.s))
	}
}

// A look owed at once (a plan attached, a boundary, a restart background work holds) is made by the next
// tick, not a minute later, and logged background_audited with what it found.
func TestALookOwedIsMadeAndLogged(t *testing.T) {
	r := newTaskRig(t)
	r.shell("b5", "./wait-for-deploy.sh", "Wait for the deploy", "P0", 50*time.Minute, 45*time.Minute)
	r.s.Update(func(st *state.State) error { st.Run.OweAudit(state.AuditBoundary, r.now); return nil })
	r.answer = func(Completion) (string, error) { return "KEEP 45\nA deploy may be slow.", nil }
	st := r.state()
	r.c.tk.at = r.now
	r.c.tasks(viewAt(r.now), r.s, st, false)
	r.c.tk.wg.Wait()
	if !r.logged("background_audited", map[string]any{"at": "boundary", "suspects": "[b5]", "reviewing": "[b5]"}) || r.state().Run.Audit != nil {
		t.Fatalf("background_audited:\n%s", readEvents(r.s))
	}
	// A restart that background work holds owes one, once.
	st = r.state()
	st.Run.Background = []state.Task{{ID: "b5", Type: state.TaskShell, Status: "running"}}
	r.now = r.now.Add(time.Second)
	r.c.tasks(viewAt(r.now), r.s, st, true)
	r.c.tk.wg.Wait()
	r.now = r.now.Add(time.Second)
	r.c.tasks(viewAt(r.now), r.s, r.state(), true)
	r.c.tk.wg.Wait()
	if !r.logged("background_audited", map[string]any{"at": "restart", "kept": "[b5]"}) || r.count("background_audited") != 2 {
		t.Fatalf("background_audited:\n%s", readEvents(r.s))
	}
}

// A shell is found among claude's children by the command its script evals, quoted as the shell quotes it,
// whole or as far as it was clipped; never two that run one command, nor a process outside claude's tree.
func TestAShellIsFoundByItsCommand(t *testing.T) {
	quote := func(c string) string { return "eval '" + strings.ReplaceAll(c, "'", `'"'"'`) + "' < /dev/null" }
	shells := []procs.Proc{
		{PID: 10, Args: []string{"/bin/zsh", "-c", "source s && " + quote(`echo 'hi' && sleep 9`)}},
		{PID: 11, Args: []string{"/bin/zsh", "-c", quote("sleep 1000")}},
		{PID: 12, Args: []string{"/bin/zsh", "-c", quote("make test")}},
		{PID: 13, Args: []string{"/bin/zsh", "-c", quote("make test")}},
	}
	for _, c := range []struct {
		command string
		pid     int
	}{
		{`echo 'hi' && sleep 9`, 10},
		{"sleep 1000", 11},
		{"sleep 100", 0},
		{"sleep 10…", 11},
		{"make test", 0},
		{"", 0},
	} {
		if got := shellOf(shells, c.command); got != c.pid {
			t.Errorf("%q: pid %d, want %d", c.command, got, c.pid)
		}
	}
	r := newTaskRig(t)
	r.process(300, "sleep 1000", 0)
	r.mu.Lock()
	r.procs = append(r.procs, procs.Proc{PID: 301, PPID: 999, PGID: 301, Args: []string{"/bin/zsh", "-c", quote("sleep 1000")}})
	r.mu.Unlock()
	if found, read := r.c.shells(); !read || len(found) != 1 || found[0].PID != 300 {
		t.Errorf("shells %+v", found)
	}
}

// A task's output ends with the line Claude Code writes as it ends.
func TestATasksEndIsReadFromItsOutput(t *testing.T) {
	dir := t.TempDir()
	for text, over := range map[string]bool{"a\nb\n[exited with code 0]\n": true, "x\n[killed]": true, "running\n": false, "": false,
		strings.Repeat("y", 500) + "\n[exited with code 144]\n": true, "[exited with code 1] and more\n": false} {
		p := filepath.Join(dir, "t.output")
		os.WriteFile(p, []byte(text), 0o644)
		if got := endedOutput(p, int64(len(text))); got != over {
			t.Errorf("%q: ended %v, want %v", text, got, over)
		}
	}
}
