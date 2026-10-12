package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/version"
)

// modRun is a hosted session's run, as its first hook bound it, and what `baton mod report` reads there.
type modRun struct {
	t   *testing.T
	dir string
	run *state.Store
	env map[string]string
}

func newModRun(t *testing.T) *modRun {
	dir := filepath.Join(t.TempDir(), ".baton")
	p, _ := state.OpenProject(dir, nil)
	run, err := p.Bind(state.Binding{Session: "sess", Instance: "i"})
	if err != nil {
		t.Fatal(err)
	}
	return &modRun{t: t, dir: dir, run: run, env: map[string]string{"BATON_HOST": "1", "BATON_DIR": dir, "BATON_INSTANCE": "i"}}
}

// report runs `baton mod report` with stdin, and returns its reply.
func (m *modRun) report(stdin string) modReply {
	m.t.Helper()
	io, out, errb := testIO(stdin, m.env)
	if code := Main([]string{"mod", "report"}, io); code != 0 {
		m.t.Fatalf("code %d: %s", code, errb)
	}
	var r modReply
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		m.t.Fatalf("reply %q: %v", out, err)
	}
	return r
}

func (m *modRun) events() string {
	b, _ := os.ReadFile(filepath.Join(m.run.Dir, "events.jsonl"))
	return string(b)
}

func (m *modRun) bridge() *bridge.Bridge { return bridge.Open(m.run.Dir, m.run.Now, nil) }

const modHelloJSON = `{"kind":"hello","session":"sess","version":"0.8.0","protocol":1}`

// The mod says hello, and is told the run, the directory it polls, where its count of reports stands and
// the agent type it registers for phases; then each report it makes is recorded once and logged.
func TestTheModReportsThroughTheCLI(t *testing.T) {
	m := newModRun(t)
	r := m.report(modHelloJSON)
	if !r.OK || r.Run != m.run.Ref || r.Dir != filepath.Join(m.run.Dir, "mod") || r.Seen != 0 || r.Agent == nil ||
		r.Agent.Name != "phase" || r.Agent.Prompt != decide.PhaseAgentPrompt || r.Agent.Description != decide.PhaseAgentDescription ||
		r.Board == nil || r.Board.Description != decide.BoardCommand || r.Board.Title != decide.BoardTitle {
		t.Fatalf("hello: %+v", r)
	}
	ask, err := m.bridge().Issue(bridge.Command{Kind: bridge.Ask, Instance: "i", Question: "May P3 commit unsigned?"})
	if err != nil {
		t.Fatal(err)
	}
	spawn, _ := m.bridge().Issue(bridge.Command{Kind: bridge.Spawn, Instance: "i", Phase: "P3", Prompt: "Begin P3."})
	for i, c := range []struct{ report, kind string }{
		{`{"seq":1,"kind":"ack","session":"sess","command":1,"ok":true,"result":"asked"}`, "mod_ack"},
		{`{"seq":2,"kind":"ack","session":"sess","command":2,"ok":true,"agent":"a3"}`, "mod_ack"},
		{`{"seq":3,"kind":"turn","session":"sess","agent":"a3","reason":"aborted","ms":900}`, "mod_turn"},
		{`{"seq":4,"kind":"compacting","session":"sess","trigger":"auto"}`, "mod_compacting"},
		{`{"seq":5,"kind":"compacted","session":"sess","trigger":"auto"}`, "mod_compacted"},
		{`{"seq":6,"kind":"dropped","session":"sess","agent":"a3"}`, "mod_dropped"},
		{`{"seq":7,"kind":"answered","session":"sess","command":1,"answer":"Go ahead"}`, "mod_answered"},
	} {
		if r := m.report(c.report); !r.OK || r.Ignored != "" {
			t.Errorf("%s: %+v", c.report, r)
		}
		if n := strings.Count(m.events(), `"kind":"`+c.kind+`"`); c.kind == "mod_ack" && n != i+1 || c.kind != "mod_ack" && n != 1 {
			t.Errorf("%s: %d %s events", c.report, n, c.kind)
		}
	}
	if r := m.report(`{"seq":6,"kind":"dropped","session":"sess","agent":"a3"}`); !r.OK || r.Ignored != "recorded already" {
		t.Errorf("a repeat: %+v", r)
	}
	taken, _ := m.bridge().Take()
	if len(taken) != 7 || taken[1].Agent != "a3" || taken[2].Reason != "aborted" || taken[6].Answer != "Go ahead" || taken[0].Instance != "i" {
		t.Errorf("recorded %+v", taken)
	}
	agents, _ := m.bridge().Agents()
	if len(agents) != 1 || agents[0].ID != "a3" || agents[0].Phase != "P3" || agents[0].Drop != decide.ReportKept("P3") {
		t.Errorf("agents %+v", agents)
	}
	if rec, _ := m.bridge().Load(); len(rec.Open) != 0 || rec.Hello.Instance != "i" || rec.Hello.Session != "sess" || rec.Hello.Version != "0.8.0" {
		t.Errorf("record %+v (ask %d, spawn %d)", rec, ask.ID, spawn.ID)
	}
	// Reloaded, the mod says hello again and goes on after its last report.
	if r := m.report(modHelloJSON); !r.OK || r.Seen != 7 {
		t.Errorf("hello again: %+v", r)
	}
}

