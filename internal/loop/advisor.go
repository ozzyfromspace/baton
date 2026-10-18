package loop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The advisor: when a look-in fires about a gate (lookin.go's, a gate's first failure, or a red gate's
// result its environment failed), with advisor on and baton's mod in the lead's claude, the host asks a small
// model (advisor_model, Haiku by default) for a short note on it, through the mod (Complete), and the
// session is given the look-in with the note after it ("Advisor (Haiku): …"). baton writes every word the
// advisor reads (decide.AdvisorPrompt); its answer is a suggestion, and it routes none of the session's work.
// It is never a step of the run, so the handoff cannot get stuck:
//
//   - the look-in is held from the session while the host asks (state.HoldForAdvice), and released when the
//     answer comes or at Timing.AdvisorWait, whichever is first, by the host's own tick (releaseAdvice),
//     whatever the mod or the model does: no ack, an API error, the mod gone, each releases it at the
//     deadline at the latest, with no note;
//   - an answer that comes after that is dropped, and logged advisor_late;
//   - the calls are in the host's memory, each hold's deadline in the run's state: a hold no call in memory
//     stands for, an earlier host's, is released at once, and a hook releases one past its deadline itself
//     (state.LapseAdvice);
//   - each look-in is asked about once, logged advisor_asked, then advisor_said (its note's length) or
//     advisor_skipped (why); with no mod, or in a serial run, nothing is asked, and advisor_skipped says why.
//
// Each loop's run is looked at by its own tick: the lead's by the lead's (Coordinator.duties), a phase
// agent's sub-run by its agent's (Agent.tick); the call goes through the lead's bridge either way.

// AdvisorWait is how long a look-in is held for the advisor's note at most, unless Timing.AdvisorWait says
// otherwise.
const AdvisorWait = 60 * time.Second

// Advisor is the advisor's settings, advisor and advisor_model, as the host carries them from its config.
type Advisor struct {
	On    bool
	Model string // "" for config.DefaultAdvisorModel
}

// What the advisor reads of a gate's log: its end, and the lines before it that report a failure, each
// clipped (decide.AdvisorPrompt caps the whole).
const (
	adviceTailLines = 80
	adviceTailBytes = 6 << 10
	adviceFailures  = 20
	adviceClip      = 240
)

// Why a look-in went without the advisor's note, beside Complete's own reasons (Unanswered.Why).
const (
	adviceRestart = "restart"     // the hold is an earlier host's: this one has no call for it in memory
	adviceLapsed  = "lapsed"      // the hold went from the run before the host released it: a hook did, past its deadline
	adviceEmpty   = "empty-reply" // the answer had no words
	advicePanic   = "panic"       // the call panicked
)

// advising is what the coordinator's advisor keeps in memory: the calls it made (ask), by adviceKey, until
// their look-in is released and the call has returned; the look-ins it has looked at, each once; how it
// makes a call (Complete; tests stub it); and when a panic in it was last logged.
type advising struct {
	mu      sync.Mutex
	asks    map[string]*ask
	seen    map[string]bool
	call    func(ctx context.Context, s *state.Store, c Completion) (Answer, error)
	panicAt time.Time
}

// ask is one call to the advisor about the look-in of kind about job in the run store: held until until;
// once the call has returned (done), its note as the session is given it (text), or why there is none.
// released says its look-in went: an answer after that is late.
type ask struct {
	store     *state.Store
	kind, job string
	until     time.Time
	done      bool
	text, why string
	took      time.Duration
	released  bool
}

// adviceKey names the look-in of kind about job in the run s among the advisor's calls.
func adviceKey(s *state.Store, kind, job string) string { return s.Dir + "\x00" + kind + "\x00" + job }

// lookFound is a look-in of a run the advisor is asked about: its kind, the gate it is about, and what baton
// saw, as the advisor reads it.
type lookFound struct {
	kind  string
	job   state.Job
	facts string
}

// advise is the advisor's part of a loop's tick, for the run s (the lead's, or a phase agent's sub-run) as
// st has it: it releases the look-ins held there whose call has answered, whose deadline has passed, or that
// no call in memory stands for (releaseAdvice); then, with the advisor on and the plan running, it asks the
// advisor about each look-in of the run not asked about yet (advisable), holding it meanwhile. It reports
// whether it changed the run's state, so that its loop, which goes on to deliver what is due, reads it again
// (a look-in typed for before its hold would come to nothing). A panic in it is logged, at most once a
// minute, and goes no further than it.
func (c *Coordinator) advise(now time.Time, s *state.Store, st state.State) (changed bool) {
	if s == nil {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			c.Lead.logf("loop: advisor: panic: %v", r)
			c.adv.mu.Lock()
			log := now.Sub(c.adv.panicAt) > time.Minute
			if log {
				c.adv.panicAt = now
			}
			c.adv.mu.Unlock()
			if log {
				s.Event("controller_panic", map[string]any{"error": fmt.Sprint(r), "in": "advisor"})
			}
		}
	}()
	changed = c.releaseAdvice(now, s, st)
	if !c.Advisor.On || st.Mode != state.ModeRunning {
		return changed
	}
	found := c.advisable(s, st)
	if len(found) == 0 {
		return changed
	}
	if u := modUp(c.Lead.Store); u != nil {
		for _, f := range found {
			s.Event("advisor_skipped", map[string]any{"type": f.kind, "job": f.job.ID, "phase": f.job.Phase, "why": u.Why})
		}
		c.Lead.logf("loop: advisor: %d look-in(s) go without a note: %s", len(found), u.Why)
		return changed
	}
	for _, f := range found {
		if c.askAdvisor(now, s, st, f) {
			changed = true
		}
	}
	return changed
}

