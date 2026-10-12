package loop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/hooks"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// crew is a host running phase agents: the run sess-1, on the branch main of a git project, whose plan has
// P0 (the lead's), P1 and P2, which can start at once, and P3, after P1. Its work on the workers runs in
// place. The lead's claude is a fake, with baton's mod in it (runMod), which runs the host's commands as
// the real one does: for a spawn, Claude Code's hooks fire, through baton's real handlers.
type crew struct {
	t        *testing.T
	nowMu    sync.Mutex // the clock is read off the lead's tick too
	now      time.Time
	repo     string
	home     string
	proj     *state.Project
	pl       plan.Plan
	lead     *state.Store
	c        *Coordinator
	leadIdle bool            // the lead's hooks do not run
	machine  machine.Reading // what the machine reads (roomy, unless the test says otherwise)

	// The mod: what it ran, the agents it started, the ones it stopped, the messages it sent and what an
	// idle agent that a message started again was given (its SubagentStart's output), and how it fails,
	// if it does.
	ran      map[int]bool
	agents   []started
	stopped  []string
	sent     []sent
	resumes  []string
	modOff   bool   // it runs nothing: no spawn is acked
	ackless  bool   // it runs a spawn, whose agent starts, but its ack never comes
	unplaced bool   // a spawn's agent starts without its worktree hook (somewhere else)
	spawnErr string // the spawn's Agent call fails with this
	sendErr  string // a message's SendMessage call fails with this
}

// sent is a message the crew's mod sent: to whom, and what it said.
type sent struct {
	agent, text string
}

// started is an agent the crew's mod started: its phase, its id, and where its worktree hook put it.
type started struct {
	phase, id, tree string
}

// roomy is a machine with room for workers: 14 CPUs, 48 GiB, little load, and memory pressure normal.
var roomy = machine.Reading{CPUs: 14, MemTotal: 48 << 30, MemAvailablePct: 45, Pressure: machine.PressureNormal, Load1: 3.1, Known: true}

func newCrew(t *testing.T, max int) *crew {
	return newCrewOn(t, max, plan.Plan{Version: 1, Title: "Demo", File: "/plans/demo.md", Phases: []plan.Phase{
		{ID: "P0", Title: "zero", After: []string{}}, {ID: "P1", Title: "one", After: []string{}},
		{ID: "P2", Title: "two", After: []string{}}, {ID: "P3", Title: "three", After: []string{"P1"}}}})
}

// newCrewOn is a crew whose run has the plan pl.
func newCrewOn(t *testing.T, max int, pl plan.Plan) *crew {
	gittest.Isolate(t)
	x := &crew{t: t, now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), repo: t.TempDir(), home: t.TempDir(), machine: roomy, pl: pl, ran: map[int]bool{}}
	gittest.Git(t, x.repo, "init", "-q", "-b", "main")
	gittest.Write(t, x.repo, "a.txt", "a\n")
	gittest.Commit(t, x.repo, "first")
	var err error
	if x.proj, err = state.OpenProject(filepath.Join(x.repo, ".baton"), x.clock); err != nil {
		t.Fatal(err)
	}
	state.ExcludeFromGit(x.repo)
	x.c = x.coordinator("inst", max)
	return x
}

// coordinator makes the host instance's coordinator, with its lead bound to the run, and baton's mod
// loaded in its claude.
func (x *crew) coordinator(instance string, max int) *Coordinator {
	t := x.t
	lead, err := x.proj.Bind(state.Binding{Session: "sess-1", Instance: instance})
	if err != nil {
		t.Fatal(err)
	}
	if x.lead == nil {
		lead.WritePlan(x.pl)
		lead.Update(func(st *state.State) error {
			*st = state.Reattach(*st, x.pl, x.now, state.Origin{})
			st.RunBranch = "main"
			return nil
		})
	}
	x.lead = lead
	helloFrom(t, lead) // baton's mod is loaded in the lead's claude: workers may start
	c := &Coordinator{Lead: &Loop{Notify: &bomb{}, Project: "demo", Timing: DefaultTiming}, Project: x.proj, Home: x.home}
	t.Cleanup(c.snapshotsTaken) // before the test's directories are removed (cleanups run last first)
	c.Fleet = &Fleet{Max: max, UsageMax: 80, Go: func(fn func()) { fn() }, Machine: func() machine.Reading { return x.machine }}
	return c
}

// snapshotsTaken waits for the snapshots the lead's and the agents' loops take off the tick, when a run
// halts, so that none writes into a test's directories once the test is over.
func (c *Coordinator) snapshotsTaken() {
	c.mu.Lock()
	loops := []*Loop{c.Lead}
	for _, w := range c.workers {
		loops = append(loops, w.agent.loop)
	}
	c.mu.Unlock()
	for _, l := range loops {
		l.saving.Wait()
	}
}

