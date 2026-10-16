// Package replan restructures the phases of a running plan that have not started. The human's
// `/baton replan "<what to consider>"` opens one (Open); the lead's model edits a draft of the plan file,
// in the session, where the human sees it; and baton checks the draft against what the replan may change
// before it writes it over the plan file and follows it (Finish), records the change as a decision with
// its undo, and says what changed. The model decides how; the host decides when, and whether the draft
// stands. While a replan is open no new phase agent starts, and one not finished within Max lapses
// (Lapse): its draft is dropped, and the plan goes on as it was.
//
// Every replan is compared with the plan as the human approved it (replan/0.md, KeepApproved), and the
// text each one found is kept (replan/<n>.md): copied back over the plan file, it undoes it (Restore).
package replan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Max is how long a replan stays open before it lapses (loop's Timing.ReplanMax).
const Max = 20 * time.Minute

// MaxFrom is Max, or the shorter one an end-to-end test sets (BATON_REPLAN_MAX), as the host's timing has
// it.
func MaxFrom(env func(string) string) time.Duration {
	if d, err := time.ParseDuration(env("BATON_REPLAN_MAX")); err == nil && d > 0 {
		return d
	}
	return Max
}

// LimitsOf is what replan r may change beyond the order and the After: lines of the phases not started.
// The human's may split and reword phases (the shape rules split enabling work and contracts out), and
// drop or merge phases, or change the plan's goal sections, only when they gave words to consider: the
// model goes as far as those ask, and names in its notes each phase it drops.
func LimitsOf(r state.Replan) plan.Limits {
	if r.By == state.ReplanHuman {
		return plan.Limits{Add: true, Edit: true, Drop: r.Words != "", Goal: r.Words != ""}
	}
	return plan.Limits{}
}

// Opened is a replan Open opened, or found open already (Again), and the brief the model replans from.
type Opened struct {
	Replan state.Replan
	Brief  string
	Again  bool
}

// Open opens a replan of the plan of run s, by by (state.ReplanHuman, …), with the human's words: the plan
// file is copied to the draft the model edits and kept as the text the replan found (replan/<n>.md), the
// plan as the human approved it is kept too if baton has not kept it yet (a plan attached before baton
// did), and the replan is marked open, which holds new phase agents. A replan open already is given
// again, the human's words taking it over. max is how long it may stay open, for the brief.
func Open(s *state.Store, by, words string, now time.Time, max time.Duration) (Opened, error) {
	st, err := s.Load()
	if err != nil {
		return Opened{}, err
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return Opened{}, state.ErrNoPlan
	}
	if st.Mode != state.ModeRunning && st.Mode != state.ModePaused {
		return Opened{}, fmt.Errorf("no plan is running here to replan (mode: %s)", st.Mode)
	}
	if r := st.Replan; r != nil {
		return again(s, st, pl, by, words, max)
	}
	doc, err := os.ReadFile(pl.File)
	if err != nil {
		return Opened{}, fmt.Errorf("cannot read the plan file: %w", err)
	}
	old, err := follow(st, pl, doc)
	if err != nil {
		return Opened{}, errors.New(decide.ReplanCannotOpen(pl.File, firstLine(err)))
	}
	if len(state.Unstarted(st, old)) == 0 {
		return Opened{}, errors.New(decide.NothingToReplan)
	}
	n := s.NextReplan()
	r := state.Replan{By: by, Words: words, Since: now, N: n}
	if err := errors.Join(s.WriteReplanText(0, doc, false), s.WriteReplanText(n, doc, true), s.WriteDraft(doc)); err != nil {
		s.DropReplan(n)
		return Opened{}, fmt.Errorf("cannot keep the replan's files: %w", err)
	}
	if _, err := s.Update(func(st *state.State) error {
		if err := state.OpenReplan(st, r); err != nil {
			return err
		}
		st.Run.Announce = append(st.Run.Announce, decide.ReplanOpened(by, words, max))
		return nil
	}); err != nil {
		s.DropReplan(n)
		return Opened{}, err
	}
	fields := map[string]any{"by": by, "n": n, "unstarted": len(state.Unstarted(st, old))}
	if words != "" {
		fields["words"] = words
	}
	s.Event("replan_opened", fields)
	return Opened{Replan: r, Brief: brief(st, old, r, s.DraftPath(), max)}, nil
}

// again gives the replan open on run s again, its draft copied afresh from the text it found if it is
// gone; the human's words (by state.ReplanHuman) take it over.
func again(s *state.Store, st state.State, pl plan.Plan, by, words string, max time.Duration) (Opened, error) {
	r := *st.Replan
	if by == state.ReplanHuman && (r.By != by || words != "" && words != r.Words) {
		after, err := s.Update(func(st *state.State) error {
			if st.Replan == nil {
				return errors.New(decide.NoReplanOpen)
			}
			st.Replan.By = by
			if words != "" {
				st.Replan.Words = words
			}
			return nil
		})
		if err != nil {
			return Opened{}, err
		}
		r = *after.Replan
		s.Event("replan_taken_over", map[string]any{"by": by, "n": r.N, "words": r.Words})
	}
	if _, err := os.Stat(s.DraftPath()); err != nil {
		if doc, err := os.ReadFile(s.ReplanText(r.N)); err == nil {
			s.WriteDraft(doc)
		}
	}
	old := pl
	if doc, err := os.ReadFile(s.ReplanText(r.N)); err == nil {
		if p, err := follow(st, pl, doc); err == nil {
			old = p
		}
	}
	return Opened{Replan: r, Brief: brief(st, old, r, s.DraftPath(), max), Again: true}, nil
}

// follow is pl as it reads in doc: pl itself when doc is the text it was made from, or the change in doc
// followed as the Stop hook would follow it (plan.Follow).
func follow(st state.State, pl plan.Plan, doc []byte) (plan.Plan, error) {
	if sum(doc) == pl.SHA256 {
		return pl, nil
	}
	adopted, _, err := plan.Follow(pl, doc, state.Remaining(st, pl))
	return adopted, err
}

// brief is the brief of replan r of plan pl, run st, whose draft is at draft.
func brief(st state.State, pl plan.Plan, r state.Replan, draft string, max time.Duration) string {
	frozen := state.Frozen(st, pl)
	b := decide.ReplanBrief{Title: pl.Title, By: r.By, Words: r.Words, Draft: draft, Limits: LimitsOf(r), Max: max}
	var pending []string
	for _, ph := range pl.Phases {
		if frozen[ph.ID] {
			ps := st.Phases[ph.ID]
			status := state.PhasePending
			if ps != nil {
				status = ps.Status
			}
			b.Frozen = append(b.Frozen, decide.ReplanPhase{ID: ph.ID, Title: ph.Title, Stands: decide.ReplanStands(status, ph.ID == st.Current)})
			continue
		}
		pending = append(pending, ph.ID)
		b.Pending = append(b.Pending, decide.ReplanPhase{ID: ph.ID, Title: ph.Title, Waits: pl.DepsLabel(ph.ID)})
	}
	b.Shape = pl.Part(pending).Shape()
	return b.Brief()
}

// Refused is a draft Finish did not follow, and why: each breach, as the model is told them.
type Refused struct {
	Breaches []plan.Breach
	Text     string
}

func (r Refused) Error() string { return r.Text }

// Finished is what Finish did: the replan it followed, or found unchanged (Unchanged: the draft was the
// text it found, so it is closed as declined).
type Finished struct {
	Replanned     state.Replanned
	Decision      state.Decision
	Plan          plan.Plan
	Before, After plan.Shape  // the phases not started, before and after
	Took          string      // the phase the run took up, which starts after a compaction
	State         state.State // the run once the replan is followed
	Unchanged     bool
}

// Finish follows the draft of the replan open on run s, with the model's notes on what changed and why.
// The draft is checked first (plan.Redraft, against LimitsOf the replan; a phase it drops must be named in
// the notes), and refused (Refused) with the plan file untouched and the replan open, for the model to fix
// it. A draft made while the plan file itself changed is refused too, and the replan closed. A draft that
// passes is written over the plan file, and the run follows it (state.AdoptReplan): its plan.json, which
// the coordinator passes on to the phase agents at work, its phases, and the decision that records the
// replan with its undo (copy replan/<n>.md back over the plan file). Nothing else changes the plan file
// meanwhile: the Stop hook leaves a plan file alone while a replan is open.
func Finish(s *state.Store, notes string, now time.Time) (f Finished, err error) {
	st, err := s.Load()
	if err != nil {
		return f, err
	}
	r := st.Replan
	if r == nil {
		return f, errors.New(decide.NoReplanOpen)
	}
	pl, err := s.LoadPlan()
	if err != nil {
		return f, state.ErrNoPlan
	}
	found, err := os.ReadFile(s.ReplanText(r.N))
	if err != nil {
		return f, fmt.Errorf("cannot read the text the replan started from: %w", err)
	}
	draft, err := os.ReadFile(s.DraftPath())
	if err != nil {
		return f, fmt.Errorf("cannot read the draft (%s): %w", s.DraftPath(), err)
	}
	if cur, err := os.ReadFile(pl.File); err != nil || !bytes.Equal(cur, found) {
		closeReplan(s, *r, now, "", "replan_refused", map[string]any{"why": "the plan file changed", "closed": true})
		return f, errors.New(decide.ReplanStale)
	}
	if bytes.Equal(draft, found) {
		say, _ := decide.ReplanDeclined("", true)
		closeReplan(s, *r, now, say, "replan_declined", map[string]any{"why": "the draft is unchanged", "unchanged": true})
		f.Unchanged = true
		return f, nil
	}
	old, err := follow(st, pl, found)
	if err != nil {
		closeReplan(s, *r, now, "", "replan_refused", map[string]any{"why": firstLine(err), "closed": true})
		return f, errors.New(decide.ReplanCannotOpen(pl.File, firstLine(err)))
	}
	frozen, lim := state.Frozen(st, old), LimitsOf(*r)
	next, breaches := check(old, found, draft, frozen, lim, notes)
	if len(breaches) > 0 {
		s.Event("replan_refused", map[string]any{"by": r.By, "n": r.N, "breaches": breaches})
		return f, Refused{breaches, decide.ReplanRefused(breaches, s.DraftPath())}
	}
	changes := plan.Compare(old, found, next, draft)
	rec := state.Replanned{N: r.N, By: r.By, Words: r.Words, At: now, Notes: notes, Changed: changes, Approved: approved(s, old, next, draft)}
	f.Before, f.After = old.Part(unstarted(old, frozen)).Shape(), next.Part(unstarted(next, frozen)).Shape()
	if err := state.ReplacePlanFile(pl.File, draft); err != nil {
		return f, fmt.Errorf("cannot write the draft over the plan file: %w", err)
	}
	written := true
	defer func() {
		p := recover()
		if written && (err != nil || p != nil) {
			state.ReplacePlanFile(pl.File, found) // the plan file goes back as it was
		}
		if p != nil {
			err = fmt.Errorf("the replan failed midway (%v); the plan file is as it was", p)
		}
	}()
	midway()
	what, undo := decide.ReplanWhat(r.By, changes, notes), decide.ReplanUndo(s.ReplanText(r.N), pl.File)
	f.State, err = s.Update(func(st *state.State) error {
		if st.Replan == nil || st.Replan.N != r.N {
			return errors.New(decide.NoReplanOpen) // it lapsed meanwhile
		}
		if again := state.Frozen(*st, old); !sameSet(again, frozen) {
			// A phase started since the check: the draft must leave it as it was too.
			if _, bs := check(old, found, draft, again, lim, notes); len(bs) > 0 {
				return Refused{bs, decide.ReplanRefused(bs, s.DraftPath())}
			}
		}
		took, err := state.AdoptReplan(st, next, now)
		if err != nil {
			return err
		}
		if err := s.WritePlan(next); err != nil {
			return err
		}
		f.Took = took
		f.Decision = state.RecordReplan(st, rec, what, undo)
		st.Replan = nil
		st.Run.Announce = append(st.Run.Announce, decide.Replanned(changes, f.Before, f.After, f.Decision.ID))
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "replanned", Text: decide.ReplanNotice(pl.Title, changes), At: now})
		return nil
	})
	if err != nil {
		var refused Refused
		if errors.As(err, &refused) {
			s.Event("replan_refused", map[string]any{"by": r.By, "n": r.N, "breaches": refused.Breaches})
		}
		return f, err
	}
	written = false
	os.Remove(s.DraftPath())
	f.Plan, f.Replanned = next, rec
	f.Replanned.Decision = f.Decision.ID
	fields := map[string]any{"by": r.By, "n": r.N, "plan": pl.File, "decision": f.Decision.ID, "phases": len(next.Phases),
		"before": shapeFields(f.Before), "after": shapeFields(f.After), "changed": changes, "phases_changed": changedIDs(changes)}
	if f.Took != "" {
		fields["next"] = f.Took
	}
	s.Event("plan_replanned", fields)
	return f, nil
}

