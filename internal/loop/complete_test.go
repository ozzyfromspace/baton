package loop

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/state"
)

// fakeMod stands in for baton's mod in the lead's claude: it answers each complete command of the run's
// bridge with answer's ack (nil: it never answers), as `baton mod report` records one, until stopped. got
// is each command it took.
type fakeMod struct {
	mu   sync.Mutex
	got  []bridge.Command
	stop chan struct{}
	done chan struct{}
}

func runMod(t *testing.T, s *state.Store, answer func(bridge.Command) *bridge.Report) *fakeMod {
	m := &fakeMod{stop: make(chan struct{}), done: make(chan struct{})}
	b := bridge.Open(s.Dir, s.Now, nil)
	go func() {
		defer close(m.done)
		seen := map[int]bool{}
		for {
			cmds, _ := b.Commands()
			for _, c := range cmds {
				if c.Kind != bridge.Complete || seen[c.ID] {
					continue
				}
				seen[c.ID] = true
				m.mu.Lock()
				m.got = append(m.got, c)
				m.mu.Unlock()
				if rep := answer(c); rep != nil {
					rec, _ := b.Peek()
					rep.Seq, rep.Kind, rep.Instance, rep.Command = rec.Seen+1, bridge.Ack, c.Instance, c.ID
					if _, err := b.Accept(*rep); err != nil {
						t.Errorf("ack of %d: %v", c.ID, err)
					}
				}
			}
			select {
			case <-m.stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	t.Cleanup(m.end)
	return m
}

func (m *fakeMod) end() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	<-m.done
}

func (m *fakeMod) commands() []bridge.Command {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]bridge.Command(nil), m.got...)
}

