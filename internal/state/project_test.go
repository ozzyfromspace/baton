package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newProject(t *testing.T) (*Project, *clock) {
	t.Helper()
	c := &clock{t0}
	p, err := OpenProject(filepath.Join(t.TempDir(), ".baton"), c.now)
	if err != nil {
		t.Fatal(err)
	}
	return p, c
}

func bind(t *testing.T, p *Project, b Binding) *Store {
	t.Helper()
	s, err := p.Bind(b)
	if err != nil {
		t.Fatalf("bind %+v: %v", b, err)
	}
	return s
}

func ownerOf(t *testing.T, s *Store) string {
	t.Helper()
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Owner == nil {
		return ""
	}
	return st.Owner.Instance
}

func withPlan(t *testing.T, s *Store, title string) {
	t.Helper()
	pl := threePhases()
	pl.Title = title
	if err := s.SavePlan(pl); err != nil {
		t.Fatal(err)
	}
	s.Update(func(st *State) error { *st = Reattach(*st, pl, t0, Origin{}); return nil })
}

func TestEachSessionGetsItsOwnRun(t *testing.T) {
	p, _ := newProject(t)
	a := bind(t, p, Binding{Session: "sess-a", Instance: "host-a"})
	b := bind(t, p, Binding{Session: "sess-b", Instance: "host-b"})
	if a.Dir == b.Dir {
		t.Fatalf("two sessions share %s", a.Dir)
	}
	if a.Root != p.Root || filepath.Base(a.Dir) != "sess-a" {
		t.Fatalf("run a: dir %s root %s", a.Dir, a.Root)
	}
	if ownerOf(t, a) != "host-a" || ownerOf(t, b) != "host-b" {
		t.Fatalf("owners %q %q", ownerOf(t, a), ownerOf(t, b))
	}
	if again := bind(t, p, Binding{Session: "sess-a", Instance: "host-a"}); again.Dir != a.Dir {
		t.Fatalf("the same session got another run: %s", again.Dir)
	}
	if p.RunOf("sess-a") != "sess-a" || p.HostRun("host-b") != "sess-b" {
		t.Fatalf("links: %q %q", p.RunOf("sess-a"), p.HostRun("host-b"))
	}
	// The model's CLI, inside the session, finds the same run by the session id alone.
	if cli := bind(t, p, Binding{Session: "sess-b"}); cli.Dir != b.Dir {
		t.Fatalf("cli found %s", cli.Dir)
	}
}

// /clear and a "clear context" approval give the conversation a new session id in the same terminal:
// the run goes with it. Switching to another conversation (/resume inside Claude Code) does not.
func TestAClearedSessionKeepsItsRun(t *testing.T) {
	p, _ := newProject(t)
	a := bind(t, p, Binding{Session: "before", Instance: "host"})
	after := bind(t, p, Binding{Session: "after", Instance: "host", Carry: true})
	if after.Dir != a.Dir || p.RunOf("after") != filepath.Base(a.Dir) {
		t.Fatalf("after /clear: %s, want %s", after.Dir, a.Dir)
	}
	other := bind(t, p, Binding{Session: "elsewhere", Instance: "host"})
	if other.Dir == a.Dir {
		t.Fatal("another conversation took over the run")
	}
	if ownerOf(t, a) != "" || ownerOf(t, other) != "host" || p.HostRun("host") != "elsewhere" {
		t.Fatalf("the host still holds its old run: %q %q %q", ownerOf(t, a), ownerOf(t, other), p.HostRun("host"))
	}
}

