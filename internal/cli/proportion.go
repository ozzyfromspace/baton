package cli

import (
	"bufio"
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Proportion at queue time (v0.8.7): before a gate is queued, baton looks at what it would cost against
// what it could prove, from the run's record of earlier runs of its command and from git, and questions a
// gate out of proportion to the change once (decide.GateQuestioned). Each question is asked once per
// command and commit (Run.GatesQuestioned), so the same call again queues the gate: nothing waits on the
// human, and the model decides. Since v0.8.11 the questions lead with what the gate spends of the human's
// time and what the phase spent so far, and --why answers them, the reason told to the human; a long repeat
// queued again without one is queued too, flagged (why.go).

// gateAsk is what baton asks of a gate as it is queued (questionGate).
type gateAsk struct {
	types, questions, keys []string      // the questions not asked before, of each kind, and their records
	applies                []string      // the kinds that apply, asked before or not: what a --why answers
	job                    string        // the earlier run they draw on, if any
	took                   time.Duration // how long that run took
	repeat                 bool          // the gate repeats a long run of its command (GateRepeat), asked now or before
	cost                   state.Cost    // the phase's gates so far, and its runs of the command
	qaAsked                bool          // the QA question was put to this command on this commit, or is put now

	// whole are a --failed rerun's long red stages that it runs whole (questionWhole), which took took
	// together; job is the gate it reruns.
	whole []state.WholeStage
}

// questionGate is what baton asks of gate command, about to be queued on commit for phase (qa: the --qa
// the model gave), from st, the run's state read before its lock: git and the jobs' logs are read here, so
// it runs outside the lock.
func questionGate(io IO, s *state.Store, st state.State, pl plan.Plan, phase, command, commit, qa string) gateAsk {
	var a gateAsk
	asked := func(kind string) bool { return slices.Contains(st.Run.GatesQuestioned, gateKey(kind, command, commit)) }
	add := func(kind, question string) {
		a.applies = append(a.applies, kind)
		if !asked(kind) {
			a.types, a.questions, a.keys = append(a.types, kind), append(a.questions, question), append(a.keys, gateKey(kind, command, commit))
		}
	}

	a.cost = state.GateCost(st, phase, command, io.Now())
	last, long := commandRuns(st, command)
	if last != nil && ranFor(*last) >= decide.GateLong {
		a.job, a.took, a.repeat = last.ID, ranFor(*last), true
		add(decide.GateRepeat, repeatOf(s, st, phase, *last, command, commit).Question(phase))
	}
	if paths := outsideScripts(command, s, io.Env); len(paths) > 0 {
		add(decide.GateOutside, decide.OutsideScript(paths))
	}
	if long != nil && unstaged(s, *long) {
		a.job = long.ID
		if a.took == 0 {
			a.took = ranFor(*long)
		}
		add(decide.GateUnstaged, decide.Unstaged(*long, ranFor(*long)))
	}
	if next := qaAhead(io, pl, phase); next != "" && qa == "" {
		var took time.Duration
		if long != nil && last != nil && long.ID == last.ID {
			took = ranFor(*long)
		}
		switch suite := lane.IsSuite(command); {
		case long != nil || suite:
			add(decide.GateBeforeQA, decide.LongBeforeQA(phase, next, command, took, suite))
		case len(a.questions) > 0: // asked with the others, rather than after the job is queued
			add(decide.GateQAAsk, decide.QAAskOnce(phase, next, command))
		}
		a.qaAsked = slices.Contains(a.types, decide.GateBeforeQA) || slices.Contains(a.types, decide.GateQAAsk) ||
			asked(decide.GateBeforeQA) || asked(decide.GateQAAsk)
	}
	return a
}

// refuse records the questions as asked and puts them to the model: the gate is not queued this time.
func (a gateAsk) refuse(io IO, s *state.Store, phase, command, commit string) int {
	a.markAsked(s, phase, command, commit)
	return fail(io, "%s", decide.GateQuestioned(command, decide.GateLead(phase, a.took, a.cost), a.questions))
}

// markAsked records the questions as asked (Run.GatesQuestioned) and logs each (gate_questioned).
func (a gateAsk) markAsked(s *state.Store, phase, command, commit string) {
	s.Update(func(st *state.State) error {
		for _, k := range a.keys {
			if !slices.Contains(st.Run.GatesQuestioned, k) {
				st.Run.GatesQuestioned = append(st.Run.GatesQuestioned, k)
			}
		}
		return nil
	})
	for _, kind := range a.types {
		ev := map[string]any{"type": kind, "command": command, "phase": phase, "commit": commit}
		if a.job != "" {
			ev["job"] = a.job
		}
		a.wholeEvent(ev)
		s.Event("gate_questioned", ev)
	}
}

// gateKey is a question's record in Run.GatesQuestioned: its kind, the command and the commit.
func gateKey(kind, command, commit string) string { return kind + "\x00" + command + "\x00" + commit }

// commandRuns finds the earlier gates of command in st: last, the latest with a verdict (passed, failed, out
// of time), and long, the latest that ran for decide.GateLong or more, whatever its end. Either is nil for
// none.
func commandRuns(st state.State, command string) (last, long *state.Job) {
	for i := range st.Jobs {
		j := &st.Jobs[i]
		if j.Kind != state.JobVerify || j.Command != command || j.Ended.IsZero() {
			continue
		}
		if (j.Status == state.JobPassed || j.Red()) && (last == nil || j.Ended.After(last.Ended)) {
			last = j
		}
		if ranFor(*j) >= decide.GateLong && (long == nil || j.Ended.After(long.Ended)) {
			long = j
		}
	}
	return last, long
}

// ranFor is how long a finished job's command ran: from when it got its lane (Job.Ran), or, recorded
// without it, from when its runner started.
func ranFor(j state.Job) time.Duration {
	began := j.Ran
	if began.IsZero() {
		began = j.Started
	}
	if began.IsZero() || j.Ended.IsZero() {
		return 0
	}
	return j.Ended.Sub(began)
}

// repeatOf is what baton knows of a repeat of prev, the latest run of command, on commit: what changed
// since, whether --failed applies, and whether a full run that passed still covers the code.
func repeatOf(s *state.Store, st state.State, phase string, prev state.Job, command, commit string) decide.Repeat {
	r := decide.Repeat{Prev: prev, Took: ranFor(prev)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	since := func(from string) ([]string, bool) {
		if from == commit {
			return []string{}, true
		}
		changes, err := gitx.Changes(ctx, s.Root, from, commit)
		if err != nil {
			return nil, false
		}
		paths := []string{}
		for _, c := range changes {
			paths = append(paths, c.Path)
		}
		return paths, true
	}
	changed, known := since(prev.Commit)
	switch {
	case known && len(changed) == 0:
		r.Same = true
	case known:
		r.Changed = changed[:min(len(changed), decide.GateListed)]
		r.More = len(changed) - len(r.Changed)
	}
	if rr, why := st.RerunOf(phase, s.StagesOf); why == "" && prev.Red() && rr.Of.Command == command {
		r.Failed = true
	}
	r.Failures, r.MoreFailures = failuresOf(s, prev)
	var green *state.Job
	for i := range st.Jobs {
		if j := &st.Jobs[i]; j.Kind == state.JobVerify && j.Command == command && j.Status == state.JobPassed && (green == nil || j.Ended.After(green.Ended)) {
			green = j
		}
	}
	if green != nil {
		paths, ok := changed, known
		if green.Commit != prev.Commit {
			paths, ok = since(green.Commit)
		}
		if ok && !slices.ContainsFunc(paths, func(p string) bool { return !testSide(p) }) {
			g := *green
			r.Green = &g
		}
	}
	return r
}

// testSide reports a path on the tests' side of a change, or a document: a test file, a file under a test
// folder, or Markdown. A change of such paths alone leaves the code a full run checked as it was.
func testSide(p string) bool {
	base := strings.ToLower(path.Base(p))
	switch {
	case strings.HasSuffix(base, ".md"), strings.HasSuffix(base, ".markdown"),
		strings.HasSuffix(base, "_test.go"), strings.HasSuffix(base, "_test.py"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.Contains(base, ".test."), strings.Contains(base, ".spec."):
		return true
	}
	return slices.ContainsFunc(strings.Split(path.Dir(p), "/"), func(dir string) bool {
		return slices.Contains([]string{"test", "tests", "__tests__", "testdata", "spec", "e2e"}, dir)
	})
}

// outsideScripts lists the scripts command runs (lane.Scripts) that lie outside the repository: a path
// that leaves it, or an absolute one in none of the session's worktree and the project's checkout. The
// system's programs and those of a bin folder are not scripts of the gate's own; a path baton cannot
// expand (a variable other than HOME and TMPDIR) is left alone.
func outsideScripts(command string, s *state.Store, env func(string) string) []string {
	var out []string
	for _, p := range lane.Scripts(command) {
		full := p
		for _, v := range []string{"HOME", "TMPDIR"} {
			for _, form := range []string{"$" + v + "/", "${" + v + "}/"} {
				if strings.HasPrefix(full, form) && env(v) != "" {
					full = filepath.Join(env(v), strings.TrimPrefix(full, form))
				}
			}
		}
		if strings.HasPrefix(full, "~/") && env("HOME") != "" {
			full = filepath.Join(env("HOME"), full[2:])
		}
		switch {
		case strings.HasPrefix(full, "$") || strings.HasPrefix(full, "~"):
			continue
		case !filepath.IsAbs(full) && !strings.HasPrefix(filepath.ToSlash(full), "/"): // rooted, with no drive, on Windows
			if !filepath.IsLocal(full) {
				out = append(out, p)
			}
			continue
		}
		full = filepath.Clean(full)
		if systemProgram(full) || within(full, s.Root) || within(full, s.Checkout) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// systemProgram reports a path among the system's programs or in a bin folder.
func systemProgram(p string) bool {
	p = filepath.ToSlash(p)
	for _, dir := range []string{"/usr/", "/bin/", "/sbin/", "/opt/", "/nix/", "/System/", "/Library/"} {
		if strings.HasPrefix(p, dir) {
			return true
		}
	}
	return slices.Contains(strings.Split(filepath.ToSlash(filepath.Dir(p)), "/"), "bin")
}

// within reports whether p is dir or a path in it, as given or with symlinks resolved.
func within(p, dir string) bool {
	if dir == "" {
		return false
	}
	in := func(p, dir string) bool {
		rel, err := filepath.Rel(dir, p)
		return err == nil && filepath.IsLocal(rel)
	}
	if in(p, dir) {
		return true
	}
	rp, rd := p, dir
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		rd = r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		rp = filepath.Join(r, filepath.Base(p))
	}
	return in(rp, rd)
}

// unstaged reports a gate run, j, that was not built from stages: no stage recorded, and no stage marker in
// its log.
func unstaged(s *state.Store, j state.Job) bool {
	if len(s.StagesOf(j)) > 0 {
		return false
	}
	log := j.Log
	if log == "" {
		log = s.JobLog(j.ID)
	}
	f, err := os.Open(log)
	if err != nil {
		return true
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if _, ok := lane.ParseMarker(sc.Text()); ok {
			return false
		}
	}
	return true
}
