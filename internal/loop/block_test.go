package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// blockQuestion is the question the hooks issue for a block of the lead's on P1 (need a key).
const blockQuestion = "baton: blocked on P1: need a key"

// blockRig is an idle run whose model blocked on P1 (need a key) and stopped, as the hooks leave it: the
// block put to the human (an escalation, asked), its question not on screen.
func blockRig(t *testing.T) *rig {
	r := newIdleRig(t)
	r.set(func(st *state.State) {
		state.SetBlocked(st, "need a key", r.now)
		st.Run.Escalation = &state.Escalation{Kind: "blocked", Reason: "blocked on P1: need a key", Since: r.now, Question: blockQuestion, Asked: true}
	})
	return r
}

// onScreen opens the block's question in the lead's terminal, as the hooks record it.
func (r *rig) onScreen() {
	r.set(func(st *state.State) {
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize(blockQuestion), Since: r.now, Kind: "escalation"})
	})
}

// A block of the lead's that nobody has answered for BlockLook, counted from the block or the human's
// last word, and not while the machine slept, is lifted by the loop itself where its question is not on
// screen (the model never asked it, it went away, or no question was put yet): the model is told to look
// again for a safe way on and propose it, and woken; it is announced, logged block_lapsed by timeout, and
// pushed. While the run is paused nothing lapses, and a human answer first works as it always did.
func TestTheLeadsBlockLooksAgain(t *testing.T) {
	look := DefaultTiming.BlockLook
	for _, c := range []struct {
		name   string
		setup  func(r *rig) // runs once the block is in place
		at     func(r *rig, blocked time.Time) time.Time
		lifted bool
	}{
		{"its question never asked", nil, func(r *rig, b time.Time) time.Time { return b.Add(look) }, true},
		{"no question put yet", func(r *rig) {
			r.set(func(st *state.State) { st.Run.Escalation = nil }) // the model's turn went on past baton blocked
		}, func(r *rig, b time.Time) time.Time { return b.Add(look) }, true},
		{"the human wrote first", func(r *rig) {
			r.run(5 * time.Minute)
			r.set(func(st *state.State) { st.Run.HumanAt, st.Run.Escalation = r.now, nil }) // a prompt of theirs, and the block stands
		}, func(r *rig, b time.Time) time.Time { return b.Add(5*time.Minute + look) }, true},
		{"after a sleep", func(r *rig) {
			r.tick(time.Hour) // asleep: nobody could have answered
		}, func(r *rig, b time.Time) time.Time { return r.now.Add(look) }, true},
		{"paused", func(r *rig) {
			r.set(func(st *state.State) { st.Mode = state.ModePaused })
		}, func(r *rig, b time.Time) time.Time { return b.Add(3 * look) }, false},
		{"the human answered first", func(r *rig) {
			r.run(5 * time.Minute)
			r.set(func(st *state.State) { state.Resume(st); st.Run.HumanAt = r.now })
		}, func(r *rig, b time.Time) time.Time { return b.Add(2 * look) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := blockRig(t)
			blocked := r.now
			if c.setup != nil {
				c.setup(r)
			}
			at := c.at(r, blocked)
			r.runTo(at.Add(-20 * time.Second))
			if st := r.state(); c.lifted && st.Blocked == nil {
				t.Fatalf("lifted %s early", at.Sub(r.now))
			}
			if len(r.in.text) != 0 && c.lifted {
				t.Fatalf("typed while blocked: %q", r.in.text)
			}
			r.run(30 * time.Second)
			st := r.state()
			if !c.lifted {
				if hasEvent(r.events(), "block_lapsed", nil) || c.name == "paused" && st.Blocked == nil {
					t.Fatalf("lifted: %+v\n%s", st.Blocked, r.events())
				}
				return
			}
			if st.Blocked != nil || st.Run.Escalation != nil {
				t.Fatalf("not lifted: %+v, escalation %+v", st.Blocked, st.Run.Escalation)
			}
			var told string
			for _, m := range st.Inbox {
				if m.Kind == state.MessageAnswer {
					told = m.Text
				}
			}
			for _, w := range []string{"You were blocked on P1 (need a key), and nobody has answered for 30 minutes", decide.LookAgainRule(DefaultTiming.EscalationTimeout)} {
				if !strings.Contains(told, w) {
					t.Errorf("the model is not told %q:\n%s", w, told)
				}
			}
			if !strings.Contains(strings.Join(st.Run.Announce, "\n"), "baton: nobody answered the block on P1 in 30 minutes, so baton lifted it → Claude looks again") {
				t.Errorf("not announced: %q", st.Run.Announce)
			}
			if !hasEvent(r.events(), "block_lapsed", map[string]any{"phase": "P1", "reason": "need a key", "by": state.ByTimeout, "after": look.String(), "via": "loop"}) {
				t.Errorf("events:\n%s", r.events())
			}
			if pushed := strings.Join(r.push.kinds, " "); !strings.Contains(pushed, "block_lapsed") {
				t.Errorf("pushed %q", pushed)
			}
			if len(r.in.text) != 1 || r.in.text[0] != decide.MailTyped([]string{state.MessageAnswer}) {
				t.Errorf("the idle model is not woken: %q", r.in.text)
			}
		})
	}
}

