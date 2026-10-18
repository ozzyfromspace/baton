package loop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The merge queue lands each finished worker's phase on the run's branch, in the lead's checkout, while
// the lead works on there (spike 25, docs/research/workers.md). A worker's phase joins the queue once its
// `baton done` completes its sub-run, its verification green; the worker stays, idle, until its work has
// landed. Then, one step at a time and once its session is idle:
//
//  1. if the run's branch moved on, it is merged into the worker's branch, in its worktree (gitx.Merge).
//     A merge that stops is aborted, and the phase goes back to the worker, which merges it in itself
//     (state.Reopen). Nothing baton runs, or asks a worker to run, rewrites a commit: a rebase drops a
//     merge commit, and with it a conflict the worker resolved;
//  2. if the run's branch has commits the one its verification passed on lacks, the phase is verified
//     again on the merged commit (state.Reverify): red takes it up again in the worker. Each landing is
//     verified against the others: a commit a worker landed (of this run or another in the checkout)
//     verifies it again every time. The checkout's own commits (the lead's, or the human's) verify it
//     again once: those made while the merge queue's verification runs are landed over, unverified with
//     it, and the lead is told that its next gate checks them together (Landing.Unverified), so a lead
//     that commits more often than the gate takes cannot hold a landing up for good. The verification
//     again is sized to the change (sized.go): run with the phase's land check if it gave one, and
//     skipped, the phase landing over those commits, when none of them changes a file of its (Markdown
//     aside) and a QA phase not started yet follows it, with aggregate_qa on;
//  3. the branch's changed paths are checked against the lead's local changes, ignored files included,
//     without taking git's locks: on an overlap, landing waits until the lead has committed those files.
//     Once they have held it for Timing.LandNudge, the lead's model is asked to commit them, by path
//     (nudge), once a landing;
//  4. holding the project's landing lock, and only while no tool call of a lead in the checkout runs, nor
//     a gate a lead runs there (baton verify --here, state.HereGate), the checkout fast-forwards to the
//     branch (git merge --ff-only), all or nothing. The run's branch moving on meanwhile sends it back
//     to 1; a locked index tries again shortly. A checkout on another branch that holds the run's
//     branch's tip has the run's branch follow it (follow.go); one on any other lands nothing, and the
//     human is told once a landing, the lead's model once a branch (offBranch): baton never moves it;
//  5. HEAD must be the branch's tip, and the lead's local changes as they were: anything else is
//     reported (land_check);
//  6. the phase is done (state.Landed), the lead is told what landed at its next hook, and the worker is
//     ended, its worktree and branch removed.
//
// The run's branch never moves except by a fast-forward of the checkout, and nothing is stashed.

// landGit is the git the merge queue runs (gitx; tests fake parts of it).
type landGit interface {
	RevParse(root, rev string) string
	Branch(root string) string
	IsAncestor(ctx context.Context, root, a, b string) (bool, error)
	Merge(ctx context.Context, dir, onto, msg string) error
	Changes(ctx context.Context, root, from, to string) ([]gitx.Change, error)
	Commits(ctx context.Context, root, from, to string) ([]string, error)
	Overlap(root string, changes []gitx.Change) ([]string, error)
	FastForward(ctx context.Context, root, commit string) error
	Dirty(root string) ([]string, error)
	MergeBase(ctx context.Context, root, a, b string) (string, error)
	ChangedBetween(ctx context.Context, root, base, to string) ([]string, error)
}

type realGit struct{}

