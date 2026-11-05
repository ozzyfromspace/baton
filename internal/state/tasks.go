package state

import (
	"cmp"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"time"
)

// The model's own background tasks, and the signals that one may do nothing more for the run (v0.8.21). A
// 74-phase campaign lost about 8 agent-hours to one: a subagent of the lead's model started an until loop in
// the background to wait for a pytest task's last lines, the task ended without the pattern, and the loop
// polled every 5 seconds for 22 hours, writing nothing, while it kept every restart away. So baton records
// each task as it starts (BackgroundStart: what started it, in which phase of which plan, and where its output
// goes), and Suspects names, from that record, the loop's last list of the session's tasks and what the host
// read of each one's output and processes, the background shells with signals that they may do nothing more
// for the run. A signal never stops a task: the host has each suspect reviewed, and stops one only on the
// reviewer's verdict, since losing honest work to a blind rule is as bad as a stuck task. Subagents, workflows
// and monitors are never suspects: they are named (LongTasks).

// Background work's kinds, as Claude Code lists a task at Stop (Task.Type) and as baton records a start
// (BackgroundStart.Type).
const (
	TaskShell    = "shell"    // a command run in the background, or moved there
	TaskMonitor  = "monitor"  // a Monitor, which Claude Code lists as a shell (Runtime.Monitors)
	TaskSubagent = "subagent" // an agent launched in the background
	TaskWorkflow = "workflow" // a workflow launched in the background
)

// TaskQuiet is how long a background shell's output stays still before that is a signal (SignalQuiet;
// task_quiet, which the human may set: config.DefaultTaskQuiet).
const TaskQuiet = 30 * time.Minute

// LongTask is how long the model's own background work runs before it is named, and for a shell, before that
// is a signal (SignalLong).
const LongTask = time.Hour

// PastEnd is how many times the run time its description declares (DeclaredEnd) a shell runs before that is a
// signal (SignalPastEnd).
const PastEnd = 3

// IdleShare is the share of one core a shell's process tree uses, at most, between two looks, for that to be a
// signal (SignalIdle): 1%.
const IdleShare = 0.01

// The signals that a background shell may do nothing more for the run (Signal.Why).
const (
	// SignalPhaseOver: it started in a phase that is over for its loop: done (or landed), taken from the lead by
	// a phase agent (handover.go), its worker retired (RetiredSuspects), or a phase of a plan no longer attached.
	SignalPhaseOver = "phase_over"
	// SignalQuiet: its output has not changed for task_quiet, whatever its command.
	SignalQuiet = "quiet"
	// SignalIdle: its process tree, where the host found it, used under IdleShare of a core since its last look.
	// A server waiting for requests is idle, and may be needed.
	SignalIdle = "idle"
	// SignalPastEnd: it runs past PastEnd times the run time its description declares.
	SignalPastEnd = "past_end"
	// SignalWaitsOnEnded: its command names another task's output file, and that task has ended.
	SignalWaitsOnEnded = "waits_on_ended"
	// SignalLong: it has run for LongTask or more.
	SignalLong = "long"
)

// PhaseGone is where a phase stands (Suspect.Stands) that is a phase of a plan no longer attached, or one the
// plan attached now has no more.
const PhaseGone = "gone"

// BackgroundStart is what baton saw of a piece of background work of the model's own as the tool call that
// started it came back (PostToolUse), kept in its loop's Runtime.BackgroundStarts by its task id.
type BackgroundStart struct {
	Type        string `json:"type"`                  // TaskShell, TaskMonitor, TaskSubagent or TaskWorkflow
	Tool        string `json:"tool,omitempty"`        // the tool that started it: Bash, PowerShell, Monitor, Agent, …
	Command     string `json:"command,omitempty"`     // the command it runs, clipped
	Description string `json:"description,omitempty"` // as the call gave it
	// Phase is the phase the loop had in hand as it started while baton drove its plan, "" for work started
	// with no phase in hand, or while baton did not drive one (paused, or no plan): the conversation's.
	// Generation is the run's plan then (State.Generation).
	Phase      string `json:"phase,omitempty"`
	Generation int    `json:"generation,omitempty"`
	// Agent is the model's own subagent that started it, "" for the loop's own agent.
	Agent string `json:"agent,omitempty"`
	// Output is the file Claude Code writes its output to (TasksDir), "" when that was not known.
	Output string    `json:"output,omitempty"`
	At     time.Time `json:"at"`
}

