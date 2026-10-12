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
			line := fmt.Sprintf("  %-6s %s · branch %s", w.Phase, workerState(w, now), w.Branch)
			if w.AgentID != "" {
				line += " · agent " + w.AgentID
			}
			lines = append(lines, line, "         worktree: "+w.Worktree)
			if w.Status == state.WorkerSuspended {
				lines = append(lines, "         "+w.Why+"; the next baton session of this run starts its agent again")
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
	fmt.Fprintln(io.Out, "workers (each an agent in a worktree of its own; the agent panel below the prompt shows them):")
	for _, l := range lines {
		fmt.Fprintln(io.Out, l)
	}
}

// printMachine says, for a run with workers on, how many may run at once, and how the machine reads now,
// which holds new ones back while it has no room for another session; then what holds back a phase that
// waits for a worker, if anything does. A worker's own run has none of its own.
func printMachine(io IO, s *state.Store, st state.State) {
	n, auto, err := config.Load(batonRoot(io), io.Env).WorkerCount()
	if s.Parent != "" || err != nil || n == 0 && !auto {
		return
	}
	m := io.reading()
	line := fmt.Sprintf("workers: up to %d", n)
	if auto {
		size := fmt.Sprintf("%d CPUs, %d GiB", m.CPUs, m.MemGiB())
		if m.MemTotal == 0 {
			size = fmt.Sprintf("%d CPUs, memory unknown", m.CPUs)
		}
		if n = config.AutoWorkers(m); n == 0 {
			line = fmt.Sprintf("workers: auto, none on this machine (%s)", size)
		} else {
			line = fmt.Sprintf("workers: auto, up to %d on this machine (%s)", n, size)
		}
	}
	if m.Known {
		line += fmt.Sprintf(" · load %.1f", m.Load1)
		if m.Pressure != 0 {
			line += fmt.Sprintf(" · memory pressure %s (%d%% free)", m.Pressure, m.MemAvailablePct)
		} else {
			line += fmt.Sprintf(" · %d%% memory free", m.MemAvailablePct)
		}
	}
	fmt.Fprintln(io.Out, line)
	if h := st.Hold; h != nil {
		fmt.Fprintf(io.Out, "  held: %s waits for a worker since %s: %s\n", h.Phase, h.Since.Local().Format("15:04"), h.Why)
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
	if n, _, err := cfg.WorkerCap(io.reading); err != nil || n == 0 || !u.Resets.IsZero() && !now.Before(u.Resets) {
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
