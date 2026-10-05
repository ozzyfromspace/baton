package loop

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

type typed struct {
	mu      sync.Mutex
	text    []string
	cleared int
}

func (t *typed) Type(s string, enter bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.text = append(t.text, s)
	return nil
}

func (t *typed) ClearInput() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleared++
	return nil
}

type pushes struct {
	kinds []string
	fail  int // fail this many sends first
}

func (p *pushes) Notify(project, kind, detail string) error {
	p.kinds = append(p.kinds, kind)
	if p.fail > 0 {
		p.fail--
		return fmt.Errorf("network down")
	}
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
		*st = state.Attach(pl, r.now, state.Origin{})
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

// run advances d in 10-second ticks. Ticks are continuous in real life; one long gap looks like sleep.
func (r *rig) run(d time.Duration) {
	for step := 10 * time.Second; d > 0; d -= step {
		r.tick(min(step, d))
	}
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
		{"dialog", func() { r.set(func(st *state.State) { st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion"}) }) }, func() { r.set(func(st *state.State) { st.Run.Dialogs.Clear() }) }},
		{"background subagent", func() { r.set(func(st *state.State) { st.Run.Background = []state.Task{{Type: "subagent"}} }) }, func() { r.set(func(st *state.State) { st.Run.Background = nil }) }},
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
	if st.Run.Compaction.Status != state.CompactFailed || st.Run.Escalation == nil || !strings.Contains(st.Run.Escalation.Reason, "will try again") {
		t.Fatalf("no escalation: %+v", st.Run)
	}
	r.tick(time.Second) // the next tick pushes the notice
	if len(r.push.kinds) != 1 || r.push.kinds[0] != "compaction_failed" || len(r.state().Run.Notices) != 0 {
		t.Fatalf("pushes %v notices %v", r.push.kinds, r.state().Run.Notices)
	}
	// A compaction that is still owed is never abandoned: the round starts over after a backoff.
	r.run(DefaultTiming.FailedRetry)
	r.tick(time.Second)
	if c := r.state().Run.Compaction; c.Rounds != 1 || len(r.in.text) != 3 || !strings.HasPrefix(r.in.text[2], clearLine) {
		t.Fatalf("no new round: %+v typed %q", c, r.in.text)
	}
	// It finally happens: the escalation clears itself.
	r.set(func(st *state.State) {
		st.Run.Compaction.Status, st.Run.Compaction.ByBaton, st.Run.Compaction.Finished = state.CompactDone, true, r.now
		st.Run.TurnStarted = r.now.Add(time.Second)
	})
	r.tick(2 * time.Second)
	if e := r.state().Run.Escalation; e != nil {
		t.Fatalf("escalation outlived its cause: %+v", e)
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
	r.run(DefaultTiming.CompactTimeout)
	if r.state().Run.Compaction.Status != state.CompactFailed {
		t.Fatal("a compaction that never finished was not escalated")
	}
}

func TestNudgeThenEscalateWhenTheModelDoesNotResume(t *testing.T) {
	r := newRig(t)
	r.set(func(st *state.State) {
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, ByBaton: true, Reason: "boundary", Finished: r.now}
	})
	r.tick(0)
	r.tick(DefaultTiming.ResumeNudge / 2)
	if len(r.in.text) != 0 {
		t.Fatal("nudged too early")
	}
	r.tick(DefaultTiming.ResumeNudge)
	if len(r.in.text) != 1 || !strings.HasPrefix(r.in.text[0], "[baton] Context compacted. Begin P1") {
		t.Fatalf("nudge %q", r.in.text)
	}
	r.run(DefaultTiming.ResumeEscalate + time.Second)
	if e := r.state().Run.Escalation; e == nil || !strings.Contains(e.Reason, "did not resume") {
		t.Fatalf("escalation %+v", e)
	}
	// The model wakes after all (a late rewake): the escalation clears itself.
	r.set(func(st *state.State) {
		st.Run.LastActivity = r.now.Add(time.Second)
		st.Run.TurnStarted = r.now.Add(time.Second)
	})
	r.tick(2 * time.Second)
	if e := r.state().Run.Escalation; e != nil {
		t.Fatalf("escalation outlived its cause: %+v", e)
	}
}

func TestResumedModelIsNotNudged(t *testing.T) {
	r := newRig(t)
	r.set(func(st *state.State) {
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, ByBaton: true, Finished: r.now}
		st.Run.TurnStarted = r.now.Add(2 * time.Second)
	})
	r.run(10 * time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("nudged a model that resumed: %q", r.in.text)
	}
}

// A draft used to hold a compaction for as long as it sat in the box — measured 2026-10-05, a real run
// sat queued for 35 minutes and only a human noticing could have cleared it. It is saved and cleared now.
func TestADraftIsSavedAndClearedRatherThanBlocking(t *testing.T) {
	r := newRig(t)
	r.view.Draft, r.view.DraftText = true, "half a thought I had not sent"

	// The control: inside DraftGrace nothing is touched, so an ordinary message being composed and
	// then sent is never taken away.
	r.run(DefaultTiming.DraftGrace - 10*time.Second)
	if len(r.in.text) != 0 || r.in.cleared != 0 {
		t.Fatalf("acted inside the grace: typed %q cleared %d", r.in.text, r.in.cleared)
	}

	r.run(DefaultTiming.DraftGrace + 30*time.Second)
	if r.in.cleared != 1 {
		t.Fatalf("the input box was not cleared: %d", r.in.cleared)
	}
	if len(r.in.text) == 0 || r.in.text[0] != "/compact" {
		t.Fatalf("a draft still blocks the compaction: %q", r.in.text)
	}
	// The text is recoverable, and the file is nothing but the text.
	drafts, err := r.loop.Store.Drafts()
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts %+v err %v", drafts, err)
	}
	if drafts[0].Text != "half a thought I had not sent" || drafts[0].Phase != "P1" {
		t.Fatalf("draft %+v", drafts[0])
	}
	// And nobody is asked to do anything about it.
	if len(r.push.kinds) != 0 {
		t.Fatalf("pushed for something baton handled itself: %v", r.push.kinds)
	}
}

// Mid-word is the one moment not to take somebody's line away.
func TestADraftIsNotTakenWhileSomebodyIsTyping(t *testing.T) {
	r := newRig(t)
	r.view.Draft, r.view.DraftText = true, "still typing"
	for i := 0; i < 60; i++ {
		r.view.LastHumanKey = r.now // a key every tick
		r.tick(time.Second)
	}
	if r.in.cleared != 0 || len(r.in.text) != 0 {
		t.Fatalf("interrupted a live typist: cleared %d typed %q", r.in.cleared, r.in.text)
	}
	r.run(DefaultTiming.DraftGrace + 30*time.Second)
	if r.in.cleared != 1 || len(r.in.text) == 0 || r.in.text[0] != "/compact" {
		t.Fatalf("after hands off: cleared %d typed %q", r.in.cleared, r.in.text)
	}
}

// keyTracker counts keystrokes and errs toward "there is a draft", so it can report one when the box is
// empty. That must cost nothing: no file, and the run still moves.
func TestAPhantomDraftWritesNoFileAndStillCompacts(t *testing.T) {
	r := newRig(t)
	r.view.Draft, r.view.DraftText = true, ""
	r.run(DefaultTiming.DraftGrace + 30*time.Second)
	if r.in.cleared != 1 || len(r.in.text) == 0 || r.in.text[0] != "/compact" {
		t.Fatalf("cleared %d typed %q", r.in.cleared, r.in.text)
	}
	if drafts, _ := r.loop.Store.Drafts(); len(drafts) != 0 {
		t.Fatalf("saved an empty draft: %+v", drafts)
	}
}

// A status line that redraws every second never leaves the screen still for Quiet. Typing is safe
// anyway (Claude Code buffers input), so after QuietMax with every other gate open, baton types.
func TestAScreenThatNeverSettlesDoesNotBlockForever(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 20; i++ {
		r.view.LastOutput = r.now
		r.tick(time.Second)
	}
	if len(r.in.text) != 0 {
		t.Fatalf("typed after %d busy seconds: %q", 20, r.in.text)
	}
	for i := 0; i < 15 && len(r.in.text) == 0; i++ {
		r.view.LastOutput = r.now
		r.tick(time.Second)
	}
	if len(r.in.text) != 1 || r.in.text[0] != "/compact" {
		t.Fatalf("never typed into a busy screen: %q", r.in.text)
	}
}

// The Stop hook does not run when a turn is interrupted, and a hook can fail; an open turn with no
// sign of life must not hold everything forever.
func TestAStaleTurnStopsBlocking(t *testing.T) {
	r := newRig(t)
	r.set(func(st *state.State) { st.Run.TurnOpen, st.Run.TurnStarted, st.Run.LastActivity = true, r.now, r.now })
	r.tick(0)
	r.run(DefaultTiming.StaleTurn - time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("typed during a live turn: %q", r.in.text)
	}
	r.run(2 * time.Minute)
	if len(r.in.text) == 0 || r.in.text[0] != "/compact" {
		t.Fatalf("a stale turn still blocks: %q", r.in.text)
	}
}

// Background subagents may hold a compaction for a while — they finish and report on their own — but a
// hung one must not park the plan. They used to hold it for good: the gate escalated once and then
// waited on a human. After BackgroundMax baton carries on; a compaction does not cancel the work.
func TestHungBackgroundSubagentsStopBlocking(t *testing.T) {
	r := newRig(t)
	r.set(func(st *state.State) { st.Run.Background = []state.Task{{Type: "subagent", Status: "running"}} })
	r.run(DefaultTiming.BackgroundMax - time.Minute)
	if len(r.in.text) != 0 {
		t.Fatalf("typed while a subagent was running: %q", r.in.text)
	}
	r.run(2 * time.Minute)
	if len(r.in.text) != 1 || r.in.text[0] != "/compact" {
		t.Fatalf("a hung subagent still blocks: %q", r.in.text)
	}
}

// A machine that sleeps through a compaction does not fail it the moment it wakes.
func TestSleepDoesNotFailACompaction(t *testing.T) {
	r := newRig(t)
	r.tick(time.Second)
	r.set(func(st *state.State) {
		st.Run.Compaction.Status, st.Run.Compaction.Started, st.Run.Compaction.ByBaton = state.CompactActive, r.now, true
	})
	r.tick(time.Second)
	r.tick(3 * time.Hour) // asleep
	r.tick(time.Second)
	if c := r.state().Run.Compaction; c.Status != state.CompactActive {
		t.Fatalf("failed right after waking: %+v", c)
	}
}

// stalls raises a real escalation for the push-mechanics tests below: a compaction baton ran, after
// which the model never took its next turn. It is deliberately NOT a draft — a draft no longer waits
// for anybody (see TestADraftIsSavedAndClearedRatherThanBlocking), so it cannot stand in for one.
func (r *rig) stalls() {
	r.set(func(st *state.State) {
		st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, ByBaton: true, Finished: r.now}
	})
	r.run(DefaultTiming.ResumeNudge + DefaultTiming.ResumeEscalate + 30*time.Second)
	if e := r.state().Run.Escalation; e == nil || e.Kind != "stalled" {
		r.t.Fatalf("the fixture did not escalate: %+v", e)
	}
}

// One escalation push is easy to miss; unresolved ones are pushed again, a few times.
func TestUnresolvedEscalationsAreRepeated(t *testing.T) {
	r := newRig(t)
	r.stalls()
	r.run(Reminders[1] + time.Minute)
	if len(r.push.kinds) != 3 {
		t.Fatalf("pushes %v", r.push.kinds)
	}
}

// A push that fails (no network) stays queued and is retried.
func TestFailedPushesAreRetried(t *testing.T) {
	r := newRig(t)
	r.push.fail = 1
	r.stalls()
	r.run(2 * DefaultTiming.NoticeRetry)
	if len(r.push.kinds) == 0 || len(r.state().Run.Notices) != 0 {
		t.Fatalf("pushes %v queued %v", r.push.kinds, r.state().Run.Notices)
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
