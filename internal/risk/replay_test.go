//go:build replay

package risk

// The replay scores the phases of real runs' records as the ledger would have, to calibrate its points and
// lines (DefaultWeights, config.DefaultRiskReview and DefaultRiskFull):
//
//	BATON_REPLAY_RUNS=<run folder>[:<run folder>...] go test -tags replay -run TestReplay -v ./internal/risk
//
// A run folder is a run's .baton/runs/<id>, or an archived plan of one (gen-<n>). Records are read, never
// written, and git only read. It logs aggregate numbers only, and no file's name: never commit a run's data
// or its output. Without BATON_REPLAY_RUNS it is skipped.
//
// What a phase "later needed" is read from the full check after it: the files the QA phase's own commits
// changed (its fixes, Markdown aside), against the files each phase scored since the last full check
// changed. The aim is lines the troubled stretches cross, early, and quiet ones do not.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

func TestReplay(t *testing.T) {
	runs := os.Getenv("BATON_REPLAY_RUNS")
	if runs == "" {
		t.Skip("set BATON_REPLAY_RUNS to the run folders to replay")
	}
	w := DefaultWeights
	w.DeviationUnknown = 0 // older records have no deviation: unknown for every phase, it tells nothing apart
	var all []scored
	var windows []window
	for _, dir := range filepath.SplitList(runs) {
		r, err := loadReplay(dir)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(dir), err)
		}
		s, ws := r.replay(t, w)
		all = append(all, s...)
		windows = append(windows, ws...)
	}
	report(t, all, windows)
}

// replayRun is a run's records, as the replay reads them.
type replayRun struct {
	name   string
	st     state.State
	pl     plan.Plan
	doc    []byte
	root   string
	subs   map[string]*state.State
	events []event
	now    time.Time
}

type event struct {
	kind string
	at   time.Time
	f    map[string]any
}

func (e event) s(k string) string { v, _ := e.f[k].(string); return v }

func loadReplay(dir string) (*replayRun, error) {
	r := &replayRun{name: filepath.Base(dir), subs: map[string]*state.State{}, now: time.Now()}
	if err := readJSON(filepath.Join(dir, "state.json"), &r.st); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, "plan.json"), &r.pl); err != nil {
		return nil, err
	}
	// The project's checkout is the folder above .baton.
	parts := strings.Split(filepath.ToSlash(dir), "/")
	i := slices.Index(parts, ".baton")
	if i < 1 {
		return nil, fmt.Errorf("not under a .baton folder")
	}
	r.root = filepath.FromSlash(strings.Join(parts[:i], "/"))
	// The plan's text: its file while its sections still match, else the replans kept with the run.
	for _, cand := range r.docs(dir) {
		if doc, err := os.ReadFile(cand); err == nil && sectionsMatch(r.pl, doc) {
			r.doc = doc
			break
		}
	}
	runDir := dir
	if strings.HasPrefix(filepath.Base(dir), "gen-") {
		runDir = filepath.Dir(dir) // an archived plan: its sub-runs are gone, its events are the run's
	} else {
		subs, _ := os.ReadDir(filepath.Join(dir, "workers"))
		for _, e := range subs {
			var sub state.State
			if readJSON(filepath.Join(dir, "workers", e.Name(), "state.json"), &sub) == nil {
				r.subs[e.Name()] = &sub
			}
		}
	}
	evs, err := readEvents(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	// The plan's own events: from its attaching (the last before its first phase started) to its end.
	first, last := time.Time{}, time.Time{}
	for _, ps := range r.st.Phases {
		if !ps.StartedAt.IsZero() && (first.IsZero() || ps.StartedAt.Before(first)) {
			first = ps.StartedAt
		}
		if ps.DoneAt.After(last) {
			last = ps.DoneAt
		}
	}
	from := time.Time{}
	for _, e := range evs {
		if e.kind == "attached" && e.at.Before(first) {
			from = e.at
		}
	}
	if last.IsZero() || len(r.st.Phases) > 0 && r.st.Mode != state.ModeComplete {
		last = r.now
	} else {
		last = last.Add(time.Hour)
		r.now = last
	}
	for _, e := range evs {
		if !e.at.Before(from) && !e.at.After(last) {
			r.events = append(r.events, e)
		}
	}
	return r, nil
}

func (r *replayRun) docs(dir string) []string {
	out := []string{r.pl.File}
	if r.pl.Origin != "" {
		out = append(out, r.pl.Origin)
	}
	replans, _ := filepath.Glob(filepath.Join(dir, "replan", "*.md"))
	sort.Slice(replans, func(i, j int) bool { return replans[i] > replans[j] })
	return append(out, replans...)
}

func sectionsMatch(p plan.Plan, doc []byte) bool {
	for i := range p.Phases {
		if _, err := p.Section(doc, i); err != nil {
			return false
		}
	}
	return len(p.Phases) > 0
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func readEvents(path string) ([]event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		e := event{f: m}
		e.kind, _ = m["kind"].(string)
		ts, _ := m["ts"].(string)
		e.at, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, e)
	}
	return out, sc.Err()
}