// A conversation resumed in a second terminal while the first still drives its run: the second gets
// the run (to read) but neither drives it nor points at it, until the first goes away.
func TestARunHasOneDriver(t *testing.T) {
	p, c := newProject(t)
	first := bind(t, p, Binding{Session: "s", Instance: "one"})
	second := bind(t, p, Binding{Session: "s", Instance: "two"})
	if second.Dir != first.Dir || ownerOf(t, first) != "one" || p.HostRun("two") != "" {
		t.Fatalf("second terminal: dir %s owner %q pointer %q", second.Dir, ownerOf(t, first), p.HostRun("two"))
	}
	c.t = c.t.Add(OwnerTTL + time.Second) // the first terminal is gone
	bind(t, p, Binding{Session: "s", Instance: "two"})
	if ownerOf(t, first) != "two" || p.HostRun("two") != "s" {
		t.Fatalf("no takeover: owner %q pointer %q", ownerOf(t, first), p.HostRun("two"))
	}
}

// A fresh session the host starts for a run whose conversation could not be resumed is linked to that
// run first, under the project's lock: its first hook binds the run, not a new one of its own.
func TestALinkedSessionBindsItsRun(t *testing.T) {
	p, _ := newProject(t)
	run := bind(t, p, Binding{Session: "crashed", Instance: "host-1"})
	p.Unbind("host-1") // the host that ran it execs a new one
	linked := make(chan error, 1)
	p.withLock(func() error {
		go func() { linked <- p.Link("fresh", "crashed") }()
		time.Sleep(100 * time.Millisecond)
		select {
		case err := <-linked:
			t.Errorf("linked while the project was locked (%v)", err)
		default:
		}
		return nil
	})
	if err := <-linked; err != nil {
		t.Fatal(err)
	}
	if p.RunOf("fresh") != "crashed" {
		t.Fatalf("the fresh session belongs to %q", p.RunOf("fresh"))
	}
	if s := bind(t, p, Binding{Session: "fresh", Instance: "host-2"}); s.Dir != run.Dir || ownerOf(t, run) != "host-2" || p.HostRun("host-2") != "crashed" {
		t.Fatalf("the fresh session bound %s (owner %q, host's run %q), want %s", s.Dir, ownerOf(t, run), p.HostRun("host-2"), run.Dir)
	}
	if err := p.Link("fresh", "crashed"); err != nil {
		t.Errorf("linking it again: %v", err)
	}

	bind(t, p, Binding{Session: "other", Instance: "host-3"})
	for _, tc := range []struct{ name, session, run string }{
		{"a run that does not exist", "new", "gone"},
		{"a session of another run", "fresh", "other"},
		{"a worker's sub-run", "new", "crashed/P3"},
		{"no session", "", "crashed"},
	} {
		if err := p.Link(tc.session, tc.run); err == nil {
			t.Errorf("%s: linked", tc.name)
		}
	}
	if p.RunOf("new") != "" || p.RunOf("fresh") != "crashed" {
		t.Errorf("a failed link changed the links: %q %q", p.RunOf("new"), p.RunOf("fresh"))
	}
}

func TestAPlanAttachedFromAShellGoesToTheNextSession(t *testing.T) {
	p, _ := newProject(t)
	pend, err := p.Pending("")
	if err != nil {
		t.Fatal(err)
	}
	withPlan(t, pend, "Shell plan")
	if again, _ := p.Pending(""); again.Dir != pend.Dir {
		t.Fatalf("a second attach from the shell made another run: %s", again.Dir)
	}
	if plain := bind(t, p, Binding{Session: "plain"}); plain.Dir == pend.Dir {
		t.Fatal("an unhosted session took the pending run")
	}
	hosted := bind(t, p, Binding{Session: "hosted", Instance: "host"})
	if hosted.Dir != pend.Dir {
		t.Fatalf("the next hosted session got %s, want %s", hosted.Dir, pend.Dir)
	}
	if exists(p.path(pendingFile)) {
		t.Fatal("the run is still pending")
	}
	if next := bind(t, p, Binding{Session: "later", Instance: "host2"}); next.Dir == pend.Dir {
		t.Fatal("a later session took the same run")
	}
}