func completeStore(t *testing.T) *state.Store {
	t.Helper()
	old := completePoll
	completePoll = time.Millisecond
	t.Cleanup(func() { completePoll = old })
	now := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	s, err := state.Open(filepath.Join(t.TempDir(), ".baton"), "inst", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The host's model call goes to the mod with every word the host wrote, and comes back with the mod's
// answer, how long the call took and what it used; the bridge keeps nothing of it after.
func TestCompleteAsksTheModAndReturnsItsAnswer(t *testing.T) {
	s := completeStore(t)
	helloFrom(t, s)
	usage := &bridge.Usage{Input: 6959, Output: 120}
	mod := runMod(t, s, func(bridge.Command) *bridge.Report {
		return &bridge.Report{OK: true, Result: "The environment's: rerun TestLanding.", Ms: 3511, Usage: usage}
	})
	a, err := Complete(context.Background(), s, Completion{Model: "haiku", System: "You advise.", Prompt: "Why did it fail?", MaxTokens: 400})
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != "The environment's: rerun TestLanding." || a.Took != 3511*time.Millisecond || a.Usage == nil || *a.Usage != *usage {
		t.Errorf("answer %+v", a)
	}
	got := mod.commands()
	if len(got) != 1 {
		t.Fatalf("commands %+v", got)
	}
	if c := got[0]; c.Model != "haiku" || c.System != "You advise." || c.Prompt != "Why did it fail?" || c.MaxTokens != 400 || c.TimeoutMs != CompleteTimeout.Milliseconds() || c.Instance != "inst" {
		t.Errorf("command %+v", c)
	}
	if rec, _ := bridge.Open(s.Dir, s.Now, nil).Load(); len(rec.Open) != 0 || len(rec.Answers) != 0 {
		t.Errorf("left open %+v, answers %+v", rec.Open, rec.Answers)
	}
}

// Every way a model call has no answer says why, and the caller goes on: no mod in the lead's claude, a
// serial run, the model's own error, the mod reloaded, a mod too old for the command, the deadline (the
// command taken back), the caller giving up.
func TestCompleteSaysWhyThereIsNoAnswer(t *testing.T) {
	for _, c := range []struct {
		name   string
		set    func(t *testing.T, s *state.Store)
		answer func(bridge.Command) *bridge.Report // nil: no mod polls the bridge
		ctx    func() (context.Context, context.CancelFunc)
		why    string
		detail string
		issued bool // a command was written for the mod
	}{
		{name: "no hello", why: NoMod},
		{name: "another claude's mod", set: func(t *testing.T, s *state.Store) {
			say(t, s, bridge.HelloInfo{Instance: "inst-old", Protocol: bridge.Protocol})
		}, why: NoMod},
		{name: "a mod baton does not work with", set: func(t *testing.T, s *state.Store) {
			say(t, s, bridge.HelloInfo{Instance: "inst", Protocol: 1, Refused: "baton's mod is another version"})
		}, why: SerialRun, detail: "baton's mod is another version"},
		{name: "the run gone serial", set: func(t *testing.T, s *state.Store) {
			s.Update(func(st *state.State) error {
				st.Run.Serial = &state.Serial{Instance: "inst", Why: "no mod said hello"}
				return nil
			})
		}, why: SerialRun, detail: "no mod said hello"},
		{name: "an API error", set: helloFrom, answer: func(bridge.Command) *bridge.Report {
			return &bridge.Report{Reason: "api-error", Error: "api-error 529 overloaded"}
		}, why: "api-error", detail: "api-error 529 overloaded", issued: true},
		{name: "a call Claude Code would not send", set: helloFrom, answer: func(bridge.Command) *bridge.Report {
			return &bridge.Report{Reason: "refused", Error: "HooksError: maxTokens 200000 is past the limit"}
		}, why: "refused", detail: "HooksError: maxTokens 200000 is past the limit", issued: true},
		{name: "the mod reloaded", set: helloFrom, answer: func(bridge.Command) *bridge.Report {
			return &bridge.Report{Interrupted: true}
		}, why: Interrupted, issued: true},
		{name: "a mod too old for it", set: helloFrom, answer: func(bridge.Command) *bridge.Report {
			return &bridge.Report{Error: "no such command: complete"}
		}, why: "failed", detail: "no such command: complete", issued: true},
		{name: "no ack by the deadline", set: helloFrom, answer: func(bridge.Command) *bridge.Report { return nil },
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 30*time.Millisecond)
			},
			why: TimedOut, issued: true},
		{name: "the caller gave up", set: helloFrom, answer: func(bridge.Command) *bridge.Report { return nil },
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(20*time.Millisecond, cancel)
				return ctx, cancel
			}, why: Cancelled, issued: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := completeStore(t)
			if c.set != nil {
				c.set(t, s)
			}
			var mod *fakeMod
			if c.answer != nil {
				mod = runMod(t, s, c.answer)
			}
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if c.ctx != nil {
				ctx, cancel = c.ctx()
			}
			defer cancel()
			a, err := Complete(ctx, s, Completion{Model: "haiku", Prompt: "Why?"})
			var u *Unanswered
			if !errors.As(err, &u) || u.Why != c.why || c.detail != "" && u.Detail != c.detail {
				t.Fatalf("answer %+v, error %v; want why %q, detail %q", a, err, c.why, c.detail)
			}
			if mod != nil {
				mod.end()
			}
			b := bridge.Open(s.Dir, s.Now, nil)
			rec, _ := b.Load()
			if issued := rec.Seq > 0; issued != c.issued {
				t.Errorf("issued %v, want %v", issued, c.issued)
			}
			if cmds, _ := b.Commands(); len(rec.Open) != 0 || len(cmds) != 0 || len(rec.Answers) != 0 {
				t.Errorf("left open %+v, files %+v, answers %+v", rec.Open, cmds, rec.Answers)
			}
		})
	}
}

// A phase agent's sub-run has no mod of its own: a model call goes through the run's store.
func TestCompleteNeedsTheRunsStore(t *testing.T) {
	s := completeStore(t)
	helloFrom(t, s)
	sub := *s
	sub.Parent, sub.Ref = s.Ref, s.Ref+"/P1"
	_, err := Complete(context.Background(), &sub, Completion{Model: "haiku", Prompt: "Why?"})
	if u := (*Unanswered)(nil); !errors.As(err, &u) || u.Why != NoMod {
		t.Errorf("error %v", err)
	}
}
