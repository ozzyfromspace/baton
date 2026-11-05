package loop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// baton's reviewer of the model's own background shells (tasks.go): a model, review_model (Sonnet by default,
// at its API's default effort, high), asked through baton's mod (Complete) whether a suspect can do nothing
// more for the run, from what baton has on record of it (decide.TaskReviewPrompt). Its first line is its
// verdict, which alone decides:
//
//   - STOP: the task is stopped (state.TaskStop): the mod's stop command calls TaskStop with its id, and
//     tasks.go's stops sees it through;
//   - KEEP <minutes>: it is looked at again then, or sooner on a kind of signal no review has seen;
//   - UNSURE, an answer that is none of the three, or none at all (task_review off, no mod, a serial run,
//     past Timing.ReviewWait, an API error): its owner is asked once to look (look_in task_suspect), the human
//     is told once (background_suspect), and nothing is stopped.
//
// Each review logs task_reviewed. One is in flight per task at a time (state.Runtime.AskReview), and a task
// gets state.MaxReviews in all.

// ReviewWait is how long the reviewer's verdict is waited for, unless Timing.ReviewWait says otherwise.
const ReviewWait = 3 * time.Minute

// Reviewer is the reviewer's settings, task_review and review_model, as the host carries them from its config.
type Reviewer struct {
	On    bool
	Model string // "" for config.DefaultReviewModel
}

// Why a review gave no verdict, beside Complete's own reasons (Unanswered.Why).
const (
	reviewOff     = "off"     // task_review is off
	reviewRestart = "restart" // the host that asked is gone: no call in this host's memory stands for it
	reviewPanic   = "panic"
)

// reviewWait is how long the reviewer's verdict is waited for.
func (c *Coordinator) reviewWait() time.Duration {
	if w := c.Lead.Timing.ReviewWait; w > 0 {
		return w
	}
	return ReviewWait
}

// reviewModel is the reviewer's model.
func (c *Coordinator) reviewModel() string {
	if c.Reviewer.Model != "" {
		return c.Reviewer.Model
	}
	return config.DefaultReviewModel
}

// reviewLapsed settles a review of suspect s of loop x (its record rv) that no call in this host's memory stands
// for (an earlier host asked it), or that has outlasted its wait by a minute: as no verdict, so its owner is
// asked to look. It reports whether it did.
func (c *Coordinator) reviewLapsed(now time.Time, lead *state.Store, x taskLoop, s state.Suspect, rv *state.TaskReview) bool {
	if rv == nil || rv.Asking.IsZero() {
		return false
	}
	c.tk.mu.Lock()
	asked, mine := c.tk.asks[taskKey(x.store, s.Task.ID)]
	c.tk.mu.Unlock()
	why := ""
	switch {
	case !mine || !asked.Equal(rv.Asking):
		why = reviewRestart
	case now.Sub(rv.Asking) >= c.reviewWait()+time.Minute:
		why = TimedOut
	default:
		return false
	}
	c.verdict(now, lead, x, s, rv.Asking, "", 0, "", why, 0)
	return true
}

// askReview asks the reviewer about suspect s of loop x, given on kinds of signal, off the tick: the review
// is recorded in flight first (state.Runtime.AskReview), so no other is asked meanwhile. pgid is its shell's
// process group, 0 when not found. It reports whether it asked.
func (c *Coordinator) askReview(now time.Time, lead *state.Store, x taskLoop, s state.Suspect, kinds []string, pgid int) bool {
	asked, earlier := false, ""
	x.store.Update(func(st *state.State) error {
		if rv := st.Run.Reviews[s.Task.ID]; rv != nil && rv.Verdict != "" {
			earlier = strings.ToUpper(rv.Verdict)
			if rv.Reason != "" {
				earlier += ": " + rv.Reason
			}
		}
		asked = st.Run.AskReview(s.Task.ID, kinds, now)
		return nil
	})
	if !asked {
		return false
	}
	key := taskKey(x.store, s.Task.ID)
	c.tk.mu.Lock()
	if c.tk.asks == nil {
		c.tk.asks = map[string]time.Time{}
	}
	c.tk.asks[key] = now
	call := c.tk.call
	c.tk.mu.Unlock()
	if why := c.unreviewable(lead, call); why != "" {
		c.verdict(now, lead, x, s, now, "", 0, "", why, 0) // no call to wait for: settled at once
		return true
	}
	in := c.reviewInput(now, x, s, earlier)
	c.Lead.logf("loop: background task %s (%s): asking its reviewer, on %v", s.Task.ID, x.owner, kinds)
	c.tk.wg.Add(1)
	go func() {
		defer c.tk.wg.Done()
		verdict, again, reason, why, took := "", time.Duration(0), "", "", time.Duration(0)
		defer func() {
			if r := recover(); r != nil {
				c.Lead.logf("loop: background task %s: the review panicked: %v", s.Task.ID, r)
				verdict, why = "", reviewPanic
			}
			c.verdict(lead.Now(), lead, x, s, now, verdict, again, reason, why, took)
		}()
		verdict, again, reason, why, took = c.consultReviewer(lead, call, in)
	}()
	return true
}

