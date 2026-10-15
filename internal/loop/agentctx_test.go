package loop

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// reply is an assistant entry of a phase agent's own transcript (a sidechain's, as all its entries are),
// whose request held tokens of context.
func reply(tokens int, at time.Time) string {
	return fmt.Sprintf(`{"isSidechain":true,"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"on"}],"usage":{"input_tokens":3,"cache_read_input_tokens":%d,"cache_creation_input_tokens":0}},"uuid":"r%d","timestamp":%q}`,
		tokens-3, tokens, at.UTC().Format(time.RFC3339Nano))
}

// compacted is a compact boundary in a phase agent's own transcript.
func compacted(at time.Time) string {
	return fmt.Sprintf(`{"isSidechain":true,"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto","postTokens":12000},"uuid":"cb%d","timestamp":%q}`,
		at.Unix(), at.UTC().Format(time.RFC3339Nano))
}

// leadWindow is the lead's context window, as its status line records it.
func (x *crew) leadWindow(size int) {
	x.lead.Update(func(st *state.State) error {
		st.Run.Context = &state.ContextUse{Tokens: 40_000, WindowSize: size, Limit: size, At: x.now}
		return nil
	})
}

// A phase agent's context is read from its own transcript, against the lead's window (an agent compacts
// where the lead does), for its context valve; nothing is read before the lead's status line has said
// what its window is.
func TestAnAgentsContextIsReadFromItsOwnTranscript(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	if st, _ := x.sub("P1").Load(); st.Run.Transcript != x.transcript("P1") {
		t.Fatalf("its transcript: %q", st.Run.Transcript)
	}
	x.says("P1", reply(130_000, x.now))
	x.tick(contextEvery)
	x.leadWindow(0)
	x.tick(contextEvery)
	if st, _ := x.sub("P1").Load(); st.Run.Context != nil {
		t.Fatalf("read with no window: %+v", st.Run.Context)
	}
	x.leadWindow(200_000)
	x.tick(contextEvery)
	st, _ := x.sub("P1").Load()
	if c := st.Run.Context; c == nil || c.Tokens != 130_000 || c.WindowSize != 200_000 || c.Limit != 200_000 || c.UsedPct != 65 {
		t.Fatalf("its context: %+v", c)
	}
	if c := x.state().Run.Context; c.Tokens != 40_000 {
		t.Errorf("the lead's context: %+v", c)
	}
	// Read again only every contextEvery.
	x.says("P1", reply(150_000, x.now))
	x.tick(time.Second)
	if st, _ := x.sub("P1").Load(); st.Run.Context.Tokens != 130_000 {
		t.Errorf("read again at once: %+v", st.Run.Context)
	}
	x.tick(contextEvery)
	if st, _ := x.sub("P1").Load(); st.Run.Context.Tokens != 150_000 {
		t.Errorf("not read again: %+v", st.Run.Context)
	}
}

// A compaction of a phase agent's own is proven by its transcript, and owes it its brief again: an idle
// agent is woken for it once, and given it as it ends that turn. The lead's run is untouched.
func TestAnAgentsCompactionBriefsItAgain(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	x.waiting("P1")
	x.tick(5 * time.Second)
	before := x.state().Run.Compaction
	x.says("P1", reply(240_000, x.now), compacted(x.now))
	x.tick(time.Second)
	if !x.subLogged("P1", "agent_compacted", map[string]any{"agent": x.agent("P1").id, "checkpoint": false}) {
		t.Fatalf("not proven:\n%s", x.subEvents("P1"))
	}
	if !slices.ContainsFunc(x.sent, func(s sent) bool { return s.text == decide.AgentBriefAgain("P1") }) {
		t.Fatalf("not woken for its brief: %+v", x.sent)
	}
	if len(x.resumes) == 0 {
		t.Fatal("not started again")
	}
	if got := x.endTurn("P1"); !strings.Contains(got, "Claude Code compacted the context automatically") {
		t.Fatalf("its brief: %q", got)
	}
	if st, _ := x.sub("P1").Load(); st.Run.Briefing != nil {
		t.Errorf("still owed: %+v", st.Run.Briefing)
	}
	// One the mod could not deliver is sent again, but only a while later: the brief is still owed.
	x.sendErr = "the mod could not send it"
	x.says("P1", compacted(x.now.Add(time.Second)))
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = false; return nil })
	n := len(x.sent)
	briefs := func() int {
		return len(slices.DeleteFunc(slices.Clone(x.sent[n:]), func(s sent) bool { return s.text != decide.AgentBriefAgain("P1") }))
	}
	for range 30 {
		x.tick(time.Second)
	}
	if briefs() != 1 {
		t.Errorf("woken %d times before ResumeNudge: %+v", briefs(), x.sent[n:])
	}
	x.wait(time.Minute)
	if briefs() != 2 {
		t.Errorf("woken %d times by ResumeNudge: %+v", briefs(), x.sent[n:])
	}
	if st := x.state(); st.Run.Compaction != before || st.Run.Briefing != nil || strings.Contains(x.events(), "compact_finished") {
		t.Errorf("the lead's run was touched: %+v", st.Run.Compaction)
	}
}

