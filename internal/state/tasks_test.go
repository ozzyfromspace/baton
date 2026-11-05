package state

import (
	"maps"
	"slices"
	"testing"
	"time"
)

// The 74-phase campaign's loop (its paths' project left out): started in the background by a subagent of the
// lead's model in P23, to wait for a pytest task's last lines that never came.
const (
	campaignTasks = "/private/tmp/claude-501/-Users-me-Projects-app/77ee516d-53ba-4648-a763-0a22ca6723f9/tasks/"
	campaignLoop  = "until [ -s " + campaignTasks + "bj514tpgn.output ] && tail -2 " + campaignTasks + "bj514tpgn.output | grep -qE \"passed|failed|error|Error\"; do sleep 5; done; tail -40 " + campaignTasks + "bj514tpgn.output"
)

// tasksRun is a running plan of three phases, P23 done 21 hours ago, P24 in hand and P25 not started, in its
// second generation, at now.
func tasksRun(now time.Time) State {
	st := New()
	st.Mode, st.Generation, st.Current = ModeRunning, 2, "P24"
	st.Phases = map[string]*PhaseState{
		"P23": {Status: PhaseDone, DoneAt: now.Add(-21 * time.Hour)},
		"P24": {Status: PhaseActive, StartedAt: now.Add(-21 * time.Hour)},
		"P25": {Status: PhasePending},
	}
	st.Run.TasksDir = campaignTasks
	return st
}

// shell adds a background shell of the model's own to st's loop, started at at in phase (of the plan attached
// now, "" for none), running command with description, as the last Stop listed it; started is false for one
// whose start baton did not see.
func shell(st *State, id, phase, command, description string, at time.Time, started bool) {
	st.Run.Background = append(st.Run.Background, Task{ID: id, Type: TaskShell, Status: "running", Description: description, Command: command, Since: at})
	if started {
		st.Run.Start(id, BackgroundStart{Type: TaskShell, Tool: "Bash", Command: command, Description: description, Phase: phase,
			Generation: st.Generation, Output: st.Run.TaskOutput(id), At: at})
	}
}

