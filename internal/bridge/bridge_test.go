package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type rig struct {
	t      *testing.T
	b      *Bridge
	now    time.Time
	events []string
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	r.b = Open(t.TempDir(), func() time.Time { return r.now }, func(kind string, fields map[string]any) {
		b, _ := json.Marshal(fields)
		r.events = append(r.events, kind+" "+string(b))
	})
	r.b.Drop = func(phase string) string { return phase + "'s report went to baton" }
	return r
}

func (r *rig) issue(c Command) Command {
	r.t.Helper()
	if c.Instance == "" {
		c.Instance = "inst"
	}
	got, err := r.b.Issue(c)
	if err != nil {
		r.t.Fatalf("issue %+v: %v", c, err)
	}
	return got
}

func (r *rig) hello(instance string) int {
	r.t.Helper()
	seen, err := r.b.SayHello(HelloInfo{Instance: instance, Version: "0.8.2", Protocol: Protocol})
	if err != nil {
		r.t.Fatal(err)
	}
	return seen
}

func (r *rig) record(rep Report) bool {
	r.t.Helper()
	if rep.Instance == "" {
		rep.Instance = "inst"
	}
	dup, err := r.b.Record(rep)
	if err != nil {
		r.t.Fatalf("record %+v: %v", rep, err)
	}
	return dup
}

func (r *rig) load() Record {
	r.t.Helper()
	rec, err := r.b.Load()
	if err != nil {
		r.t.Fatal(err)
	}
	return rec
}

func (r *rig) files() []string {
	entries, _ := os.ReadDir(r.b.Out())
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func (r *rig) agents() []Agent {
	r.t.Helper()
	a, err := r.b.Agents()
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

func ids(cmds []Command) []int {
	var out []int
	for _, c := range cmds {
		out = append(out, c.ID)
	}
	return out
}

// Commands are numbered one up from the last, each file is written whole (no temporary file is left for
// the mod to find), and the mod reads them in order.
func TestCommandsAreNumberedAndWrittenWhole(t *testing.T) {
	r := newRig(t)
	r.issue(Command{Kind: Ask, Question: "May P3 commit unsigned?", Options: []string{"Go ahead", "Wait"}})
	r.issue(Command{Kind: Ask, Question: "Second?"})
	for range 10 {
		r.issue(Command{Kind: Ask, Question: "More?"})
	}
	if got := r.files(); len(got) != 12 || got[0] != "000001.json" || got[11] != "000012.json" {
		t.Fatalf("files %q", got)
	}
	cmds, err := r.b.Commands()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(cmds), []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}) {
		t.Errorf("read in the order %v", ids(cmds))
	}
	if c := cmds[0]; c.Instance != "inst" || c.Question != "May P3 commit unsigned?" || len(c.Options) != 2 || !c.At.Equal(r.now) {
		t.Errorf("command %+v", c)
	}
	if rec := r.load(); rec.Seq != 12 || len(rec.Open) != 12 {
		t.Errorf("record %+v", rec)
	}
	// A temporary file beside them is no command.
	os.WriteFile(filepath.Join(r.b.Out(), ".000013.json.tmp-1"), []byte(`{"id":13`), 0o644)
	if cmds, _ := r.b.Commands(); len(cmds) != 12 {
		t.Errorf("%d commands with a temporary file there", len(cmds))
	}
	if _, err := r.b.Issue(Command{Kind: "dance", Instance: "inst"}); err == nil {
		t.Error("an unknown command was issued")
	}
}