// midway runs between the draft written over the plan file and the run following it (tests: what happens
// meanwhile).
var midway = func() {}

// check is plan.Redraft of the draft, and, where its limits let it drop phases, a breach for each it drops
// that the notes do not name.
func check(old plan.Plan, found, draft []byte, frozen map[string]bool, lim plan.Limits, notes string) (plan.Plan, []plan.Breach) {
	next, bs := plan.Redraft(old, found, draft, frozen, lim)
	if !lim.Drop || len(bs) == 1 && bs[0].Kind == plan.BreachBuild {
		return next, bs // a draft that does not build drops nothing yet
	}
	for _, ph := range old.Phases {
		if next.Index(ph.ID) < 0 && !names(notes, ph.ID) {
			bs = append(bs, plan.Breach{Kind: decide.BreachUnsaid, Phase: ph.ID})
		}
	}
	return next, bs
}

// names reports whether text names phase id as a word of its own.
func names(text, id string) bool {
	return regexp.MustCompile(`(^|[^A-Za-z0-9._-])` + regexp.QuoteMeta(id) + `($|[^A-Za-z0-9_-])`).MatchString(text)
}

// approved is what next, the plan of draft, changed of the plan as the human approved it (replan/0.md, read
// as old's phases are read), or nil when that is not at hand.
func approved(s *state.Store, old, next plan.Plan, draft []byte) []plan.Change {
	doc, err := os.ReadFile(s.ReplanText(0))
	if err != nil {
		return nil
	}
	first, err := plan.Rebuild(old, nil, doc)
	if err != nil {
		return nil
	}
	return plan.Compare(first, doc, next, draft)
}

