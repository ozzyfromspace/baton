package board

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

var now = time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local)

// thePlan has four steps: P1; then P2, P3 and P4, each after P1; then P5, after P3; then P6, after all.
var thePlan = plan.Plan{Version: 1, Title: "Workers you can see", Phases: []plan.Phase{
	{ID: "P1", Title: "The mod", After: []string{}},
	{ID: "P2", Title: "Every hook knows its loop", After: []string{"P1"}},
	{ID: "P3", Title: "Phase agents", After: []string{"P1"}},
	{ID: "P4", Title: "Gates and questions", After: []string{"P1"}},
	{ID: "P5", Title: "baton's view", After: []string{"P3"}},
	{ID: "P6", Title: "Release", After: []string{"P1", "P2", "P3", "P4", "P5"}},
}}

// run is a run of thePlan at step 2: P1 done, and the lead on P2 for 5 minutes, its turn open.
type run struct {
	st   state.State
	subs []state.RunInfo
}

func newRun() *run {
	st := state.Attach(thePlan, now.Add(-time.Hour), state.Origin{})
	st.Owner = &state.Owner{Instance: "inst", Heartbeat: now}
	st.Phases["P1"] = &state.PhaseState{Status: state.PhaseDone}
	st.Current = "P2"
	st.Phases["P2"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: now.Add(-5 * time.Minute)}
	st.Run.TurnOpen, st.Run.TurnStarted = true, now.Add(-2*time.Minute)
	return &run{st: st}
}

// agent gives phase to a phase agent at work on it since ago, its turn open in its sub-run, and returns
// the sub-run's state to change.
func (r *run) agent(phase, id string, ago time.Duration) *state.State {
	ref := "r1/" + phase
	r.st.Phases[phase] = &state.PhaseState{Status: state.PhaseWorking, Worker: ref}
	r.st.Workers = append(r.st.Workers, state.Worker{Phase: phase, Ref: ref, AgentID: id, Status: state.WorkerWorking,
		Assigned: now.Add(-ago), Since: now.Add(-ago), Generation: r.st.Generation})
	sub := state.AttachWorker(r.st, phase)
	sub.Phases[phase].Status = state.PhaseActive
	sub.Run.TurnOpen = true
	r.subs = append(r.subs, state.RunInfo{ID: ref, State: sub})
	return &r.subs[len(r.subs)-1].State
}

func (r *run) board(serial string) Board {
	return Build(Input{State: r.st, Plan: thePlan, HavePlan: true, Subs: r.subs, Serial: serial, Now: now})
}

