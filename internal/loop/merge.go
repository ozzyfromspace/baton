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
//  1. its branch is rebased onto the run's branch, in its worktree. A rebase that stops is aborted, and
//     the phase goes back to the worker, which rebases it itself (state.Reopen);
//  2. if anything landed on the run's branch since the commit its verification passed on, the phase is
//     verified again on the rebased commit (state.Reverify): red takes it up again in the worker;
//  3. the branch's changed paths are checked against the lead's local changes, ignored files included,
//     without taking git's locks: on an overlap, landing waits until the lead has committed those files;
//  4. holding the project's landing lock, and only while no tool call of a lead in the checkout runs,
//     the checkout fast-forwards to the branch (git merge --ff-only), all or nothing. The run's branch
//     moving on meanwhile sends it back to 1; a locked index tries again shortly;
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
	Rebase(ctx context.Context, dir, onto string) error
	Changes(ctx context.Context, root, from, to string) ([]gitx.Change, error)
	Commits(ctx context.Context, root, from, to string) ([]string, error)
	Overlap(root string, changes []gitx.Change) ([]string, error)
	FastForward(ctx context.Context, root, commit string) error
	Dirty(root string) ([]string, error)
}

type realGit struct{}

func (realGit) RevParse(root, rev string) string { return gitx.RevParse(root, rev) }
func (realGit) Branch(root string) string        { return gitx.Branch(root) }
func (realGit) IsAncestor(ctx context.Context, root, a, b string) (bool, error) {
	return gitx.IsAncestor(ctx, root, a, b)
}
func (realGit) Rebase(ctx context.Context, dir, onto string) error {
	return gitx.Rebase(ctx, dir, onto)
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

// workerQuiet is how long a worker's session must have been quiet before baton touches its worktree: a
// stop baton refuses ends no turn, though the session's state says it did.
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
		e := rosterEntry(&st, w.entry.Ref, w.entry.Session)
		if e == nil || e.Status != state.WorkerWorking {
			continue
		}
		if sub, err := w.store.Load(); err == nil && sub.Mode == state.ModeComplete {
			c.queue(now, s, w, *e)
		}
	}
	if st, err = s.Load(); err != nil || st.Mode != state.ModeRunning || st.Blocked != nil {
		return // a paused run lands nothing: its workers' results wait
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

// session is the worker this host runs as e, or nil.
func (c *Coordinator) session(e state.Worker) *worker {
	for _, w := range c.launched() {
		if w.entry.Ref == e.Ref && w.entry.Session == e.Session {
			return w
		}
	}
	return nil
}

// queue puts worker e, whose phase is finished, in the merge queue.
func (c *Coordinator) queue(now time.Time, s *state.Store, w *worker, e state.Worker) {
	c.setEntry(s, e, func(x *state.Worker) {
		x.Status, x.Since, x.Merge = state.WorkerLanding, now, &state.Merge{Queued: now}
	})
	s.Event("land_queued", map[string]any{"phase": e.Phase, "branch": e.Branch})
	c.announce(s, fmt.Sprintf("baton: %s is finished → its work waits to land on the run's branch", e.Phase))
}

// land works on landing worker e, whose session this host runs as w, in the run whose state is st. It
// reports whether it did something that took this step: it rebased, landed, or handed the phase back.
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
			c.returned(now, s, e, "its verification failed on its branch rebased onto the run's branch", nil)
			return true
		}
	} else if sub.Mode != state.ModeComplete {
		c.returned(now, s, e, "its phase was taken up again", nil)
		return true
	}
	if sub.Run.TurnOpen || now.Sub(sub.Run.LastActivity) < workerQuiet {
		return false // its session is still at work, in its worktree
	}
	if !isDir(e.Worktree) {
		c.end(w, "its worktree is gone")
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
		return c.rebase(now, s, st, w, e, onto, tip)
	}
	if j := verified(sub, e.Phase); j != nil && j.Commit != tip {
		if ok, err := g.IsAncestor(ctx, s.Checkout, onto, j.Commit); err == nil && !ok {
			c.reverify(now, s, st, w, e, *j, tip)
			return true
		}
	}
	return c.fastForward(ctx, now, s, st, e, onto, tip)
}