// Decline closes the replan open on run s with the plan unchanged, for the model's reason why
// (`baton replanned --none`).
func Decline(s *state.Store, why string, now time.Time) (state.Replan, error) {
	st, err := s.Load()
	if err != nil {
		return state.Replan{}, err
	}
	if st.Replan == nil {
		return state.Replan{}, errors.New(decide.NoReplanOpen)
	}
	say, _ := decide.ReplanDeclined(why, false)
	return closeReplan(s, *st.Replan, now, say, "replan_declined", map[string]any{"why": why})
}

// Lapse closes replan n of run s, open longer than max, with the plan unchanged: its draft is dropped. It
// reports whether it did (false once that replan is closed already). The caller says when it is due, by
// its own clock.
func Lapse(s *state.Store, n int, now time.Time, max time.Duration) bool {
	st, err := s.Load()
	if err != nil || st.Replan == nil || st.Replan.N != n {
		return false
	}
	_, err = closeReplan(s, *st.Replan, now, decide.ReplanLapsed(max), "replan_lapsed", map[string]any{"after": max.String()})
	return err == nil
}

// closeReplan closes replan r on run s with the plan unchanged, saying say to the human ("" for nothing),
// drops its files, and logs kind with fields.
func closeReplan(s *state.Store, r state.Replan, now time.Time, say, kind string, fields map[string]any) (state.Replan, error) {
	if _, err := s.Update(func(st *state.State) error {
		if st.Replan == nil || st.Replan.N != r.N {
			return errors.New(decide.NoReplanOpen)
		}
		st.Replan = nil
		if say != "" {
			st.Run.Announce = append(st.Run.Announce, say)
		}
		return nil
	}); err != nil {
		return r, err
	}
	s.DropReplan(r.N)
	fields["by"], fields["n"] = r.By, r.N
	s.Event(kind, fields)
	return r, nil
}