// modUp says why the lead's claude has no mod to make the advisor's call (nil when it has one): Complete's
// own reasons, no-mod and serial, so nothing is asked that could only be skipped.
func modUp(lead *state.Store) *Unanswered {
	if lead == nil {
		return &Unanswered{Why: NoMod}
	}
	return modFor(lead, bridge.Open(lead.Dir, lead.Now, func(kind string, fields map[string]any) { lead.Event(kind, fields) }))
}

// advisable lists the look-ins of the run s, as st has it, that the advisor has not been asked about, each
// once a host: the first failures of its gates that run on, the look-ins the host left about a gate, and the
// red gates' results due whose log shows their environment failed them (lane.Environment, read once).
func (c *Coordinator) advisable(s *state.Store, st state.State) []lookFound {
	var out []lookFound
	for _, j := range st.UntoldFailures() {
		if !st.Advised(state.LookFailing, j.ID) && c.fresh(s, state.LookFailing, j.ID) {
			in := time.Duration(0)
			if from := gateBegan(j); !from.IsZero() && j.Failing.At.After(from) {
				in = j.Failing.At.Sub(from)
			}
			out = append(out, lookFound{kind: state.LookFailing, job: j, facts: decide.FailingFacts(j, *j.Failing, in)})
		}
	}
	for _, li := range st.LeftLookIns() {
		j := st.Job(li.Job)
		if li.Job == "" || j == nil || st.Advised(li.Kind, li.Job) || !c.fresh(s, li.Kind, li.Job) {
			continue
		}
		facts := decide.LookInFacts(li.Say)
		if facts == "" {
			facts = li.Text
		}
		out = append(out, lookFound{kind: li.Kind, job: *j, facts: facts})
	}
	for _, j := range st.Due() {
		if j.Kind != state.JobVerify || !j.Red() || st.Advised(state.LookEnv, j.ID) || !c.fresh(s, state.LookEnv, j.ID) {
			continue
		}
		path := j.Log
		if path == "" {
			path = s.JobLog(j.ID)
		}
		if m := lane.Environment(path); m.Kind != "" {
			out = append(out, lookFound{kind: state.LookEnv, job: j, facts: decide.EnvFailFacts(j, m.Kind, m.Line)})
		}
	}
	return out
}

// fresh reports whether the look-in of kind about job in the run s is one the advisor has not looked at
// before, and marks it looked at.
func (c *Coordinator) fresh(s *state.Store, kind, job string) bool {
	key := adviceKey(s, kind, job)
	c.adv.mu.Lock()
	defer c.adv.mu.Unlock()
	if c.adv.seen[key] {
		return false
	}
	if c.adv.seen == nil {
		c.adv.seen = map[string]bool{}
	}
	c.adv.seen[key] = true
	return true
}