// rebase rebases worker e's branch, at tip, onto the run's branch, at onto. A rebase that does not go
// through hands the phase back to the worker.
func (c *Coordinator) rebase(now time.Time, s *state.Store, st state.State, w *worker, e state.Worker, onto, tip string) bool {
	g := c.Fleet.gitx()
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if err := g.Rebase(ctx, e.Worktree, onto); err != nil {
		var re *gitx.RebaseError
		if !errors.As(err, &re) {
			c.deferLanding(now, s, e, "baton could not rebase its branch: "+err.Error(), nil, failedRetry)
			return false
		}
		text, why := decide.LandRefused(e.Phase, st.RunBranch, re.Msg), "baton could not rebase its branch: "+re.Msg
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
	rebased := g.RevParse(s.Checkout, "refs/heads/"+e.Branch)
	c.setEntry(s, e, func(x *state.Worker) {
		x.Merge.Rebased, x.Merge.Onto, x.Merge.Tries = rebased, onto, x.Merge.Tries+1
	})
	s.Event("rebased", map[string]any{"phase": e.Phase, "onto": onto, "from": tip, "to": rebased})
	c.announce(s, fmt.Sprintf("baton: rebased %s's branch onto %s (now %s)", e.Phase, st.RunBranch, short(rebased)))
	return true
}

// verified is the verification phase's sub-run last passed on a commit, or nil.
func verified(sub state.State, phase string) *state.Job {
	j := sub.Latest(phase)
	if j == nil || j.Kind != state.JobVerify || j.Commit == "" || j.Status != state.JobPassed {
		return nil
	}
	return j
}

// reverify verifies worker e's phase again in its sub-run, on its rebased branch at tip, with the
// verification j it passed before the run's branch moved on.
func (c *Coordinator) reverify(now time.Time, s *state.Store, st state.State, w *worker, e state.Worker, j state.Job, tip string) {
	var q state.Job
	_, err := w.store.Update(func(sub *state.State) error {
		var err error
		q, err = state.Reverify(sub, state.Job{Phase: e.Phase, Command: j.Command, Commit: tip, Notes: decide.ReverifyNotes(st.RunBranch, j.Notes)}, now)
		return err
	})
	if err != nil {
		c.deferLanding(now, s, e, "baton could not verify it again: "+err.Error(), nil, failedRetry)
		return
	}
	c.setEntry(s, e, func(x *state.Worker) { x.Merge.Reverify = q.ID })
	s.Event("reverify_queued", map[string]any{"phase": e.Phase, "job": q.ID, "command": j.Command, "commit": tip, "verified": j.Commit})
	c.announce(s, fmt.Sprintf("baton: other work landed on %s since %s was verified → verifying it again on its rebased branch (job %s, `%s`)",
		st.RunBranch, e.Phase, q.ID, j.Command))
}

// fastForward lands worker e's branch, at tip, rebased onto the run's branch at onto: the checkout
// fast-forwards to it, unless the lead's local changes are in its way, or a tool call of a lead in the
// checkout is running. It reports whether it landed.
func (c *Coordinator) fastForward(ctx context.Context, now time.Time, s *state.Store, st state.State, e state.Worker, onto, tip string) bool {
	g := c.Fleet.gitx()
	if c.busy(now, s) != "" {
		return false // checked again under the landing lock; until then, the probe can wait too
	}
	if br := g.Branch(s.Checkout); br != st.RunBranch {
		c.deferLanding(now, s, e, fmt.Sprintf("the lead's checkout is on %q, not on the run's branch %s", br, st.RunBranch), nil, overlapRetry)
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
		return false
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
		return false // the run's branch moved on: the next step rebases again
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
	c.landed(now, s, e, state.Landing{Phase: e.Phase, Commits: commits, Files: files, Head: tip, At: now})
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

// landed records worker e's work landed (l): its phase is done, the lead is told, and the worker ends.
func (c *Coordinator) landed(now time.Time, s *state.Store, e state.Worker, l state.Landing) {
	pl, _ := s.LoadPlan()
	var next string
	var complete bool
	var done string
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
			done = decide.PlanDone(len(pl.Phases), len(decide.WithoutHuman(st.Decisions))+len(decide.WithoutHuman(workers)))
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
	s.Event("landed", fields)
	c.announce(s, fmt.Sprintf("baton: %s landed on the run's branch: %d %s (%s)", e.Phase, len(l.Commits), plural(len(l.Commits), "commit", "commits"), filesLine(l.Files)))
	switch {
	case next != "":
		s.Event("compact_queued", map[string]any{"reason": "boundary", "next": next, "by": "landed"})
		c.announce(s, fmt.Sprintf("baton: %s can start → compacting, then %s starts", next, next))
	case complete:
		s.Event("plan_complete", map[string]any{"by": "landed"})
		c.announce(s, "baton: plan complete — "+done)
	}
	if w := c.session(e); w != nil {
		c.end(w, "its work landed")
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
		if x := rosterEntry(st, e.Ref, e.Session); x != nil {
			x.Status, x.Since, x.Merge = state.WorkerWorking, now, nil
			if len(paths) > 0 {
				st.Conflicts++
			}
		}
		return nil
	})
	fields := map[string]any{"phase": e.Phase, "why": why}
	if len(paths) > 0 {
		fields["paths"] = paths
	}
	s.Event("land_returned", fields)
	c.announce(s, fmt.Sprintf("baton: %s's work did not land: %s → its worker takes it up again", e.Phase, why))
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
