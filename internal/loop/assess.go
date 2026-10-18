package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/brief"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/risk"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The risk assessment (v0.8.7): when the ledger crosses a line (riskCrossing, once per crossing), with
// risk_model set and baton's mod in the lead's claude, the host asks an independent model (Opus unless
// risk_model says otherwise) to judge the exposure of building further on the work no full check has
// passed yet, through the mod (Complete). It reads what baton gathers (assessInput: the plan's goal, the
// phases since the last full check with their notes, deviations, decisions and factors, the records of red
// verifications finished over, and the diff since the full check's commit), at most decide.RiskInputMax
// characters, marked as data, and answers in at most decide.RiskAnswerTokens tokens within Timing.RiskWait.
// Its first word is its verdict, and its answer a suggestion:
//
//   - continue or targeted goes to the lead as a look-in (Risk.Owed, given once it holds a phase), the
//     targeted checks named: information only;
//   - full starts the ledger's hold (riskVerdict), whose message quotes it.
//
// It is never a step of the run, so nothing can get stuck on it: the score's look-in (decide.RiskLookIn)
// is kept as Owed from the crossing, held from the lead only while the call runs (Risk.Asking), and given
// in its place when the call fails, has no verdict, or outlasts its deadline, or when no call in this
// host's memory stands for it (riskAsking: a restart). Each crossing logs risk_assessed{verdict, chars} or
// risk_assess_skipped{why}.

// RiskWait is how long the assessment's answer is waited for at most, unless Timing.RiskWait says
// otherwise.
const RiskWait = 2 * time.Minute

// riskLapse is how far past its deadline the ledger's step gives up on an assessment whose call never
// returned: the call itself gives up at its deadline.
const riskLapse = 30 * time.Second

// Why an assessment went unanswered, beside Complete's own reasons (Unanswered.Why).
const (
	assessOff       = "off"        // risk_model is off: the lead is told the score alone
	assessNoVerdict = "no-verdict" // the answer's first word is no verdict
	assessRestart   = "restart"    // the host that asked is gone: no call in this host's memory stands for it
	assessPanic     = "panic"
)

// riskWait is how long the assessment's answer is waited for.
func (c *Coordinator) riskWait() time.Duration {
	if w := c.Lead.Timing.RiskWait; w > 0 {
		return w
	}
	return RiskWait
}

// assessable says why no assessment is asked of the ledger in the run s ("" when one is): risk_model off,
// or no mod in the lead's claude to make the call (Complete's no-mod and serial).
func (c *Coordinator) assessable(s *state.Store) string {
	switch {
	case c.Risk.Model == "":
		return assessOff
	case c.rsk.call != nil:
		return "" // a test stands in for the mod and the model
	}
	if u := modFor(s, bridge.Open(s.Dir, s.Now, func(kind string, fields map[string]any) { s.Event(kind, fields) })); u != nil {
		return u.Why
	}
	return ""
}

// assess asks the assessment of the ledger in the run s, its line crossed at asked (Risk.Asking), off the
// ledger's step: a call that takes up to its deadline holds nothing else of the ledger, and the host's end
// does not wait for it (the next host gives the score's look-in in its place, riskAsking).
func (c *Coordinator) assess(asked time.Time, s *state.Store, line string, points int) {
	c.rsk.calls.Add(1)
	run := func(fn func()) { go fn() }
	if c.Fleet != nil {
		run = c.Fleet.run
	}
	run(func() {
		defer c.rsk.calls.Done()
		defer c.rsk.asking.CompareAndSwap(asked.UnixNano(), 0)
		defer func() {
			if r := recover(); r != nil {
				c.Lead.logf("loop: risk assessment: panic: %v", r)
				s.Event("controller_panic", map[string]any{"error": fmt.Sprint(r), "in": "risk assessment"})
				c.assessSkipped(asked, s, line, assessPanic, fmt.Sprint(r), 0)
			}
		}()
		c.assessNow(asked, s, line, points)
	})
}

