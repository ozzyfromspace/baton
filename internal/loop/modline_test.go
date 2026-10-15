package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// modRig is a rig whose lead's claude runs baton's mod, which has said hello: the loop types nothing, and
// gives the mod its commands.
func modRig(t *testing.T) *rig {
	r := newRig(t)
	helloFrom(t, r.loop.Store)
	return r
}

// open lists the lead's commands the mod has not acked, oldest first.
func (r *rig) open() []bridge.Issued {
	rec, err := bridge.Open(r.loop.Store.Dir, r.loop.Store.Now, nil).Peek()
	if err != nil {
		r.t.Fatal(err)
	}
	return rec.Open
}

// command is the command id as written for the mod.
func (r *rig) command(id int) bridge.Command {
	r.t.Helper()
	cmds, _ := bridge.Open(r.loop.Store.Dir, r.loop.Store.Now, nil).Commands()
	for _, c := range cmds {
		if c.ID == id {
			return c
		}
	}
	r.t.Fatalf("no command %d", id)
	return bridge.Command{}
}

// ack does what `baton mod report` does with the mod's ack of the lead's command o: the bridge records it,
// and a compact's says the compaction is under way (ok) or declined for why.
func (r *rig) ack(o bridge.Issued, ok bool, why string) {
	r.t.Helper()
	b := bridge.Open(r.loop.Store.Dir, r.loop.Store.Now, nil)
	rec, _ := b.Peek()
	if _, err := b.Accept(bridge.Report{Seq: rec.Seen + 1, Kind: bridge.Ack, Instance: o.Instance, Command: o.ID, OK: ok, Error: why}); err != nil {
		r.t.Fatal(err)
	}
	if o.Kind != bridge.Compact {
		return
	}
	r.set(func(st *state.State) {
		if ok {
			state.Report(st, state.Ran(o.ID, o.Epoch, false, r.now), r.now)
		} else {
			state.Declined(st, o.Epoch, o.ID, why, r.now)
		}
	})
}

// Where baton's mod runs, baton types nothing into the session, and never touches the human's draft: a
// boundary's or a checkpoint's compaction, a retry round, a result, a reminder after a compaction or an idle
// spell, and a retry after an API error all go to the mod, the draft left in the box.
func TestWhereTheModRunsNothingIsTyped(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(r *rig)
		kind string // the command the mod is given
		text string // a submit's line holds this
	}{
		{"a boundary", func(r *rig) {}, bridge.Compact, ""},
		{"a checkpoint", func(r *rig) {
			r.set(func(st *state.State) {
				st.BoundaryOwed = false
				state.Start(st, r.now, state.Origin{})
				state.SetCheckpoint(st, r.now)
				st.Run.Compaction.Queue("checkpoint", r.now)
			})
		}, bridge.Compact, ""},
		{"a retry round", func(r *rig) {
			r.set(func(st *state.State) {
				st.Run.Compaction.Status, st.Run.Compaction.FailedAt = state.CompactFailed, r.now
			})
			r.tick(0)
			r.run(DefaultTiming.FailedRetry)
		}, bridge.Compact, ""},
		{"a result", func(r *rig) {
			r.set(func(st *state.State) {
				st.BoundaryOwed = false
				state.Start(st, r.now, state.Origin{})
				st.Run.Compaction = state.Compaction{Epoch: 1}
				st.Run.LastActivity, st.Run.LastStop = r.now, r.now
				st.Jobs = []state.Job{{ID: "j1", Phase: "P1", Command: "make test", Status: state.JobFailed, Exit: 1}}
			})
		}, bridge.Submit, "Job j1 (P1) finished"},
		{"a reminder to resume", func(r *rig) {
			r.set(func(st *state.State) {
				st.BoundaryOwed = false
				state.Start(st, r.now, state.Origin{})
				st.Run.Compaction = state.Compaction{Epoch: 1, Status: state.CompactDone, ByBaton: true, Reason: "boundary", Finished: r.now}
			})
			r.tick(0)
			r.tick(DefaultTiming.ResumeNudge + time.Second)
		}, bridge.Submit, "[baton] Context compacted. Begin P1 now"},
		{"an idle reminder", func(r *rig) {
			r.set(func(st *state.State) {
				st.BoundaryOwed = false
				state.Start(st, r.now, state.Origin{})
				st.Run.Compaction = state.Compaction{Epoch: 1}
				st.Run.LastActivity, st.Run.LastStop = r.now, r.now
			})
			r.run(DefaultTiming.IdleNudge + 10*time.Second)
		}, bridge.Submit, "No activity for"},
		{"a retry after an API error", func(r *rig) {
			r.set(func(st *state.State) {
				st.BoundaryOwed = false
				state.Start(st, r.now, state.Origin{})
				st.Run.Compaction = state.Compaction{Epoch: 1}
				st.Run.LastError = &state.StopError{Error: "overloaded", At: r.now}
			})
			r.tick(0)
			r.tick(DefaultTiming.OverloadRetry + time.Second)
		}, bridge.Submit, "API error (overloaded)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := modRig(t)
			r.view.Draft, r.view.DraftText = true, "half a thought I have not sent"
			c.set(r)
			r.run(DefaultTiming.DraftGrace + 10*time.Second) // within modAckWait of the command
			if len(r.in.text) != 0 || r.in.cleared != 0 {
				t.Fatalf("typed %q, cleared the box %d times", r.in.text, r.in.cleared)
			}
			open := r.open()
			if len(open) != 1 || open[0].Kind != c.kind {
				t.Fatalf("the mod was given %+v", open)
			}
			if cmd := r.command(open[0].ID); c.text != "" && !strings.Contains(cmd.Text, c.text) {
				t.Errorf("the line %q", cmd.Text)
			}
			if c.kind == bridge.Compact {
				comp := r.state().Run.Compaction
				if comp.Status != state.CompactTyped || comp.Command != open[0].ID || open[0].Epoch != comp.Epoch {
					t.Errorf("the compaction asked for: %+v", comp)
				}
			}
			if drafts, _ := r.loop.Store.Drafts(); len(drafts) != 0 || strings.Contains(r.events(), "draft_rescued") {
				t.Errorf("the draft was taken: %+v", drafts)
			}
		})
	}
}