// A report that cannot be read is let go (the mod drops it), and logged; one before any hello, or from a
// mod another claude ran, sends the mod back to say hello.
func TestABadModReportIsLetGo(t *testing.T) {
	m := newModRun(t)
	for _, c := range []struct{ report, why string }{
		{`not json`, "not JSON"},
		{`{"seq":1,"kind":"dance"}`, `no such report: "dance"`},
		{`{"kind":"turn","agent":"a1"}`, "not numbered"},
		{`{"seq":1,"kind":"turn"}`, "turn names no agent"},
		{`{"seq":1,"kind":"ack"}`, "ack names no command"},
		{`{"seq":1,"kind":"dropped","text":"` + strings.Repeat("x", maxReport) + `"}`, "too long"},
	} {
		r := m.report(c.report)
		if !r.OK || !strings.HasPrefix(r.Ignored, c.why) {
			t.Errorf("%.40s: %+v", c.report, r)
		}
	}
	if n := strings.Count(m.events(), `"kind":"mod_report_bad"`); n != 6 {
		t.Errorf("%d bad reports logged:\n%s", n, m.events())
	}
	if r := m.report(`{"seq":1,"kind":"turn","agent":"a1"}`); r.OK || !r.Stale {
		t.Errorf("a report before any hello: %+v", r)
	}
	m.report(modHelloJSON)
	m.env["BATON_INSTANCE"] = "j" // the host restarted, and its old claude's mod reports
	if r := m.report(`{"seq":1,"kind":"turn","session":"sess","agent":"a1"}`); r.OK || !r.Stale {
		t.Errorf("another claude's report: %+v", r)
	}
}

// The mod does nothing where baton does not want it: in a session baton does not host, with no run yet or in a claude whose host does not own the run (it tries again), and as a mod of
// another baton, which is recorded as refused.
func TestTheModIsDormantUnlessBatonWantsIt(t *testing.T) {
	m := newModRun(t)
	for _, c := range []struct {
		name  string
		env   map[string]string
		retry bool
		why   string
	}{
		{"not hosted", map[string]string{"BATON_DIR": m.dir, "BATON_INSTANCE": "i"}, false, "not hosted"},
		{"no run yet", map[string]string{"BATON_HOST": "1", "BATON_DIR": m.dir, "BATON_INSTANCE": "k"}, true, "no run yet"},
	} {
		saved := m.env
		m.env = c.env
		r := m.report(`{"kind":"hello","session":"other","protocol":1}`)
		m.env = saved
		if r.OK || r.Retry != c.retry || !strings.Contains(r.Why, c.why) {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	if _, err := os.Stat(filepath.Join(m.run.Dir, "mod")); err == nil {
		t.Error("a dormant mod's report wrote the bridge")
	}
	m.run.Update(func(st *state.State) error { st.Owner = &state.Owner{Instance: "other"}; return nil })
	if r := m.report(modHelloJSON); r.OK || !r.Retry || !strings.Contains(r.Why, "another baton session") {
		t.Errorf("a mod whose host does not own the run: %+v", r)
	}
	m.run.Update(func(st *state.State) error { st.Owner = &state.Owner{Instance: "i"}; return nil })
	r := m.report(`{"kind":"hello","session":"sess","version":"0.9.0","protocol":2}`)
	if want := decide.ModMismatch("0.9.0", version.Version); r.OK || r.Retry || r.Why != want {
		t.Errorf("another baton's mod: %+v", r)
	}
	if rec, _ := m.bridge().Load(); rec.Hello == nil || rec.Hello.Refused == "" || rec.Hello.Ready("i") {
		t.Errorf("hello %+v", rec.Hello)
	}
	if r := m.report(`{"seq":1,"kind":"turn","session":"sess","agent":"a1"}`); r.OK {
		t.Errorf("a refused mod's report: %+v", r)
	}
}

// Like a hook, `baton mod report` never fails loudly: a panic is a reply that says to try again, and
// exit code 0.
func TestAModReportPanicFailsOpen(t *testing.T) {
	old := modReport
	defer func() { modReport = old }()
	modReport = func(IO) modReply { panic("boom") }
	io, out, _ := testIO(modHelloJSON, nil)
	if code := Main([]string{"mod", "report"}, io); code != 0 {
		t.Fatalf("code %d", code)
	}
	var r modReply
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.OK || !r.Retry || !strings.Contains(r.Why, "boom") {
		t.Errorf("reply %q (%v)", out, err)
	}
}

// The mod's report of a compaction of the lead is a proof of it (state.Reported), counted as it comes: one
// begun moves the /compact baton typed to under way, and one over is kept for the rewake. A compaction a
// hook vetoed proves nothing.
func TestTheModsCompactionReportsCount(t *testing.T) {
	m := newModRun(t)
	m.report(modHelloJSON)
	m.run.Update(func(st *state.State) error {
		st.Run.Compaction.Queue("boundary", m.run.Now())
		st.Run.Compaction.Status, st.Run.Compaction.Typed = state.CompactTyped, m.run.Now()
		return nil
	})
	m.report(`{"seq":1,"kind":"compacting","session":"sess","trigger":"manual","skipped":true}`)
	if st, _ := m.run.Load(); st.Run.Compaction.Status != state.CompactTyped || !st.Run.ModReports.IsZero() {
		t.Errorf("a vetoed compaction counted: %+v", st.Run.Compaction)
	}
	m.report(`{"seq":2,"kind":"compacting","session":"sess","trigger":"manual"}`)
	if st, _ := m.run.Load(); st.Run.Compaction.Status != state.CompactActive || st.Run.ModReports.IsZero() {
		t.Errorf("begun: %+v", st.Run.Compaction)
	}
	m.report(`{"seq":3,"kind":"compacted","session":"sess","trigger":"manual"}`)
	if st, _ := m.run.Load(); st.Run.Compaction.Reported == nil || st.Run.Compaction.Reported.Trigger != "manual" {
		t.Errorf("over: %+v", st.Run.Compaction)
	}
	if n := strings.Count(m.events(), `"kind":"compact_reported"`); n != 2 {
		t.Errorf("%d compact_reported events:\n%s", n, m.events())
	}
}