func TestSuspects(t *testing.T) {
	now := time.Date(2026, 11, 5, 16, 10, 0, 0, time.UTC)
	quiet := 30 * time.Minute
	wrote := func(ago time.Duration) TaskStat { return TaskStat{Size: 1 << 10, ModTime: now.Add(-ago)} }
	for _, c := range []struct {
		name  string
		setup func(st *State)
		stats map[string]TaskStat
		want  map[string][]string // each suspect's signals, by task
		check func(t *testing.T, s Suspect)
	}{
		{
			name: "the campaign's loop: 22 hours, empty output, its phase done, its target ended",
			setup: func(st *State) {
				shell(st, "b8196fcr0", "P23", campaignLoop, "Wait for pytest output then show results", now.Add(-22*time.Hour), true)
				st.Run.BackgroundStarts["b8196fcr0"] = func(b BackgroundStart) BackgroundStart { b.Agent = "a49d677f86ae3ba93"; return b }(st.Run.BackgroundStarts["b8196fcr0"])
				st.Run.Gone = map[string]time.Time{"bj514tpgn": now.Add(-21*time.Hour - 45*time.Minute)}
			},
			stats: map[string]TaskStat{"b8196fcr0": {ModTime: now.Add(-22 * time.Hour)}},
			want:  map[string][]string{"b8196fcr0": {SignalPhaseOver, SignalQuiet, SignalWaitsOnEnded, SignalLong}},
			check: func(t *testing.T, s Suspect) {
				if s.Phase != "P23" || s.Stands != PhaseDone || s.Generation != 2 || s.Command() != campaignLoop || s.Description() != "Wait for pytest output then show results" {
					t.Errorf("facts: %+v", s)
				}
				if want := []Waited{{ID: "bj514tpgn", Ended: true, EndedAt: now.Add(-21*time.Hour - 45*time.Minute)}}; !slices.Equal(s.WaitsOn, want) {
					t.Errorf("waits on %+v, want %+v", s.WaitsOn, want)
				}
				for _, g := range []Signal{{SignalPhaseOver, 21 * time.Hour}, {SignalQuiet, 22 * time.Hour}, {SignalWaitsOnEnded, 21*time.Hour + 45*time.Minute}, {SignalLong, 22 * time.Hour}} {
					if !slices.Contains(s.Signals, g) {
						t.Errorf("signals %+v lack %+v", s.Signals, g)
					}
				}
				if s.Age != 22*time.Hour || s.Start.Output != campaignTasks+"b8196fcr0.output" {
					t.Errorf("age %s, output %q", s.Age, s.Start.Output)
				}
			},
		},
		{
			name: "a dev server writing within 30 minutes, in the phase in hand",
			setup: func(st *State) {
				shell(st, "bdev", "P24", "pnpm dev", "Run the dev server", now.Add(-50*time.Minute), true)
			},
			stats: map[string]TaskStat{"bdev": wrote(2 * time.Minute)},
		},
		{
			name: "the same server quiet for 31 minutes",
			setup: func(st *State) {
				shell(st, "bdev", "P24", "pnpm dev", "Run the dev server", now.Add(-50*time.Minute), true)
			},
			stats: map[string]TaskStat{"bdev": wrote(31 * time.Minute)},
			want:  map[string][]string{"bdev": {SignalQuiet}},
			check: func(t *testing.T, s Suspect) {
				if s.Stands != PhaseActive || s.Signals[0].For != 31*time.Minute || s.Stat.Size != 1<<10 {
					t.Errorf("%+v", s)
				}
			},
		},
		{
			name: "a shell declared ~10m, running 31 minutes",
			setup: func(st *State) {
				shell(st, "bbuild", "P24", "make bundle", "Build the bundle (~10m)", now.Add(-31*time.Minute), true)
			},
			stats: map[string]TaskStat{"bbuild": wrote(time.Minute)},
			want:  map[string][]string{"bbuild": {SignalPastEnd}},
			check: func(t *testing.T, s Suspect) {
				if s.Declared != 10*time.Minute || s.Signals[0].For != time.Minute {
					t.Errorf("declared %s, %+v", s.Declared, s.Signals)
				}
			},
		},
		{
			name: "nearly idle: under 1% of a core over the last look",
			setup: func(st *State) {
				shell(st, "bserve", "P24", "python -m http.server", "Serve the docs", now.Add(-10*time.Minute), true)
			},
			stats: map[string]TaskStat{"bserve": {ModTime: now.Add(-time.Minute), CPU: 100 * time.Millisecond, Span: time.Minute}},
			want:  map[string][]string{"bserve": {SignalIdle}},
		},
		{
			name: "busy: 2 seconds of CPU in a minute",
			setup: func(st *State) {
				shell(st, "bserve", "P24", "python -m http.server", "Serve the docs", now.Add(-10*time.Minute), true)
			},
			stats: map[string]TaskStat{"bserve": {ModTime: now.Add(-time.Minute), CPU: 2 * time.Second, Span: time.Minute}},
		},
		{
			name: "a task from before the plan never has phase_over",
			setup: func(st *State) {
				shell(st, "bold", "", "npm run watch", "Watch the styles", now.Add(-23*time.Hour), true) // seen to start with no plan
				shell(st, "bunseen", "", "npm run serve", "Serve", now.Add(-30*time.Minute), false)      // never seen to start
			},
			stats: map[string]TaskStat{"bold": wrote(2 * time.Hour), "bunseen": wrote(40 * time.Minute)},
			want:  map[string][]string{"bold": {SignalQuiet, SignalLong}, "bunseen": {SignalQuiet}},
			check: func(t *testing.T, s Suspect) {
				if s.Phase != "" || s.Stands != "" {
					t.Errorf("a phase for the conversation's work: %+v", s)
				}
			},
		},
		{
			name:  "with no stat, judged on its other signals",
			setup: func(st *State) { shell(st, "bnostat", "P23", "sleep 100000", "", now.Add(-10*time.Minute), true) },
			want:  map[string][]string{"bnostat": {SignalPhaseOver}},
		},
		{
			name: "a phase of an earlier plan, and one a phase agent took from the lead",
			setup: func(st *State) {
				shell(st, "bearlier", "P24", "pnpm dev", "", now.Add(-10*time.Minute), true)
				st.Run.BackgroundStarts["bearlier"] = func(b BackgroundStart) BackgroundStart { b.Generation = 1; return b }(st.Run.BackgroundStarts["bearlier"])
				st.Phases["P25"] = &PhaseState{Status: PhaseWorking, Handed: &Handed{At: now.Add(-5 * time.Minute)}}
				shell(st, "bhanded", "P25", "pnpm dev", "", now.Add(-10*time.Minute), true)
			},
			want: map[string][]string{"bearlier": {SignalPhaseOver}, "bhanded": {SignalPhaseOver}},
			check: func(t *testing.T, s Suspect) {
				switch s.Task.ID {
				case "bearlier":
					if s.Stands != PhaseGone || s.Signals[0].For != 0 {
						t.Errorf("%+v", s)
					}
				case "bhanded":
					if s.Stands != PhaseWorking || s.Signals[0].For != 5*time.Minute {
						t.Errorf("%+v", s)
					}
				}
			},
		},
		{
			name: "a subagent, a workflow, a monitor and a phase agent are never suspects",
			setup: func(st *State) {
				st.Run.Background = append(st.Run.Background,
					Task{ID: "asub", Type: TaskSubagent, Status: "running", Since: now.Add(-3 * time.Hour)},
					Task{ID: "wflow", Type: TaskWorkflow, Status: "running", Since: now.Add(-3 * time.Hour)},
					Task{ID: "aphase", Type: TaskSubagent, Status: "running", AgentType: PhaseAgent, Since: now.Add(-3 * time.Hour)})
				st.Run.Start("asub", BackgroundStart{Type: TaskSubagent, Tool: "Agent", Phase: "P23", Generation: 2, At: now.Add(-3 * time.Hour)})
				shell(st, "mwatch", "P23", "tail -F app.log", "watch the log", now.Add(-3*time.Hour), false)
				st.Run.Watch("mwatch") // a Monitor, which Claude Code lists as a shell
			},
			stats: map[string]TaskStat{"asub": wrote(3 * time.Hour), "mwatch": wrote(3 * time.Hour)},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := tasksRun(now)
			c.setup(&st)
			got := Suspects(st, c.stats, quiet, now)
			whys := map[string][]string{}
			for _, s := range got {
				whys[s.Task.ID] = s.Whys()
			}
			if !maps.EqualFunc(whys, c.want, slices.Equal) {
				t.Fatalf("suspects %v, want %v", whys, c.want)
			}
			for _, s := range got {
				if c.check != nil {
					c.check(t, s)
				}
			}
		})
	}
}