// consultReviewer makes the review call about in, through the lead's mod (call: a test's stand-in), within
// the reviewer's wait: the verdict, for a KEEP when to look again, and the reason; or why there is none.
func (c *Coordinator) consultReviewer(lead *state.Store, call func(context.Context, *state.Store, Completion) (Answer, error),
	in decide.TaskReviewInput) (verdict string, again time.Duration, reason, why string, took time.Duration) {
	if why := c.unreviewable(lead, call); why != "" {
		return "", 0, "", why, 0
	}
	if call == nil {
		call = Complete
	}
	wait := c.reviewWait()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	began := time.Now()
	ans, err := call(ctx, lead, Completion{Model: c.reviewModel(), System: decide.TaskReviewSystem, Prompt: decide.TaskReviewPrompt(in),
		MaxTokens: decide.TaskReviewMaxTokens, Timeout: wait - wait/10})
	if took = ans.Took; took == 0 {
		took = time.Since(began)
	}
	if err != nil {
		why = "failed"
		var u *Unanswered
		if errors.As(err, &u) {
			why = u.Why
		}
		return "", 0, "", why, took
	}
	verdict, again, reason = decide.TaskVerdictOf(ans.Text)
	return verdict, again, reason, "", took
}

// unreviewable says why no review call can be made ("" when one can): task_review off, or no mod in the lead's
// claude to make it (Complete's no-mod and serial). call is a test's stand-in for the mod and the model.
func (c *Coordinator) unreviewable(lead *state.Store, call func(context.Context, *state.Store, Completion) (Answer, error)) string {
	switch {
	case !c.Reviewer.On:
		return reviewOff
	case call != nil:
		return ""
	}
	if u := modUp(lead); u != nil {
		return u.Why
	}
	return ""
}

// reviewInput is what the reviewer reads of suspect s of loop x, at now: the task, whose it is, the phase it
// started in with where that stands, its output's end, its CPU, what it waits on, the signals, and the
// verdict before (earlier: "" for none).
func (c *Coordinator) reviewInput(now time.Time, x taskLoop, s state.Suspect, earlier string) decide.TaskReviewInput {
	in := decide.TaskReviewInput{ID: s.Task.ID, Type: s.Kind, Command: s.Command(), Description: s.Description(), Declared: s.Declared,
		Owner: x.owner, Age: s.Age, Phase: s.Phase, Stands: s.Stands, Generation: s.Generation, Output: x.st.Run.TaskOutput(s.Task.ID),
		CPU: s.Stat.CPU, Span: s.Stat.Span, WaitsOn: s.WaitsOn, Signals: s.Signals, Now: now}
	if x.retired && s.Stands != state.PhaseGone {
		in.Stands = "taken from its phase agent, which baton let go of"
	}
	if pl, err := x.store.LoadPlan(); err == nil {
		in.Plan = pl.Title
	}
	if earlier != "" {
		in.Earlier = []string{earlier}
	}
	if in.Output != "" {
		if fi, err := os.Stat(in.Output); err == nil {
			in.Read, in.Size, in.Changed = true, fi.Size(), fi.ModTime()
			in.Tail = tailLines(in.Output, fi.Size(), decide.TaskTailLines)
		}
	}
	return in
}

// tailLines is the last n lines of the file at path (of size bytes), each clipped, read from its last 16 KiB.
func tailLines(path string, size int64, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const most = 16 << 10
	if size > most {
		f.Seek(size-most, io.SeekStart)
	}
	b, _ := io.ReadAll(io.LimitReader(f, most))
	lines := strings.Split(string(bytes.TrimRight(b, "\n")), "\n")
	if size > most && len(lines) > 1 {
		lines = lines[1:] // the first is cut
	}
	lines = lines[max(0, len(lines)-n):]
	for i, l := range lines {
		lines[i] = clipLine(l)
	}
	return strings.Join(lines, "\n")
}