// One spawn at a time, so that a worktree Claude Code asks for belongs to exactly one phase; issued again
// for its phase, a spawn is the same one; a phase with an agent gets none; and once acked, its agent is
// one of baton's before anything else is recorded.
func TestOneSpawnAtATime(t *testing.T) {
	r := newRig(t)
	r.hello("inst")
	p1 := r.issue(Command{Kind: Spawn, Phase: "P1", Prompt: "Begin P1.", Description: "P1 — one", Name: "P1"})
	if cmds, _ := r.b.Commands(); len(cmds) != 1 || cmds[0].Drop != "P1's report went to baton" || cmds[0].Name != "P1" || cmds[0].Prompt != "Begin P1." {
		t.Errorf("spawn %+v", cmds)
	}
	if again := r.issue(Command{Kind: Spawn, Phase: "P1", Prompt: "Begin P1."}); again.ID != p1.ID || len(r.files()) != 1 {
		t.Errorf("spawned again: %+v, files %q", again, r.files())
	}
	if _, err := r.b.Issue(Command{Kind: Spawn, Phase: "P2", Instance: "inst"}); !errors.Is(err, ErrSpawnOpen) {
		t.Errorf("a second spawn while one is open: %v", err)
	}
	if o, ok := r.load().PendingSpawn("inst"); !ok || o.Phase != "P1" || o.ID != p1.ID {
		t.Errorf("pending spawn %+v %v", o, ok)
	}
	r.record(Report{Seq: 1, Kind: Ack, Command: p1.ID, OK: true, Agent: "a1"})
	if got := r.agents(); len(got) != 1 || got[0] != (Agent{ID: "a1", Phase: "P1", Drop: "P1's report went to baton"}) {
		t.Errorf("agents %+v", got)
	}
	if _, ok := r.load().PendingSpawn("inst"); ok || len(r.files()) != 0 {
		t.Errorf("the spawn is still open: %q", r.files())
	}
	if _, err := r.b.Issue(Command{Kind: Spawn, Phase: "P1", Instance: "inst"}); !errors.Is(err, ErrHasAgent) {
		t.Errorf("a spawn for a phase with an agent: %v", err)
	}
	p2 := r.issue(Command{Kind: Spawn, Phase: "P2"})
	r.record(Report{Seq: 2, Kind: Ack, Command: p2.ID, Error: "the agent type is not registered"})
	if got := r.agents(); len(got) != 1 {
		t.Errorf("a failed spawn's agent was tracked: %+v", got)
	}
	r.b.Forget("a1")
	if c := r.issue(Command{Kind: Spawn, Phase: "P1"}); c.ID != p2.ID+1 {
		t.Errorf("respawn %+v", c)
	}
	if ev := strings.Join(r.events, "\n"); !strings.Contains(ev, `mod_ack {"agent":"a1","command":1,"command_kind":"spawn","ok":true,"phase":"P1","seq":1}`) ||
		!strings.Contains(ev, `mod_agent {"agent":"a1","phase":"P1"}`) || !strings.Contains(ev, `mod_forgot {"agent":"a1"}`) {
		t.Errorf("events:\n%s", ev)
	}
}

// Only baton's agents are messaged or stopped: a message would resume an agent baton forgot.
func TestOnlyBatonsAgentsAreSentTo(t *testing.T) {
	r := newRig(t)
	for _, kind := range []string{Send, Stop} {
		if _, err := r.b.Issue(Command{Kind: kind, Agent: "a1", Text: "Go on.", Instance: "inst"}); !errors.Is(err, ErrUnknownAgent) {
			t.Errorf("%s to an agent baton does not have: %v", kind, err)
		}
	}
	r.b.Track("a1", "P1")
	r.b.Track("a1", "P1")
	if got := r.agents(); len(got) != 1 {
		t.Errorf("tracked twice: %+v", got)
	}
	if c := r.issue(Command{Kind: Send, Agent: "a1", Text: "Go on.", Summary: "baton"}); c.Agent != "a1" || c.Text != "Go on." {
		t.Errorf("sent %+v", c)
	}
	r.b.Forget("a1")
	if _, err := r.b.Issue(Command{Kind: Send, Agent: "a1", Text: "Go on.", Instance: "inst"}); !errors.Is(err, ErrUnknownAgent) {
		t.Errorf("sent to a forgotten agent: %v", err)
	}
	if data, _ := os.ReadFile(r.b.AgentsPath()); !strings.Contains(string(data), `"agents": []`) {
		t.Errorf("agents.json %s", data)
	}
}