// scored is one phase as the replay scored it.
type scored struct {
	run, phase string
	at         time.Time
	points     int
	factors    []state.RiskFactor
	files      []string
	fixed      bool // a file of it was changed by the next full check's fixes
	qa         bool
}

// window is the phases scored between two full checks, and the check that ended it.
type window struct {
	run, qa         string
	phases          []scored // in the order they were done
	start, end      time.Time
	hours           float64 // how long the QA phase took (so far, for one under way)
	reds            int     // its red jobs
	fixFiles        int
	underWay, clean bool
}

func (r *replayRun) replay(t *testing.T, w Weights) ([]scored, []window) {
	ctx := context.Background()
	perPhase := func(kind string, test func(event) bool) map[string]int {
		n := map[string]int{}
		for _, e := range r.events {
			if e.kind == kind && (test == nil || test(e)) {
				n[e.s("phase")]++
			}
		}
		return n
	}
	conflicts := perPhase("land_returned", func(e event) bool { _, ok := e.f["paths"]; return ok })
	despite := perPhase("note", func(e event) bool { return e.s("despite_red") != "" })
	decisions := perPhase("note", nil)
	for p, n := range perPhase("proposed", nil) {
		decisions[p] += n
	}
	jobs := perPhase("job_queued", nil)
	redJobs := perPhase("job_finished", func(e event) bool { return e.s("status") != "passed" && e.s("status") != "" })

	var out []scored
	for _, ph := range r.pl.Phases {
		ps := r.st.Phases[ph.ID]
		if ps == nil || ps.Status != state.PhaseDone {
			continue
		}
		rec := Record{Phase: ph.ID, Plan: r.pl, Doc: r.doc, Lead: r.st, Root: r.root}
		if sub := r.subs[ph.ID]; sub != nil && ps.Worker != "" {
			rec.Sub = sub
		}
		f := Gather(ctx, rec)
		f.ChecksKnown = false
		f.DespiteRed = f.DespiteRed || despite[ph.ID] > 0
		f.Decisions = max(f.Decisions, decisions[ph.ID])
		f.Conflicts = max(f.Conflicts, conflicts[ph.ID])
		f.Jobs = max(f.Jobs, jobs[ph.ID])
		at := ps.DoneAt
		if l := landing(r.st, ph.ID); l != nil && !l.At.IsZero() {
			at = l.At
		}
		factors := Score(f, w)
		s := scored{run: r.name, phase: ph.ID, at: at, points: Points(factors), factors: factors, qa: ph.QA() && !f.DespiteRed}
		for _, fs := range f.Files {
			s.files = append(s.files, fs.Path)
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })

	// The full checks, in order: each QA phase (done, or under way), and each --qa gate that passed.
	type check struct {
		qa         string
		start, end time.Time
		underWay   bool
	}
	var checks []check
	for _, ph := range r.pl.Phases {
		ps := r.st.Phases[ph.ID]
		if !ph.QA() || ps == nil || ps.StartedAt.IsZero() {
			continue
		}
		c := check{qa: ph.ID, start: ps.StartedAt, end: ps.DoneAt}
		if ps.Status != state.PhaseDone {
			c.end, c.underWay = r.now, true
		}
		checks = append(checks, c)
	}
	early := map[string]time.Time{} // a --qa gate's job → when it was queued
	for _, e := range r.events {
		if e.kind == "qa_early" {
			early[e.s("job")] = e.at
		}
	}
	for _, e := range r.events {
		if q, ok := early[e.s("job")]; ok && e.kind == "job_finished" && e.s("status") == "passed" {
			checks = append(checks, check{qa: "gate " + e.s("job"), start: q, end: e.at})
		}
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].start.Before(checks[j].start) })

	// A QA phase checks what it comes after (rule 7), less what an earlier one checked; a --qa gate what was
	// done before it was queued, since the last check.
	var windows []window
	prev := time.Time{}
	taken := map[string]bool{}
	var under func(id string, seen map[string]bool)
	under = func(id string, seen map[string]bool) {
		for _, d := range r.pl.Deps(id) {
			if !seen[d] {
				seen[d] = true
				under(d, seen)
			}
		}
	}
	for _, c := range checks {
		win := window{run: r.name, qa: c.qa, start: prev, end: c.start, hours: c.end.Sub(c.start).Hours(), underWay: c.underWay, reds: redJobs[c.qa]}
		deps := map[string]bool{}
		gate := strings.HasPrefix(c.qa, "gate ")
		if !gate {
			under(c.qa, deps)
		}
		for _, s := range out {
			if s.qa || taken[s.phase] {
				continue
			}
			if gate && s.at.After(prev) && !s.at.After(c.start) || !gate && deps[s.phase] {
				win.phases = append(win.phases, s)
				taken[s.phase] = true
			}
		}
		// The QA phase's own commits are its fixes.
		if ps := r.st.Phases[c.qa]; ps != nil && ps.StartHead != "" {
			fix := Gather(ctx, Record{Phase: c.qa, Plan: r.pl, Lead: withDone(r.st, c.qa, c.end), Root: r.root})
			var files []string
			for _, fs := range fix.Files {
				if !strings.HasSuffix(fs.Path, ".md") {
					files = append(files, fs.Path)
				}
			}
			win.fixFiles = len(files)
			for i := range win.phases {
				win.phases[i].fixed = slices.ContainsFunc(win.phases[i].files, func(p string) bool { return slices.Contains(files, p) })
			}
			for i := range out {
				for _, p := range win.phases {
					if out[i].phase == p.phase && out[i].run == p.run {
						out[i].fixed = p.fixed
					}
				}
			}
		}
		win.clean = !slices.ContainsFunc(win.phases, func(p scored) bool { return p.fixed })
		windows = append(windows, win)
		if !c.underWay {
			prev = c.end
			if strings.HasPrefix(c.qa, "gate ") {
				prev = c.start // a gate checks the work done before it was queued
			}
		}
	}
	return out, windows
}

