package state

import (
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// A QA phase's gate runs slim (v0.8.15): once its full run found every red, the same command reruns what
// failed until all of it passes (cli/slim.go). One more full run follows only if the fixes could have broken
// what passed. At the phase's done, when the files changed since that full run go beyond Markdown and the
// failing tests' own (NeedsConfirm), baton queues one whole run of the gate's command itself, at HEAD
// (ConfirmAtDone); while the rerun that clears the gate still runs, it is owed, and queued once that passes
// (Settle, confirmDue). It is queued as the merge queue queues a landing's check (Reverify): baton's own
// (Job.Requeue), with no questions, the phase verifying on it, every stage run (Job.QAPhase). If it passes,
// the phase is done; if it fails, it is the phase's red gate, and slim reruns begin again.

// Confirm is the one full run a QA phase owes at its done, after reruns cleared its gate (PhaseState.Confirm).
type Confirm struct {
	// Of is the whole run the reruns go back to: the last run of the gate's command that reran nothing; ""
	// when it is gone from the record.
	Of string `json:"of,omitempty"`
	// Command and Here are the gate's: what the confirmation runs, whole.
	Command string `json:"command"`
	Here    bool   `json:"here,omitempty"`
	// Commit is what it checks: HEAD at the phase's done.
	Commit string `json:"commit"`
	// Files are the files changed since Of's commit beyond Markdown and the failing tests' own, at most
	// MaxChanged, and More how many more; Unknown when they could not be read (or Of is gone), so a
	// confirmation runs all the same.
	Files   []string `json:"files,omitempty"`
	More    int      `json:"more,omitempty"`
	Unknown bool     `json:"unknown,omitempty"`
	// Notes are what the phase resumes from if the confirmation fails.
	Notes string `json:"notes,omitempty"`
	// After is the rerun the phase verified on at its done, still running then: the confirmation is queued
	// once it passes. "" when it was queued at the done.
	After string `json:"after,omitempty"`
	// Job is the confirmation run, once queued.
	Job string `json:"job,omitempty"`
}

// Fields are c's for an event about it (qa_confirm_queued, qa_confirm_owed).
func (c Confirm) Fields() map[string]any {
	f := map[string]any{"of": c.Of, "command": c.Command, "commit": c.Commit, "files": c.Files}
	if c.More > 0 {
		f["more"] = c.More
	}
	if c.Unknown {
		f["unknown"] = true
	}
	if c.After != "" {
		f["after"] = c.After
	}
	if c.Job != "" {
		f["job"] = c.Job
	}
	return f
}

// ConfirmFrom is what a confirmation of phase would go back to: its gate (the verify job its verification
// stands on, as gate has it), when that is a rerun (Job.Reruns) that passed or has not ended, and the whole
// run its reruns go back to, the first with no Reruns. A whole run gone from the record is returned with no
// ID, and with the gate's command when the oldest rerun on record ran it (staged), none when it ran a
// narrowed command of its own (Job.Narrows). ok is false for a gate that is no rerun, is red, or ended with
// no verdict.
func (st State) ConfirmFrom(phase string) (gate, whole Job, ok bool) {
	g := st.gate(phase, false)
	if g == nil || g.Reruns == "" || g.Finished() && g.Status != JobPassed {
		return Job{}, Job{}, false
	}
	at := *g
	for range st.Jobs {
		x := st.Job(at.Reruns)
		if x == nil {
			w := Job{Phase: phase, Kind: JobVerify}
			if len(at.Narrows) == 0 {
				w.Command, w.Here = at.Command, at.Here
			}
			return *g, w, true
		}
		if at = *x; at.Reruns == "" {
			return *g, at, true
		}
	}
	return Job{}, Job{}, false // reruns that go round: not one baton queues
}

// NeedsConfirm lists the files of changed (paths from the repository's root, as git gives them) that a QA
// phase's fixes since its full run touched beyond what cannot have broken what passed there: Markdown files,
// and the failing tests' own files (failed): a pytest, vitest, jest or Playwright test's file, by its id
// (lane.FailedTest.File), and a `_test.go` file in a go package that failed. An id's path may be from the
// runner's own root, so a path that ends with it is its file. None means no confirmation is needed.
func NeedsConfirm(changed []string, failed []lane.FailedTest) []string {
	var files, pkgs []string
	for _, f := range failed {
		switch {
		case f.Runner == lane.RunGo && f.Whole:
			pkgs = append(pkgs, f.ID)
		case f.Runner == lane.RunGo:
			if f.Scope != "" {
				pkgs = append(pkgs, f.Scope)
			}
		default:
			if file := f.File(); file != "" {
				files = append(files, file)
			}
		}
	}
	var out []string
	for _, p := range changed {
		switch {
		case markdown(p):
		case slices.ContainsFunc(files, func(f string) bool { return samePath(p, f) }):
		case strings.HasSuffix(p, "_test.go") && slices.ContainsFunc(pkgs, func(pkg string) bool { return inPackage(p, pkg) }):
		default:
			out = append(out, p)
		}
	}
	return out
}

// markdown reports a Markdown file, by its name.
func markdown(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// samePath reports whether p, a path from the repository's root, is the file a test id names (id): the same
// path, or one of them ends with the other, as the runner's root may be another than the repository's.
func samePath(p, id string) bool {
	id = strings.TrimPrefix(strings.ReplaceAll(id, `\`, "/"), "./")
	return id != "" && (p == id || strings.HasSuffix(p, "/"+id) || strings.HasSuffix(id, "/"+p))
}

// inPackage reports whether file, a path from the repository's root, is in go package pkg (an import path):
// its directory is pkg, or the end of it. A file at the root is in no package this can tell.
func inPackage(file, pkg string) bool {
	dir := path.Dir(file)
	pkg = strings.TrimPrefix(pkg, "./")
	return dir != "." && (pkg == dir || strings.HasSuffix(pkg, "/"+dir))
}

// ConfirmAtDone records c as the confirmation phase owes at its done, under the run's lock, when gate, the
// rerun it was worked out for, is still the phase's gate: queued now when gate passed (it returns the job), or
// owed while gate runs (Confirm.After), to be queued once it passes. It reports false, recording nothing, when
// the phase's gate is another now, or ended with no pass.
func ConfirmAtDone(st *State, phase string, c Confirm, gate string, now time.Time) (Job, bool) {
	g, ps := st.gate(phase, false), st.Phases[phase]
	switch {
	case g == nil || g.ID != gate || ps == nil:
		return Job{}, false
	case g.Status == JobPassed:
		c.After = ""
		q := queueConfirm(st, phase, &c, now)
		ps.Confirm = &c
		return q, true
	case !g.Finished():
		c.After = gate
		ps.Confirm = &c
		return Job{}, true
	}
	return Job{}, false
}

// queueConfirm queues confirmation c of phase, recording it as c's (Confirm.Job).
func queueConfirm(st *State, phase string, c *Confirm, now time.Time) Job {
	q, _, _ := QueueJob(st, Job{Phase: phase, Kind: JobVerify, Command: c.Command, Commit: c.Commit, Here: c.Here, Notes: c.Notes,
		Requeue: true, QAPhase: true}, now)
	c.Job = q.ID
	return q
}

// confirmDue queues the confirmation phase owes once the rerun it waits for passed, as Settle finds the jobs
// phase verified on (jobs) all passed: the phase verifies on it now, and their results need no telling. It
// reports false when the phase owes none for them.
func confirmDue(st *State, phase string, jobs []Job, now time.Time) (Job, bool) {
	ps := st.Phases[phase]
	if ps == nil || ps.Confirm == nil || ps.Confirm.Job != "" || ps.Confirm.After == "" ||
		!slices.ContainsFunc(jobs, func(j Job) bool { return j.ID == ps.Confirm.After }) {
		return Job{}, false
	}
	for _, x := range jobs {
		if j := st.Job(x.ID); j != nil && j.Delivered.IsZero() {
			j.Delivered = now
		}
	}
	q := queueConfirm(st, phase, ps.Confirm, now)
	ps.On = []string{q.ID}
	return q, true
}