func (realGit) RevParse(root, rev string) string { return gitx.RevParse(root, rev) }
func (realGit) Branch(root string) string        { return gitx.Branch(root) }
func (realGit) IsAncestor(ctx context.Context, root, a, b string) (bool, error) {
	return gitx.IsAncestor(ctx, root, a, b)
}
func (realGit) Merge(ctx context.Context, dir, onto, msg string) error {
	return gitx.Merge(ctx, dir, onto, msg)
}
func (realGit) Changes(ctx context.Context, root, from, to string) ([]gitx.Change, error) {
	return gitx.Changes(ctx, root, from, to)
}
func (realGit) Commits(ctx context.Context, root, from, to string) ([]string, error) {
	return gitx.Commits(ctx, root, from, to)
}
func (realGit) Overlap(root string, changes []gitx.Change) ([]string, error) {
	return gitx.Overlap(root, changes)
}
func (realGit) FastForward(ctx context.Context, root, commit string) error {
	return gitx.FastForward(ctx, root, commit)
}
func (realGit) Dirty(root string) ([]string, error) { return gitx.Dirty(root) }
func (realGit) MergeBase(ctx context.Context, root, a, b string) (string, error) {
	return gitx.MergeBase(ctx, root, a, b)
}
func (realGit) ChangedBetween(ctx context.Context, root, base, to string) ([]string, error) {
	return gitx.ChangedBetween(ctx, root, base, to)
}

func (f *Fleet) gitx() landGit {
	if f.git != nil {
		return f.git
	}
	return realGit{}
}

// How long a landing waits before it tries again: after the lead's local changes were in its way (it
// commits them in its own time), after git failed for another reason, and after a locked index (another
// git command of the lead's, done in a moment).
const (
	overlapRetry = 10 * time.Second
	failedRetry  = time.Minute
	lockedRetry  = 2 * time.Second
)

// workerQuiet is how long a phase agent must have been quiet before baton touches its worktree: a stop
// baton refuses ends no turn, though the sub-run's state says it did.
const workerQuiet = 5 * time.Second

// merge moves the merge queue on by a step: it queues the workers whose phases are finished, then tries
// to land the first one queued that can go now.
func (c *Coordinator) merge(now time.Time, s *state.Store) {
	if c.landAt == nil {
		c.landAt = map[string]time.Time{}
	}
	st, err := s.Load()
	if err != nil {
		return
	}
	for _, w := range c.launched() {
		e := st.Entry(w.entry)
		if e == nil || e.Status != state.WorkerWorking {
			continue
		}
		if sub, err := w.store.Load(); err == nil && sub.Mode == state.ModeComplete {
			c.queue(now, s, w, *e, finishedAt(sub, e.Phase, now))
		}
	}
	if st, err = s.Load(); err != nil || st.Mode != state.ModeRunning {
		return // a paused run lands nothing: its workers' results wait (a block of the lead's holds the lead alone)
	}
	queued := slices.DeleteFunc(slices.Clone(st.Workers), func(e state.Worker) bool { return e.Status != state.WorkerLanding || e.Merge == nil })
	slices.SortStableFunc(queued, func(a, b state.Worker) int { return a.Merge.Queued.Compare(b.Merge.Queued) })
	for _, e := range queued {
		w := c.session(e)
		if w == nil || w.ending != "" || now.Before(c.landAt[e.Ref]) {
			continue
		}
		if c.land(now, s, st, w, e) {
			return
		}
	}
}

// session is the phase agent of this host that works for e, or nil.
func (c *Coordinator) session(e state.Worker) *worker {
	for _, w := range c.launched() {
		if w.entry.Is(e) {
			return w
		}
	}
	return nil
}

// queue puts worker e, whose phase its sub-run finished at finished, in the merge queue, with its
// branch's tip as it finished (Worker.Tip).
func (c *Coordinator) queue(now time.Time, s *state.Store, w *worker, e state.Worker, finished time.Time) {
	tip := c.Fleet.gitx().RevParse(s.Checkout, "refs/heads/"+e.Branch)
	c.setEntry(s, e, func(x *state.Worker) {
		x.Status, x.Since, x.Merge, x.Finished, x.Tip = state.WorkerLanding, now, &state.Merge{Queued: now}, finished, tip
	})
	s.Event("land_queued", map[string]any{"phase": e.Phase, "branch": e.Branch})
	c.announce(s, fmt.Sprintf("baton: %s is finished → its work waits to land on the run's branch", e.Phase))
}