// The mod's ack moves the compaction on: under way, it is the compaction's proof's to finish. Claude Code
// refuses one asked for while a turn runs, which is asked again once the turn is over, using up no try; any
// other refusal, or a veto, is asked again at once, as a try, until the round has failed.
func TestARefusedCompactionIsAskedAgain(t *testing.T) {
	r := modRig(t)
	r.tick(0)
	o := r.open()[0]
	r.ack(o, false, "HooksError: baton: $.session.compact: a turn is running (t1); the conversation compacts between turns")
	r.set(func(st *state.State) { st.Run.TurnOpen = true })
	r.tick(time.Second)
	if comp := r.state().Run.Compaction; comp.Status != state.CompactQueued || comp.Tries != 0 || comp.Declined != nil {
		t.Fatalf("after a turn's refusal: %+v", comp)
	}
	r.tick(time.Minute)
	if open := r.open(); len(open) != 0 {
		t.Fatalf("asked again while the turn runs: %+v", open)
	}
	r.set(func(st *state.State) { st.Run.TurnOpen = false })
	r.tick(time.Second)
	if open := r.open(); len(open) != 1 || open[0].ID == o.ID {
		t.Fatalf("not asked again once the turn ended: %+v", open)
	}
	r.ack(r.open()[0], false, decide.CompactVetoed)
	r.tick(time.Second)
	if comp := r.state().Run.Compaction; comp.Status != state.CompactQueued || comp.Tries != 1 {
		t.Fatalf("after a veto: %+v", comp)
	}
	r.tick(DefaultTiming.HandsOff)
	r.ack(r.open()[0], false, "the API failed")
	r.tick(time.Second)
	if comp := r.state().Run.Compaction; comp.Status != state.CompactFailed {
		t.Fatalf("after %d tries: %+v", MaxTries, comp)
	}
	if e := r.state().Run.Escalation; e == nil || e.Kind != "compaction_failed" || !strings.Contains(e.Reason, "the API failed") {
		t.Errorf("escalation %+v", e)
	}
	if len(r.in.text) != 0 {
		t.Errorf("typed %q", r.in.text)
	}

	// Under way, then vetoed after all (the mod's report of its end): asked again at once.
	r = modRig(t)
	r.tick(0)
	o = r.open()[0]
	r.ack(o, true, "")
	r.set(func(st *state.State) { state.Declined(st, o.Epoch, o.ID, decide.CompactVetoed, r.now) })
	r.tick(time.Second)
	r.tick(time.Second)
	if open := r.open(); len(open) != 1 || open[0].ID == o.ID || r.state().Run.Compaction.Tries != 2 {
		t.Fatalf("a vetoed compaction under way: %+v %+v", open, r.state().Run.Compaction)
	}
}

