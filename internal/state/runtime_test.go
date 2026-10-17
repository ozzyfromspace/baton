package state

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func keys(q DialogQueue) []string {
	var out []string
	for _, d := range q {
		out = append(out, d.Agent+"/"+d.Tool+"/"+d.Key)
	}
	return out
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The two orders measured in spikes/16-escalation (S1-C and S1-D): which dialog is in front depends
// on which was requested first, and only the queue can tell.
func TestDialogQueueOrders(t *testing.T) {
	question := Dialog{Tool: "AskUserQuestion", Key: "baton: q", Kind: "context_warning", Since: t0}
	subBash := Dialog{Tool: "Bash", Agent: "sub", Key: "h1", Since: t0.Add(time.Second)}

	// S1-C: the question first, then a background subagent's permission prompt behind it.
	var q DialogQueue
	q.Open(question)
	if d, ok := q.Only(); !ok || !d.Same(question) {
		t.Fatalf("alone: %v %v", d, ok)
	}
	q.Open(subBash)
	if _, ok := q.Only(); ok {
		t.Fatal("two dialogs open, yet one counted as the only one")
	}
	if d, _ := q.Front(); !d.Same(question) {
		t.Fatalf("front %+v", d)
	}
	if _, ok := q.CloseExact("", "AskUserQuestion", "baton: q"); !ok || !same(keys(q), []string{"sub/Bash/h1"}) {
		t.Fatalf("after the answer: %v", keys(q))
	}

	// S1-D: the subagent asked first, so its prompt is on screen and the question waits behind it.
	q = nil
	q.Open(Dialog{Tool: "Bash", Agent: "sub", Key: "h1", Since: t0})
	q.Open(Dialog{Tool: "AskUserQuestion", Key: "baton: q", Since: t0.Add(3 * time.Second)})
	if d, _ := q.Front(); d.Agent != "sub" {
		t.Fatalf("front %+v", d)
	}
	if _, ok := q.Only(); ok {
		t.Fatal("the question counted as alone behind a subagent's prompt")
	}
	q.CloseAgent("sub") // the subagent stopped: whatever it asked is gone
	if d, ok := q.Only(); !ok || d.Tool != "AskUserQuestion" {
		t.Fatalf("after the subagent stopped: %v", keys(q))
	}
}

func TestDialogQueueCloses(t *testing.T) {
	open := func() DialogQueue {
		var q DialogQueue
		q.Open(Dialog{Tool: "Bash", Key: "a", Since: t0})
		q.Open(Dialog{Tool: "Bash", Agent: "sub", Key: "a", Since: t0})
		q.Open(Dialog{Tool: "Bash", Key: "a", Since: t0.Add(time.Second)})
		q.Open(Dialog{Tool: "Write", Agent: "other", Key: "w", Since: t0})
		return q
	}
	cases := []struct {
		name string
		do   func(*DialogQueue)
		want []string
	}{
		{"a call with no dialog closes nothing", func(q *DialogQueue) { q.CloseExact("", "Bash", "zzz") },
			[]string{"/Bash/a", "sub/Bash/a", "/Bash/a", "other/Write/w"}},
		{"another tool with the same key closes nothing", func(q *DialogQueue) { q.CloseExact("", "Edit", "a") },
			[]string{"/Bash/a", "sub/Bash/a", "/Bash/a", "other/Write/w"}},
		{"another agent's call closes nothing of the main agent", func(q *DialogQueue) { q.CloseExact("third", "Bash", "a") },
			[]string{"/Bash/a", "sub/Bash/a", "/Bash/a", "other/Write/w"}},
		{"an exact match closes the oldest of its kind", func(q *DialogQueue) { q.CloseExact("", "Bash", "a") },
			[]string{"sub/Bash/a", "/Bash/a", "other/Write/w"}},
		{"a subagent stopping closes only its own", func(q *DialogQueue) { q.CloseAgent("sub") },
			[]string{"/Bash/a", "/Bash/a", "other/Write/w"}},
		{"an empty agent id is not the main agent", func(q *DialogQueue) { q.CloseAgent("") },
			[]string{"/Bash/a", "sub/Bash/a", "/Bash/a", "other/Write/w"}},
		{"the main turn ending closes only the main agent's", func(q *DialogQueue) { q.CloseMain() },
			[]string{"sub/Bash/a", "other/Write/w"}},
		{"idle closes everything", func(q *DialogQueue) { q.Clear() }, nil},
	}
	for _, c := range cases {
		q := open()
		c.do(&q)
		if !same(keys(q), c.want) {
			t.Errorf("%s: %v, want %v", c.name, keys(q), c.want)
		}
		if q.AnyOpen() != (len(c.want) > 0) {
			t.Errorf("%s: AnyOpen %v", c.name, q.AnyOpen())
		}
	}
}

// A state file written by v0.1 holds a single "dialog". It loads, and the field is dropped.
func TestOldDialogFieldIsIgnored(t *testing.T) {
	dir := t.TempDir()
	old := `{"version":1,"mode":"running","current":"P0","phases":{"P0":{"status":"active"}},` +
		`"run":{"turn_open":false,"subagents":0,"stop_blocks":0,"crons":0,"compaction":{"epoch":0},` +
		`"dialog":{"tool":"AskUserQuestion","since":"2026-10-04T12:00:00Z","kind":"context_warning"}}}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := Open(dir, "", func() time.Time { return t0 })
	st, err := s.Load()
	if err != nil || st.Current != "P0" || st.Run.Dialogs.AnyOpen() {
		t.Fatalf("%v %+v", err, st.Run.Dialogs)
	}
}

// Progress (the plan moving, the model reporting) clears escalations about a run standing still, but
// never one that waits on the human's decision.
func TestProgressKeepsWhatTheHumanMustDecide(t *testing.T) {
	for kind, kept := range map[string]bool{
		"blocked": true, "decision": true, "review": true,
		"stalled": false, "stuck": false, "draft": false, "api_error": false, "compaction_failed": false,
	} {
		r := Runtime{IdleNudges: 2, StopBlocks: 3, Escalation: &Escalation{Kind: kind}}
		r.Progress()
		if (r.Escalation != nil) != kept || r.IdleNudges != 0 || r.StopBlocks != 0 {
			t.Errorf("%s: escalation %+v nudges %d blocks %d", kind, r.Escalation, r.IdleNudges, r.StopBlocks)
		}
	}
}

// Claude Code reports a Monitor at Stop as a shell (spike 19): baton tells the two apart by the task id
// the Monitor's PostToolUse returned. A Monitor is never work, but it ends with claude.
func TestMonitorsAreToldApartByID(t *testing.T) {
	ids := func(ts []Task) []string {
		var out []string
		for _, x := range ts {
			out = append(out, x.ID)
		}
		return out
	}
	var r Runtime
	r.Watch("m1")
	r.Watch("m1")
	r.Watch("")
	r.SetBackground([]Task{{ID: "b1", Type: "shell"}, {ID: "m1", Type: "shell"}, {ID: "d", Type: "dream"}, {ID: "s", Type: "subagent"}}, time.Time{})
	if got := ids(r.BusyBackground()); !same(got, []string{"b1", "s"}) {
		t.Errorf("busy = %v", got)
	}
	if got := ids(r.Watching()); !same(got, []string{"m1"}) {
		t.Errorf("watching = %v", got)
	}
	// A Monitor armed in this turn is in the next Stop's list; one that ended is forgotten.
	r.Watch("m2")
	r.SetBackground([]Task{{ID: "b1", Type: "shell"}, {ID: "m2", Type: "shell"}}, time.Time{})
	if !same(r.Monitors, []string{"m2"}) || !same(ids(r.BusyBackground()), []string{"b1"}) {
		t.Errorf("monitors = %v, busy = %v", r.Monitors, ids(r.BusyBackground()))
	}
}

// Claude Code does not say when a background task started, so baton keeps when a Stop first reported it.
func TestBackgroundWorkKeepsWhenItWasFirstSeen(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	var r Runtime
	r.SetBackground([]Task{{ID: "b1", Type: "shell"}}, t0)
	r.SetBackground([]Task{{ID: "b1", Type: "shell"}, {ID: "b2", Type: "subagent"}}, t0.Add(time.Minute))
	if b := r.Background; !b[0].Since.Equal(t0) || !b[1].Since.Equal(t0.Add(time.Minute)) || !Oldest(r.BusyBackground()).Equal(t0) {
		t.Fatalf("background %+v", b)
	}
	r.SetBackground([]Task{{ID: "b2", Type: "subagent"}}, t0.Add(2*time.Minute))
	if !Oldest(r.BusyBackground()).Equal(t0.Add(time.Minute)) || !Oldest(nil).IsZero() {
		t.Fatalf("after b1 ended: %+v", r.Background)
	}
}

// A stop is refused over background work once; work started since makes it news again, and work that
// ended does not.
func TestUntoldBackgroundWork(t *testing.T) {
	r := Runtime{BackgroundTold: []string{"b1", "b2"}}
	for _, c := range []struct {
		ids  []string
		want bool
	}{{[]string{"b1"}, false}, {[]string{"b1", "b2"}, false}, {[]string{"b1", "b3"}, true}, {nil, false}} {
		var bg []Task
		for _, id := range c.ids {
			bg = append(bg, Task{ID: id, Type: "shell"})
		}
		if got := r.Untold(bg); got != c.want {
			t.Errorf("%v: %v, want %v", c.ids, got, c.want)
		}
	}
}

// Work told about at its start is told once, with when, and forgotten when no Stop reports it any more,
// which returns it as ended with when it was told; a Monitor's told id goes with it. Work told by a baton
// that kept no time ends with none.
func TestToldBackgroundWorkIsForgottenWhenItEnds(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	var r Runtime
	if !r.Tell("b1", t0) || r.Tell("b1", t0.Add(time.Second)) || r.Tell("", t0) || !r.Tell("m1", t0.Add(10*time.Second)) {
		t.Fatalf("told %v", r.BackgroundTold)
	}
	if !r.BackgroundToldAt["b1"].Equal(t0) || !r.BackgroundToldAt["m1"].Equal(t0.Add(10*time.Second)) || len(r.BackgroundToldAt) != 2 {
		t.Fatalf("told at %v", r.BackgroundToldAt)
	}
	r.Watch("m1")
	if ended := r.SetBackground([]Task{{ID: "b1", Type: "shell"}, {ID: "m1", Type: "shell"}}, t0); r.Untold(r.BusyBackground()) || len(r.BackgroundTold) != 2 || len(ended) != 0 {
		t.Fatalf("told %v, ended %v", r.BackgroundTold, ended)
	}
	ended := r.SetBackground([]Task{{ID: "b1", Type: "shell"}}, t0.Add(time.Minute))
	if len(r.BackgroundTold) != 1 || r.BackgroundTold[0] != "b1" || len(r.Monitors) != 0 {
		t.Fatalf("after the monitor ended: told %v, monitors %v", r.BackgroundTold, r.Monitors)
	}
	if want := []TaskEnded{{ID: "m1", Told: t0.Add(10 * time.Second)}}; !slices.Equal(ended, want) || len(r.BackgroundToldAt) != 1 {
		t.Fatalf("ended %v, want %v; told at %v", ended, want, r.BackgroundToldAt)
	}
	r.BackgroundTold = append(r.BackgroundTold, "old") // told by a baton that kept no time
	ended = r.SetBackground(nil, t0.Add(2*time.Minute))
	if len(r.BackgroundTold) != 0 || r.BackgroundToldAt != nil {
		t.Fatalf("after the work ended: told %v at %v", r.BackgroundTold, r.BackgroundToldAt)
	}
	if want := []TaskEnded{{ID: "b1", Told: t0}, {ID: "old"}}; !slices.Equal(ended, want) {
		t.Fatalf("ended %v, want %v", ended, want)
	}
}