// If the model blocks again for the same reason, the same loop runs again: put to the human, and lifted
// again once nobody has answered for BlockLook.
func TestASecondBlockLooksAgainToo(t *testing.T) {
	look := DefaultTiming.BlockLook
	r := blockRig(t)
	r.run(look + 20*time.Second)
	if r.state().Blocked != nil {
		t.Fatal("the first block was not lifted")
	}
	r.set(func(st *state.State) {
		state.SetBlocked(st, "need a key", r.now)
		st.Run.Escalation = &state.Escalation{Kind: "blocked", Reason: "blocked on P1: need a key", Since: r.now, Question: blockQuestion, Asked: true}
	})
	r.run(look - time.Minute)
	if r.state().Blocked == nil {
		t.Fatal("the second block was lifted early")
	}
	r.run(2 * time.Minute)
	if st := r.state(); st.Blocked != nil {
		t.Fatalf("the second block was not lifted: %+v", st.Blocked)
	}
	if n := strings.Count(r.events(), `"kind":"block_lapsed"`); n != 2 {
		t.Errorf("lifted %d times:\n%s", n, r.events())
	}
}

// Where the block's question is on screen, the model's call waits on its dialog, so the loop only marks
// the block lapsed, and the dialog is answered Look again: where baton's mod does not run (serial mode),
// by dismissing it with Esc once the question is alone and recording Look again; where it runs, by the
// mod (hooks.Bound), and nothing is typed. The answer lifts it (the hooks). Behind another dialog nothing
// is typed.
func TestABlockOnScreenIsAnsweredLookAgain(t *testing.T) {
	look := DefaultTiming.BlockLook
	for _, c := range []struct {
		name  string
		mod   bool
		other bool // a permission prompt is open too
		typed bool
	}{
		{"serial mode", false, false, true},
		{"serial mode, behind a permission prompt", false, true, false},
		{"the mod runs", true, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := blockRig(t)
			got := r.recordLapses()
			if c.mod {
				helloFrom(t, r.loop.Store)
			}
			if c.other {
				r.set(func(st *state.State) {
					st.Run.Dialogs.Open(state.Dialog{Tool: "Bash", Agent: "a1", Key: "k", Since: r.now})
				})
			}
			r.onScreen()
			r.run(look - 20*time.Second)
			if st := r.state(); st.LookDue() || len(r.in.text) != 0 {
				t.Fatalf("due early: %+v, typed %q", st.Blocked, r.in.text)
			}
			r.run(time.Minute)
			st := r.state()
			if !st.LookDue() || st.Blocked == nil || st.Run.Escalation == nil {
				t.Fatalf("block %+v, escalation %+v: want it marked lapsed, and left for its question's answer", st.Blocked, st.Run.Escalation)
			}
			if typed := len(r.in.text) == 1 && r.in.text[0] == ownAnswerKey; typed != c.typed || !c.typed && len(r.in.text) != 0 {
				t.Fatalf("typed %q", r.in.text)
			}
			if c.typed && !hasEvent(r.events(), "auto_answered", map[string]any{"dialog": "escalation", "blocked": true}) {
				t.Errorf("events:\n%s", r.events())
			}
			if want := (lapsedAnswer{decide.Normalize(blockQuestion), decide.LookAgain}); c.typed != (len(*got) == 1 && (*got)[0] == want) || !c.typed && len(*got) != 0 {
				t.Errorf("recorded %+v", *got)
			}
			if hasEvent(r.events(), "block_lapsed", nil) {
				t.Errorf("lifted by the loop with its question on screen:\n%s", r.events())
			}
			// Once only.
			r.run(5 * time.Minute)
			if len(r.in.text) > 1 {
				t.Errorf("typed %q", r.in.text)
			}
		})
	}

	// The human wrote after it was marked (its dialog went, they wrote, and the model asked it again): it
	// is not due until nobody has answered for BlockLook again.
	r := blockRig(t)
	r.onScreen()
	r.run(look + 20*time.Second)
	r.set(func(st *state.State) { st.Run.HumanAt = r.now })
	if r.state().LookDue() {
		t.Fatal("still due after the human wrote")
	}
}