// A compaction the mod does not take up within modAckWait is taken back, so the mod never runs it, and
// asked for again; one taken up whose word got lost fails at CompactTimeout. One line to the model is open
// at a time, and one not taken up is taken back the same way.
func TestAnUntakenCommandIsTakenBack(t *testing.T) {
	r := modRig(t)
	r.tick(0)
	first := r.open()[0]
	r.tick(modAckWait - time.Second)
	if open := r.open(); len(open) != 1 || open[0].ID != first.ID {
		t.Fatalf("asked twice: %+v", open)
	}
	r.tick(2 * time.Second)
	r.tick(time.Second)
	open := r.open()
	if len(open) != 1 || open[0].ID == first.ID || r.state().Run.Compaction.Command != open[0].ID {
		t.Fatalf("not asked again: %+v", open)
	}
	if !strings.Contains(r.events(), `"kind":"mod_voided"`) {
		t.Errorf("events:\n%s", r.events())
	}
	b := bridge.Open(r.loop.Store.Dir, r.loop.Store.Now, nil)
	rec, _ := b.Peek()
	b.Accept(bridge.Report{Seq: rec.Seen + 1, Kind: bridge.Ack, Instance: "inst", Command: open[0].ID, Interrupted: true})
	r.run(DefaultTiming.CompactTimeout + time.Minute)
	if comp := r.state().Run.Compaction; comp.Status != state.CompactFailed && comp.Rounds == 0 {
		t.Errorf("a compaction nothing more was said of: %+v", comp)
	}

	r = modRig(t)
	r.tick(0)
	if err := r.loop.submit(r.view, "one"); err != nil {
		t.Fatal(err)
	}
	if err := r.loop.submit(r.view, "two"); err != errSubmitOpen {
		t.Errorf("a second line while the first is open: %v", err)
	}
	r.now = r.now.Add(modAckWait)
	r.view.Now = r.now
	if err := r.loop.submit(r.view, "three"); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, o := range r.open() {
		if o.Kind == bridge.Submit {
			lines = append(lines, r.command(o.ID).Text)
		}
	}
	if strings.Join(lines, ",") != "three" {
		t.Errorf("lines open: %q", lines)
	}
}

// Ctrl-C is how baton clears a draft where it types, and one Ctrl-C at the prompt stops every agent of the
// session for good (spike 35): baton never sends it while a phase agent is on the roster or the model's own
// background agent may be at work. The draft holds baton then; with a phase agent at work, baton tells the
// human why (the model's own background agents hold baton before the draft does: gateAgents).
func TestNoCtrlCWhileAnAgentMayBeLive(t *testing.T) {
	for _, c := range []struct {
		name  string
		set   func(st *state.State)
		holds bool // the draft is what holds baton, and the human is told so
	}{
		{"a phase agent", func(st *state.State) {
			st.Workers = []state.Worker{{Phase: "P2", Ref: "run/P2", Status: state.WorkerWorking, AgentID: "a1"}}
		}, true},
		{"the model's background agent", func(st *state.State) {
			st.Run.Background = []state.Task{{ID: "t1", Type: "subagent", Since: st.Run.LastStop}}
		}, false},
		{"the model's workflow", func(st *state.State) {
			st.Run.Background = []state.Task{{ID: "t1", Type: "workflow"}}
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.set(c.set)
			r.view.Draft, r.view.DraftText = true, "half a thought"
			r.run(DefaultTiming.BackgroundMax + 10*time.Minute)
			if r.in.cleared != 0 || len(r.in.text) != 0 {
				t.Fatalf("cleared %d, typed %q", r.in.cleared, r.in.text)
			}
			if e := r.state().Run.Escalation; c.holds && (e == nil || e.Reason != decide.DraftHolds("compact the context")) {
				t.Errorf("escalation %+v", e)
			}
		})
	}
}

// After a restart, the mod of the new claude says hello a moment after it starts: baton waits for it,
// typing nothing, then gives it the compaction. A mod that never says hello leaves baton to type, once
// Timing.ModHello has passed; and where none ever said hello in the run, baton types at once.
func TestTheLoopWaitsForAModAboutToSayHello(t *testing.T) {
	r := newRig(t)
	say(t, r.loop.Store, bridge.HelloInfo{Instance: "inst-before", Protocol: bridge.Protocol})
	r.tick(0)
	r.tick(5 * time.Second)
	if len(r.in.text) != 0 || len(r.open()) != 0 {
		t.Fatalf("before the hello: typed %q, asked %+v", r.in.text, r.open())
	}
	helloFrom(t, r.loop.Store)
	r.tick(2 * time.Second)
	if open := r.open(); len(r.in.text) != 0 || len(open) != 1 || open[0].Kind != bridge.Compact {
		t.Fatalf("after the hello: typed %q, asked %+v", r.in.text, open)
	}

	r = newRig(t)
	say(t, r.loop.Store, bridge.HelloInfo{Instance: "inst-before", Protocol: bridge.Protocol})
	r.run(DefaultTiming.ModHello + 20*time.Second)
	if len(r.in.text) != 1 || r.in.text[0] != "/compact" {
		t.Fatalf("no hello: typed %q", r.in.text)
	}
}