// finishedAt is when sub-run sub, complete, finished phase: its done on record, or now for none (or one
// ahead of now's clock).
func finishedAt(sub state.State, phase string, now time.Time) time.Time {
	if ps := sub.Phases[phase]; ps != nil && !ps.DoneAt.IsZero() && !ps.DoneAt.After(now) {
		return ps.DoneAt
	}
	return now
}

// land works on landing worker e, whose session this host runs as w, in the run whose state is st. It
// reports whether it did something that took this step: it merged, landed, or handed the phase back.
func (c *Coordinator) land(now time.Time, s *state.Store, st state.State, w *worker, e state.Worker) bool {
	g := c.Fleet.gitx()
	sub, err := w.store.Load()
	if err != nil {
		return false
	}
	m := e.Merge
	if m.Reverify != "" {
		switch ps := sub.Phases[e.Phase]; {
		case sub.Mode == state.ModeComplete:
			c.setEntry(s, e, func(x *state.Worker) { x.Merge.Reverify = "" })
		case ps != nil && ps.Status == state.PhaseVerifying:
			return false // its verification runs again: the result decides
		default:
			c.returned(now, s, e, "its verification failed on its branch, with the run's branch merged in", nil)
			return true
		}
	} else if sub.Mode != state.ModeComplete {
		c.returned(now, s, e, "its phase was taken up again", nil)
		return true
	}
	if sub.Run.TurnOpen || w.sending != 0 || now.Sub(sub.Run.LastActivity) < workerQuiet {
		return false // its agent is at work in its worktree, or a message on its way to it would start it there
	}
	if !isDir(e.Worktree) {
		c.end(now, s, c.bridge(s), w, "its worktree is gone")
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	onto := g.RevParse(s.Checkout, "refs/heads/"+st.RunBranch)
	tip := g.RevParse(s.Checkout, "refs/heads/"+e.Branch)
	if onto == "" || tip == "" {
		c.deferLanding(now, s, e, fmt.Sprintf("the branch %s or %s is not there", st.RunBranch, e.Branch), nil, failedRetry)
		return false
	}
	if ok, err := g.IsAncestor(ctx, s.Checkout, onto, tip); err != nil {
		c.deferLanding(now, s, e, err.Error(), nil, failedRetry)
		return false
	} else if !ok {
		return c.mergeIn(now, s, st, w, e, onto, tip)
	}
	var over unverified
	if j := verified(sub, e.Phase); j != nil && j.Commit != tip {
		if ok, err := g.IsAncestor(ctx, s.Checkout, onto, j.Commit); err == nil && !ok {
			since, err := g.Commits(ctx, s.Checkout, j.Commit, onto)
			if err != nil {
				c.deferLanding(now, s, e, err.Error(), nil, failedRetry)
				return false
			}
			var sk skip
			if why := c.moved(s, st, since); why == movedLanded || !j.Requeue {
				if sk = c.sized(ctx, s, st, e, since, onto, tip); sk.qa == "" {
					c.reverify(now, s, st, w, e, *j, tip, why)
					return true
				}
			}
			over = unverified{verified: j.Commit, commits: since, skip: sk}
		}
	}
	return c.fastForward(ctx, now, s, st, e, onto, tip, over)
}

// How the run's branch moved on since a verification passed (the event reverify_queued's why): a worker's
// landing came, or only the checkout's own commits did.
const (
	movedLanded = "landed"
	movedLead   = "lead"
)

// moved says how the run's branch moved on by commits: movedLanded if a worker's landing brought one of
// them, in this run (st) or in another run of the checkout, else movedLead.
func (c *Coordinator) moved(s *state.Store, st state.State, commits []string) string {
	landed := map[string]bool{}
	add := func(ls []state.Landing) {
		for _, l := range ls {
			for _, x := range l.Commits {
				landed[x] = true
			}
		}
	}
	add(st.Landed)
	for _, r := range c.Project.Runs() {
		if r.ID != s.Ref {
			add(r.State.Landed)
		}
	}
	for _, x := range commits {
		if landed[x] {
			return movedLanded
		}
	}
	return movedLead
}

// unverified are the checkout's own commits a landing goes over without its verification, which passed
// on verified before them; or, when its verification again was skipped (skip, sized.go), every commit
// that came on the run's branch since.
type unverified struct {
	verified string
	commits  []string
	skip
}

// mergeIn merges the run's branch, at onto, into worker e's branch, at tip. A merge that does not go
// through hands the phase back to the worker.
func (c *Coordinator) mergeIn(now time.Time, s *state.Store, st state.State, w *worker, e state.Worker, onto, tip string) bool {
	g := c.Fleet.gitx()
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if err := g.Merge(ctx, e.Worktree, onto, decide.MergeMessage(st.RunBranch, e.Branch)); err != nil {
		var re *gitx.MergeError
		if !errors.As(err, &re) {
			c.deferLanding(now, s, e, "baton could not merge the run's branch into its branch: "+err.Error(), nil, failedRetry)
			return false
		}
		text, why := decide.LandRefused(e.Phase, st.RunBranch, re.Msg), "baton could not merge the run's branch into its branch: "+re.Msg
		if len(re.Paths) > 0 {
			text, why = decide.LandConflict(e.Phase, st.RunBranch, re.Paths), "its branch conflicts with the run's in "+strings.Join(re.Paths, ", ")
		}
		if _, err := w.store.Update(func(sub *state.State) error {
			return state.Reopen(sub, e.Phase, state.Message{Kind: state.MessageLand, Text: text}, now)
		}); err != nil {
			c.deferLanding(now, s, e, "baton could not hand its phase back: "+err.Error(), nil, failedRetry)
			return false
		}
		c.returned(now, s, e, why, re.Paths)
		return true
	}
	merged := g.RevParse(s.Checkout, "refs/heads/"+e.Branch)
	c.setEntry(s, e, func(x *state.Worker) {
		x.Merge.Merged, x.Merge.Onto, x.Merge.Tries = merged, onto, x.Merge.Tries+1
	})
	s.Event("merged_in", map[string]any{"phase": e.Phase, "onto": onto, "from": tip, "to": merged})
	c.announce(s, fmt.Sprintf("baton: merged %s into %s's branch (now %s)", st.RunBranch, e.Phase, short(merged)))
	return true
}

// verified is the gate phase's sub-run last passed on a commit (its latest verify, which stands beside any
// wait on something outside), or nil.
func verified(sub state.State, phase string) *state.Job {
	j := sub.LatestOf(phase, state.JobVerify)
	if j == nil || j.Commit == "" || j.Status != state.JobPassed {
		return nil
	}
	return j
}

// reverify verifies worker e's phase again in its sub-run, on its merged branch at tip, with the
// verification j it passed before the run's branch moved on (why: moved).
func (c *Coordinator) reverify(now time.Time, s *state.Store, st state.State, w *worker, e state.Worker, j state.Job, tip, why string) {
	var q state.Job
	notes := decide.ReverifyNotes(st.RunBranch, j.Notes, why == movedLead)
	var command string
	_, err := w.store.Update(func(sub *state.State) error {
		command = j.Command
		if ps := sub.Phases[e.Phase]; ps != nil && ps.LandCheck != "" {
			command = ps.LandCheck // the focused check it gave with its done, in its gate's place
		}
		var err error
		q, err = state.Reverify(sub, state.Job{Phase: e.Phase, Command: command, Commit: tip, Here: j.Here, Notes: notes}, now)
		return err
	})
	if err != nil {
		c.deferLanding(now, s, e, "baton could not verify it again: "+err.Error(), nil, failedRetry)
		return
	}
	c.setEntry(s, e, func(x *state.Worker) { x.Merge.Reverify = q.ID })
	fields := map[string]any{"phase": e.Phase, "job": q.ID, "command": command, "commit": tip, "verified": j.Commit, "why": why}
	by := ""
	if command != j.Command {
		fields["land_check"], by = true, ", by its land check"
	}
	s.Event("reverify_queued", fields)
	came := "other work landed on " + st.RunBranch
	if why == movedLead {
		came = "commits were made on " + st.RunBranch + " in the checkout"
	}
	c.announce(s, fmt.Sprintf("baton: %s since %s was verified → verifying it again with the run's branch merged in%s (job %s, `%s`)", came, e.Phase, by, q.ID, command))
}

// fastForward lands worker e's branch, at tip, with the run's branch at onto merged in, over the commits
// there its verification did not see (over): the checkout fast-forwards to it, unless the lead's local
// changes are in its way, or a tool call or a gate of a lead in the checkout is running. A checkout on
// another branch that holds the run's has the run's branch follow it (FollowBranch). It reports whether
// it landed, or the run's branch followed the checkout's, which ends the step: it lands at the next.
func (c *Coordinator) fastForward(ctx context.Context, now time.Time, s *state.Store, st state.State, e state.Worker, onto, tip string, over unverified) bool {
	g := c.Fleet.gitx()
	if c.busy(now, s) != "" {
		return false // checked again under the landing lock; until then, the probe can wait too
	}
	if gate := c.gate(s); gate != "" {
		c.deferLanding(now, s, e, gate, nil, overlapRetry)
		return false
	}
	if br := g.Branch(s.Checkout); br != st.RunBranch {
		seen, followed := FollowBranch(ctx, s)
		if followed {
			return true // the run's branch is the checkout's now: the next step lands on it
		}
		c.deferLanding(now, s, e, fmt.Sprintf("the lead's checkout is on %q, not on the run's branch %s", br, st.RunBranch), nil, overlapRetry)
		c.offBranch(now, s, e, br, seen)
		return false
	}
	changes, err := g.Changes(ctx, s.Checkout, onto, tip)
	if err != nil {
		c.deferLanding(now, s, e, err.Error(), nil, failedRetry)
		return false
	}
	overlap, err := g.Overlap(s.Checkout, changes)
	if err != nil {
		c.deferLanding(now, s, e, err.Error(), nil, failedRetry)
		return false
	}
	if len(overlap) > 0 {
		c.deferLanding(now, s, e, "the lead has local changes to "+strings.Join(overlap, ", ")+", which its work changes too", overlap, overlapRetry)
		c.nudge(now, s, e, overlap)
		return false
	}
	if !e.Merge.Overlap.IsZero() {
		c.setEntry(s, e, func(x *state.Worker) {
			if x.Merge != nil {
				x.Merge.Overlap = time.Time{} // the lead's changes are out of its way: a new overlap counts afresh
			}
		})
	}
	commits, err := g.Commits(ctx, s.Checkout, onto, tip)
	if err != nil {
		c.deferLanding(now, s, e, err.Error(), nil, failedRetry)
		return false
	}
	release, ok, err := state.HoldLanding(s.Project)
	if !ok {
		return false // a lead's tool call is checking for a landing this moment
	}
	defer release()
	if busy := c.busy(now, s); busy != "" {
		c.Lead.logf("loop: %s: waits to land: %s", e.Phase, busy)
		return false
	}
	if gate := c.gate(s); gate != "" {
		release()
		c.deferLanding(now, s, e, gate, nil, overlapRetry)
		return false
	}
	local, _ := g.Dirty(s.Checkout)
	before := gitx.Fingerprint(s.Checkout, local)
	err = g.FastForward(ctx, s.Checkout, tip)
	var check string
	if err == nil {
		check = c.check(s, tip, local, before)
	}
	release()
	switch {
	case errors.Is(err, gitx.ErrNotFastForward):
		return false // the run's branch moved on: the next step merges it in again
	case errors.Is(err, gitx.ErrLocked):
		c.landAt[e.Ref] = now.Add(lockedRetry)
		return false
	case errors.Is(err, gitx.ErrOverwrite):
		c.deferLanding(now, s, e, "the lead's local changes are in its way: "+err.Error(), nil, overlapRetry)
		return false
	case err != nil:
		c.deferLanding(now, s, e, err.Error(), nil, failedRetry)
		return false
	}
	var files []string
	for _, ch := range changes {
		files = append(files, ch.Path)
	}
	if over.qa != "" {
		s.Event("reverify_skipped", map[string]any{"phase": e.Phase, "why": over.why, "qa": over.qa, "verified": over.verified, "commits": over.commits})
	}
	c.landed(now, s, e, state.Landing{Phase: e.Phase, Commits: commits, Files: files, Head: tip, At: now, Verified: over.verified, Unverified: over.commits, QA: over.qa})
	if check != "" {
		s.Event("land_check", map[string]any{"phase": e.Phase, "problem": check})
		c.announce(s, fmt.Sprintf("baton: after %s landed, %s: look over the checkout", e.Phase, check))
	}
	return true
}

// check says what is wrong with the checkout after a landing at tip, or "": HEAD must be tip, and the
// lead's local changes (local, whose content was before) as they were. A fast-forward touches neither,
// so anything else is something baton does not know about, such as a process of the lead's writing files.
func (c *Coordinator) check(s *state.Store, tip string, local []string, before map[string]string) string {
	g := c.Fleet.gitx()
	if head := g.RevParse(s.Checkout, "HEAD"); head != tip {
		return fmt.Sprintf("HEAD is %s, not the landed %s", short(head), short(tip))
	}
	after, _ := g.Dirty(s.Checkout)
	var changed []string
	for p, fp := range gitx.Fingerprint(s.Checkout, local) {
		if before[p] != fp {
			changed = append(changed, p)
		}
	}
	for _, p := range after {
		if _, ok := before[p]; !ok {
			changed = append(changed, p)
		}
	}
	if len(changed) == 0 {
		return ""
	}
	slices.Sort(changed)
	return "the lead's local changes to " + strings.Join(changed, ", ") + " changed as it landed"
}

// busy names a tool call running in the checkout, by the lead of this run or of another live run of the
// project, or "" if none is.
func (c *Coordinator) busy(now time.Time, s *state.Store) string {
	st, err := s.Load()
	if err != nil {
		return err.Error()
	}
	calls := st.Run.InFlight(now)
	for _, r := range c.Project.Others(s.Ref) {
		calls = append(calls, r.State.Run.InFlight(now)...)
	}
	if len(calls) == 0 {
		return ""
	}
	return fmt.Sprintf("a tool call (%s) runs in the checkout", calls[0].Tool)
}

// gate names a gate running in the checkout, by the lead of this run or of another live run of the
// project (baton verify --here), or "" if none is: it reads the checkout's files as it runs, so nothing
// lands under it.
func (c *Coordinator) gate(s *state.Store) string {
	st, err := s.Load()
	if err != nil {
		return err.Error()
	}
	j := st.HereGate()
	for _, r := range c.Project.Others(s.Ref) {
		if j == nil {
			j = r.State.HereGate()
		}
	}
	if j == nil {
		return ""
	}
	return heldByGate(j.ID)
}

// landed records worker e's work landed (l): its phase is done, the lead is told, and the worker ends.
func (c *Coordinator) landed(now time.Time, s *state.Store, e state.Worker, l state.Landing) {
	pl, _ := s.LoadPlan()
	var next string
	var complete bool
	var done string
	var saving state.Saving
	before, _ := s.Load()
	workers := s.WorkerDecisions(before) // read before the run's lock is taken: no code holds two
	_, err := s.Update(func(st *state.State) error {
		var err error
		if next, err = state.Landed(st, pl, l, now); err != nil {
			return err
		}
		if complete = st.Mode == state.ModeComplete && !st.Run.CompleteNotified; complete {
			// The lead holds no phase, so no stop of its own reports the plan complete.
			st.Run.CompleteNotified = true
			saving = state.SavingOf(*st)
			done = decide.PlanDone(len(pl.Phases), len(decide.WithoutHuman(st.Decisions))+len(decide.WithoutHuman(workers)),
				len(decide.LeftForYou(st.Decisions))+len(decide.LeftForYou(workers)), saving)
			st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "plan_complete", Text: pl.Title + ": " + done, At: now})
		}
		return nil
	})
	if err != nil {
		c.Lead.logf("loop: %s: landed, but not recorded: %v", e.Phase, err)
		s.Event("landed", map[string]any{"phase": e.Phase, "commits": l.Commits, "head": l.Head, "unrecorded": err.Error()})
		return
	}
	fields := map[string]any{"phase": e.Phase, "commits": l.Commits, "files": l.Files, "head": l.Head}
	if next != "" {
		fields["next"] = next
	}
	over := ""
	switch {
	case len(l.Unverified) > 0 && l.QA != "":
		fields["verified"], fields["unverified"], fields["qa"] = l.Verified, l.Unverified, l.QA
		over = decide.SkippedOver(l.Unverified, l.QA)
	case len(l.Unverified) > 0:
		fields["verified"], fields["unverified"] = l.Verified, l.Unverified
		var shorts []string
		for _, x := range l.Unverified {
			shorts = append(shorts, short(x))
		}
		over = fmt.Sprintf(", over the checkout's %s %s, which its gate did not run with: the lead's next gate runs them together",
			plural(len(l.Unverified), "commit", "commits"), strings.Join(shorts, ", "))
	}
	s.Event("landed", fields)
	c.announce(s, fmt.Sprintf("baton: %s landed on the run's branch: %d %s (%s)%s", e.Phase, len(l.Commits), plural(len(l.Commits), "commit", "commits"), filesLine(l.Files), over))
	switch {
	case next != "":
		s.Event("compact_queued", map[string]any{"reason": "boundary", "next": next, "by": "landed"})
		c.announce(s, fmt.Sprintf("baton: %s can start → compacting, then %s starts", next, next))
	case complete:
		s.Event("plan_complete", saving.Log(map[string]any{"by": "landed"}))
		c.announce(s, "baton: plan complete — "+done)
	}
	if w := c.session(e); w != nil {
		c.end(now, s, c.bridge(s), w, "its work landed")
	}
}

