package state

import (
	"errors"
	"os"
	"path/filepath"
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
	p.Sweep()
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
