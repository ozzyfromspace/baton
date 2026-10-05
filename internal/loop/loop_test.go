package loop

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

type typed struct {
	mu   sync.Mutex
	text []string
}

func (t *typed) Type(s string, enter bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.text = append(t.text, s)
	return nil
}

type pushes struct{ kinds []string }

func (p *pushes) Notify(project, kind, detail string) error {
	p.kinds = append(p.kinds, kind)
	return nil
}

type rig struct {
	t    *testing.T
	now  time.Time
	loop *Loop
	in   *typed
	push *pushes
	view host.View
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), in: &typed{}, push: &pushes{}}
	store, _ := state.Open(t.TempDir(), "inst", func() time.Time { return r.now })
	pl := plan.Plan{Version: 1, Title: "Demo", Phases: []plan.Phase{{ID: "P0", Title: "a"}, {ID: "P1", Title: "b"}}}
	store.Update(func(st *state.State) error {
		*st = state.Attach(pl, r.now, "")
		state.Done(st, pl, "P0", r.now, false)
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactQueued, Reason: "boundary", Queued: r.now}
		return state.Claim(st, "inst", 1, r.now)
	})
	r.loop = &Loop{Store: store, Notify: r.push, Project: "demo", Timing: DefaultTiming}
	r.view = host.View{Owner: true, LastOutput: r.now.Add(-time.Hour), LastHumanKey: r.now.Add(-time.Hour)}
	return r
}

func (r *rig) tick(advance time.Duration) {
	r.now = r.now.Add(advance)
	r.view.Now = r.now
	r.loop.Tick(r.view, r.in)
}

func (r *rig) state() state.State {
	st, _ := r.loop.Store.Load()
	return st
}

func (r *rig) set(fn func(*state.State)) {
	r.loop.Store.Update(func(st *state.State) error { fn(st); return nil })
}

func TestTypesCompactOnlyWhenEveryGatePasses(t *testing.T) {
	r := newRig(t)
	blockers := []struct {
		name string
		on   func()
		off  func()
	}{
		{"turn open", func() { r.set(func(st *state.State) { st.Run.TurnOpen = true }) }, func() { r.set(func(st *state.State) { st.Run.TurnOpen = false }) }},
		{"dialog", func() { r.set(func(st *state.State) { st.Run.Dialog = &state.Dialog{Tool: "AskUserQuestion"} }) }, func() { r.set(func(st *state.State) { st.Run.Dialog = nil }) }},
		{"subagent", func() { r.set(func(st *state.State) { st.Run.Subagents = 1 }) }, func() { r.set(func(st *state.State) { st.Run.Subagents = 0 }) }},
		{"draft", func() { r.view.Draft = true }, func() { r.view.Draft = false }},
		{"human typing", func() { r.view.LastHumanKey = r.now }, func() { r.view.LastHumanKey = r.now.Add(-time.Hour) }},
		{"screen busy", func() { r.view.LastOutput = r.now }, func() { r.view.LastOutput = r.now.Add(-time.Hour) }},
	}
	for _, b := range blockers {
		b.on()
		r.tick(100 * time.Millisecond)
		if len(r.in.text) != 0 {
			t.Fatalf("typed while %s", b.name)
		}
		b.off()
	}
	// A background shell (say, a dev server) does not hold a compaction.
	r.set(func(st *state.State) { st.Run.Background = []state.Task{{Type: "shell", Status: "running"}} })
	r.tick(100 * time.Millisecond)
	if len(r.in.text) != 1 || r.in.text[0] != "/compact" {
		t.Fatalf("typed %q", r.in.text)
	}
	if c := r.state().Run.Compaction; c.Status != state.CompactTyped || c.Tries != 1 {
		t.Fatalf("after typing: %+v", c)
	}
}

func TestRetryOnceThenEscalate(t *testing.T) {
	r := newRig(t)
	r.tick(time.Second) // types
	r.tick(DefaultTiming.AckTimeout + time.Second)
	if c := r.state().Run.Compaction; c.Status != state.CompactQueued {
		t.Fatalf("no retry: %+v", c)
	}
	r.tick(time.Second) // types again, clearing its own residue first
	if len(r.in.text) != 2 || !strings.HasPrefix(r.in.text[1], clearLine) {
		t.Fatalf("retry typed %q", r.in.text)
	}
	r.tick(DefaultTiming.AckTimeout + time.Second)
	st := r.state()
	if st.Run.Compaction.Status != state.CompactFailed || st.Run.Escalation == nil {
		t.Fatalf("no escalation: %+v", st.Run)
	}
	r.tick(time.Second) // the next tick pushes the notice
	if len(r.push.kinds) != 1 || r.push.kinds[0] != "compaction_failed" || len(r.state().Run.Notices) != 0 {
		t.Fatalf("pushes %v notices %v", r.push.kinds, r.state().Run.Notices)
	}
}

func TestAcknowledgedCompactionIsLeftAlone(t *testing.T) {
	r := newRig(t)
	r.tick(time.Second)
	r.set(func(st *state.State) {
		st.Run.Compaction.Status, st.Run.Compaction.Started, st.Run.Compaction.ByBaton = state.CompactActive, r.now, true
	})
	r.tick(DefaultTiming.AckTimeout * 3)
	if len(r.in.text) != 1 || r.state().Run.Compaction.Status != state.CompactActive {
		t.Fatalf("typed %q state %+v", r.in.text, r.state().Run.Compaction)
	}
	r.tick(DefaultTiming.CompactTimeout)
	if r.state().Run.Compaction.Status != state.CompactFailed {
		t.Fatal("a compaction that never finished was not escalated")
	}
}

func TestNudgeThenEscalateWhenTheModelDoesNotResume(t *testing.T) {
	r := newRig(t)
	r.set(func(st *state.State) {
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, ByBaton: true, Reason: "boundary", Finished: r.now}
	})
	r.tick(DefaultTiming.ResumeNudge / 2)
	if len(r.in.text) != 0 {
		t.Fatal("nudged too early")
	}
	r.tick(DefaultTiming.ResumeNudge)
	if len(r.in.text) != 1 || !strings.HasPrefix(r.in.text[0], "[baton] Context compacted. Begin P1") {
		t.Fatalf("nudge %q", r.in.text)
	}
	r.tick(DefaultTiming.ResumeEscalate + time.Second)
	if e := r.state().Run.Escalation; e == nil || !strings.Contains(e.Reason, "did not resume") {
		t.Fatalf("escalation %+v", e)
	}
}

func TestResumedModelIsNotNudged(t *testing.T) {
	r := newRig(t)
	r.set(func(st *state.State) {
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, ByBaton: true, Finished: r.now}
		st.Run.TurnStarted = r.now.Add(2 * time.Second)
	})
	r.tick(10 * time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("nudged a model that resumed: %q", r.in.text)
	}
}

func TestADraftBlockingACompactionIsReported(t *testing.T) {
	r := newRig(t)
	r.view.Draft = true
	r.tick(time.Second)
	r.tick(DefaultTiming.DraftEscalate + time.Second)
	r.tick(time.Second)
	if len(r.in.text) != 0 || len(r.push.kinds) != 1 || r.push.kinds[0] != "draft" {
		t.Fatalf("typed %q pushes %v", r.in.text, r.push.kinds)
	}
}

func TestNonOwnerDoesNothing(t *testing.T) {
	r := newRig(t)
	r.view.Owner = false
	r.tick(time.Minute)
	if len(r.in.text) != 0 {
		t.Fatal("a non-owner typed")
	}
}