// A report is recorded once: the mod that said hello last numbers them, a number recorded already is
// left out, and a report from any other mod is stale. A mod reloaded in the same claude goes on from its
// count; one in another claude starts over.
func TestReportsAreRecordedOnce(t *testing.T) {
	r := newRig(t)
	if _, err := r.b.Record(Report{Seq: 1, Kind: Turn, Agent: "a1", Instance: "inst"}); !errors.Is(err, ErrStale) {
		t.Errorf("a report before any hello: %v", err)
	}
	if seen := r.hello("inst"); seen != 0 {
		t.Errorf("seen %d", seen)
	}
	turn := Report{Seq: 1, Kind: Turn, Agent: "a1", Reason: "answer", Text: "DONE", Ms: 1200}
	if r.record(turn) {
		t.Error("the first report was a repeat")
	}
	if !r.record(turn) {
		t.Error("a repeat was recorded")
	}
	r.record(Report{Seq: 2, Kind: Compacting, Trigger: "auto"})
	r.record(Report{Seq: 3, Kind: Compacted, Trigger: "auto"})
	if _, err := r.b.Record(Report{Seq: 4, Kind: Turn, Agent: "a1", Instance: "inst-gone"}); !errors.Is(err, ErrStale) {
		t.Errorf("another claude's report: %v", err)
	}
	if seen := r.hello("inst"); seen != 3 {
		t.Errorf("a reloaded mod goes on after %d", seen)
	}
	if !r.record(Report{Seq: 3, Kind: Dropped, Agent: "a1"}) {
		t.Error("a reloaded mod's repeat was recorded")
	}
	taken, err := r.b.Take()
	if err != nil || len(taken) != 3 || taken[0].Kind != Turn || taken[0].Text != "DONE" || !taken[0].At.Equal(r.now) || taken[2].Kind != Compacted {
		t.Fatalf("took %+v (%v)", taken, err)
	}
	if again, _ := r.b.Take(); len(again) != 0 {
		t.Errorf("took again %+v", again)
	}
	if seen := r.hello("inst-2"); seen != 0 {
		t.Errorf("a mod in another claude goes on after %d", seen)
	}
	if r.record(Report{Seq: 1, Kind: Dropped, Agent: "a1", Instance: "inst-2"}) {
		t.Error("the new mod's first report was taken for a repeat")
	}
	if _, err := r.b.Record(Report{Seq: 4, Kind: Turn, Agent: "a1", Instance: "inst"}); !errors.Is(err, ErrStale) {
		t.Errorf("the old mod's report: %v", err)
	}
	// A refused mod reports nothing.
	r.b.SayHello(HelloInfo{Instance: "inst-3", Protocol: Protocol + 1, Refused: "another baton's"})
	if _, err := r.b.Record(Report{Seq: 1, Kind: Turn, Agent: "a1", Instance: "inst-3"}); !errors.Is(err, ErrStale) {
		t.Errorf("a refused mod's report: %v", err)
	}
	if ev := strings.Join(r.events, "\n"); strings.Count(ev, "mod_turn ") != 1 || !strings.Contains(ev, `mod_turn {"agent":"a1","ms":1200,"reason":"answer","seq":1}`) ||
		!strings.Contains(ev, `mod_hello {"mod_instance":"inst-3","protocol":3,"refused":"another baton's","version":""}`) {
		t.Errorf("events:\n%s", ev)
	}
}

// An ack ends its command whatever it says: its file goes, so the mod reports it no more. An ack the
// record has no command for (voided, or acked already) is recorded all the same.
func TestAnAckEndsItsCommand(t *testing.T) {
	r := newRig(t)
	r.hello("inst")
	r.b.Track("a1", "P1")
	send := r.issue(Command{Kind: Send, Agent: "a1", Text: "The gate passed."})
	ask := r.issue(Command{Kind: Ask, Question: "May it?"})
	r.record(Report{Seq: 1, Kind: Ack, Command: send.ID, OK: true, Result: "Message queued for delivery to a1 at its next tool round."})
	r.record(Report{Seq: 2, Kind: Ack, Command: ask.ID, Interrupted: true})
	r.record(Report{Seq: 3, Kind: Ack, Command: send.ID, OK: true})
	if rec := r.load(); len(rec.Open) != 0 || len(r.files()) != 0 || len(rec.Inbox) != 3 {
		t.Errorf("open %+v, files %q, inbox %d", rec.Open, r.files(), len(rec.Inbox))
	}
	if ev := strings.Join(r.events, "\n"); !strings.Contains(ev, `"interrupted":true`) || !strings.Contains(ev, `mod_ack {"command":1,"ok":true,"seq":3,"unknown":true}`) {
		t.Errorf("events:\n%s", ev)
	}
}