// assessNow gathers the assessment's input, makes the call, and gives the lead what it says: see above.
func (c *Coordinator) assessNow(asked time.Time, s *state.Store, line string, points int) {
	ctx, cancel := context.WithTimeout(context.Background(), c.riskWait())
	defer cancel()
	in, err := c.assessInput(ctx, s, line, points)
	if err != nil {
		c.assessSkipped(asked, s, line, "failed", err.Error(), 0)
		return
	}
	prompt := decide.RiskAssessPrompt(in)
	call := c.rsk.call
	if call == nil {
		call = Complete
	}
	wait := c.riskWait()
	began := time.Now()
	ans, err := call(ctx, s, Completion{Model: c.Risk.Model, System: decide.RiskAssessSystem, Prompt: prompt,
		MaxTokens: decide.RiskAnswerTokens, Timeout: wait - wait/10})
	if err != nil {
		why, detail := "failed", err.Error()
		var u *Unanswered
		if errors.As(err, &u) {
			why, detail = u.Why, u.Detail
		}
		c.assessSkipped(asked, s, line, why, detail, len(prompt))
		return
	}
	verdict, reason := decide.RiskVerdictOf(ans.Text)
	if verdict == "" {
		c.assessSkipped(asked, s, line, assessNoVerdict, ans.Text, len(prompt))
		return
	}
	words := verdict
	if reason != "" {
		words += ": " + reason
	}
	lookIn := decide.RiskAssessed(c.Risk.Model, verdict, reason, in.Risk, line, points)
	current := false
	s.Update(func(st *state.State) error {
		r := st.Risk
		if current = r != nil && asks(r.Asking, asked); !current {
			return nil
		}
		r.Asking, r.Assessment, r.Assessor, r.Owed = time.Time{}, words, c.Risk.Model, lookIn
		if verdict == state.VerdictFull {
			r.Owed = "" // the hold's message quotes it
		}
		return nil
	})
	took := ans.Took
	if took == 0 {
		took = time.Since(began)
	}
	fields := map[string]any{"verdict": verdict, "chars": len(prompt), "line": line, "model": c.Risk.Model, "seconds": took.Round(time.Second).Seconds()}
	if ans.Usage != nil {
		fields["input_tokens"], fields["output_tokens"] = ans.Usage.Input, ans.Usage.Output
	}
	if !current {
		fields["stale"] = true // the ledger moved on meanwhile (a full check passed, the next line was crossed): the answer goes nowhere
	}
	s.Event("risk_assessed", fields)
	if !current {
		return
	}
	c.riskVerdict(s.Now(), s, verdict)
	if verdict != state.VerdictFull {
		c.announce(s, decide.RiskAssessedSay(c.Risk.Model, verdict))
	}
}

// asks reports whether a ledger asking since asking (Risk.Asking) waits on the assessment asked at asked.
func asks(asking, asked time.Time) bool {
	return !asking.IsZero() && asking.UnixNano() == asked.UnixNano()
}

// assessSkipped records that the assessment asked at asked gave no answer (why, with detail), so the score's
// look-in, kept as Owed, goes to the lead in its place. chars is the input's size, once it was made.
func (c *Coordinator) assessSkipped(asked time.Time, s *state.Store, line, why, detail string, chars int) {
	current := false
	s.Update(func(st *state.State) error {
		if r := st.Risk; r != nil && asks(r.Asking, asked) {
			r.Asking, current = time.Time{}, true
		}
		return nil
	})
	fields := map[string]any{"why": why, "line": line}
	if detail = decide.Sanitize(detail); detail != "" {
		fields["detail"] = clipLog(detail)
	}
	if chars > 0 {
		fields["chars"] = chars
	}
	if !current {
		fields["stale"] = true
	}
	s.Event("risk_assess_skipped", fields)
	if current {
		c.announce(s, decide.RiskAssessSkippedSay(why))
	}
}

// clipLog is s cut to what an event keeps of it.
func clipLog(s string) string {
	if r := []rune(s); len(r) > 300 {
		return string(r[:299]) + "…"
	}
	return s
}