// askAdvisor holds look-in f of the run s from its session (state.HoldForAdvice) until now and the
// advisor's wait, and asks the advisor about it off the tick (consult), which reads what the prompt needs
// (the log, the run's earlier runs) there too. The call is in memory before the hold is in the run, so no
// tick takes the hold for an earlier host's. A look-in a hook told meanwhile is not held, and not asked
// about. It reports whether it held it.
func (c *Coordinator) askAdvisor(now time.Time, s *state.Store, st state.State, f lookFound) bool {
	wait := c.Lead.Timing.AdvisorWait
	if wait <= 0 {
		wait = AdvisorWait
	}
	model := c.Advisor.Model
	if model == "" {
		model = config.DefaultAdvisorModel
	}
	key := adviceKey(s, f.kind, f.job.ID)
	a := &ask{store: s, kind: f.kind, job: f.job.ID, until: now.Add(wait)}
	c.adv.mu.Lock()
	if c.adv.asks == nil {
		c.adv.asks = map[string]*ask{}
	}
	c.adv.asks[key] = a
	call := c.adv.call
	c.adv.mu.Unlock()
	held := false
	if _, err := s.Update(func(st *state.State) error { held = state.HoldForAdvice(st, f.kind, f.job.ID, a.until); return nil }); err != nil || !held {
		c.adv.mu.Lock()
		delete(c.adv.asks, key)
		c.adv.mu.Unlock()
		return false
	}
	if call == nil {
		call = Complete
	}
	c.Lead.logf("loop: advisor: asking %s about %s's %s look-in (job %s)", model, f.job.Phase, f.kind, f.job.ID)
	q := Completion{Model: model, System: decide.AdvisorSystem, MaxTokens: decide.AdvisorMaxTokens, Timeout: wait}
	go c.consult(a, key, call, q, wait, func() decide.AdvisorInput { return c.adviceInput(now, s, st, f) }, f.job.Phase)
	return true
}

