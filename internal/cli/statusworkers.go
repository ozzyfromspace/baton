package cli

import (
	"fmt"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What baton status says about the run's workers: how each stands, its worktree and branch, what of it
// waits on the human, the merge queue its finished work joins, and the account's usage, which holds them.

// workerState is how worker w stands, in a few words: "starting", "working 14m", "finished, landing",
// "finished, waits to land", or "suspended".
func workerState(w state.Worker, now time.Time) string {
	switch w.Status {
	case state.WorkerWorking:
		return "working " + decide.Span(max(now.Sub(w.Assigned), 0))
	case state.WorkerLanding:
		if w.Merge != nil && w.Merge.Deferred != "" {
			return "finished, waits to land"
		}
		return "finished, landing"
	}
	return w.Status
}

// printWorkers lists the run's workers: each one the run counts on, where it works and how it stands,
// and each that ended keeping commits on its branch that never landed.
func printWorkers(io IO, s *state.Store, st state.State) {
	now := io.Now()
	var lines []string
	for i, w := range st.Workers {
		switch {
		case w.Live():
			line := fmt.Sprintf("  %-6s %s · branch %s · session %s", w.Phase, workerState(w, now), w.Branch, short(w.Session))
			if w.Resumes > 0 {
				line += fmt.Sprintf(" · resumed %d %s", w.Resumes, plural(w.Resumes, "time", "times"))
			}
			lines = append(lines, line, "         worktree: "+w.Worktree)
			switch {
			case w.Needs != "":
				lines = append(lines, fmt.Sprintf("         needs you: %s — answer it in its session: baton view %s", w.Needs, w.Phase))
			case w.Status == state.WorkerSuspended:
				lines = append(lines, "         "+w.Why+"; the next baton session of this run resumes it")
			}
		case w.Status == state.WorkerFailed && w.Unlanded > 0 && !slices.ContainsFunc(st.Workers[i+1:], func(o state.Worker) bool { return o.Phase == w.Phase }) &&
			gitx.RevParse(s.Checkout, "refs/heads/"+w.Branch) != "":
			lines = append(lines, fmt.Sprintf("  %-6s ended: %s · its branch %s keeps %d %s that never landed", w.Phase, w.Why, w.Branch,
				w.Unlanded, plural(w.Unlanded, "commit", "commits")))
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(io.Out, "workers (each in a worktree of its own; baton view <phase>, in another terminal, shows one):")
	for _, l := range lines {
		fmt.Fprintln(io.Out, l)
	}
}

// printMergeQueue lists the workers' finished work waiting to land on the run's branch, first finished
// first, and what each waits on.
func printMergeQueue(io IO, st state.State) {
	var q []state.Worker
	for _, w := range st.Workers {
		if w.Status == state.WorkerLanding && w.Merge != nil {
			q = append(q, w)
		}
	}
	if len(q) == 0 {
		return
	}
	slices.SortStableFunc(q, func(a, b state.Worker) int { return a.Merge.Queued.Compare(b.Merge.Queued) })
	fmt.Fprintf(io.Out, "merge queue (first finished, first landed on %s):\n", st.RunBranch)
	for i, w := range q {
		m := w.Merge
		line := fmt.Sprintf("  %d. %-6s finished %s", i+1, w.Phase, m.Queued.Local().Format("15:04"))
		switch {
		case m.Reverify != "":
			line += " · verifying again on its rebased branch (job " + m.Reverify + ")"
		case m.Deferred != "":
			line += " · waits to land: " + m.Deferred + " (since " + m.DeferredAt.Local().Format("15:04") + ")"
		case m.Rebased != "":
			line += " · rebased onto " + shortCommit(m.Onto)
		}
		fmt.Fprintln(io.Out, line)
	}
}

// printUsage shows the account's five-hour usage, as the run's sessions last read it, where it bears on
// the run: a usage limit it waits on, and, with workers, the reading that new workers start below.
func printUsage(io IO, st state.State) {
	u := st.Usage
	if u == nil {
		return
	}
	now := io.Now()
	if now.Before(u.Limited) {
		who := "the lead session"
		if u.By != "lead" && u.By != "" {
			who = "the worker on " + u.By
		}
		fmt.Fprintf(io.Out, "usage limit: %s hit it at %s → no worker starts, and baton types nothing into the run's sessions, until %s\n",
			who, u.Hit.Local().Format("15:04"), u.Limited.Local().Format("15:04"))
	}
	cfg := config.Load(batonRoot(io), io.Env)
	if n, err := cfg.WorkerCount(); err != nil || n == 0 || !u.Resets.IsZero() && !now.Before(u.Resets) {
		return
	}
	line := fmt.Sprintf("usage: %.0f%% of the five-hour allowance", u.UsedPct)
	if !u.Resets.IsZero() {
		line += ", which resets at " + u.Resets.Local().Format("15:04")
	}
	line += " (read " + u.At.Local().Format("15:04") + ")"
	if most, _ := cfg.WorkerUsage(); most > 0 {
		line += fmt.Sprintf(" · new workers start below %.0f%%", most)
	}
	fmt.Fprintln(io.Out, line)
}