// filesLine names the files a landing changed, the first few of them.
func filesLine(files []string) string {
	if len(files) > 5 {
		return strings.Join(files[:5], ", ") + fmt.Sprintf(" and %d more", len(files)-5)
	}
	return strings.Join(files, ", ")
}

// returned records that worker e's phase is back with it, for why (paths: what conflicted): it is no
// longer queued, and joins the queue again once its `baton done` completes it again. A conflict counts
// toward the landings in a row that conflicted (State.Conflicts), which hold new workers back.
func (c *Coordinator) returned(now time.Time, s *state.Store, e state.Worker, why string, paths []string) {
	s.Update(func(st *state.State) error {
		if x := st.Entry(e); x != nil {
			x.Status, x.Since, x.Merge = state.WorkerWorking, now, nil
			if len(paths) > 0 {
				st.Conflicts++
				state.CountReturned(st, e.Phase) // the risk ledger's (risk.go)
			}
		}
		return nil
	})
	fields := map[string]any{"phase": e.Phase, "why": why}
	if len(paths) > 0 {
		fields["paths"] = paths
	}
	s.Event("land_returned", fields)
	c.conflicted(now, s, e.Phase, paths)
	c.announce(s, fmt.Sprintf("baton: %s's work did not land: %s → its worker takes it up again", e.Phase, why))
}