// A sub-run whose worker baton let go of: each shell its loop saw start has phase_over, whatever its phase's
// status there says; one it did not see start may be another loop's, and is judged on its own signals.
func TestRetiredSuspects(t *testing.T) {
	now := time.Date(2026, 11, 5, 16, 10, 0, 0, time.UTC)
	st := New()
	st.Mode, st.Generation, st.Current = ModeRunning, 1, "P7"
	st.Phases = map[string]*PhaseState{"P7": {Status: PhaseActive}}
	shell(&st, "bmine", "P7", "pnpm dev", "Run the dev server", now.Add(-20*time.Minute), true)
	shell(&st, "bother", "", "pnpm dev", "Run the dev server", now.Add(-20*time.Minute), false)
	stats := map[string]TaskStat{"bmine": {ModTime: now}, "bother": {ModTime: now}}
	if got := Suspects(st, stats, 30*time.Minute, now); len(got) != 0 {
		t.Fatalf("suspects while its worker works: %+v", got)
	}
	got := RetiredSuspects(st, stats, 30*time.Minute, now)
	if len(got) != 1 || got[0].Task.ID != "bmine" || !slices.Equal(got[0].Whys(), []string{SignalPhaseOver}) {
		t.Fatalf("retired: %+v", got)
	}
}

// The rest of the model's own work that runs an hour or more is named, the longest first: never a suspect,
// and never a phase agent.
func TestLongTasks(t *testing.T) {
	now := time.Date(2026, 11, 5, 16, 10, 0, 0, time.UTC)
	st := tasksRun(now)
	st.Run.Background = []Task{
		{ID: "asub", Type: TaskSubagent, Status: "running", Since: now.Add(-2 * time.Hour)},
		{ID: "aphase", Type: TaskSubagent, Status: "running", AgentType: PhaseAgentAt("high"), Since: now.Add(-5 * time.Hour)},
		{ID: "dream", Type: "dream", Status: "running", Since: now.Add(-5 * time.Hour)},
		{ID: "ashort", Type: TaskSubagent, Status: "running", Since: now.Add(-10 * time.Minute)},
	}
	shell(&st, "mwatch", "", "tail -F app.log", "watch the log", now.Add(-3*time.Hour), true)
	st.Run.BackgroundStarts["mwatch"] = BackgroundStart{Type: TaskMonitor, Tool: "Monitor", At: now.Add(-3 * time.Hour)}
	shell(&st, "bloop", "P23", campaignLoop, "", now.Add(-22*time.Hour), true)
	suspects := Suspects(st, nil, 30*time.Minute, now)
	var ids []string
	for _, l := range LongTasks(st, suspects, now) {
		ids = append(ids, l.Task.ID+":"+l.Kind)
	}
	if want := []string{"mwatch:monitor", "asub:subagent"}; !slices.Equal(ids, want) {
		t.Fatalf("long %v, want %v (suspects %d)", ids, want, len(suspects))
	}
}

