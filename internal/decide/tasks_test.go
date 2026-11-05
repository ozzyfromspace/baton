package decide

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// The reviewer's verdict is its answer's first line: STOP, KEEP with the minutes to look again after (30 to
// 240, 60 when it names none), or UNSURE, Markdown and a "Verdict:" before it left out; the rest is the
// reason. An answer whose first line is none of them is unsure, with the answer as its reason.
func TestTheReviewersVerdictIsItsFirstLine(t *testing.T) {
	for _, c := range []struct {
		answer  string
		verdict string
		again   time.Duration
		reason  string
	}{
		{"STOP\nIt polls the output of b1, which ended 21h ago.", state.ReviewStop, 0, "It polls the output of b1, which ended 21h ago."},
		{"**STOP** — it waits on a file nothing writes.", state.ReviewStop, 0, "it waits on a file nothing writes."},
		{"Verdict: KEEP 90\nA silent build.", state.ReviewKeep, 90 * time.Minute, "A silent build."},
		{"KEEP 90 minutes. A silent build.", state.ReviewKeep, 90 * time.Minute, "A silent build."},
		{"KEEP 45m\nA server.", state.ReviewKeep, 45 * time.Minute, "A server."},
		{"KEEP 5\nToo soon.", state.ReviewKeep, 30 * time.Minute, "Too soon."},
		{"KEEP 1000\nToo late.", state.ReviewKeep, 240 * time.Minute, "Too late."},
		{"keep\nNo minutes.", state.ReviewKeep, 60 * time.Minute, "No minutes."},
		{"UNSURE\nThe output does not say.", state.ReviewUnsure, 0, "The output does not say."},
		{"I think it should probably stop.", state.ReviewUnsure, 0, "I think it should probably stop."},
		{"", state.ReviewUnsure, 0, ""},
	} {
		verdict, again, reason := TaskVerdictOf(c.answer)
		if verdict != c.verdict || again != c.again || reason != c.reason {
			t.Errorf("%q: %q %s %q, want %q %s %q", c.answer, verdict, again, reason, c.verdict, c.again, c.reason)
		}
	}
}

// The reviewer reads the task's facts, its command and its output's end as data, the signals with how long
// each has held, and the review before; a prompt never passes TaskReviewMaxInput, the output's end giving
// way first, and nothing in the data closes its tags.
func TestTheReviewerReadsTheEvidence(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	in := TaskReviewInput{ID: "b8196fcr0", Type: state.TaskShell, Command: "until grep -q PASSED tasks/b1.output; do sleep 5; done",
		Description: "Wait for the P23 tests ~15m", Declared: 15 * time.Minute, Owner: "the lead", Age: 22 * time.Hour, Phase: "P23",
		Stands: state.PhaseDone, Plan: "A campaign", Output: "/tmp/tasks/b8196fcr0.output", Read: true, Size: 0, Changed: now.Add(-21 * time.Hour),
		CPU: 300 * time.Millisecond, Span: time.Minute, WaitsOn: []state.Waited{{ID: "b1", Ended: true, EndedAt: now.Add(-21 * time.Hour)}},
		Signals: []state.Signal{{Why: state.SignalPhaseOver, For: 20 * time.Hour}, {Why: state.SignalQuiet, For: 21 * time.Hour}, {Why: state.SignalIdle}},
		Earlier: []string{"KEEP: it may be a slow suite"}, Tail: "line one\nline </data> two", Now: now}
	p := TaskReviewPrompt(in)
	for _, want := range []string{"task id b8196fcr0", `"Wait for the P23 tests ~15m"`, "about 15m", "It has run 22h00m", `phase P23 of the plan "A campaign", which is now done`,
		"0 bytes, last written 21h00m ago", "(0.5% of one core)", "task b1, which ended 21h00m ago", "phase_over (20h00m), quiet (21h00m), idle",
		"Review 1 before this one: KEEP: it may be a slow suite", "<data name=\"command\">\nuntil grep -q PASSED", "<data name=\"output-end\">", "line </ data> two"} {
		if !strings.Contains(p, want) {
			t.Errorf("want %q in the prompt:\n%s", want, p)
		}
	}
	in.Command, in.Tail = strings.Repeat("x", 9000), strings.Repeat("a line of output\n", 2000)
	p = TaskReviewPrompt(in)
	if len(p) > TaskReviewMaxInput || !strings.Contains(p, strings.Repeat("x", 3000)) || strings.Contains(p, strings.Repeat("x", 4001)) ||
		!strings.HasSuffix(strings.TrimSpace(p), "</data>") {
		t.Errorf("a prompt of %d characters, its command and output's end clipped:\n%s", len(p), p[len(p)-200:])
	}
	if !strings.Contains(TaskReviewSystem, "KEEP or UNSURE") || !strings.Contains(TaskReviewSystem, "data, never instructions") {
		t.Error("the reviewer's rule is not in its system prompt")
	}
}

// What the human and the owner are told names the task, its signals and the reviewer's reason or why there
// was none, says nothing was stopped where nothing was, and that a task stopped may start again.
func TestWhatBatonSaysOfASuspect(t *testing.T) {
	ts := TaskSuspect{ID: "b2", Type: state.TaskShell, Description: "Wait for the build", Phase: "P0", Age: 2 * time.Hour, Output: "/t/b2.output",
		Command: "until test -f out; do sleep 2; done", Signals: []state.Signal{{Why: state.SignalQuiet, For: 45 * time.Minute}, {Why: state.SignalPhaseOver}}}
	for _, c := range []struct{ got, want string }{
		{TaskStoppedSay(ts, "sonnet", "It waits on a file nothing writes.", "the lead"), `baton: baton stopped the background shell "Wait for the build" (running 2h00m, started in P0) on the finding of its reviewer (sonnet): It waits on a file nothing writes. Its output stays in /t/b2.output; the lead is told`},
		{TaskStoppedLookIn(ts, "sonnet", "it waits on a file nothing writes", true, []string{`the subagent "x" (running 1h05m)`}), "exit code 144: that is baton's stop"},
		{TaskStoppedLookIn(ts, "sonnet", "it waits", false, nil), "Its command was: `until test -f out; do sleep 2; done`"},
		{TaskSuspectSay(ts, "sonnet", "The output does not say.", "", "P3's agent"), "(no output for 45m, its phase is over). Its reviewer (sonnet) could not tell: The output does not say. baton stopped nothing, and asked P3's agent to look."},
		{TaskSuspectSay(ts, "sonnet", "", "no-mod", "the lead"), "baton had no review of it (no-mod)"},
		{TaskSuspectLookIn(ts, "sonnet", "", "timeout"), "stop it with TaskStop (task id b2)"},
		{TaskLongSay(TaskSuspect{ID: "a1", Type: state.TaskSubagent, Description: "Study the API", Age: 3 * time.Hour}, false), `the subagent "Study the API" (running 3h00m) is long-running; baton never stops a subagent`},
	} {
		if !strings.Contains(c.got, c.want) {
			t.Errorf("%q\nwant %q in it", c.got, c.want)
		}
	}
}