// KeepApproved keeps the text of plan pl, which the human approved, as replan/0.md, where every replan of
// it is compared with it; the replans of the plan it replaces go into kept, that plan's record (Archive).
// Best effort: a plan file that is not the text pl was made from is not kept.
func KeepApproved(s *state.Store, pl plan.Plan, kept string) {
	doc, err := os.ReadFile(pl.File)
	if err != nil || sum(doc) != pl.SHA256 {
		return
	}
	if err := s.KeepApproved(doc, kept); err == nil {
		s.Event("plan_kept", map[string]any{"plan": pl.File, "as": s.ReplanText(0)})
	}
}

// Restored is a plan file Restore found copied back from a replan's kept text.
type Restored struct {
	N       int // the kept text: replan/<n>.md (0: the plan as the human approved it)
	Plan    plan.Plan
	Changes []plan.Change
	Took    string
	Err     error // why baton cannot follow it ("" when it does)
}

// Restore follows the plan file of pl when it is a replan's kept text copied back over it (replan/<n>.md),
// which undoes the replans from n on: inside the update of st, which holds the run's lock (the Stop
// hook's). The text must build from pl, and keep every phase that started since: it may drop the phases
// not started that it lacks, and rewords those it has as it has them, which is what undoing a replan
// does. ok is false for a plan file that is no such text, or one baton follows as it is.
func Restore(st *state.State, s *state.Store, pl plan.Plan, now time.Time) (u Restored, ok bool) {
	doc, err := os.ReadFile(pl.File)
	if err != nil || sum(doc) == pl.SHA256 || st.Replan != nil {
		return u, false
	}
	if u.N, ok = s.Restores(doc); !ok {
		return u, false
	}
	next, err := plan.Rebuild(pl, nil, doc)
	if err != nil {
		u.Err = fmt.Errorf("it does not build as a plan: %s", firstLine(err))
		return u, true
	}
	var gone []string
	for id := range state.Frozen(*st, pl) {
		if next.Index(id) < 0 {
			gone = append(gone, id)
		}
	}
	if len(gone) > 0 {
		slices.Sort(gone)
		u.Err = fmt.Errorf("%s started since, and the text copied back lacks %s", strings.Join(gone, ", "), plural(len(gone), "it", "them"))
		return u, true
	}
	if u.Took, err = state.AdoptReplan(st, next, now); err != nil {
		u.Err = err
		return u, true
	}
	if err := s.WritePlan(next); err != nil {
		u.Err = err
		return u, true
	}
	for i := range st.Replans {
		if r := &st.Replans[i]; r.N >= u.N && r.Undone.IsZero() {
			r.Undone = now
		}
	}
	u.Plan, u.Changes = next, plan.Compare(pl, nil, next, doc)
	return u, true
}