// A host claims the bridge as it starts: the commands an earlier host issued are void, since the claude
// they were for is gone.
func TestAnotherHostsCommandsAreVoid(t *testing.T) {
	r := newRig(t)
	r.issue(Command{Kind: Ask, Question: "Old?", Instance: "inst-old"})
	r.issue(Command{Kind: Spawn, Phase: "P1", Instance: "inst-old"})
	mine := r.issue(Command{Kind: Ask, Question: "New?", Instance: "inst-new"})
	if err := r.b.Claim("inst-new"); err != nil {
		t.Fatal(err)
	}
	rec := r.load()
	if len(rec.Open) != 1 || rec.Open[0].ID != mine.ID || !slices.Equal(r.files(), []string{"000003.json"}) {
		t.Errorf("open %+v, files %q", rec.Open, r.files())
	}
	if _, ok := rec.PendingSpawn("inst-new"); ok {
		t.Error("the old host's spawn is still pending")
	}
	if c := r.issue(Command{Kind: Spawn, Phase: "P1", Instance: "inst-new"}); c.ID != 4 {
		t.Errorf("spawned %+v", c)
	}
	if ev := strings.Join(r.events, "\n"); !strings.Contains(ev, `mod_voided {"ids":[1,2]}`) {
		t.Errorf("events:\n%s", ev)
	}
}

// Overdue lists the host's own commands that waited for an ack longer than it may.
func TestOverdueCommands(t *testing.T) {
	r := newRig(t)
	old := r.issue(Command{Kind: Ask, Question: "Old?"})
	r.issue(Command{Kind: Ask, Question: "Other's?", Instance: "inst-2"})
	r.now = r.now.Add(20 * time.Second)
	r.issue(Command{Kind: Ask, Question: "New?"})
	late := r.load().Overdue("inst", r.now, 15*time.Second)
	if len(late) != 1 || late[0].ID != old.ID {
		t.Errorf("overdue %+v", late)
	}
}

// The inbox keeps the newest reports the host has not taken, and counts those it let go.
func TestTheInboxIsBounded(t *testing.T) {
	r := newRig(t)
	r.hello("inst")
	for i := 1; i <= MaxInbox+5; i++ {
		r.record(Report{Seq: i, Kind: Dropped, Agent: "a1"})
	}
	rec := r.load()
	if len(rec.Inbox) != MaxInbox || rec.Lost != 5 || rec.Inbox[0].Seq != 6 {
		t.Errorf("inbox %d from %d, lost %d", len(rec.Inbox), rec.Inbox[0].Seq, rec.Lost)
	}
}

// reader does what baton's mod does with the bridge (plugin/hooks/baton.js), so the protocol is tested
// from this side: it runs the commands of its host instance after the last it ran, in order, and acks
// each through the report it keeps until it is recorded. store is what the mod keeps in Claude Code's
// store, which a reload keeps.
type reader struct {
	b        *Bridge
	instance string
	store    *readerStore
	ran      []int // what this reader ran
	down     bool  // its reports cannot reach the host
}

type readerStore struct {
	last, running, seq int
	outbox             []Report
}

// load is the mod loaded, or loaded again: a command it was running when it went is acked as interrupted.
func load(b *Bridge, instance string, st *readerStore) *reader {
	x := &reader{b: b, instance: instance, store: st}
	if st.running > st.last {
		x.report(Report{Kind: Ack, Command: st.running, Interrupted: true})
		st.last = st.running
	}
	return x
}

func (x *reader) report(rep Report) {
	x.store.seq++
	rep.Seq, rep.Instance = x.store.seq, x.instance
	x.store.outbox = append(x.store.outbox, rep)
}