func TestBindNeedsASessionOrAHost(t *testing.T) {
	p, _ := newProject(t)
	if _, err := p.Bind(Binding{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("got %v", err)
	}
	for _, bad := range []string{"../x", "a/b", ".."} {
		if _, err := p.Bind(Binding{Session: bad}); err == nil {
			t.Errorf("session %q accepted", bad)
		}
	}
	s := bind(t, p, Binding{Session: "s", Instance: "host"})
	if byHost := bind(t, p, Binding{Instance: "host"}); byHost.Dir != s.Dir {
		t.Fatalf("by host: %s", byHost.Dir)
	}
}

func TestLookupAndUnbind(t *testing.T) {
	p, _ := newProject(t)
	if _, ok := p.Lookup("s", "host"); ok {
		t.Fatal("found a run before any was bound")
	}
	s := bind(t, p, Binding{Session: "s", Instance: "host"})
	if got, ok := p.Lookup("s", ""); !ok || got.Dir != s.Dir {
		t.Fatal("lookup by session")
	}
	if got, ok := p.Lookup("", "host"); !ok || got.Dir != s.Dir {
		t.Fatal("lookup by host")
	}
	p.Unbind("host")
	if p.HostRun("host") != "" || ownerOf(t, s) != "" {
		t.Fatalf("after unbind: pointer %q owner %q", p.HostRun("host"), ownerOf(t, s))
	}
}

func TestPickOutsideASession(t *testing.T) {
	p, _ := newProject(t)
	if _, err := p.Pick(""); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("empty project: %v", err)
	}
	bind(t, p, Binding{Session: "idle", Instance: "h0"}) // a session with no plan does not count
	a := bind(t, p, Binding{Session: "a", Instance: "h1"})
	withPlan(t, a, "A")
	if got, err := p.Pick(""); err != nil || got.Dir != a.Dir {
		t.Fatalf("one run with a plan: %v %v", got, err)
	}
	b := bind(t, p, Binding{Session: "b", Instance: "h2"})
	withPlan(t, b, "B")
	var several ErrSeveralRuns
	if _, err := p.Pick(""); !errors.As(err, &several) || len(several.Runs) != 2 {
		t.Fatalf("two runs with plans: %v", err)
	}
	a.Update(func(st *State) error { st.Mode = ModeComplete; return nil })
	if got, err := p.Pick(""); err != nil || got.Dir != b.Dir {
		t.Fatalf("the one run under way wins over a finished one: %v %v", got, err)
	}
	b.Update(func(st *State) error { st.Mode = ModeComplete; return nil })
	if _, err := p.Pick(""); !errors.As(err, &several) || len(several.Runs) != 2 {
		t.Fatalf("two finished runs: %v", err)
	}
	pend, _ := p.Pending("")
	if got, err := p.Pick(""); err != nil || got.Dir != pend.Dir {
		t.Fatalf("a pending run wins: %v %v", got, err)
	}
}

func TestOthersAreTheLiveRuns(t *testing.T) {
	p, c := newProject(t)
	a := bind(t, p, Binding{Session: "a", Instance: "h1"})
	bind(t, p, Binding{Session: "b", Instance: "h2"})
	if o := p.Others(filepath.Base(a.Dir)); len(o) != 1 || o[0].ID != "b" {
		t.Fatalf("others: %+v", o)
	}
	c.t = c.t.Add(OwnerTTL + time.Second)
	if o := p.Others(filepath.Base(a.Dir)); len(o) != 0 {
		t.Fatalf("a host that went away still counts: %+v", o)
	}
}