func TestBand(t *testing.T) {
	const step2 = "◆ baton · step 2/4 · "
	for _, c := range []struct {
		name   string
		run    func() *run
		serial string
		want   string
	}{
		{"no plan", func() *run { return &run{st: state.New()} }, "", ""},
		{"serial: the lead works the phases in turn", newRun, decide.ModMissing,
			step2 + "one at a time · P2 ▶ 5m · P3 ready · P4 ready · P5 waits on P3 · lead: working on P2"},
		{"two agents at once, beside the lead", func() *run {
			r := newRun()
			r.agent("P3", "a1b2c3d4e5", 12*time.Minute)
			r.agent("P4", "f6e5d4c3b2", 3*time.Minute)
			return r
		}, "", step2 + "P2 ▶ 5m · P3 ⚙ 12m · P4 ⚙ 3m · P5 waits on P3 · lead: working on P2"},
		{"the lead's phase verifying, its turn over", func() *run {
			r := newRun()
			r.st.Phases["P2"] = &state.PhaseState{Status: state.PhaseDone}
			r.agent("P3", "a1b2c3d4e5", 12*time.Minute)
			r.st.Current = "P4"
			r.st.Phases["P4"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: now.Add(-20 * time.Minute)}
			r.st.Jobs = []state.Job{{ID: "j4", Phase: "P4", Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-3 * time.Minute)}}
			r.st.Run.TurnOpen = false
			return r
		}, "", step2 + "P3 ⚙ 12m · P4 verifying 3m · P5 waits on P3 · lead: waiting on j4"},
		{"an agent verifying its phase", func() *run {
			r := newRun()
			sub := r.agent("P3", "a1b2c3d4e5", 12*time.Minute)
			sub.Phases["P3"].Status = state.PhaseVerifying
			sub.Jobs = []state.Job{{ID: "P3.j1", Phase: "P3", Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-4 * time.Minute)}}
			return r
		}, "", step2 + "P2 ▶ 5m · P3 verifying 4m · P4 ready · P5 waits on P3 · lead: working on P2"},
		{"waiting on a phase: the lead holds none", func() *run {
			r := newRun()
			for _, id := range []string{"P2", "P4"} {
				r.st.Phases[id] = &state.PhaseState{Status: state.PhaseDone}
			}
			r.agent("P3", "a1b2c3d4e5", 12*time.Minute)
			r.st.Current, r.st.Run.TurnOpen = "", false
			return r
		}, "", step2 + "P3 ⚙ 12m · P5 waits on P3 · lead: waiting on P3"},
		{"a phase that needs the human", func() *run {
			r := newRun()
			sub := r.agent("P3", "a1b2c3d4e5", 12*time.Minute)
			sub.Blocked = &state.Block{Reason: "the signing key is gone", Since: now}
			return r
		}, "", step2 + "P2 ▶ 5m · P3 needs you · P4 ready · P5 waits on P3 · lead: working on P2"},
		{"an agent's permission prompt on the lead's screen", func() *run {
			r := newRun()
			r.agent("P3", "a1b2c3d4e5", 12*time.Minute)
			r.st.Run.Dialogs = state.DialogQueue{{Tool: "Bash", Agent: "a1b2c3d4e5", Since: now}}
			return r
		}, "", step2 + "P2 ▶ 5m · P3 needs you · P4 ready · P5 waits on P3 · lead: working on P2"},
		{"a phase whose agent the human stopped", func() *run {
			r := newRun()
			r.agent("P3", "a1b2c3d4e5", 12*time.Minute)
			r.st.Workers[0].Status = state.WorkerHeld
			return r
		}, "", step2 + "P2 ▶ 5m · P3 needs you · P4 ready · P5 waits on P3 · lead: working on P2"},
		{"the lead needs the human", func() *run {
			r := newRun()
			r.st.Run.Escalation = &state.Escalation{Kind: "refused", Reason: "P2 stopped: the model declined the last turn"}
			return r
		}, "", step2 + "P2 ▶ 5m · P3 ready · P4 ready · P5 waits on P3 · lead: needs you"},
		{"starting, landing, and held back", func() *run {
			r := newRun()
			r.agent("P3", "", 0)
			r.st.Workers[0].Status = state.WorkerStarting
			r.agent("P4", "f6e5d4c3b2", time.Hour)
			r.st.Workers[1].Status, r.st.Workers[1].Merge = state.WorkerLanding, &state.Merge{Queued: now}
			r.st.Phases["P2"] = &state.PhaseState{Status: state.PhaseDone}
			r.st.Current, r.st.Run.TurnOpen = "", false
			return r
		}, "", step2 + "P3 starting · P4 landing · P5 waits on P3 · lead: waiting on P3, P4"},
		{"a landing held back, and one verified again", func() *run {
			r := newRun()
			r.agent("P3", "a1", time.Hour)
			r.st.Workers[0].Status, r.st.Workers[0].Merge = state.WorkerLanding, &state.Merge{Queued: now, Deferred: "the lead changed a.go too"}
			r.agent("P4", "a2", time.Hour)
			r.st.Workers[1].Status, r.st.Workers[1].Merge = state.WorkerLanding, &state.Merge{Queued: now, Reverify: "P4.j2"}
			return r
		}, "", step2 + "P2 ▶ 5m · P3 waits to land · P4 verifying again · P5 waits on P3 · lead: working on P2"},
		{"a late phase of step 2 still landing: the run is at step 3", func() *run {
			r := newRun()
			for _, id := range []string{"P2", "P3"} {
				r.st.Phases[id] = &state.PhaseState{Status: state.PhaseDone}
			}
			r.agent("P4", "a4", time.Hour)
			r.st.Workers[0].Status, r.st.Workers[0].Merge = state.WorkerLanding, &state.Merge{Queued: now}
			r.st.Current = "P5"
			r.st.Phases["P5"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: now.Add(-5 * time.Minute)}
			return r
		}, "", "◆ baton · step 3/4 · P4 landing · P5 ▶ 5m · P6 waits on P4, P5 · lead: working on P5"},
		{"a late phase of step 2 verifying, the lead's or an agent's: the run is at step 3", func() *run {
			r := newRun()
			r.st.Phases["P2"] = &state.PhaseState{Status: state.PhaseVerifying}
			r.st.Jobs = []state.Job{{ID: "j2", Phase: "P2", Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-2 * time.Minute)}}
			r.st.Phases["P3"] = &state.PhaseState{Status: state.PhaseDone}
			sub := r.agent("P4", "a4", time.Hour)
			sub.Phases["P4"].Status = state.PhaseVerifying
			sub.Jobs = []state.Job{{ID: "P4.j1", Phase: "P4", Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-4 * time.Minute)}}
			r.st.Current = "P5"
			r.st.Phases["P5"] = &state.PhaseState{Status: state.PhaseActive, StartedAt: now.Add(-5 * time.Minute)}
			return r
		}, "", "◆ baton · step 3/4 · P2 verifying 2m · P4 verifying 4m · P5 ▶ 5m · P6 waits on P2, P4, P5 · lead: working on P5"},
		{"a phase agent still at work holds the step", func() *run {
			r := newRun()
			r.st.Phases["P2"] = &state.PhaseState{Status: state.PhaseDone}
			r.agent("P3", "a3", time.Hour)
			r.st.Phases["P4"] = &state.PhaseState{Status: state.PhaseDone}
			r.st.Current, r.st.Run.TurnOpen = "", false
			return r
		}, "", step2 + "P3 ⚙ 1h00m · P5 waits on P3 · lead: waiting on P3"},
		{"a phase held back from an agent", func() *run {
			r := newRun()
			r.agent("P3", "a1", time.Minute)
			r.st.Hold = &state.Hold{Phase: "P4", Kind: state.HoldBusy, Why: "memory pressure is critical", Since: now}
			return r
		}, "", step2 + "P2 ▶ 5m · P3 ⚙ 1m · P4 held · P5 waits on P3 · lead: working on P2"},
		{"every agent slot at work: the band keeps it short", func() *run {
			r := newRun()
			r.agent("P3", "a1", time.Minute)
			r.st.Hold = &state.Hold{Phase: "P4", Kind: state.HoldCap, Why: decide.SlotsAtWork(1), Since: now, Cap: 1}
			return r
		}, "", step2 + "P2 ▶ 5m · P3 ⚙ 1m · P4 ready · P5 waits on P3 · lead: working on P2"},
		{"a usage limit", func() *run {
			r := newRun()
			r.st.Usage = &state.Usage{UsedPct: 100, Limited: now.Add(40 * time.Minute), Hit: now, By: "lead"}
			return r
		}, "", step2 + "usage limit until " + now.Add(40*time.Minute).Format("15:04") + " · P2 ▶ 5m · P3 ready · P4 ready · P5 waits on P3 · lead: working on P2"},
		{"a usage limit the resume timer ends", func() *run {
			r := newRun()
			r.st.Usage = &state.Usage{Limited: now.Add(40 * time.Minute), Hit: now, By: "lead", ResumeAt: now.Add(40 * time.Minute)}
			return r
		}, "", step2 + "usage limit · resumes at " + now.Add(40*time.Minute).Format("15:04") + " · P2 ▶ 5m · P3 ready · P4 ready · P5 waits on P3 · lead: working on P2"},
		{"compacting at a boundary", func() *run {
			r := newRun()
			r.st.Phases["P2"].Status = state.PhaseDone
			r.st.Current, r.st.BoundaryOwed = "P3", true
			r.st.Phases["P3"] = &state.PhaseState{Status: state.PhasePending}
			r.st.Run.Compaction = state.Compaction{Status: state.CompactActive, Started: now.Add(-time.Minute)}
			return r
		}, "", step2 + "P3 next · P4 ready · P5 waits on P3 · lead: compacting"},
		{"a compaction held back", func() *run {
			r := newRun()
			r.st.Run.Compaction, r.st.Run.Gate = state.Compaction{Status: state.CompactQueued}, "the human has a draft in the input box"
			return r
		}, "", step2 + "P2 ▶ 5m · P3 ready · P4 ready · P5 waits on P3 · lead: compaction held"},
		{"paused", func() *run { r := newRun(); r.st.Mode = state.ModePaused; return r }, "",
			"◆ baton · paused · step 2/4 · P2 ▶ 5m · P3 ready · P4 ready · P5 waits on P3 · lead: paused"},
		{"complete", func() *run { r := newRun(); r.st.Mode = state.ModeComplete; return r }, "", "◆ baton · ✓ plan complete"},
	} {
		if got := c.run().board(c.serial).Band(0); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// Short of room, the band leaves out the phases worth least, the later first, and counts them; the mark,
// the step and the lead stay, and a phase that needs the human goes last of all.
func TestBandNarrow(t *testing.T) {
	r := newRun()
	sub := r.agent("P3", "a1", 12*time.Minute)
	sub.Blocked = &state.Block{Reason: "the key is gone", Since: now}
	r.agent("P4", "a2", 3*time.Minute)
	b := r.board("")
	const whole = "◆ baton · step 2/4 · P2 ▶ 5m · P3 needs you · P4 ⚙ 3m · P5 waits on P3 · lead: working on P2"
	for _, c := range []struct {
		width int
		want  string
	}{
		{0, whole},
		{200, whole},
		{cells(whole), whole},
		{cells(whole) - 1, "◆ baton · step 2/4 · P2 ▶ 5m · P3 needs you · P4 ⚙ 3m · +1 · lead: working on P2"},
		{70, "◆ baton · step 2/4 · P2 ▶ 5m · P3 needs you · +2 · lead: working on P2"},
		{60, "◆ baton · step 2/4 · P3 needs you · +3 · lead: working on P2"},
		{45, "◆ baton · step 2/4 · +4 · lead: working on P2"},
		{30, "◆ baton · step 2/4 · +4 · lea…"},
		{10, "◆ baton ·…"},
		{1, "…"},
	} {
		got := b.Band(c.width)
		if got != c.want {
			t.Errorf("width %d:\n got %q\nwant %q", c.width, got, c.want)
		}
		if c.width > 0 && cells(got) > c.width {
			t.Errorf("width %d: %d cells", c.width, cells(got))
		}
	}
}

func TestLines(t *testing.T) {
	for _, c := range []struct {
		name   string
		run    func() *run
		serial string
		width  int
		want   []string
	}{
		{"no plan", func() *run { return &run{st: state.New()} }, "", 0, []string{"baton: no plan attached here."}},
		{"two agents, one verifying, one needing the human", func() *run {
			r := newRun()
			sub := r.agent("P3", "a1b2c3d4e5f6", 12*time.Minute)
			sub.Phases["P3"].Status = state.PhaseVerifying
			sub.Jobs = []state.Job{{ID: "P3.j1", Phase: "P3", Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-4 * time.Minute)}}
			sub = r.agent("P4", "f6e5d4c3b2a1", 3*time.Minute)
			state.AddProposal(sub, state.Decision{What: "commit unsigned", Deadline: now.Add(4 * time.Minute)}, now)
			return r
		}, "", 0, []string{
			`◆ baton · "Workers you can see" · step 2 of 4`,
			"step 1  ✓ P1  The mod — done",
			"step 2  ▶ P2  Every hook knows its loop — the lead works on it 5m",
			"        ⧗ P3  Phase agents — verifying 4m (job P3.j1) · @P3 <your words> · agent a1b2c3d4",
			"        ? P4  Gates and questions — needs you: a proposal, which goes ahead at " + now.Add(4*time.Minute).Format("15:04") + " · @P4 <your words> · agent f6e5d4c3",
			"step 3  · P5  baton's view — waits on P3",
			"step 4  · P6  Release — waits on P2, P3, P4 and P5",
			"lead: working on P2, its turn 2m",
			decide.BoardWrite[0],
			decide.BoardWrite[1],
		}},
		{"a phase whose agent the human stopped", func() *run {
			r := newRun()
			r.agent("P3", "a1b2c3d4e5f6", 12*time.Minute)
			r.st.Workers[0].Status = state.WorkerHeld
			return r
		}, "", 0, []string{
			`◆ baton · "Workers you can see" · step 2 of 4`,
			"step 1  ✓ P1  The mod — done",
			"step 2  ▶ P2  Every hook knows its loop — the lead works on it 5m",
			"        ? P3  Phase agents — needs you: its agent was stopped; /baton run starts a fresh one · agent a1b2c3d4",
			"        · P4  Gates and questions — ready",
			"step 3  · P5  baton's view — waits on P3",
			"step 4  · P6  Release — waits on P2, P3, P4 and P5",
			"lead: working on P2, its turn 2m",
		}},
		{"every agent slot at work: every ready phase says so", func() *run {
			r := newRun()
			r.st.Hold = &state.Hold{Phase: "P3", Kind: state.HoldCap, Why: decide.SlotsAtWork(6), Since: now, Cap: 6}
			return r
		}, "", 0, []string{
			`◆ baton · "Workers you can see" · step 2 of 4`,
			"step 1  ✓ P1  The mod — done",
			"step 2  ▶ P2  Every hook knows its loop — the lead works on it 5m",
			"        · P3  Phase agents — ready: all 6 agent slots at work",
			"        · P4  Gates and questions — ready: all 6 agent slots at work",
			"step 3  · P5  baton's view — waits on P3",
			"step 4  · P6  Release — waits on P2, P3, P4 and P5",
			"lead: working on P2, its turn 2m",
		}},
		{"the one agent slot at work", func() *run {
			r := newRun()
			r.agent("P3", "a1b2c3d4e5f6", 12*time.Minute)
			r.st.Hold = &state.Hold{Phase: "P4", Kind: state.HoldCap, Why: decide.SlotsAtWork(1), Since: now, Cap: 1}
			return r
		}, "", 0, []string{
			`◆ baton · "Workers you can see" · step 2 of 4`,
			"step 1  ✓ P1  The mod — done",
			"step 2  ▶ P2  Every hook knows its loop — the lead works on it 5m",
			"        ⚙ P3  Phase agents — agent at work 12m · @P3 <your words> · agent a1b2c3d4",
			"        · P4  Gates and questions — ready: the one agent slot at work",
			"step 3  · P5  baton's view — waits on P3",
			"step 4  · P6  Release — waits on P2, P3, P4 and P5",
			"lead: working on P2, its turn 2m",
			decide.BoardWrite[0],
			decide.BoardWrite[1],
		}},
		{"the weekly wind-down, every agent slot at work too: every ready phase names the wind-down", func() *run {
			r := newRun()
			r.st.Hold = &state.Hold{Phase: "P3", Kind: state.HoldWeekly, Why: "held", Since: now}
			r.st.Usage = &state.Usage{WeeklyPct: 98.4, WeeklyResets: now.Add(30 * time.Hour), WoundDown: now, WindFor: now.Add(30 * time.Hour)}
			return r
		}, "", 0, []string{
			`◆ baton · "Workers you can see" · step 2 of 4`,
			"step 1  ✓ P1  The mod — done",
			"step 2  ▶ P2  Every hook knows its loop — the lead works on it 5m",
			"        · P3  Phase agents — ready: " + decide.WindDownHolds(now.Add(30*time.Hour+state.ResumeGrace), now),
			"        · P4  Gates and questions — ready: " + decide.WindDownHolds(now.Add(30*time.Hour+state.ResumeGrace), now),
			"step 3  · P5  baton's view — waits on P3",
			"step 4  · P6  Release — waits on P2, P3, P4 and P5",
			"lead: working on P2, its turn 2m",
		}},
		{"serial, waiting on a gate", func() *run {
			r := newRun()
			r.st.Jobs = []state.Job{{ID: "j2", Phase: "P2", Kind: state.JobAwait, Status: state.JobRunning, Queued: now.Add(-90 * time.Second)}}
			r.st.Run.TurnOpen = false
			return r
		}, decide.SerialConfig(false), 0, []string{
			`◆ baton · "Workers you can see" · step 2 of 4`,
			"step 1  ✓ P1  The mod — done",
			"step 2  ⧗ P2  Every hook knows its loop — awaiting 1m (job j2)",
			"        · P3  Phase agents — ready",
			"        · P4  Gates and questions — ready",
			"step 3  · P5  baton's view — waits on P3",
			"step 4  · P6  Release — waits on P2, P3, P4 and P5",
			"lead: its turn over, waiting on j2",
			"phases run one at a time here: workers is 0 in baton's config",
		}},
		{"narrow: titles left out where too little would show", func() *run {
			r := newRun()
			r.agent("P3", "a1b2c3d4e5f6", 12*time.Minute)
			return r
		}, "", 48, []string{
			`◆ baton · "Workers you can see" · step 2 of 4`,
			"step 1  ✓ P1  The mod — done",
			"step 2  ▶ P2  the lead works on it 5m",
			"        ⚙ P3  agent at work 12m · @P3 <your wor…",
			"        · P4  Gates and questions — ready",
			"step 3  · P5  baton's view — waits on P3",
			"step 4  · P6  waits on P2, P3, P4 and P5",
			"lead: working on P2, its turn 2m",
			"@<phase> <your words> at the lead's prompt", // the note, wrapped
			"writes to its agent, which gets them at its next",
			"tool call (an idle one wakes and goes on); a",
			"phase with no agent at work, a done one among",
			"them, gets them through the lead",
			"an agent's own view (Claude Code's agent panel)",
			"wakes it, whatever it is; baton refuses one it",
			"let go every tool call",
		}},
		{"narrow: titles cut", func() *run { return newRun() }, "", 56, []string{
			`◆ baton · "Workers you can see" · step 2 of 4`,
			"step 1  ✓ P1  The mod — done",
			"step 2  ▶ P2  Every hook know… — the lead works on it 5m",
			"        · P3  Phase agents — ready",
			"        · P4  Gates and questions — ready",
			"step 3  · P5  baton's view — waits on P3",
			"step 4  · P6  Release — waits on P2, P3, P4 and P5",
			"lead: working on P2, its turn 2m",
		}},
		{"paused, with a usage limit", func() *run {
			r := newRun()
			r.st.Mode = state.ModePaused
			r.st.Usage = &state.Usage{Limited: now.Add(time.Hour), Hit: now, By: "P3", Cancelled: state.CancelPause}
			return r
		}, "", 0, []string{
			`◆ baton · "Workers you can see" · paused · step 2 of 4`,
			"step 1  ✓ P1  The mod — done",
			"step 2  ▶ P2  Every hook knows its loop — the lead works on it 5m",
			"        · P3  Phase agents — ready",
			"        · P4  Gates and questions — ready",
			"step 3  · P5  baton's view — waits on P3",
			"step 4  · P6  Release — waits on P2, P3, P4 and P5",
			"lead: paused, the human has the wheel (/baton run resumes the plan)",
			"usage limit: the agent on P3 hit it; nothing starts, and baton types nothing into the lead, until " + now.Add(time.Hour).Format("15:04") +
				"; auto-resume cancelled: /baton run resumes",
		}},
	} {
		got := c.run().board(c.serial).Lines(c.width)
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s:\n got\n%s\nwant\n%s", c.name, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
		for _, l := range got {
			if c.width > 0 && cells(l) > c.width {
				t.Errorf("%s: %q is %d cells, over %d", c.name, l, cells(l), c.width)
			}
		}
	}
}

// The board as JSON says how each phase stands, in the same words' terms, with the agent at work on it.
func TestJSON(t *testing.T) {
	r := newRun()
	r.agent("P3", "a1b2c3d4e5f6", 12*time.Minute)
	b, err := json.Marshal(r.board(""))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Mode  string
		Step  int
		Steps []struct {
			N      int
			Phases []struct {
				ID, Status, Agent, Worker string
				Stand                     struct{ Kind string }
			}
		}
		Lead struct{ Kind, Phase string }
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	p3 := got.Steps[1].Phases[1]
	if got.Mode != "running" || got.Step != 2 || len(got.Steps) != 4 || p3.ID != "P3" || p3.Status != "working" ||
		p3.Agent != "a1b2c3d4e5f6" || p3.Worker != "working" || p3.Stand.Kind != "agent" || got.Lead.Kind != "working" || got.Lead.Phase != "P2" {
		t.Errorf("%s", b)
	}
}