// A start seen since the last Stop counts as running until a Stop says otherwise; a Stop that no longer lists a
// task forgets its start.
func TestOwnTasksAndTheirStarts(t *testing.T) {
	now := time.Date(2026, 11, 5, 16, 10, 0, 0, time.UTC)
	st := tasksRun(now)
	shell(&st, "b1", "P24", "pnpm dev", "Run the dev server", now.Add(-time.Hour), true)
	st.Run.Start("b2", BackgroundStart{Type: TaskShell, Command: "make bundle", Phase: "P24", Generation: 2, At: now.Add(-time.Minute)})
	if st.Run.Start("b2", BackgroundStart{Type: TaskShell, At: now}) {
		t.Error("a second record of a start was taken")
	}
	var ids []string
	for _, o := range st.OwnTasks(now) {
		ids = append(ids, o.Task.ID)
	}
	if !slices.Equal(ids, []string{"b1", "b2"}) {
		t.Fatalf("own %v", ids)
	}
	if got := st.Run.TaskOutput("b9"); got != campaignTasks+"b9.output" {
		t.Errorf("an unseen task's output: %q", got)
	}
	if known := st.Run.Known(); !slices.Equal(known, []string{"b1", "b2"}) {
		t.Errorf("known %v", known)
	}
	st.Run.SetBackground([]Task{{ID: "b2", Type: TaskShell, Status: "running"}}, now)
	if _, ok := st.Run.BackgroundStarts["b1"]; ok || len(st.Run.BackgroundStarts) != 1 {
		t.Errorf("starts after b1 ended: %v", st.Run.BackgroundStarts)
	}
	st.Run.SetBackground(nil, now)
	if st.Run.BackgroundStarts != nil {
		t.Errorf("starts after all ended: %v", st.Run.BackgroundStarts)
	}
}

// When a task that work of the loop's waits on ended is the first Stop that no longer listed it, whoever started
// it; it is forgotten once nothing waits on it.
func TestNoteGone(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 18, 9, 0, 0, time.UTC)
	st := tasksRun(t0)
	shell(&st, "b8196fcr0", "P23", campaignLoop, "", t0, true)
	pytest := Task{ID: "bj514tpgn", Type: TaskShell, Status: "running"}
	loop := st.Run.Background[0]
	st.Run.NoteGone([]Task{loop, pytest}, t0.Add(time.Minute))
	if st.Run.Gone != nil {
		t.Fatalf("gone while listed: %v", st.Run.Gone)
	}
	ended := t0.Add(16 * time.Minute)
	st.Run.NoteGone([]Task{loop}, ended)
	st.Run.NoteGone([]Task{loop}, ended.Add(time.Hour))
	if got := st.Run.Gone["bj514tpgn"]; !got.Equal(ended) || len(st.Run.Gone) != 1 {
		t.Fatalf("gone %v, want bj514tpgn at %s", st.Run.Gone, ended)
	}
	st.Run.SetBackground(nil, ended.Add(2*time.Hour))
	st.Run.NoteGone(nil, ended.Add(2*time.Hour))
	if st.Run.Gone != nil {
		t.Fatalf("gone once nothing waits: %v", st.Run.Gone)
	}
}

func TestWaitsOn(t *testing.T) {
	for _, c := range []struct {
		command, self string
		want          []string
	}{
		{campaignLoop, "b8196fcr0", []string{"bj514tpgn"}},
		{"F=/tmp/claude-501/p/s/tasks/bj514tpgn.output; until grep -q x \"$F\"; do sleep 5; done; cat /tmp/claude-501/p/s/tasks/b2.output", "", []string{"bj514tpgn", "b2"}},
		{"tail -f /tmp/claude-501/p/s/tasks/bself.output", "bself", nil},
		{"cat tasks/notes.md", "", nil},
		{"pnpm dev", "", nil},
	} {
		if got := WaitsOn(c.command, c.self); !slices.Equal(got, c.want) {
			t.Errorf("WaitsOn(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}

func TestDeclaredEnd(t *testing.T) {
	for _, c := range []struct {
		description string
		want        time.Duration
	}{
		{"Run the e2e suite (~15m)", 15 * time.Minute},
		{"Build the bundle, 15 min", 15 * time.Minute},
		{"Soak test for 2h", 2 * time.Hour},
		{"Load test, about 2 hours", 2 * time.Hour},
		{"Migrate the data (~1.5h)", 90 * time.Minute},
		{"Warm the cache, ~90s", 90 * time.Second},
		{"Reindex, 1h30m", 90 * time.Minute},
		{"Benchmark ~ 3 mins", 3 * time.Minute},
		{"Retry every 5m, done in ~20m", 20 * time.Minute}, // the one marked ~ wins
		{"Poll every 5s for 10 minutes", 5 * time.Second},  // else the first
		{"Run the dev server until P12's browser checks pass", 0},
		{"Download the 5mb fixture", 0},
		{"Run P4's gate", 0},
		{"", 0},
		{"Run for 0m", 0},
	} {
		if got := DeclaredEnd(c.description); got != c.want {
			t.Errorf("DeclaredEnd(%q) = %s, want %s", c.description, got, c.want)
		}
	}
}