func (x *reader) flush() {
	for len(x.store.outbox) > 0 && !x.down {
		if _, err := x.b.Record(x.store.outbox[0]); err != nil {
			return
		}
		x.store.outbox = x.store.outbox[1:]
	}
}

func (x *reader) poll(t *testing.T) {
	x.flush()
	cmds, err := x.b.Commands()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if c.Instance != x.instance || c.ID <= x.store.last {
			continue
		}
		x.store.running = c.ID
		x.ran = append(x.ran, c.ID)
		x.store.last, x.store.running = c.ID, 0
		x.report(Report{Kind: Ack, Command: c.ID, OK: true})
	}
	x.flush()
}

// The protocol end to end: each command runs once, in order, also when an ack cannot reach the host
// (the file stays, and the ack is reported again, not the command run again), when the mod is reloaded
// (it goes on after the last command it ran), and when it was reloaded while it ran one (that one is
// acked as interrupted, never run twice).
func TestEachCommandRunsOnce(t *testing.T) {
	r := newRig(t)
	st := &readerStore{}
	x := load(r.b, "inst", st)
	r.hello("inst")
	r.issue(Command{Kind: Ask, Question: "1?"})
	r.issue(Command{Kind: Ask, Question: "2?"})
	r.issue(Command{Kind: Ask, Question: "Another host's?", Instance: "inst-2"})
	x.poll(t)
	if !slices.Equal(x.ran, []int{1, 2}) || len(r.files()) != 1 {
		t.Fatalf("ran %v, files left %q", x.ran, r.files())
	}

	// The host does not hear the ack: the command stays, and the next poll reports the ack again.
	x.down = true
	r.issue(Command{Kind: Ask, Question: "4?"})
	x.poll(t)
	if !slices.Contains(r.files(), "000004.json") {
		t.Fatalf("an unrecorded ack ended its command: %q", r.files())
	}
	x.down = false
	x.poll(t)
	if !slices.Equal(x.ran, []int{1, 2, 4}) || slices.Contains(r.files(), "000004.json") {
		t.Fatalf("ran %v, files %q", x.ran, r.files())
	}

	// Reloaded: it goes on after the last it ran.
	r.issue(Command{Kind: Ask, Question: "5?"})
	x = load(r.b, "inst", st)
	r.hello("inst")
	x.poll(t)
	if !slices.Equal(x.ran, []int{5}) {
		t.Fatalf("the reloaded mod ran %v", x.ran)
	}

	// Reloaded while it ran 6: 6 is acked as interrupted, not run again; 7 runs.
	r.issue(Command{Kind: Ask, Question: "6?"})
	r.issue(Command{Kind: Ask, Question: "7?"})
	st.running = 6
	x = load(r.b, "inst", st)
	x.poll(t)
	if !slices.Equal(x.ran, []int{7}) || slices.ContainsFunc(r.files(), func(n string) bool { return n == "000006.json" || n == "000007.json" }) {
		t.Fatalf("ran %v, files %q", x.ran, r.files())
	}
	taken, _ := r.b.Take()
	var interrupted []int
	for _, rep := range taken {
		if rep.Interrupted {
			interrupted = append(interrupted, rep.Command)
		}
	}
	if !slices.Equal(interrupted, []int{6}) {
		t.Errorf("interrupted %v", interrupted)
	}

	// A mod in another claude (the host restarted) never runs the commands the old host left.
	r.issue(Command{Kind: Ask, Question: "8?"})
	y := load(r.b, "inst-2", &readerStore{})
	r.hello("inst-2")
	y.poll(t)
	if !slices.Equal(y.ran, []int{3}) {
		t.Errorf("the other claude's mod ran %v", y.ran)
	}
}