// blockCrew is a crew whose phase agent on P1 blocked (need a key) and stopped: the block in its sub-run,
// put to the human through the run's relay.
func blockCrew(t *testing.T) *crew {
	t.Helper()
	x := newCrew(t, 1)
	x.atWork("P1")
	x.sub("P1").Update(func(st *state.State) error {
		state.Start(st, x.now, state.Origin{})
		return nil
	})
	x.blocks("P1")
	return x
}

// blocks has the agent on phase block (need a key) and stop, as its command and its hooks leave it.
func (x *crew) blocks(phase string) {
	x.t.Helper()
	var since time.Time
	x.sub(phase).Update(func(st *state.State) error {
		if err := state.SetBlocked(st, "need a key", x.now); err != nil {
			return err
		}
		since = st.Blocked.Since
		st.Run.Escalation = &state.Escalation{Kind: "blocked", Reason: "blocked on " + phase + ": need a key", Since: x.now, Asked: true}
		st.Run.TurnOpen, st.Run.LastActivity, st.Run.LastStop = false, x.now, x.now
		return nil
	})
	if err := x.sub(phase).Relay(state.Relay{Kind: state.RelayBlocked, Since: since, Because: "need a key"}); err != nil {
		x.t.Fatal(err)
	}
}

// shown records that the question the mod was told to ask is on screen, as the hooks do.
func (x *crew) shown() {
	q := x.question()
	x.lead.Update(func(st *state.State) error {
		st.Run.Dialogs.Open(state.Dialog{Tool: "AskUserQuestion", Key: decide.Normalize(q), Since: x.now, Kind: "relay"})
		if r := st.RelayIssued(); r != nil {
			r.Asked, r.AskedAt = true, x.now
		}
		return nil
	})
}