// A message on its way to a phase agent as it checkpoints starts it again: the agent goes on with its turn,
// and only once that has ended is its phase handed to a fresh agent. Never two agents at once on a phase.
func TestAMessageInFlightHoldsTheCheckpointBack(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	first := x.agent("P1")
	x.modOff = true
	id := x.waiting("P1")
	x.passed("P1", id)
	x.tick(5 * time.Second) // the result's message is issued, and not run yet
	x.sub("P1").Update(func(st *state.State) error { st.Run.TurnOpen = true; return state.SetCheckpoint(st, x.now) })
	x.stops("P1")
	x.tick(time.Second)
	if x.status("P1") != state.WorkerWorking || len(x.agents) != 1 {
		t.Fatalf("set aside with a message open: %s, %+v", x.status("P1"), x.agents)
	}
	x.modOff = false
	x.runMod() // the message starts it again
	if st, _ := x.sub("P1").Load(); !st.Run.TurnOpen || len(x.resumes) != 1 {
		t.Fatalf("not started again: %v", x.resumes)
	}
	for range 3 {
		x.tick(time.Second)
	}
	if x.status("P1") != state.WorkerWorking || len(x.agents) != 1 || slices.Contains(x.stopped, first.id) {
		t.Fatalf("set aside mid-turn: %s, %+v", x.status("P1"), x.agents)
	}
	x.stops("P1")
	for range 4 {
		x.tick(time.Second)
	}
	if fresh := x.agent("P1"); fresh.id == first.id || !slices.Contains(x.stopped, first.id) {
		t.Errorf("no fresh agent after its turn: %+v, stopped %v", x.agents, x.stopped)
	}
}

// A phase agent's checkpoint ends it: once it has stopped, its worker is set aside, its agent stopped and
// forgotten, and a fresh agent takes the phase up in its worktree, whose start ends the checkpoint.
func TestAnAgentsCheckpointStartsAFreshAgent(t *testing.T) {
	x := newCrew(t, 1)
	x.atWork("P1")
	first := x.agent("P1")
	x.sub("P1").Update(func(st *state.State) error { return state.SetCheckpoint(st, x.now) })
	x.tick(time.Second)
	if x.status("P1") != state.WorkerWorking {
		t.Fatalf("ended mid-turn: %+v", x.worker("P1"))
	}
	x.stops("P1")
	for range 4 {
		x.tick(time.Second)
	}
	fresh := x.agent("P1")
	if fresh.id == first.id || fresh.tree != first.tree || !slices.Contains(x.stopped, first.id) || !slices.Equal(x.tracked(), []string{fresh.id}) {
		t.Fatalf("agents %+v, stopped %v, tracked %v", x.agents, x.stopped, x.tracked())
	}
	if !slices.Equal(x.spawns(), []string{"P1", "P1"}) || !x.logged("worker_resumed", map[string]any{"phase": "P1", "why": decide.AgentCheckpointed}) {
		t.Errorf("spawns %v:\n%s", x.spawns(), x.events())
	}
	st, _ := x.sub("P1").Load()
	if st.CheckpointOwed || st.Run.Compaction.Status != state.CompactDone || st.Run.Briefed != fresh.id || x.status("P1") != state.WorkerWorking {
		t.Errorf("after the fresh agent: owed %v, %+v, briefed %q, %s", st.CheckpointOwed, st.Run.Compaction, st.Run.Briefed, x.status("P1"))
	}
	if !x.subLogged("P1", "checkpoint_over", map[string]any{"agent": fresh.id}) || !x.subLogged("P1", "brief", map[string]any{"type": "worker"}) {
		t.Errorf("events:\n%s", x.subEvents("P1"))
	}
}