// A command the host takes back is gone: its file, and its place in the record, so a spawn voided leaves
// no spawn open, and its ack, if the mod ran it all the same, ends nothing. Only the host's own commands
// are its to take back.
func TestAVoidedCommandIsGone(t *testing.T) {
	r := newRig(t)
	spawn := r.issue(Command{Kind: Spawn, Phase: "P3", Prompt: "Begin P3."})
	if ok, err := r.b.Void("other", spawn.ID); ok || err != nil {
		t.Errorf("another host voided it: %v %v", ok, err)
	}
	if ok, err := r.b.Void("inst", spawn.ID); !ok || err != nil {
		t.Fatalf("void: %v %v", ok, err)
	}
	rec, _ := r.b.Load()
	if _, open := rec.PendingSpawn("inst"); open || len(rec.Open) != 0 {
		t.Errorf("still open: %+v", rec.Open)
	}
	if _, err := os.Stat(r.b.CommandPath(spawn.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("its file: %v", err)
	}
	if ok, _ := r.b.Void("inst", spawn.ID); ok {
		t.Error("voided twice")
	}
	r.hello("inst")
	r.record(Report{Seq: 1, Kind: Ack, Command: spawn.ID, OK: true, Agent: "a3"})
	if agents, _ := r.b.Agents(); len(agents) != 0 {
		t.Errorf("the voided spawn's agent was tracked: %+v", agents)
	}
	if !slices.ContainsFunc(r.events, func(e string) bool { return strings.HasPrefix(e, "mod_voided ") }) {
		t.Errorf("events %v", r.events)
	}
}

// The lead's own commands: a compaction names the one it is for, and a line its text; neither waits for an
// agent. Accept hands back the command an ack ended, as it was issued, so its epoch goes with it; one
// voided first ends nothing. The mod's word that the compaction it ran is over carries the command too.
func TestTheLeadsCompactionAndLines(t *testing.T) {
	r := newRig(t)
	r.hello("inst")
	if _, err := r.b.Issue(Command{Kind: Compact, Instance: "inst"}); err == nil {
		t.Error("a compact naming no compaction was issued")
	}
	compact := r.issue(Command{Kind: Compact, Epoch: 4})
	line := r.issue(Command{Kind: Submit, Text: "[baton] The results of j2 are in."})
	var c Command
	if data, err := os.ReadFile(r.b.CommandPath(compact.ID)); err != nil || json.Unmarshal(data, &c) != nil || c.Epoch != 4 || c.Kind != Compact {
		t.Fatalf("the compact as written: %s (%v)", data, err)
	}
	if rec := r.load(); len(rec.Open) != 2 || rec.Open[0].Epoch != 4 || rec.Open[1].Kind != Submit {
		t.Errorf("open %+v", rec.Open)
	}
	a, err := r.b.Accept(Report{Seq: 1, Kind: Ack, Instance: "inst", Command: compact.ID, OK: true, Result: "compacting"})
	if err != nil || a.Dup || a.Acked == nil || a.Acked.Kind != Compact || a.Acked.Epoch != 4 {
		t.Fatalf("the compact's ack: %+v %v", a, err)
	}
	if again, _ := r.b.Accept(Report{Seq: 1, Kind: Ack, Instance: "inst", Command: compact.ID, OK: true}); !again.Dup || again.Acked != nil {
		t.Errorf("a repeat: %+v", again)
	}
	if ok, _ := r.b.Void("inst", line.ID); !ok {
		t.Fatal("the line not voided")
	}
	if a, _ := r.b.Accept(Report{Seq: 2, Kind: Ack, Instance: "inst", Command: line.ID, OK: true}); a.Acked != nil {
		t.Errorf("a voided line's ack ended %+v", a.Acked)
	}
	r.record(Report{Seq: 3, Kind: Compacted, Command: compact.ID, Epoch: 4})
	ev := strings.Join(r.events, "\n")
	for _, want := range []string{
		`mod_command {"command":"compact","epoch":4,"id":1}`,
		`mod_command {"command":"submit","id":2}`,
		`mod_ack {"command":1,"command_kind":"compact","epoch":4,"ok":true,"result":"compacting","seq":1}`,
		`mod_ack {"command":2,"ok":true,"seq":2,"unknown":true}`,
		`mod_compacted {"command":1,"epoch":4,"seq":3}`,
	} {
		if !strings.Contains(ev, want) {
			t.Errorf("no %s in\n%s", want, ev)
		}
	}
}