// consult asks the advisor about a's look-in, with the advisor's wait as its deadline: it builds the prompt
// from what input reads, logs advisor_asked, makes the call q, and records what came of it on a for the
// tick to release (releaseAdvice): the note, or why there is none. An answer that comes once a's look-in
// went is dropped, and logged advisor_late.
func (c *Coordinator) consult(a *ask, key string, call func(context.Context, *state.Store, Completion) (Answer, error), q Completion, wait time.Duration,
	input func() decide.AdvisorInput, phase string) {
	var text, why string
	var took time.Duration
	defer func() {
		if r := recover(); r != nil {
			c.Lead.logf("loop: advisor: the call panicked: %v", r)
			text, why = "", advicePanic
		}
		c.adv.mu.Lock()
		a.done, a.text, a.why, a.took = true, text, why, took
		late := a.released
		if late {
			delete(c.adv.asks, key)
		}
		c.adv.mu.Unlock()
		if late && text != "" {
			a.store.Event("advisor_late", map[string]any{"type": a.kind, "job": a.job, "chars": utf8.RuneCountInString(text), "seconds": int(took.Seconds())})
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	q.Prompt = decide.AdvisorPrompt(input())
	a.store.Event("advisor_asked", map[string]any{"type": a.kind, "job": a.job, "phase": phase, "model": q.Model, "chars": len(q.Prompt),
		"wait_seconds": int(wait.Seconds())})
	ans, err := call(ctx, c.Lead.Store, q)
	took = ans.Took
	if err != nil {
		why = "failed"
		var u *Unanswered
		if errors.As(err, &u) {
			why = u.Why
		}
		return
	}
	if text = decide.AdvisorNote(q.Model, ans.Text); text == "" {
		why = adviceEmpty
	}
}

// releaseAdvice releases the look-ins held for the advisor in the run s, as st has it, that are due to go:
// with its note, one whose call has answered; with none, one whose call failed, one past its deadline, and
// one no call in memory stands for (an earlier host's). A call whose hold went from the run before the host
// released it is let go: an answer to it is late. Each release is logged, in the run: advisor_said,
// advisor_skipped, or advisor_late for a note no look-in waited for any more. It reports whether it released
// any.
func (c *Coordinator) releaseAdvice(now time.Time, s *state.Store, st state.State) (changed bool) {
	type release struct {
		kind, job, text, why string
		took                 time.Duration
	}
	var out []release
	var gone []map[string]any // calls let go, as advisor_skipped logs them
	var late []map[string]any
	holds := st.Holds()
	c.adv.mu.Lock()
	if len(holds) == 0 && len(c.adv.asks) == 0 {
		c.adv.mu.Unlock()
		return false
	}
	for _, h := range holds {
		key := adviceKey(s, h.Kind, h.Job)
		a := c.adv.asks[key]
		switch {
		case a == nil:
			out = append(out, release{kind: h.Kind, job: h.Job, why: adviceRestart})
		case a.done:
			out = append(out, release{kind: h.Kind, job: h.Job, text: a.text, why: a.why, took: a.took})
			a.released = true
			delete(c.adv.asks, key)
		case !now.Before(a.until):
			out = append(out, release{kind: h.Kind, job: h.Job, why: TimedOut})
			a.released = true
		}
	}
	for key, a := range c.adv.asks {
		if a.store.Dir != s.Dir || a.released || st.Advising(a.kind, a.job) {
			continue
		}
		a.released = true
		switch {
		case !a.done:
			gone = append(gone, map[string]any{"type": a.kind, "job": a.job, "why": adviceLapsed})
		case a.text != "":
			delete(c.adv.asks, key)
			late = append(late, map[string]any{"type": a.kind, "job": a.job, "chars": utf8.RuneCountInString(a.text), "seconds": int(a.took.Seconds())})
		default:
			delete(c.adv.asks, key)
			gone = append(gone, map[string]any{"type": a.kind, "job": a.job, "why": a.why})
		}
	}
	c.adv.mu.Unlock()
	for _, f := range gone {
		s.Event("advisor_skipped", f)
	}
	for _, f := range late {
		s.Event("advisor_late", f)
	}
	if len(out) == 0 {
		return false
	}
	released := make([]bool, len(out))
	s.Update(func(st *state.State) error {
		for i, r := range out {
			released[i] = state.ReleaseAdvice(st, r.kind, r.job, r.text)
		}
		return nil
	})
	for i, r := range out {
		fields := map[string]any{"type": r.kind, "job": r.job}
		switch {
		case !released[i] && r.text != "":
			fields["chars"], fields["seconds"] = utf8.RuneCountInString(r.text), int(r.took.Seconds())
			s.Event("advisor_late", fields)
		case !released[i]:
		case r.text != "":
			fields["chars"], fields["seconds"] = utf8.RuneCountInString(r.text), int(r.took.Seconds())
			s.Event("advisor_said", fields)
			c.Lead.logf("loop: advisor: note on job %s's %s look-in, released", r.job, r.kind)
		default:
			fields["why"] = r.why
			s.Event("advisor_skipped", fields)
			c.Lead.logf("loop: advisor: job %s's %s look-in goes without a note: %s", r.job, r.kind, r.why)
		}
	}
	return slices.Contains(released, true)
}

// adviceInput is what the advisor reads about look-in f of the run s, as st has it, at now: what baton saw,
// the gate's command, how long it has run or how it ended, the earlier passing runs of its command in the
// run, the phase's notes, the run's environment facts, and its log's failure lines and end.
func (c *Coordinator) adviceInput(now time.Time, s *state.Store, st state.State, f lookFound) decide.AdvisorInput {
	j := f.job
	path := j.Log
	if path == "" {
		path = s.JobLog(j.ID)
	}
	e, _ := lane.ReadExcerpt(path, adviceTailLines, adviceTailBytes, adviceFailures, adviceClip)
	in := decide.AdvisorInput{Facts: f.facts, Command: j.Command, Here: j.Here, Phase: j.Phase, Earlier: c.earlierRuns(s, st, j.Command),
		Failures: e.Failures, More: e.More, Tail: e.Tail}
	if j.Finished() {
		in.Outcome = decide.Outcome(j)
	} else if from := gateBegan(j); !from.IsZero() && now.After(from) {
		in.Ran = now.Sub(from)
	}
	if j.Notes != "" {
		in.Notes = append(in.Notes, j.Notes)
	}
	if ps := st.Phases[j.Phase]; ps != nil && ps.Park != nil && ps.Park.Notes != "" && ps.Park.Notes != j.Notes {
		in.Notes = append(in.Notes, ps.Park.Notes)
	}
	for _, fact := range s.Facts(st) {
		in.EnvFacts = append(in.EnvFacts, fact.Text)
	}
	return in
}

// earlierRuns is how long the latest passing runs of command took in the run (passingRuns: the lead's run
// and its live phase agents' sub-runs), oldest first; s and st are the run asking, as it has it.
func (c *Coordinator) earlierRuns(s *state.Store, st state.State, command string) []time.Duration {
	lead := c.Lead.Store
	loops := []lookLoop{{store: s, st: st}}
	leadSt := st
	if lead != nil && lead.Dir != s.Dir {
		if ls, err := lead.Load(); err == nil {
			leadSt = ls
			loops = append(loops, lookLoop{store: lead, st: ls})
		}
	}
	if lead != nil {
		for _, r := range c.liveSubs(lead, leadSt) {
			if r.ID != s.Ref {
				loops = append(loops, lookLoop{st: r.State})
			}
		}
	}
	runs := passingRuns(loops)[command]
	return runs[max(0, len(runs)-maxEarlier):]
}