// nudge records that the lead's uncommitted changes to paths hold worker e's landing, and once they have
// held it for Timing.LandNudge, asks the lead's model to commit them, by path (state.MessageCommit): once
// a landing. A lead left untold would never know it holds the landing up: a model commits when its own
// work calls for it.
func (c *Coordinator) nudge(now time.Time, s *state.Store, e state.Worker, paths []string) {
	m := e.Merge
	after := c.Lead.Timing.LandNudge
	switch {
	case m.Overlap.IsZero():
		c.setEntry(s, e, func(x *state.Worker) {
			if x.Merge != nil && x.Merge.Overlap.IsZero() {
				x.Merge.Overlap = now
			}
		})
		return
	case !m.Nudged.IsZero() || after <= 0 || now.Sub(m.Overlap) < after:
		return
	}
	waited := now.Sub(m.Overlap)
	told := false
	s.Update(func(st *state.State) error {
		x := st.Entry(e)
		if x == nil || x.Merge == nil || !x.Merge.Nudged.IsZero() {
			return nil
		}
		x.Merge.Nudged, told = now, true
		state.Tell(st, state.Message{Kind: state.MessageCommit, Text: decide.LandCommit(e.Phase, st.RunBranch, paths, waited), About: "land:" + e.Ref}, now)
		st.Run.Announce = append(st.Run.Announce, decide.LandNudged(e.Phase, paths, waited))
		return nil
	})
	if told {
		c.Lead.logf("loop: %s: the lead's changes to %s have held its landing for %s: asked the lead to commit them", e.Phase, strings.Join(paths, ", "), waited.Round(time.Second))
		s.Event("land_nudged", map[string]any{"phase": e.Phase, "paths": paths, "waited": waited.Round(time.Second).String()})
	}
}