func TestSweepRemovesOnlyWhatNobodyCanUse(t *testing.T) {
	p, c := newProject(t)
	empty := bind(t, p, Binding{Session: "empty", Instance: "h1"})
	planned := bind(t, p, Binding{Session: "planned", Instance: "h2"})
	withPlan(t, planned, "Kept")
	drafted := bind(t, p, Binding{Session: "drafted", Instance: "h3"})
	drafted.SaveDraft("half a thought", "", t0)
	recent := bind(t, p, Binding{Session: "recent", Instance: "h4"})
	pend, _ := p.Pending("")
	for _, h := range []string{"h1", "h2", "h3"} {
		p.Unbind(h)
	}
	p.writeLink("gone", sessionsDir, "dangling")
	c.t = c.t.Add(time.Hour)
	recent.Update(func(st *State) error { return Claim(st, "h4", 1, c.t) }) // still driven
	for _, dir := range []string{empty.Dir, planned.Dir, drafted.Dir, pend.Dir} {
		old := t0.Add(-time.Hour)
		os.Chtimes(filepath.Join(dir, "state.json"), old, old)
		os.Chtimes(dir, old, old)
	}
	want := []string{"runs/empty", "sessions/dangling", "sessions/empty"}
	if got := p.Swept(true, nil); !reflect.DeepEqual(got, want) || !exists(empty.Dir) || !exists(p.path(sessionsDir, "dangling")) {
		t.Fatalf("a dry sweep says %q (want %q), and removes nothing", got, want)
	}
	if got := p.Swept(false, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("swept %q, want %q", got, want)
	}
	if exists(empty.Dir) || exists(p.path(sessionsDir, "empty")) || exists(p.path(sessionsDir, "dangling")) {
		t.Fatal("an empty, abandoned run (or a dangling link) survived")
	}
	for _, s := range []*Store{planned, drafted, recent, pend} {
		if !exists(s.Dir) {
			t.Errorf("swept %s", s.Dir)
		}
	}
	if p.HostRun("h4") == "" {
		t.Error("swept the live host's pointer")
	}
}

func TestAProjectFromBeforeRunsMovesIntoOne(t *testing.T) {
	c := &clock{t0}
	dir := filepath.Join(t.TempDir(), ".baton")
	legacy, _ := Open(dir, "old", c.now)
	pl := threePhases()
	pl.Title = "Old plan"
	legacy.SavePlan(pl)
	legacy.Update(func(st *State) error {
		*st = Attach(pl, t0, Origin{})
		st.Run.SessionID = "sess-old"
		return Claim(st, "old", 1, t0)
	})
	legacy.Event("attached", nil)
	legacy.AppendHandoff("P0", "notes")

	// An older baton still drives it: it stays where that baton expects it.
	p, err := OpenProject(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dir, "state.json")) || p.RunOf("sess-old") != "" {
		t.Fatal("moved a run that a live host drives")
	}

	c.t = c.t.Add(OwnerTTL + time.Second)
	p, err = OpenProject(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "state.json")) || p.RunOf("sess-old") != "sess-old" {
		t.Fatal("the old run did not move")
	}
	s, _ := p.Run("sess-old", "")
	got, err := s.LoadPlan()
	if err != nil || got.Title != "Old plan" {
		t.Fatalf("plan after the move: %+v %v", got, err)
	}
	for _, f := range []string{"events.jsonl", "handoff.md"} {
		if !exists(filepath.Join(s.Dir, f)) {
			t.Errorf("%s did not move", f)
		}
	}
	if st, _ := s.Load(); st.Mode != ModeRunning || st.Current != "P0" {
		t.Fatalf("state after the move: %+v", st)
	}
}