// A phase agent's block is put to the human with a deadline BlockLook after it was queued, and Look again
// as its default: at that deadline it is lifted in the agent's sub-run, whether its question was on screen
// (the mod answers it), never asked (the lead's own question went first), or its dialog went away; the
// agent is told to look again and woken, the human is told in the lead's session, it is logged
// block_lapsed by timeout, and pushed. The human's own Look again lifts it too, unpushed; their Continue
// works as it always did; a hold they chose lapses after BlockLook.
func TestAnAgentsBlockLooksAgain(t *testing.T) {
	look := DefaultTiming.BlockLook
	for _, c := range []struct {
		name   string
		answer func(x *crew) // what happens to its question, once asked
		at     time.Duration // after the block, when it is lifted (0: never, by this test's end)
		by     string
		tell   string
	}{
		{"on screen, nobody answered", func(x *crew) {
			x.shown()
			x.wait(x.state().Relay[0].Deadline.Sub(x.clock()))
			x.answers(decide.LookAgain, true, false)
		}, look, state.ByTimeout, "You were blocked on P1 (need a key), and nobody has answered for 30 minutes"},
		{"never asked: the lead's own question went first", func(x *crew) {
			x.lead.Update(func(st *state.State) error {
				st.Run.Escalation = &state.Escalation{Kind: "stalled", Reason: "quiet", Since: x.now}
				return nil
			})
		}, look, state.ByTimeout, "nobody has answered for 30 minutes"},
		{"its dialog went away", func(x *crew) {
			x.shown()
			x.answers("", false, true)
		}, look, state.ByTimeout, "nobody has answered for 30 minutes"},
		{"the human chose Look again", func(x *crew) {
			x.shown()
			x.answers(decide.LookAgain, false, false)
		}, time.Minute, state.ByHuman, "The human chose Look again on your block on P1 (need a key)."},
		{"the human chose Continue", func(x *crew) {
			x.shown()
			x.answers("Continue", false, false)
		}, 0, state.ByHuman, "they have dealt with what blocked you"},
		{"the human held it (Wait for me, in their own words)", func(x *crew) {
			x.shown()
			x.answers("Wait for me", false, false)
		}, time.Minute + look, state.ByTimeout, "nobody has answered for 30 minutes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := blockCrew(t)
			blocked := x.clock()
			if c.name != "never asked: the lead's own question went first" {
				x.tick(time.Second)
				st := x.state()
				r := st.RelayIssued()
				if r == nil || len(x.asks) != 1 {
					t.Fatalf("not asked: %+v", x.state().Relay)
				}
				if want := decide.Deadline(r.Queued, look); !r.Deadline.Equal(want) || !strings.Contains(r.Question, "Unless you answer by "+want.Local().Format("15:04")+", it looks again") {
					t.Fatalf("deadline %s, want %s; question %q", r.Deadline, want, r.Question)
				}
				if got := strings.Join(x.asks[0].Options, "|"); got != "Continue|Pause baton|Look again" {
					t.Errorf("options %s", got)
				}
			}
			x.tick(time.Second)
			c.answer(x)
			if c.at > 0 {
				x.wait(blocked.Add(c.at - time.Minute).Sub(x.clock()))
			} else {
				x.wait(2 * look)
			}
			if sst, _ := x.sub("P1").Load(); c.at > time.Minute && sst.Blocked == nil {
				t.Fatalf("lifted early: %s", x.events())
			}
			x.wait(5 * time.Minute) // past it (a deadline names its minute), and a step more for the agent to be woken
			sst, _ := x.sub("P1").Load()
			if sst.Blocked != nil || sst.Run.Escalation != nil {
				t.Fatalf("still blocked: %+v, escalation %+v\n%s", sst.Blocked, sst.Run.Escalation, x.events())
			}
			told := false
			for _, m := range sst.Inbox {
				told = told || m.Kind == state.MessageAnswer && strings.Contains(m.Text, c.tell)
			}
			if !told || !x.subLogged("P1", "mail_typed", nil) {
				t.Errorf("the agent is not told %q, or not woken for it: inbox %+v\n%s", c.tell, sst.Inbox, x.subEvents("P1"))
			}
			if c.at == 0 {
				if x.logged("block_lapsed", nil) {
					t.Errorf("lapsed:\n%s", x.events())
				}
				return
			}
			pushed := x.subLogged("P1", "notified", map[string]any{"notice": "block_lapsed"})
			if lapsed := x.logged("block_lapsed", map[string]any{"phase": "P1", "type": "blocked", "by": state.ByTimeout, "after": look.String()}); lapsed != (c.by == state.ByTimeout) || pushed != lapsed {
				t.Errorf("logged block_lapsed %v, pushed %v, want %v\n%s", lapsed, pushed, c.by == state.ByTimeout, x.events())
			}
			want := "baton: nobody answered the block of the agent on P1 in 30 minutes, so baton lifted it → the agent looks again"
			if c.by == state.ByHuman {
				want = "baton: the block of the agent on P1 is lifted, as you chose → the agent looks again"
			}
			if !strings.Contains(x.announced(), want) {
				t.Errorf("announced %q", x.announced())
			}
			if !strings.Contains(c.name, "held") && !x.has("relay_applied", `"phase":"P1"`, `"result":"looks again"`) {
				t.Errorf("events:\n%s", x.events())
			}
		})
	}
}

// If the agent blocks again for the same reason, the same loop runs again: a new question, with a new
// deadline, and lifted again at it.
func TestAnAgentsSecondBlockLooksAgainToo(t *testing.T) {
	look := DefaultTiming.BlockLook
	x := blockCrew(t)
	x.tick(time.Second)
	x.shown()
	x.wait(x.state().Relay[0].Deadline.Sub(x.clock()))
	x.answers(decide.LookAgain, true, false)
	x.wait(2 * time.Minute)
	if sst, _ := x.sub("P1").Load(); sst.Blocked != nil {
		t.Fatalf("the first block was not lifted:\n%s", x.events())
	}
	x.blocks("P1")
	x.tick(time.Second)
	x.tick(time.Second)
	if len(x.asks) != 2 {
		t.Fatalf("not asked again: %+v", x.asks)
	}
	second := x.state().Relay[1]
	if want := decide.Deadline(second.Queued, look); !second.Deadline.Equal(want) {
		t.Fatalf("deadline %s, want %s", second.Deadline, want)
	}
	x.shown()
	x.wait(second.Deadline.Sub(x.clock()))
	x.answers(decide.LookAgain, true, false)
	x.wait(2 * time.Minute)
	if sst, _ := x.sub("P1").Load(); sst.Blocked != nil {
		t.Fatalf("the second block was not lifted:\n%s", x.events())
	}
	if n := x.count("block_lapsed", map[string]any{"phase": "P1"}); n != 2 {
		t.Errorf("lifted %d times:\n%s", n, x.events())
	}
}