// offBranch pushes the human, once a landing, that worker e's work waits for the checkout, on the branch
// on, to be back on the run's branch or on one that holds it: nothing of baton's moves a checkout from one
// branch to another. The lead's model is told once a branch how to let it land (state.TellBranch), seen
// being the checkout as git said it: its model may merge, which the human need not.
func (c *Coordinator) offBranch(now time.Time, s *state.Store, e state.Worker, on string, seen state.BranchSeen) {
	var branch string
	pushed, told := false, false
	s.Update(func(st *state.State) error {
		told = state.TellBranch(st, seen, decide.BranchUnfollowed(e.Phase, seen.On, seen.Run), now)
		x := st.Entry(e)
		if x == nil || x.Merge == nil || !x.Merge.OffBranch.IsZero() {
			return nil
		}
		x.Merge.OffBranch, branch, pushed = now, st.RunBranch, true
		st.Run.Notices = append(st.Run.Notices, state.Notice{Kind: "land_off_branch", Text: decide.LandOffBranch(e.Phase, on, branch), At: now})
		return nil
	})
	if pushed {
		s.Event("land_off_branch", map[string]any{"phase": e.Phase, "branch": on, "run_branch": branch})
	}
	if told {
		s.Event("run_branch_unfollowed", map[string]any{"phase": e.Phase, "branch": seen.On, "run_branch": seen.Run})
	}
}

// deferLanding records why worker e's work waits to land (paths: the lead's files in its way), and tries
// again after retry. A reason is logged and announced once.
func (c *Coordinator) deferLanding(now time.Time, s *state.Store, e state.Worker, why string, paths []string, retry time.Duration) {
	c.landAt[e.Ref] = now.Add(retry)
	if e.Merge != nil && e.Merge.Deferred == why {
		return
	}
	c.setEntry(s, e, func(x *state.Worker) {
		if x.Merge != nil {
			x.Merge.Deferred, x.Merge.DeferredAt = why, now
		}
	})
	fields := map[string]any{"phase": e.Phase, "why": why}
	if len(paths) > 0 {
		fields["paths"] = paths
	}
	s.Event("land_deferred", fields)
	c.announce(s, fmt.Sprintf("baton: %s waits to land: %s", e.Phase, why))
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