// newWorker makes the sub-run of a worker on phase of lead's run, working in a fresh directory.
func newWorker(t *testing.T, p *Project, lead *Store, phase string) *Store {
	t.Helper()
	tree := filepath.Join(t.TempDir(), phase)
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := p.NewWorker(lead.Ref, phase, "host", tree)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestAWorkerHasASubRunOfItsOwn(t *testing.T) {
	p, _ := newProject(t)
	lead := bind(t, p, Binding{Session: "lead", Instance: "host"})
	withPlan(t, lead, "Plan")
	w := newWorker(t, p, lead, "P1")

	tree, _ := filepath.EvalSymlinks(w.Root)
	switch {
	case w.Ref != "lead/P1" || w.Parent != "lead" || w.Worker() != "P1":
		t.Errorf("ref %q parent %q worker %q", w.Ref, w.Parent, w.Worker())
	case w.Dir != filepath.Join(lead.Dir, "workers", "P1"):
		t.Errorf("dir %s", w.Dir)
	case w.Root != tree || w.Root == p.Root:
		t.Errorf("root %s, want the worktree's real path %s", w.Root, tree)
	case w.Checkout != p.Root || w.Project != p.Dir:
		t.Errorf("checkout %s project %s", w.Checkout, w.Project)
	case w.HandoffPath() != lead.HandoffPath():
		t.Errorf("handoff %s, want the run's %s", w.HandoffPath(), lead.HandoffPath())
	}
	if lead.Ref != "lead" || lead.Parent != "" || lead.Worker() != "" || lead.Root != p.Root || lead.Checkout != p.Root || lead.Project != p.Dir {
		t.Errorf("lead: %+v", lead)
	}
	// A phase agent shares the lead's session: nothing links a session or a host to its sub-run.
	if p.HostRun(WorkerInstance("host", "P1")) != "" {
		t.Errorf("a host link %q", p.HostRun("host-P1"))
	}
	if ownerOf(t, w) != "host-P1" || ownerOf(t, lead) != "host" || p.HostRun("host") != "lead" {
		t.Errorf("owners: worker %q lead %q; the lead's host link %q", ownerOf(t, w), ownerOf(t, lead), p.HostRun("host"))
	}
	again, err := p.Run("lead/P1", "x")
	if err != nil || again.Dir != w.Dir || again.Root != w.Root {
		t.Fatalf("reopened: %+v %v", again, err)
	}
	if runs := p.Runs(); len(runs) != 1 || runs[0].ID != "lead" {
		t.Errorf("runs list a worker: %+v", runs)
	}
	if ws := p.Workers("lead"); len(ws) != 1 || ws[0].ID != "lead/P1" || ws[0].Worktree != w.Root || len(ws[0].Sessions) != 0 {
		t.Errorf("workers: %+v", ws)
	}
	if o := p.Others("lead"); len(o) != 0 {
		t.Errorf("a worker counts as another session in the checkout: %+v", o)
	}
	if _, err := p.NewWorker("nobody", "P1", "host", w.Root); err == nil {
		t.Error("a worker for a run that is not there")
	}
	up, err := w.ParentRun()
	if err != nil || up.Dir != lead.Dir || up.Ref != "lead" || up.Root != p.Root || up.Parent != "" || up.Instance != w.Instance {
		t.Errorf("the worker's parent run: %+v %v", up, err)
	}
	if _, err := lead.ParentRun(); err == nil {
		t.Error("a run of its own has a parent run")
	}
}

func TestRefs(t *testing.T) {
	p, _ := newProject(t)
	lead := bind(t, p, Binding{Session: "lead", Instance: "host"})
	newWorker(t, p, lead, "P1")
	for _, tc := range []struct {
		ref    string
		exists bool
	}{
		{"lead", true},
		{"lead/P1", true},
		{"lead/P2", false}, // no worker on P2
		{"other/P1", false},
		{"lead/P1/x", false},
		{"lead/..", false},
		{"../lead", false},
		{"/P1", false},
		{"lead/", false},
		{"", false},
		{`lead\P1`, false},
	} {
		if got := p.runExists(tc.ref); got != tc.exists {
			t.Errorf("runExists(%q) = %v", tc.ref, got)
		}
		if _, err := p.ExistingRun(tc.ref, ""); (err == nil) != tc.exists {
			t.Errorf("ExistingRun(%q): %v", tc.ref, err)
		}
		if strings.Contains(tc.ref, "/") && !tc.exists {
			if _, err := p.Run(tc.ref, ""); err == nil {
				t.Errorf("Run(%q) made a sub-run", tc.ref)
			}
		}
	}
	if exists(filepath.Join(lead.Dir, "workers", "P2")) {
		t.Error("opening a worker that is not there made its directory")
	}
}

func TestAWorkersEventsNameItsPhase(t *testing.T) {
	p, _ := newProject(t)
	lead := bind(t, p, Binding{Session: "lead", Instance: "host"})
	w := newWorker(t, p, lead, "P1")
	w.Event("phase_done", map[string]any{"phase": "P1", "worker": "P9"})
	lead.Event("attached", nil)
	read := func(s *Store) map[string]any {
		b, err := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		var rec map[string]any
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	if rec := read(w); rec["worker"] != "P1" || rec["instance"] != "host-P1" || rec["kind"] != "phase_done" {
		t.Errorf("worker's event: %v", rec)
	}
	if rec := read(lead); rec["worker"] != nil || rec["session"] != nil {
		t.Errorf("the lead's event: %v", rec)
	}
}

// A phase agent is linked to its phase's sub-run by its id (it shares the lead's session), and its hooks
// find the sub-run by that id alone. A working directory inside a sub-run's worktree names its phase. A
// link to a sub-run that is gone is swept.
func TestAPhaseAgentIsLinkedByItsID(t *testing.T) {
	p, _ := newProject(t)
	lead := bind(t, p, Binding{Session: "lead", Instance: "host"})
	withPlan(t, lead, "Plan")
	w := newWorker(t, p, lead, "P1")
	gone := newWorker(t, p, lead, "P2")
	for _, c := range []struct{ run, phase, agent string }{
		{"lead", "P1", ""}, {"lead", "", "a1"}, {"lead", "P1", "../a1"}, {"lead", "P9", "a1"}, {"nobody", "P1", "a1"},
	} {
		if err := p.LinkAgent(c.run, c.phase, c.agent); err == nil {
			t.Errorf("linked %+v", c)
		}
	}
	if err := p.LinkAgent("lead", "P1", "a1"); err != nil {
		t.Fatal(err)
	}
	if err := p.LinkAgent("lead", "P2", "a2"); err != nil {
		t.Fatal(err)
	}
	if p.AgentRun("a1") != "lead/P1" || p.AgentRun("unknown") != "" || p.AgentRun("../a1") != "" {
		t.Errorf("found %q %q", p.AgentRun("a1"), p.AgentRun("unknown"))
	}
	if p.RunOf("lead") != "lead" {
		t.Error("linking an agent moved the lead's session")
	}
	sub := filepath.Join(w.Root, "pkg", "deep")
	os.MkdirAll(sub, 0o755)
	for dir, want := range map[string]string{w.Root: "P1", sub: "P1", p.Root: "", filepath.Dir(w.Root): "", w.Root + "-other": "", "": ""} {
		if got := p.WorktreeOf("lead", dir); got != want {
			t.Errorf("WorktreeOf(%q) = %q, want %q", dir, got, want)
		}
	}

	os.Remove(filepath.Join(gone.Dir, treeFile)) // P2's sub-run is gone
	if p.AgentRun("a2") != "" {
		t.Error("an agent found a sub-run that is gone")
	}
	p.Sweep()
	if exists(p.path(agentsDir, "a2")) || !exists(p.path(agentsDir, "a1")) {
		t.Errorf("swept: a2 %v a1 %v", exists(p.path(agentsDir, "a2")), exists(p.path(agentsDir, "a1")))
	}

	// An agent's events are its sub-run's, and say which agent wrote them.
	s, err := p.Run("lead/P1", WorkerInstance("host", "P1"))
	if err != nil {
		t.Fatal(err)
	}
	s.Agent = "a1"
	s.Event("dialog_open", nil)
	b, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	if !strings.Contains(string(b), `"agent":"a1"`) || !strings.Contains(string(b), `"worker":"P1"`) {
		t.Errorf("events: %s", b)
	}
}
