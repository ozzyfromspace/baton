package statusline

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

func TestSegment(t *testing.T) {
	pl := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0", Title: "The overlay"}, {ID: "P1", Title: "A very long phase title that keeps going"}}}
	now := time.Now()
	run := state.Attach(pl, now, state.Origin{})
	run.Run.Context = &state.ContextUse{UsedPct: 41.4}
	deadline := time.Date(2026, 10, 5, 15, 33, 0, 0, time.Local)
	cases := []struct {
		name string
		st   func() state.State
		plan bool
		want string
	}{
		{"no plan", func() state.State { return state.New() }, false, "◆ baton"},
		{"running", func() state.State { return run }, true, "◆ baton · P0 1/2 The overlay · ctx 41%"},
		{"long title", func() state.State {
			s := run
			state.Done(&s, pl, "P0", now, state.DoneOpts{})
			state.Start(&s, now, state.Origin{})
			return s
		}, true,
			"◆ baton · P1 2/2 A very long phase title tha… · ctx 41%"},
		{"compacting", func() state.State { s := run; s.Run.Compaction.Status = state.CompactTyped; return s }, true, "◆ baton · compacting…"},
		// A queued compaction that something is holding must not claim to be compacting: that read
		// "compacting…" for 35 minutes on 2026-10-05 and looked exactly like a hung session.
		{"compaction held", func() state.State {
			s := run
			s.Run.Compaction.Status, s.Run.Gate = state.CompactQueued, "the human has a draft in the input box"
			return s
		}, true, "◆ baton · compact held: the human has a draft in the in…"},
		{"escalated", func() state.State { s := run; s.Run.Escalation = &state.Escalation{}; return s }, true, "◆ baton · ⚠ waiting on you"},
		{"paused", func() state.State { s := run; s.Mode = state.ModePaused; return s }, true, "◆ baton · paused"},
		{"proposal", func() state.State {
			s := run
			state.AddProposal(&s, state.Decision{What: "commit unsigned", Deadline: deadline}, now)
			return s
		}, true, "◆ baton · ? proposal · goes ahead " + deadline.Local().Format("15:04")},
		{"untimed proposal", func() state.State {
			s := run
			state.AddProposal(&s, state.Decision{What: "push the branch", Untimed: "'push' looks outward-facing"}, now)
			return s
		}, true, "◆ baton · ? proposal · waiting on you"},
		{"held proposal", func() state.State {
			s := run
			s.Decisions = nil
			s.Run.Escalation = &state.Escalation{Kind: "decision", Reason: "proposal d1 waits for you"}
			return s
		}, true, "◆ baton · ? proposal · waiting on you"},
		{"review due", func() state.State {
			s := run
			s.Run.Escalation = &state.Escalation{Kind: "review"}
			state.DueReview(&s, "P0", now)
			return s
		}, true, "◆ baton · ⚠ review due"},
		{"complete", func() state.State { s := run; s.Mode = state.ModeComplete; return s }, true, "◆ baton · ✓ plan complete"},
		{"tokens against the limit", func() state.State {
			s := run
			s.Run.Context = &state.ContextUse{UsedPct: 41, Tokens: 412_345, WindowSize: 1_000_000, Limit: 810_000}
			return s
		}, true, "◆ baton · P0 1/2 The overlay · ctx 412k/810k"},
		{"the gate of the phase in hand", func() state.State {
			s := withJobs(run, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-12 * time.Minute)})
			return s
		}, true, "◆ baton · P0 1/2 The overlay · P0 verifying 12m · ctx 41%"},
		{"a worker's question", func() state.State {
			s := run
			state.QueueRelay(&s, state.Relay{Phase: "P3", Kind: state.RelayBlocked, Since: now}, now)
			state.QueueRelay(&s, state.Relay{Phase: "P4", Kind: state.RelayBlocked, Since: now}, now)
			return s
		}, true, "◆ baton · P0 1/2 The overlay · ? P3 · ctx 41%"},
		{"a wait on something outside", func() state.State {
			return withJobs(run, state.Job{ID: "j1", Phase: "P0", Kind: state.JobAwait, Status: state.JobQueued, Queued: now.Add(-40 * time.Second)})
		}, true, "◆ baton · P0 1/2 The overlay · P0 awaiting 40s · ctx 41%"},
		{"the oldest job first, then how many more", func() state.State {
			s := withJobs(run,
				state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: state.JobPassed, Queued: now.Add(-3 * time.Hour)},
				state.Job{ID: "j2", Phase: "P0", Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-125 * time.Minute)},
				state.Job{ID: "j3", Phase: "P1", Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-time.Minute)})
			s.Phases["P0"].Status, s.Current = state.PhaseVerifying, "P1"
			s.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive}
			return s
		}, true, "◆ baton · P1 2/2 A very long phase title tha… · P0 verifying 2h05m +1 · ctx 41%"},
		{"a phase set aside while its job runs", func() state.State {
			s := withJobs(run, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-6 * time.Minute)})
			s.Phases["P0"].Status, s.Current = state.PhaseParked, "P1"
			s.Phases["P1"] = &state.PhaseState{Status: state.PhaseActive}
			return s
		}, true, "◆ baton · P1 2/2 A very long phase title tha… · P0 parked 6m · ctx 41%"},
		{"no phase in hand: every phase left waits on a job", func() state.State {
			s := withJobs(run, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: state.JobRunning, Queued: now.Add(-12 * time.Minute)})
			s.Phases["P0"].Status, s.Current = state.PhaseVerifying, ""
			return s
		}, true, "◆ baton · P0 verifying 12m · ctx 41%"},
		{"no phase in hand and no job", func() state.State { s := run; s.Current = ""; return s }, true, "◆ baton · ctx 41%"},
		{"a job queued a moment from now, by another clock", func() state.State {
			return withJobs(run, state.Job{ID: "j1", Phase: "P0", Kind: state.JobVerify, Status: state.JobQueued, Queued: now.Add(time.Second)})
		}, true, "◆ baton · P0 1/2 The overlay · P0 verifying 0s · ctx 41%"},
	}
	for _, c := range cases {
		if got := Segment(c.st(), pl, c.plan, now, 0); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The run's workers follow the phase in hand, each by its phase and how it stands, after a usage limit
// the run waits on, or a reading that holds new workers back.
func TestWorkerSegments(t *testing.T) {
	pl := plan.Plan{Version: 1, Phases: []plan.Phase{{ID: "P0", Title: "The overlay", After: []string{}}, {ID: "P1", Title: "One", After: []string{}},
		{ID: "P2", Title: "Two", After: []string{}}, {ID: "P3", Title: "Three", After: []string{}}}}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.Local)
	run := state.Attach(pl, now.Add(-time.Hour), state.Origin{})
	run.Run.Context = &state.ContextUse{UsedPct: 41.4}
	worker := func(phase, status string, assigned time.Duration) state.Worker {
		return state.Worker{Phase: phase, Ref: "r/" + phase, Session: "s-" + phase, Status: status, Assigned: now.Add(-assigned), Since: now.Add(-assigned)}
	}
	with := func(ws ...state.Worker) state.State {
		st := withJobs(run)
		st.Workers = ws
		for _, w := range ws {
			st.Phases[w.Phase] = &state.PhaseState{Status: state.PhaseWorking, Worker: w.Ref}
		}
		return st
	}
	const head = "◆ baton · P0 1/4 The overlay · "
	for _, c := range []struct {
		name     string
		st       func() state.State
		usageMax float64
		want     string
	}{
		{"a worker at work, by how long", func() state.State { return with(worker("P1", state.WorkerWorking, 14*time.Minute)) }, 0, head + "W P1 14m · ctx 41%"},
		{"one starting", func() state.State { return with(worker("P1", state.WorkerStarting, 0)) }, 0, head + "W P1 starting · ctx 41%"},
		{"several, in the order they started", func() state.State {
			return with(worker("P2", state.WorkerWorking, time.Hour+5*time.Minute), worker("P1", state.WorkerWorking, 3*time.Minute))
		}, 0, head + "W P2 1h05m · W P1 3m · ctx 41%"},
		{"one whose work waits to land", func() state.State {
			w := worker("P1", state.WorkerLanding, time.Hour)
			w.Merge = &state.Merge{Queued: now}
			return with(w)
		}, 0, head + "P1 landing · ctx 41%"},
		{"one whose landing is held back", func() state.State {
			w := worker("P1", state.WorkerLanding, time.Hour)
			w.Merge = &state.Merge{Queued: now, Deferred: "the lead changed a.go too", DeferredAt: now}
			return with(w)
		}, 0, head + "P1 waits to land · ctx 41%"},
		{"one with a question the lead puts to the human", func() state.State {
			st := with(worker("P2", state.WorkerWorking, time.Hour), worker("P1", state.WorkerWorking, time.Hour))
			state.QueueRelay(&st, state.Relay{Phase: "P1", Kind: state.RelayBlocked, Since: now}, now)
			return st
		}, 0, head + "W P2 1h00m · ? P1 · ctx 41%"},
		{"one waiting on the human in its own session", func() state.State {
			w := worker("P1", state.WorkerWorking, time.Hour)
			w.Needs = "a permission prompt for Bash"
			st := with(w)
			state.QueueRelay(&st, state.Relay{Phase: "P1", Kind: state.RelayBlocked, Since: now}, now)
			return st
		}, 0, head + "! P1 needs you · ctx 41%"},
		{"landed and failed workers are gone", func() state.State {
			st := with(worker("P1", state.WorkerLanded, time.Hour), worker("P2", state.WorkerFailed, time.Hour))
			st.Phases["P1"].Status, st.Phases["P2"].Status = state.PhaseDone, state.PhasePending
			return st
		}, 0, head + "ctx 41%"},
		{"a usage limit the run waits on", func() state.State {
			st := with(worker("P1", state.WorkerWorking, time.Minute))
			st.Usage = &state.Usage{UsedPct: 100, Limited: now.Add(40 * time.Minute), Hit: now}
			return st
		}, 80, head + "usage limit until " + now.Add(40*time.Minute).Format("15:04") + " · W P1 1m · ctx 41%"},
		{"a limit that is over", func() state.State {
			st := with()
			st.Usage = &state.Usage{UsedPct: 12, Limited: now, Hit: now.Add(-time.Hour)}
			return st
		}, 80, head + "ctx 41%"},
		{"a reading that holds new workers back", func() state.State {
			st := with()
			st.Usage = &state.Usage{UsedPct: 84.6, Resets: now.Add(time.Hour)}
			return st
		}, 80, head + "5h 85% · ctx 41%"},
		{"a reading at it", func() state.State {
			st := with()
			st.Usage = &state.Usage{UsedPct: 80, Resets: now.Add(time.Hour)}
			return st
		}, 80, head + "5h 80% · ctx 41%"},
		{"a reading under it", func() state.State {
			st := with()
			st.Usage = &state.Usage{UsedPct: 79, Resets: now.Add(time.Hour)}
			return st
		}, 80, head + "ctx 41%"},
		{"a reading of a window gone by", func() state.State {
			st := with()
			st.Usage = &state.Usage{UsedPct: 95, Resets: now.Add(-time.Minute)}
			return st
		}, 80, head + "ctx 41%"},
		{"no workers: no reading", func() state.State {
			st := with()
			st.Usage = &state.Usage{UsedPct: 95, Resets: now.Add(time.Hour)}
			return st
		}, 0, head + "ctx 41%"},
		{"no room on the machine for another", func() state.State {
			st := with(worker("P1", state.WorkerWorking, time.Minute))
			st.Hold = &state.Hold{Phase: "P2", Kind: state.HoldBusy, Why: "memory pressure is critical", Since: now}
			return st
		}, 0, head + "W P1 1m · W held: busy · ctx 41%"},
		{"gates queue for lanes", func() state.State {
			st := with(worker("P1", state.WorkerWorking, time.Minute))
			st.Hold = &state.Hold{Phase: "P2", Kind: state.HoldLanes, Why: "a gate waits for a lane", Since: now}
			return st
		}, 0, head + "W P1 1m · W held: lanes · ctx 41%"},
		{"landings keep conflicting", func() state.State {
			st := with(worker("P1", state.WorkerWorking, time.Minute))
			st.Hold = &state.Hold{Phase: "P2", Kind: state.HoldConflicts, Why: "2 landings in a row conflicted", Since: now}
			return st
		}, 0, head + "W P1 1m · W held: conflicts · ctx 41%"},
		{"held by the usage, which shows already", func() state.State {
			st := with(worker("P1", state.WorkerWorking, time.Minute))
			st.Usage = &state.Usage{UsedPct: 90, Resets: now.Add(time.Hour)}
			st.Hold = &state.Hold{Phase: "P2", Kind: state.HoldUsage, Why: "90% of the five-hour usage is used (worker_usage_max 80%)", Since: now}
			return st
		}, 80, head + "5h 90% · W P1 1m · ctx 41%"},
	} {
		if got := Segment(c.st(), pl, true, now, c.usageMax); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// withJobs is st with jobs on record, and a copy of its phases to change.
func withJobs(st state.State, jobs ...state.Job) state.State {
	phases := map[string]*state.PhaseState{}
	for id, ps := range st.Phases {
		x := *ps
		phases[id] = &x
	}
	st.Phases, st.Jobs = phases, jobs
	return st
}

func TestUserCommandPrecedenceSkipsBaton(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	write := func(path, cmd string) {
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(`{"statusLine":{"type":"command","command":`+quote(cmd)+`}}`), 0o644)
	}
	if UserCommand(proj, home) != "" {
		t.Fatal("found a command where none is configured")
	}
	write(filepath.Join(home, ".claude", "settings.json"), "user-line")
	write(filepath.Join(proj, ".claude", "settings.json"), "project-line")
	if got := UserCommand(proj, home); got != "project-line" {
		t.Fatalf("project should beat user: %q", got)
	}
	write(filepath.Join(proj, ".claude", "settings.local.json"), "'/opt/baton/bin/baton' statusline")
	if got := UserCommand(proj, home); got != "project-line" {
		t.Fatalf("baton's own command must be skipped: %q", got)
	}
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func TestRunUserAndCompose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	out := RunUser(`read x; echo "user: $x"; echo second`, []byte("hello\n"), t.TempDir(), 2*time.Second)
	if got := Compose("◆ baton", out); got != "◆ baton  user: hello\nsecond" {
		t.Fatalf("%q", got)
	}
	if got := RunUser("sleep 5", nil, t.TempDir(), 100*time.Millisecond); got != "" {
		t.Fatalf("slow command: %q", got)
	}
	if Compose("◆ baton", "") != "◆ baton" {
		t.Fatal("empty user line")
	}
}

func TestParse(t *testing.T) {
	in := Parse([]byte(`{"workspace":{"project_dir":"/p"},"context_window":{"used_percentage":12.5,"context_window_size":400000}}`))
	if in.Workspace.ProjectDir != "/p" || in.ContextWindow.UsedPercentage == nil || *in.ContextWindow.UsedPercentage != 12.5 || in.ContextWindow.ContextWindowSize != 400000 {
		t.Fatalf("%+v", in)
	}
	if Parse([]byte(`{"context_window":{"used_percentage":null}}`)).ContextWindow.UsedPercentage != nil {
		t.Fatal("null percentage")
	}
}

// The five-hour limit, as Claude Code 2.1.296 documents its status line input: used_percentage (0 to 100)
// and resets_at (Unix seconds), present only while its reset has not passed.
func TestFiveHour(t *testing.T) {
	now := time.Unix(1_790_000_000, 0)
	resets := now.Add(2 * time.Hour)
	for _, c := range []struct {
		name, raw string
		used      float64
		resets    time.Time
		ok        bool
	}{
		{"present", fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":%d},"seven_day":{"used_percentage":41.2,"resets_at":%d}}}`,
			resets.Unix(), now.Add(72*time.Hour).Unix()), 23.5, resets, true},
		{"at its limit", fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":100,"resets_at":%d}}}`, resets.Unix()), 100, resets, true},
		{"no reset said", `{"rate_limits":{"five_hour":{"used_percentage":61}}}`, 61, time.Time{}, true},
		{"absent: an API key, or before the first response", `{"context_window":{"used_percentage":4}}`, 0, time.Time{}, false},
		{"null", `{"rate_limits":null}`, 0, time.Time{}, false},
		{"the weekly limit only", fmt.Sprintf(`{"rate_limits":{"seven_day":{"used_percentage":90,"resets_at":%d}}}`, resets.Unix()), 0, time.Time{}, false},
		{"no percentage", fmt.Sprintf(`{"rate_limits":{"five_hour":{"resets_at":%d}}}`, resets.Unix()), 0, time.Time{}, false},
		{"past its reset", fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":97,"resets_at":%d}}}`, now.Add(-time.Second).Unix()), 0, time.Time{}, false},
		{"resetting now", fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":97,"resets_at":%d}}}`, now.Unix()), 0, time.Time{}, false},
	} {
		used, at, ok := Parse([]byte(c.raw)).FiveHour(now)
		if used != c.used || !at.Equal(c.resets) || ok != c.ok {
			t.Errorf("%s: %v %v %v, want %v %v %v", c.name, used, at, ok, c.used, c.resets, c.ok)
		}
	}
}

// Captured from Claude Code 2.1.289 (claude-opus-5-5[1m], --autocompact 810k): the window is the model's,
// not the cap, and the percentage is rounded to a whole point (10k tokens on this model).
func TestTokens(t *testing.T) {
	for _, c := range []struct {
		name, raw string
		tokens    int
		ok        bool
	}{
		{"before the first response", `{"context_window":{"context_window_size":1000000,"current_usage":null,"remaining_percentage":null,"total_input_tokens":0,"total_output_tokens":0,"used_percentage":null}}`, 0, false},
		{"exact", `{"context_window":{"context_window_size":1000000,"current_usage":{"cache_creation_input_tokens":1962,"cache_read_input_tokens":40616,"input_tokens":2,"output_tokens":4},"remaining_percentage":96,"total_input_tokens":42580,"total_output_tokens":4,"used_percentage":4}}`, 42_580, true},
		{"percentage only", `{"context_window":{"context_window_size":200000,"used_percentage":18}}`, 36_000, true},
	} {
		if tokens, ok := Parse([]byte(c.raw)).Tokens(); tokens != c.tokens || ok != c.ok {
			t.Errorf("%s: %d %v", c.name, tokens, ok)
		}
	}
}