// withDone is st with phase done at at, so a QA phase under way reads its commits so far.
func withDone(st state.State, phase string, at time.Time) state.State {
	ps := *st.Phases[phase]
	ps.DoneAt = at
	phases := map[string]*state.PhaseState{}
	for k, v := range st.Phases {
		phases[k] = v
	}
	phases[phase] = &ps
	st.Phases = phases
	return st
}

func report(t *testing.T, all []scored, windows []window) {
	var pts, fixed, quiet []int
	fired := map[string]int{}
	firedFixed, pointsFixed, pointsQuiet := map[string]int{}, map[string]int{}, map[string]int{}
	for _, s := range all {
		if s.qa {
			continue
		}
		pts = append(pts, s.points)
		if s.fixed {
			fixed = append(fixed, s.points)
		} else {
			quiet = append(quiet, s.points)
		}
		for _, f := range s.factors {
			fired[f.Name]++
			if s.fixed {
				firedFixed[f.Name]++
				pointsFixed[f.Name] += f.Points
			} else {
				pointsQuiet[f.Name] += f.Points
			}
		}
	}
	defer func() {
		for n := range fired {
			t.Logf("  factor %-14s: fixed phases %d of %d, %.1f points each on average; the rest %d of %d, %.1f", n,
				firedFixed[n], len(fixed), float64(pointsFixed[n])/float64(max(len(fixed), 1)),
				fired[n]-firedFixed[n], len(quiet), float64(pointsQuiet[n])/float64(max(len(quiet), 1)))
		}
	}()
	t.Logf("phases scored: %d; points: %s", len(pts), dist(pts))
	t.Logf("  whose files the next full check's fixes changed: %d, points %s", len(fixed), dist(fixed))
	t.Logf("  the rest: %d, points %s", len(quiet), dist(quiet))
	var names []string
	for n := range fired {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Logf("  factor %-14s fired for %d phases", n, fired[n])
	}
	t.Logf("full checks: %d", len(windows))
	for _, w := range windows {
		total := 0
		for _, p := range w.phases {
			total += p.points
		}
		nfix := 0
		for _, p := range w.phases {
			if p.fixed {
				nfix++
			}
		}
		t.Logf("  %s %-8s after %2d phases: %3d points; check %.1fh, %d red jobs, %d files fixed, %d phases' files among them; under way %v",
			w.run[:min(len(w.run), 8)], w.qa, len(w.phases), total, w.hours, w.reds, w.fixFiles, nfix, w.underWay)
	}
	for _, line := range []int{16, 24, 32, 40, 48, 52, 56, 60, 64, 72, 80, 90, 96, 110} {
		crossed, cleanCrossed, at := 0, 0, []string{}
		for _, w := range windows {
			total := 0
			for i, p := range w.phases {
				total += p.points
				if total >= line {
					crossed++
					if w.clean {
						cleanCrossed++
					}
					at = append(at, fmt.Sprintf("%d/%d", i+1, len(w.phases)))
					break
				}
			}
		}
		t.Logf("  a line at %2d points: crossed in %d of %d stretches (%d of them clean), after phase %s", line, crossed, len(windows), cleanCrossed, strings.Join(at, ", "))
	}
	t.Logf("defaults: review %d, full %d", config.DefaultRiskReview, config.DefaultRiskFull)
}

func dist(n []int) string {
	if len(n) == 0 {
		return "none"
	}
	s := slices.Clone(n)
	slices.Sort(s)
	sum := 0
	for _, x := range s {
		sum += x
	}
	return fmt.Sprintf("mean %.1f, median %d, p75 %d, max %d", float64(sum)/float64(len(s)), s[len(s)/2], s[len(s)*3/4], s[len(s)-1])
}