// tick advances the clock by d, then ticks the lead, and lets the mod run what the host gave it. The lead's
// session is at work (its hooks run), unless the test says otherwise.
func (x *crew) tick(d time.Duration) {
	x.advance(d)
	if !x.leadIdle {
		x.lead.Update(func(st *state.State) error { st.Run.LastActivity = x.now; return nil })
	}
	x.c.Tick(host.View{Now: x.now, Store: x.lead, Owner: true, LastOutput: x.now.Add(-time.Hour), LastHumanKey: x.now.Add(-time.Hour)}, &typed{})
	x.runMod()
}

// bridge is the run's bridge, as the mod reads and reports to it.
func (x *crew) bridge() *bridge.Bridge { return bridge.Open(x.lead.Dir, x.clock, nil) }

// runMod runs the commands the host left for the lead's claude, in order, each once, as baton's mod does,
// and acks each.
func (x *crew) runMod() {
	if x.modOff {
		return
	}
	cmds, err := x.bridge().Commands()
	if err != nil {
		x.t.Fatal(err)
	}
	for _, c := range cmds {
		if c.Instance != x.lead.Instance || x.ran[c.ID] {
			continue
		}
		x.ran[c.ID] = true
		switch c.Kind {
		case bridge.Spawn:
			agent, why := x.spawn(c)
			if !x.ackless {
				x.report(bridge.Report{Kind: bridge.Ack, Command: c.ID, OK: why == "", Agent: agent, Error: why})
			}
		case bridge.Stop:
			x.stopped = append(x.stopped, c.Agent)
			x.report(bridge.Report{Kind: bridge.Ack, Command: c.ID, OK: true})
		case bridge.Send:
			x.sent = append(x.sent, sent{agent: c.Agent, text: c.Text})
			if x.sendErr != "" {
				x.report(bridge.Report{Kind: bridge.Ack, Command: c.ID, Error: x.sendErr})
				continue
			}
			x.report(bridge.Report{Kind: bridge.Ack, Command: c.ID, OK: true})
			x.resume(c.Agent)
		default:
			x.report(bridge.Report{Kind: bridge.Ack, Command: c.ID, OK: true})
		}
	}
}

// spawn starts the agent command c asks for, as Claude Code does when the mod calls the Agent tool: the
// call's PreToolUse, the worktree hook (the agent's working directory is what it prints), the agent's
// SubagentStart, and the call's result. It returns the agent, or why the call failed.
func (x *crew) spawn(c bridge.Command) (agent, why string) {
	if x.spawnErr != "" {
		return "", x.spawnErr
	}
	agent = fmt.Sprintf("a%s%d", strings.ToLower(c.Phase), len(x.agents)+1)
	if x.unplaced {
		x.agents = append(x.agents, started{phase: c.Phase, id: agent})
		return agent, ""
	}
	prompt := fmt.Sprintf("p-mod-%d", c.ID)
	call := map[string]any{"tool_name": "Agent", "tool_use_id": fmt.Sprintf("toolu_plugin_%d", c.ID), "prompt_id": prompt,
		"tool_input": map[string]any{"subagent_type": state.PhaseAgent, "name": c.Name, "description": c.Description, "prompt": c.Prompt}}
	x.hook("PreToolUse", call)
	code, tree := x.hook("WorktreeCreate", map[string]any{"name": "agent-" + agent, "prompt_id": prompt})
	if code != 0 || tree == "" {
		x.hook("PostToolUseFailure", call)
		return "", "the worktree hook failed"
	}
	x.agents = append(x.agents, started{phase: c.Phase, id: agent, tree: tree})
	x.hook("SubagentStart", map[string]any{"agent_id": agent, "agent_type": state.PhaseAgent, "cwd": tree, "transcript_path": x.leadTranscript()})
	call["tool_response"] = map[string]any{"agentId": agent, "status": "async_launched"}
	x.hook("PostToolUse", call)
	return agent, ""
}

// resume starts agent again, as a message does an idle agent (its SubagentStart fires again); a busy
// one takes the message at its next tool round.
func (x *crew) resume(agent string) {
	for _, a := range x.agents {
		if a.id != agent || a.tree == "" {
			continue
		}
		if st, err := x.sub(a.phase).Load(); err != nil || st.Run.TurnOpen {
			return
		}
		_, out := x.hook("SubagentStart", map[string]any{"agent_id": a.id, "agent_type": state.PhaseAgent, "cwd": a.tree, "transcript_path": x.leadTranscript()})
		x.resumes = append(x.resumes, out)
	}
}

// hook fires event in the lead's session, with input, through baton's real handlers, and returns its exit
// code and what it printed.
func (x *crew) hook(event string, input map[string]any) (int, string) {
	x.t.Helper()
	input["hook_event_name"], input["session_id"] = event, "sess-1"
	if _, ok := input["cwd"]; !ok {
		input["cwd"] = x.repo
	}
	raw, _ := json.Marshal(input)
	env := map[string]string{"BATON_HOST": "1", "BATON_DIR": x.proj.Dir, "BATON_INSTANCE": x.lead.Instance}
	var out, errb bytes.Buffer
	code := hooks.Dispatch(event, bytes.NewReader(raw), &out, &errb, func(k string) string { return env[k] }, x.clock,
		hooks.Handlers(hooks.Deps{Open: hooks.OpenRun(x.clock), Machine: func() machine.Reading { return x.machine }}))
	if strings.Contains(errb.String(), "failed open") {
		x.t.Fatalf("%s failed open: %s", event, errb.String())
	}
	return code, strings.TrimSpace(out.String())
}

