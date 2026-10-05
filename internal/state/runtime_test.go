package state

import (
	"os"
	"path/filepath"
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