// clipLine is one line of a task's output, at most 300 characters.
func clipLine(l string) string {
	if r := []rune(l); len(r) > 300 {
		return string(r[:299]) + "…"
	}
	return l
}

// verdict records what came of the review of suspect s of loop x asked at asked (verdict "" with why: none),
// logs task_reviewed, and acts on it: STOP sends the mod's stop command, and UNSURE or none asks the owner
// to look and tells the human, each once a task. An answer for a review no longer in flight changes nothing.
func (c *Coordinator) verdict(now time.Time, lead *state.Store, x taskLoop, s state.Suspect, asked time.Time, verdict string, again time.Duration,
	reason, why string, took time.Duration) {
	c.tk.mu.Lock()
	if k := taskKey(x.store, s.Task.ID); c.tk.asks[k].Equal(asked) {
		delete(c.tk.asks, k)
	}
	c.tk.mu.Unlock()
	if verdict == "" {
		verdict = state.ReviewNone
	}
	model := c.reviewModel()
	applied, ask, tell := false, false, false
	x.store.Update(func(st *state.State) error {
		if applied = st.Run.Reviewed(s.Task.ID, asked, verdict, reason, model, now.Add(again), now); !applied {
			return nil
		}
		rv := st.Run.Reviews[s.Task.ID]
		switch verdict {
		case state.ReviewStop: // what names the task once its start record is gone
			rv.Stop.Description, rv.Stop.Phase, rv.Stop.Shell, rv.Stop.Output = s.Description(), s.Phase, clipLog(s.Command()), st.Run.TaskOutput(s.Task.ID)
			if s.Start != nil {
				rv.Stop.Started = s.Start.At
			} else if s.Age > 0 {
				rv.Stop.Started = now.Add(-s.Age)
			}
			return nil
		case state.ReviewKeep:
			return nil
		}
		ask, tell = !rv.Asked, !rv.Told
		rv.Asked, rv.Told = true, true
		return nil
	})
	fields := map[string]any{"task": s.Task.ID, "phase": s.Phase, "signals": s.Whys(), "verdict": verdict, "model": model, "ms": took.Milliseconds(),
		"owner": x.owner}
	if reason != "" {
		fields["reason"] = clipLog(reason)
	}
	if why != "" {
		fields["why"] = why
	}
	if verdict == state.ReviewKeep {
		fields["again_minutes"] = int(again.Minutes())
	}
	if !applied {
		fields["stale"] = true
	}
	x.store.Event("task_reviewed", fields)
	if !applied {
		return
	}
	c.Lead.logf("loop: background task %s (%s): %s %s", s.Task.ID, x.owner, verdict, why)
	ts := suspectOf(s, x.st.Run)
	switch verdict {
	case state.ReviewStop:
		c.sendStop(lead, x, s.Task.ID)
	case state.ReviewKeep:
	default:
		if ask {
			c.leaveLookIn(lead, lookLoop{store: x.tell}, state.LookIn{Kind: state.LookTaskSuspect, At: now,
				Text: decide.TaskSuspectLookIn(ts, model, reason, why), Say: lookSaid(x, decide.TaskSuspectSaid(ts))}, map[string]any{"task": s.Task.ID, "phase": s.Phase})
		}
		if tell {
			x.store.Event("background_suspect", map[string]any{"task": s.Task.ID, "phase": s.Phase, "command": clipLog(s.Command()), "signals": s.Whys(),
				"verdict": verdict, "why": why, "reason": clipLog(reason), "owner": x.owner})
			c.announce(lead, decide.TaskSuspectSay(ts, model, reason, why, x.owner))
		}
	}
}

// sendStop has baton's mod stop task id (bridge.Stop: TaskStop with its id), and records the command on its
// stop, for tasks.go's stops to see through. One that cannot be sent is left with no command: its process
// group is ended at the next look.
func (c *Coordinator) sendStop(lead *state.Store, x taskLoop, id string) {
	cmd, err := bridge.Open(lead.Dir, lead.Now, func(kind string, fields map[string]any) { lead.Event(kind, fields) }).
		Issue(bridge.Command{Kind: bridge.Stop, Instance: lead.Instance, Agent: id, Task: true})
	if err != nil {
		c.Lead.logf("loop: background task %s: asking the mod to stop it: %v", id, err)
		return
	}
	x.store.Update(func(st *state.State) error {
		if rv := st.Run.Reviews[id]; rv != nil && rv.Stop != nil && rv.Stop.Command == 0 {
			rv.Stop.Command = cmd.ID
		}
		return nil
	})
	c.Lead.logf("loop: background task %s: asked the mod to stop it (command %d)", id, cmd.ID)
}