// riskAsking gives up on an assessment no call in this host's memory stands for (the host that asked it is
// gone) or whose call never returned by its deadline and riskLapse: the score's look-in goes in its place.
func (c *Coordinator) riskAsking(now time.Time, s *state.Store) {
	st, err := s.Load()
	if err != nil || st.Risk == nil || st.Risk.Asking.IsZero() {
		return
	}
	asked, why := st.Risk.Asking, ""
	switch {
	case c.rsk.asking.Load() != asked.UnixNano():
		why = assessRestart
	case !now.Before(asked.Add(c.riskWait() + riskLapse)):
		why = TimedOut
	default:
		return
	}
	cleared := false
	s.Update(func(st *state.State) error {
		if r := st.Risk; r != nil && asks(r.Asking, asked) {
			r.Asking, cleared = time.Time{}, true
		}
		return nil
	})
	if cleared { // otherwise its call answered meanwhile
		s.Event("risk_assess_skipped", map[string]any{"why": why, "line": st.Risk.Crossed})
		c.announce(s, decide.RiskAssessSkippedSay(why))
	}
}

// assessInput gathers what the assessment of the ledger in the run s reads (decide.RiskInput), its line
// crossed (line, at points): the run's record is read without its lock, and git within ctx.
func (c *Coordinator) assessInput(ctx context.Context, s *state.Store, line string, points int) (decide.RiskInput, error) {
	st, err := s.Load()
	if err != nil {
		return decide.RiskInput{}, err
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return decide.RiskInput{}, err
	}
	if st.Risk == nil {
		return decide.RiskInput{}, errors.New("the run has no risk ledger")
	}
	var doc []byte
	if pl.File != "" {
		doc, _ = os.ReadFile(pl.File)
	}
	r := *st.Risk
	in := decide.RiskInput{Title: pl.Title, Goal: pl.Goal(doc), Risk: r, Line: line, Points: points, Ahead: qaNext(st, pl),
		Leave: func(p string) bool { return risk.IsLockfile(p) || risk.Generated(p) }}
	handoff, _ := os.ReadFile(s.HandoffPath())
	decisions := s.Decisions(st)
	for _, pr := range r.Phases {
		p := decide.RiskPhase{ID: pr.Phase, Points: pr.Points, Factors: pr.Factors, Deviation: pr.Deviation,
			Notes: brief.LatestNotes(string(handoff), pr.Phase)}
		if i := pl.Index(pr.Phase); i >= 0 {
			p.Title = pl.Phases[i].Title
		}
		for _, d := range decisions {
			if d.Phase != pr.Phase {
				continue
			}
			what := d.What
			if d.Because != "" {
				what += " (because " + d.Because + ")"
			}
			p.Decisions = append(p.Decisions, what)
			if strings.Contains(d.What, "despite its red verification") {
				in.DespiteRed = append(in.DespiteRed, d.Phase+": "+d.What)
			}
		}
		in.Phases = append(in.Phases, p)
	}
	in.Base, in.FromCheck = r.Commit, r.Commit != ""
	if in.Base == "" && r.Since.IsZero() {
		in.Base = planStart(st, pl)
	}
	if in.Base != "" && s.Checkout != "" {
		gctx, cancel := context.WithTimeout(ctx, gitTimeout)
		in.Stat, in.Patch, err = gitx.Diff(gctx, s.Checkout, in.Base, "HEAD")
		cancel()
		if err != nil {
			in.Stat, in.Patch = "", ""
		}
	}
	return in, nil
}

// planStart is the commit plan pl started on in run st: the start of its earliest phase on record, the
// first in the plan's order of those that started together.
func planStart(st state.State, pl plan.Plan) string {
	var head string
	var at time.Time
	for _, ph := range pl.Phases {
		if ps := st.Phases[ph.ID]; ps != nil && ps.StartHead != "" && !ps.StartedAt.IsZero() && (at.IsZero() || ps.StartedAt.Before(at)) {
			head, at = ps.StartHead, ps.StartedAt
		}
	}
	return head
}

// qaNext names the plan's next full check, its first QA phase not done, and the phases not done before
// it: "P15 (QA), after 3 more phases"; "" when none is left.
func qaNext(st state.State, pl plan.Plan) string {
	left := 0
	for _, ph := range pl.Phases {
		if ps := st.Phases[ph.ID]; ps != nil && ps.Status == state.PhaseDone {
			continue
		}
		if ph.QA() {
			s := ph.ID
			if ph.Title != "" {
				s += " (" + ph.Title + ")"
			}
			switch left {
			case 0:
				return s
			case 1:
				return s + ", after 1 more phase"
			default:
				return s + fmt.Sprintf(", after %d more phases", left)
			}
		}
		left++
	}
	return ""
}