// leadTranscript is where the lead's transcript is: each agent's own is beside it, in
// sess-1/subagents/agent-<id>.jsonl (spike 32).
func (x *crew) leadTranscript() string { return filepath.Join(x.home, "sess-1.jsonl") }

// transcript is the own transcript of the latest agent on phase.
func (x *crew) transcript(phase string) string {
	return filepath.Join(x.home, "sess-1", "subagents", "agent-"+x.agent(phase).id+".jsonl")
}

// says appends lines to the own transcript of the latest agent on phase, as Claude Code writes it.
func (x *crew) says(phase string, lines ...string) {
	x.t.Helper()
	path := x.transcript(phase)
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		x.t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
}

// stops fires the SubagentStop of the latest agent on phase, as its turn ends, and returns its output.
func (x *crew) stops(phase string) string {
	a := x.agent(phase)
	_, out := x.hook("SubagentStop", map[string]any{"agent_id": a.id, "agent_type": state.PhaseAgent, "cwd": a.tree, "stop_hook_active": false})
	return out
}

// report records what the mod reports, numbered after its last.
func (x *crew) report(r bridge.Report) {
	x.t.Helper()
	b := x.bridge()
	rec, err := b.Peek()
	if err != nil {
		x.t.Fatal(err)
	}
	r.Seq, r.Instance = rec.Seen+1, x.lead.Instance
	if _, err := b.Record(r); err != nil {
		x.t.Fatal(err)
	}
}

// agent is the latest agent the mod started on phase.
func (x *crew) agent(phase string) started {
	x.t.Helper()
	for _, a := range slices.Backward(x.agents) {
		if a.phase == phase {
			return a
		}
	}
	x.t.Fatalf("no agent on %s: %+v", phase, x.agents)
	return started{}
}

// spawns lists the spawns the host gave the mod, by phase, in order.
func (x *crew) spawns() []string {
	var out []string
	for _, line := range strings.Split(x.events(), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && (e["kind"] == "worker_spawned" || e["kind"] == "worker_resumed") {
			out = append(out, fmt.Sprint(e["phase"]))
		}
	}
	return out
}

// logged reports an event of kind in the run's log with every one of fields.
func (x *crew) logged(kind string, fields map[string]any) bool {
	for _, line := range strings.Split(x.events(), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil || e["kind"] != kind {
			continue
		}
		if !slices.ContainsFunc(slices.Collect(maps.Keys(fields)), func(k string) bool { return fmt.Sprint(e[k]) != fmt.Sprint(fields[k]) }) {
			return true
		}
	}
	return false
}

// tracked lists the agents the mod counts as baton's (bridge.Agents), by id.
func (x *crew) tracked() []string {
	agents, err := x.bridge().Agents()
	if err != nil {
		x.t.Fatal(err)
	}
	var out []string
	for _, a := range agents {
		out = append(out, a.ID)
	}
	return out
}

func (x *crew) clock() time.Time {
	x.nowMu.Lock()
	defer x.nowMu.Unlock()
	return x.now
}

func (x *crew) advance(d time.Duration) {
	x.nowMu.Lock()
	defer x.nowMu.Unlock()
	x.now = x.now.Add(d)
}

func (x *crew) state() state.State {
	st, err := x.lead.Load()
	if err != nil {
		x.t.Fatal(err)
	}
	return st
}

func (x *crew) sub(phase string) *state.Store {
	s, err := x.proj.ExistingRun(state.WorkerRef("sess-1", phase), "")
	if err != nil {
		x.t.Fatal(err)
	}
	return s
}

func (x *crew) events() string {
	b, _ := os.ReadFile(filepath.Join(x.lead.Dir, "events.jsonl"))
	return string(b)
}

func (x *crew) worker(phase string) state.Worker {
	for _, w := range slices.Backward(x.state().Workers) {
		if w.Phase == phase {
			return w
		}
	}
	x.t.Fatalf("no worker on %s: %+v", phase, x.state().Workers)
	return state.Worker{}
}

// held is the instance that holds phase's sub-run ("" for none).
func (x *crew) held(phase string) string {
	if st, err := x.sub(phase).Load(); err == nil && st.Owner != nil {
		return st.Owner.Instance
	}
	return ""
}

// atWork ticks until the agents on phases are at work, a second at a time (one spawn at a time).
func (x *crew) atWork(phases ...string) {
	x.t.Helper()
	for range 2*len(phases) + 2 {
		if !slices.ContainsFunc(phases, func(p string) bool { return x.status(p) != state.WorkerWorking }) {
			return
		}
		x.tick(time.Second)
	}
	x.t.Fatalf("not at work: %+v", x.state().Workers)
}

// status is how the latest worker on phase stands ("" for none).
func (x *crew) status(phase string) string {
	for _, w := range slices.Backward(x.state().Workers) {
		if w.Phase == phase {
			return w.Status
		}
	}
	return ""
}