// Fields are the fields of the event of a restore baton followed (replan_undone).
func (u Restored) Fields() map[string]any {
	f := map[string]any{"n": u.N, "changed": u.Changes, "phases_changed": changedIDs(u.Changes)}
	if u.Took != "" {
		f["next"] = u.Took
	}
	return f
}

// unstarted lists the phases of p not frozen, in order.
func unstarted(p plan.Plan, frozen map[string]bool) []string {
	var out []string
	for _, ph := range p.Phases {
		if !frozen[ph.ID] {
			out = append(out, ph.ID)
		}
	}
	return out
}

// shapeFields are a shape's numbers, for an event.
func shapeFields(sh plan.Shape) map[string]any {
	return map[string]any{"phases": sh.Phases, "longest_chain": len(sh.Chain), "width": sh.Width}
}

// changedIDs lists the phases changes touch, once each, in order.
func changedIDs(cs []plan.Change) []string {
	var out []string
	for _, c := range cs {
		if c.Kind != plan.ChangeGoal && !slices.Contains(out, c.Phase) {
			out = append(out, c.Phase)
		}
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sum(doc []byte) string {
	s := sha256.Sum256(doc)
	return hex.EncodeToString(s[:])
}

func firstLine(err error) string {
	first, _, _ := strings.Cut(err.Error(), "\n")
	return first
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