// Start records b, background work of the model's own that started as task id: the first record of a task
// stands. It reports whether that was news.
func (r *Runtime) Start(id string, b BackgroundStart) bool {
	if _, ok := r.BackgroundStarts[id]; ok || id == "" {
		return false
	}
	if r.BackgroundStarts == nil {
		r.BackgroundStarts = map[string]BackgroundStart{}
	}
	r.BackgroundStarts[id] = b
	return true
}

// Known lists the background work the loop knows to be its own: what it was told of (BackgroundTold) and
// what it saw start (BackgroundStarts), its subagents' too. Claude Code lists every background task of the
// session at each Stop, whoever started it, so another loop of the run counts none of these (OthersTold).
func (r Runtime) Known() []string {
	out := slices.Clone(r.BackgroundTold)
	for _, id := range slices.Sorted(maps.Keys(r.BackgroundStarts)) {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// TaskOutput is the file background task id writes its output to: as recorded at its start, or, for a task
// whose start baton did not see, where Claude Code writes every task's (TasksDir). "" when neither is known.
func (r Runtime) TaskOutput(id string) string {
	if b, ok := r.BackgroundStarts[id]; ok && b.Output != "" {
		return b.Output
	}
	if r.TasksDir == "" || id == "" {
		return ""
	}
	return filepath.Join(r.TasksDir, id+".output")
}

// outputFile is a task's output file named in a command: Claude Code writes each background task's output to
// tasks/<id>.output (spike 45).
var outputFile = regexp.MustCompile(`tasks/([A-Za-z0-9_-]+)\.output\b`)

// WaitsOn lists the background tasks whose output files command names (tasks/<id>.output), in order, each
// once, but self's: the tasks a loop over their output waits on.
func WaitsOn(command, self string) []string {
	var out []string
	for _, m := range outputFile.FindAllStringSubmatch(command, -1) {
		if id := m[1]; id != self && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// NoteGone records, from a Stop's list of every background task of the session (all, whoever started it),
// when each task a task of the loop's own waits on (WaitsOn its command) was first no longer listed: it has
// ended by then (Runtime.Gone). It forgets those no task of the loop's waits on any more. Call it after
// SetBackground.
func (r *Runtime) NoteGone(all []Task, now time.Time) {
	named := map[string]bool{}
	for id, b := range r.BackgroundStarts {
		for _, w := range WaitsOn(b.Command, id) {
			named[w] = true
		}
	}
	for _, t := range r.Background {
		for _, w := range WaitsOn(t.Command, t.ID) {
			named[w] = true
		}
	}
	for id := range named {
		switch _, gone := r.Gone[id]; {
		case slices.ContainsFunc(all, func(t Task) bool { return t.ID == id }):
			delete(r.Gone, id)
		case !gone:
			if r.Gone == nil {
				r.Gone = map[string]time.Time{}
			}
			r.Gone[id] = now
		}
	}
	maps.DeleteFunc(r.Gone, func(id string, _ time.Time) bool { return !named[id] })
	if len(r.Gone) == 0 {
		r.Gone = nil
	}
}

// TaskStat is what the host read of a background task (TaskOutput, and its process tree where it found it):
// its output's size and when it last changed (ModTime zero: not read), and the CPU time its process tree used
// since the host's last look, over Span of the wall clock (Span zero: not read).
type TaskStat struct {
	Size    int64
	ModTime time.Time
	CPU     time.Duration
	Span    time.Duration
}

// OwnTask is a piece of background work of the model's own that a loop knows runs: the task as the loop's
// last Stop listed it, or as it started where no Stop has since; its kind (TaskShell, TaskMonitor, …); what
// baton saw as it started (nil when it did not); and how long it has run, from its start, or else from the
// first Stop that listed it.
type OwnTask struct {
	Task  Task
	Kind  string
	Start *BackgroundStart
	Age   time.Duration
}

// Command is the command the task runs: as it started, or as the Stop listed it.
func (t OwnTask) Command() string {
	if t.Start != nil && t.Start.Command != "" {
		return t.Start.Command
	}
	return t.Task.Command
}

// Description is the task's description, as the call that started it gave it, or as the Stop listed it.
func (t OwnTask) Description() string {
	if t.Start != nil && t.Start.Description != "" {
		return t.Start.Description
	}
	return t.Task.Description
}

// OwnTasks lists the background work of the model's own that st's loop knows runs, as of now: each task its
// last Stop listed (Runtime.Background, which leaves out the work of the run's other loops), in its order,
// then each it saw start since, oldest first. baton's phase agents, and Claude Code's own housekeeping (a
// dream, a memory import, the watch on a published artifact: Task.Waitable), are none of the model's.
func (st State) OwnTasks(now time.Time) []OwnTask {
	r := st.Run
	var out, since []OwnTask
	for _, t := range r.Background {
		if t.Waitable() && !IsPhaseAgent(t.AgentType) {
			out = append(out, r.own(t, now))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(r.BackgroundStarts)) {
		if slices.ContainsFunc(r.Background, func(t Task) bool { return t.ID == id }) {
			continue
		}
		b := r.BackgroundStarts[id]
		since = append(since, r.own(Task{ID: id, Type: b.Type, Status: "running", Description: b.Description, Since: b.At}, now))
	}
	slices.SortStableFunc(since, func(a, b OwnTask) int { return a.Start.At.Compare(b.Start.At) })
	return append(out, since...)
}

// own is task t of the loop as an OwnTask, as of now.
func (r Runtime) own(t Task, now time.Time) OwnTask {
	o, from := OwnTask{Task: t, Kind: t.Type}, t.Since
	if b, ok := r.BackgroundStarts[t.ID]; ok {
		o.Start, o.Kind = &b, b.Type
		if !b.At.IsZero() {
			from = b.At
		}
	} else if slices.Contains(r.Monitors, t.ID) {
		o.Kind = TaskMonitor
	}
	if !from.IsZero() {
		o.Age = max(now.Sub(from), 0)
	}
	return o
}

// Signal is one sign that a background shell may do nothing more for the run: why (SignalPhaseOver, …), and
// for how long it has held, as far as that is known (0 when not): since its phase ended, since its output last
// changed, over the host's last look, since it passed PastEnd times its declared run time, since the task it
// waits on ended, or since it started.
type Signal struct {
	Why string        `json:"why"`
	For time.Duration `json:"for"`
}

// Waited is a task whose output a shell's command names (WaitsOn), and whether and when it ended, as a Stop
// first no longer listed it (Runtime.Gone).
type Waited struct {
	ID      string    `json:"id"`
	Ended   bool      `json:"ended"`
	EndedAt time.Time `json:"ended_at,omitzero"`
}

// Suspect is a background shell of the model's own with signals that it may do nothing more for the run
// (Suspects), and what its reviewer needs that state holds, beside the task itself (OwnTask: its command,
// description and kind, as it started or as the Stop listed it, and its age): the phase it started in ("" for
// the conversation's) and where that phase stands (a PhaseState status, or PhaseGone), the plan's generation,
// the run time its description declares (0 for none), the tasks it waits on, and what the host read of its
// output and processes.
type Suspect struct {
	OwnTask
	Signals    []Signal
	Phase      string
	Stands     string
	Generation int
	Declared   time.Duration
	WaitsOn    []Waited
	Stat       TaskStat
}

// Has reports signal why among the suspect's.
func (s Suspect) Has(why string) bool {
	return slices.ContainsFunc(s.Signals, func(g Signal) bool { return g.Why == why })
}

// Whys lists the suspect's signals by why, in order.
func (s Suspect) Whys() []string {
	var out []string
	for _, g := range s.Signals {
		out = append(out, g.Why)
	}
	return out
}

// Suspects returns each background shell of the model's own that st's loop knows runs (OwnTasks) with one or
// more signals that it may do nothing more for the run, as of now, each signal with its why and for how long,
// in this order:
//
//   - SignalPhaseOver: baton saw it start in a phase of st's plan that is done, or that a phase agent took
//     from the lead, or in a phase of a plan no longer attached. Work baton did not see start in a phase (with
//     no phase in hand, before the plan, while baton did not drive it, or with no record at all) is the
//     conversation's, and never has it;
//   - SignalQuiet: its output (stats, by task id, as the host read TaskOutput) has not changed for quiet,
//     whatever its command (quiet 0: never);
//   - SignalIdle: its process tree used under IdleShare of a core over the host's last look;
//   - SignalPastEnd: it has run PastEnd times the run time its description declares (DeclaredEnd);
//   - SignalWaitsOnEnded: its command names a task's output file, and that task has ended (Runtime.Gone);
//   - SignalLong: it has run for LongTask or more.
//
// A shell with no stat is judged on its other signals. A signal never stops a task by itself: the host has
// each suspect reviewed. Subagents, workflows, monitors and crons are never suspects, nor are baton's phase
// agents.
func Suspects(st State, stats map[string]TaskStat, quiet time.Duration, now time.Time) []Suspect {
	return st.suspects(stats, quiet, now, false)
}

// RetiredSuspects is Suspects for st, the sub-run of a worker baton let go of for good (loop.RetireWorker):
// its phase is over for its agents whatever the sub-run says, so each shell its loop saw start has
// SignalPhaseOver. A shell it did not see start may be another loop's, and is judged as Suspects judges it.
func RetiredSuspects(st State, stats map[string]TaskStat, quiet time.Duration, now time.Time) []Suspect {
	return st.suspects(stats, quiet, now, true)
}

func (st State) suspects(stats map[string]TaskStat, quiet time.Duration, now time.Time, retired bool) []Suspect {
	var out []Suspect
	for _, t := range st.OwnTasks(now) {
		if t.Kind != TaskShell {
			continue
		}
		s := Suspect{OwnTask: t, Stat: stats[t.Task.ID], Declared: DeclaredEnd(t.Description())}
		if t.Start != nil {
			s.Phase, s.Generation = t.Start.Phase, t.Start.Generation
		}
		s.Stands = st.phaseStands(t.Start)
		since := func(at time.Time) time.Duration {
			if at.IsZero() {
				return 0
			}
			return max(now.Sub(at), 0)
		}
		switch over, at := st.phaseOver(t.Start); {
		case over:
			s.Signals = append(s.Signals, Signal{SignalPhaseOver, since(at)})
		case retired && t.Start != nil:
			s.Signals = append(s.Signals, Signal{SignalPhaseOver, 0})
		}
		if still := since(s.Stat.ModTime); quiet > 0 && !s.Stat.ModTime.IsZero() && still >= quiet {
			s.Signals = append(s.Signals, Signal{SignalQuiet, still})
		}
		if s.Stat.Span > 0 && float64(s.Stat.CPU) < IdleShare*float64(s.Stat.Span) {
			s.Signals = append(s.Signals, Signal{SignalIdle, s.Stat.Span})
		}
		if s.Declared > 0 && t.Age >= PastEnd*s.Declared {
			s.Signals = append(s.Signals, Signal{SignalPastEnd, t.Age - PastEnd*s.Declared})
		}
		var ended time.Time
		for _, id := range WaitsOn(t.Command(), t.Task.ID) {
			w := Waited{ID: id}
			if at, ok := st.Run.Gone[id]; ok {
				w.Ended, w.EndedAt = true, at
				if at.After(ended) {
					ended = at
				}
			}
			s.WaitsOn = append(s.WaitsOn, w)
		}
		if !ended.IsZero() {
			s.Signals = append(s.Signals, Signal{SignalWaitsOnEnded, since(ended)})
		}
		if t.Age >= LongTask {
			s.Signals = append(s.Signals, Signal{SignalLong, t.Age})
		}
		if len(s.Signals) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// LongTasks lists the rest of the model's own background work that st's loop knows runs (OwnTasks, but the
// suspects) that has run for LongTask or more, the longest first: subagents, workflows and monitors, to be
// named, never reviewed or stopped.
func LongTasks(st State, suspects []Suspect, now time.Time) []OwnTask {
	var out []OwnTask
	for _, t := range st.OwnTasks(now) {
		if t.Age >= LongTask && !slices.ContainsFunc(suspects, func(s Suspect) bool { return s.Task.ID == t.Task.ID }) {
			out = append(out, t)
		}
	}
	slices.SortStableFunc(out, func(a, b OwnTask) int { return cmp.Compare(b.Age, a.Age) })
	return out
}

// phaseOver reports whether the phase background work started in (b, as baton saw it start) is over for st's
// loop, and when it ended where that is known (zero when not): a plan replaced or dropped since, or one that
// has the phase no more; the phase done (or landed); or, in the lead's run, taken by a phase agent from the
// lead (handover.go), or set aside for one. Work baton did not see start in a phase is no phase's.
func (st State) phaseOver(b *BackgroundStart) (bool, time.Time) {
	if b == nil || b.Phase == "" {
		return false, time.Time{}
	}
	ps := st.Phases[b.Phase]
	switch {
	case b.Generation != st.Generation || st.Mode == ModeIdle || ps == nil:
		return true, time.Time{}
	case ps.Status == PhaseDone:
		return true, ps.DoneAt
	case ps.Status == PhaseWorking && ps.Handed != nil:
		return true, ps.Handed.At
	case ps.Status == PhaseWorking:
		return true, time.Time{}
	case ps.Status == PhaseParked && ps.Park != nil && ps.Park.Reason == ParkHanded:
		return true, ps.Park.Since
	}
	return false, time.Time{}
}

// phaseStands is where the phase background work started in (b) stands in st: its status, or PhaseGone for a phase
// of a plan no longer attached, or one the plan has no more; "" for work started in no phase.
func (st State) phaseStands(b *BackgroundStart) string {
	if b == nil || b.Phase == "" {
		return ""
	}
	if ps := st.Phases[b.Phase]; ps != nil && b.Generation == st.Generation && st.Mode != ModeIdle {
		return ps.Status
	}
	return PhaseGone
}
