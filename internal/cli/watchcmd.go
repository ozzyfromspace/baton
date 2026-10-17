package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/ozzyfromspace/baton/internal/board"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// baton watch is a live view of the project's runs for a second terminal: each run a baton host drives,
// its phases as the board shows them, its jobs running (their stages, their time against earlier runs of
// the same command, how long their log has stood still, their first failure, their wait for a lane), the
// work waiting to land and what holds it, and its latest events in words. It reads the runs' records and
// logs, takes no lock and changes nothing. It redraws every watchEvery until interrupted; --once, or output
// that is not a terminal, prints it once, so a model's shell never waits on it.

func init() {
	register("watch", "a live view of this project's runs for a second terminal: phases, jobs as they run, landings, the latest events (--once prints it once)", cmdWatch)
}

const (
	watchEvery    = 2 * time.Second
	watchMost     = 8         // events a run shows at most
	watchTail     = 128 << 10 // how much of the end of an events.jsonl the view reads
	watchHistory  = 5         // earlier runs of a command the view compares a job with
	watchFailClip = 300       // bytes of a failure line the view keeps
)

func cmdWatch(args []string, io IO) int {
	p, err := parseArgs(args, nil, []string{"once"})
	if err != nil {
		return fail(io, "%v", err)
	}
	if len(p.pos) > 0 {
		return fail(io, "usage: baton watch [--once]")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(io, "%v", err)
	}
	// Not state.OpenProject, which makes the directory and may move an old run under the project's lock.
	dir := state.Locate(cwd, io.Env)
	w := newWatcher(&state.Project{Dir: dir, Root: filepath.Dir(dir), Now: io.Now}, func() string { return serialByConfig(io) })
	f, tty := io.Out.(*os.File)
	tty = tty && term.IsTerminal(int(f.Fd()))
	if p.bools["once"] || !tty {
		for _, l := range w.frame().lines(termWidth(io), 0) {
			fmt.Fprintln(io.Out, l)
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprint(io.Out, "\x1b[H\x1b[2J")
	for {
		width, height, err := term.GetSize(int(f.Fd()))
		if err != nil {
			width, height = 0, 0
		}
		var b strings.Builder
		b.WriteString("\x1b[H")
		for _, l := range w.frame().lines(width, height) {
			b.WriteString(l + "\x1b[K\n")
		}
		b.WriteString("\x1b[J")
		fmt.Fprint(io.Out, b.String())
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(watchEvery):
		}
	}
}

// A watcher reads the project's runs for each frame of the view. It keeps the last reading of each record,
// so a record caught while it is written shows as it last stood, and where it got to in each job's log, so
// each frame reads only what the log gained.
type watcher struct {
	p      *state.Project
	serial func() string // why phases go one at a time by baton's config (serialByConfig)

	states map[string]stateRead // the last good reading of each run's and sub-run's state, by ref
	plans  map[string]plan.Plan // the last good reading of each run's plan, by ref
	logs   map[string]*logScan  // by path
	events map[string]eventTail // by path
}

type stateRead struct {
	st state.State
	at time.Time
}

func newWatcher(p *state.Project, serial func() string) *watcher {
	return &watcher{p: p, serial: serial, states: map[string]stateRead{}, plans: map[string]plan.Plan{},
		logs: map[string]*logScan{}, events: map[string]eventTail{}}
}

// A watchView is one frame of the view.
type watchView struct {
	At         time.Time
	Runs       []watchRun
	Unreadable []string // lines of the runs whose record could not be read, with no earlier reading
}

// A watchRun is one live run in a frame.
type watchRun struct {
	Board    board.Board
	Unread   time.Time // when the reading shown was taken, when the record could not be read this time
	Jobs     []decide.WatchedJob
	Landings []state.Worker // in the merge queue, first finished first
	Here     *state.Job     // the gate running in the run's working tree, which holds its landings
	Events   []watchEvent   // oldest first
}

type watchEvent struct {
	At   time.Time
	Text string
}

// frame reads every run a host drives now.
func (w *watcher) frame() watchView {
	now := w.p.Now()
	v := watchView{At: now}
	seen := map[string]bool{}
	for _, r := range w.p.Runs() {
		s, err := w.p.ExistingRun(r.ID, "")
		if err != nil {
			continue
		}
		st, unread, err := w.read(s)
		if err != nil {
			if now.Sub(r.Updated) < state.OwnerTTL {
				v.Unreadable = append(v.Unreadable, decide.WatchUnreadable(r.ID, err))
			}
			continue
		}
		if !st.Owner.Live(now) {
			continue
		}
		v.Runs = append(v.Runs, w.run(s, st, unread, now, seen))
	}
	for path := range w.logs {
		if !seen[path] {
			delete(w.logs, path)
		}
	}
	return v
}

// read reads run s's state, or when it cannot be read (a write caught half way, say), the last reading of
// it, and when that was taken.
func (w *watcher) read(s *state.Store) (state.State, time.Time, error) {
	st, err := s.Peek()
	if err == nil {
		w.states[s.Ref] = stateRead{st: st, at: w.p.Now()}
		return st, time.Time{}, nil
	}
	if last, ok := w.states[s.Ref]; ok {
		return last.st, last.at, nil
	}
	return state.State{}, time.Time{}, err
}

// run is live run s, standing st, as the view shows it; seen gathers the logs it read.
func (w *watcher) run(s *state.Store, st state.State, unread, now time.Time, seen map[string]bool) watchRun {
	pl, err := s.LoadPlan()
	switch last, ok := w.plans[s.Ref]; {
	case err == nil:
		w.plans[s.Ref] = pl
	case ok && !errors.Is(err, fs.ErrNotExist):
		pl, err = last, nil
	}
	stores := []*state.Store{s}
	states := []state.State{st}
	var subs []state.RunInfo
	for _, info := range w.p.Workers(s.Ref) {
		sub, serr := w.p.ExistingRun(info.ID, "")
		if serr != nil {
			continue
		}
		sst, _, serr := w.read(sub)
		if serr != nil {
			continue
		}
		stores, states = append(stores, sub), append(states, sst)
		subs = append(subs, state.RunInfo{ID: info.ID, State: sst})
	}
	in := board.Input{State: st, Plan: pl, HavePlan: err == nil, Subs: subs, Now: now}
	if in.Serial = st.SerialWhy(); in.Serial == "" && in.HavePlan {
		in.Serial = w.serial()
	}
	r := watchRun{Board: unaddressed(board.Build(in)), Unread: unread, Here: st.HereGate()}
	for i, rs := range states {
		for _, j := range rs.Jobs {
			if !j.Finished() {
				r.Jobs = append(r.Jobs, w.job(stores[i], j, states, now, seen))
			}
		}
	}
	slices.SortStableFunc(r.Jobs, func(a, b decide.WatchedJob) int { return a.Job.Queued.Compare(b.Job.Queued) })
	for _, wk := range st.Workers {
		if wk.Status == state.WorkerLanding && wk.Merge != nil {
			r.Landings = append(r.Landings, wk)
		}
	}
	slices.SortStableFunc(r.Landings, func(a, b state.Worker) int { return a.Merge.Queued.Compare(b.Merge.Queued) })
	for _, rs := range stores {
		r.Events = append(r.Events, w.tail(filepath.Join(rs.Dir, "events.jsonl"))...)
	}
	slices.SortStableFunc(r.Events, func(a, b watchEvent) int { return a.At.Compare(b.At) })
	if len(r.Events) > watchMost {
		r.Events = r.Events[len(r.Events)-watchMost:]
	}
	return r
}

// unaddressed is board b without the handles by which the human writes to a phase agent at the lead's
// prompt (decide.Handle, decide.BoardWrite), which the board shows for an agent at work: the view is read
// in another terminal, where nothing is written to an agent.
func unaddressed(b board.Board) board.Board {
	steps := make([]board.Step, len(b.Steps))
	for i, s := range b.Steps {
		s.Phases = slices.Clone(s.Phases)
		for k := range s.Phases {
			s.Phases[k].Worker = ""
		}
		steps[i] = s
	}
	b.Steps = steps
	return b
}

// job is unfinished job j of run s as the view shows it: its time against the earlier runs of its command
// in the run's records (states), and, while it runs, what its log and stages file say.
func (w *watcher) job(s *state.Store, j state.Job, states []state.State, now time.Time, seen map[string]bool) decide.WatchedJob {
	wj := decide.WatchedJob{Job: j, Earlier: history(j, states)}
	if j.Status != state.JobRunning {
		return wj
	}
	if wj.Stages = j.Stages; len(wj.Stages) == 0 {
		wj.Stages, _ = lane.ReadStages(s.JobStages(j.ID))
	}
	path := j.Log
	if path == "" {
		path = s.JobLog(j.ID)
	}
	seen[path] = true
	scan := w.logs[path]
	if scan == nil {
		scan = &logScan{}
		w.logs[path] = scan
	}
	if scan.read(path) {
		wj.Stage, wj.Still = scan.stage, max(now.Sub(scan.mod), 0)
	}
	if f := j.Failing; f != nil {
		wj.Failure, wj.FailStage = f.Line, f.Stage
	} else if scan.failure != "" {
		wj.Failure, wj.FailStage = scan.failure, scan.failStage
	}
	return wj
}

// history is how long the earlier runs of j's command took that ended with a verdict, in the run and its
// sub-runs (states), newest first, at most watchHistory of them.
func history(j state.Job, states []state.State) []time.Duration {
	var runs []state.Job
	for _, st := range states {
		for _, e := range st.Jobs {
			if e.ID != j.ID && e.Command == j.Command && (e.Status == state.JobPassed || e.Status == state.JobFailed) {
				runs = append(runs, e)
			}
		}
	}
	slices.SortFunc(runs, func(a, b state.Job) int { return b.Ended.Compare(a.Ended) })
	var out []time.Duration
	for _, e := range runs {
		began := (decide.WatchedJob{Job: e}).Began()
		if began.IsZero() || !e.Ended.After(began) {
			continue
		}
		if out = append(out, e.Ended.Sub(began)); len(out) == watchHistory {
			break
		}
	}
	return out
}

// A logScan is how far the view has read a job's log, and what it found there: the stage started and not
// ended yet, and the first failure.
type logScan struct {
	off       int64 // the end of the last whole line read
	mod       time.Time
	stage     string
	failure   string
	failStage string
}

// read reads what the log at path gained since the last read, reporting whether it could read it. A log
// shorter than what was read (cut down, or made anew) is read again from its start; a line not ended yet
// is left for the next read.
func (l *logScan) read(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	if fi.Size() < l.off {
		*l = logScan{}
	}
	l.mod = fi.ModTime()
	if _, err := f.Seek(l.off, 0); err != nil {
		return false
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		b, err := r.ReadSlice('\n')
		n := int64(len(b))
		for errors.Is(err, bufio.ErrBufferFull) { // a line too long to be a marker: passed over whole
			b, err = r.ReadSlice('\n')
			n, b = n+int64(len(b)), nil
		}
		if err != nil {
			return true
		}
		l.off += n
		if b != nil {
			l.line(string(b))
		}
	}
}

// line takes in one line of the log.
func (l *logScan) line(text string) {
	text = strings.TrimRight(text, "\r\n")
	if m, ok := lane.ParseMarker(text); ok {
		switch {
		case m.Kind == lane.MarkStart:
			l.stage = m.Stage
		case m.Kind == lane.MarkEnd && m.Stage == l.stage:
			l.stage = ""
		case m.Kind == lane.MarkFail && l.failure == "":
			l.failure, l.failStage = decide.WatchFailMarker(m.ID), l.stage
		}
		return
	}
	if l.failure == "" && lane.Failure(text) {
		l.failure, l.failStage = clipLine(text, watchFailClip), l.stage
	}
}

// An eventTail is the events the view read from the end of an events.jsonl, and the file as it was then.
type eventTail struct {
	size   int64
	mod    time.Time
	events []watchEvent
}

// tail is the latest events in the events.jsonl at path that the view puts in words (decide.WatchEvent),
// oldest first, read again only when the file changed.
func (w *watcher) tail(path string) []watchEvent {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if t, ok := w.events[path]; ok && t.size == fi.Size() && t.mod.Equal(fi.ModTime()) {
		return t.events
	}
	t := eventTail{size: fi.Size(), mod: fi.ModTime()}
	defer func() { w.events[path] = t }()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	from := max(t.size-watchTail, 0)
	buf := make([]byte, t.size-from)
	n, _ := f.ReadAt(buf, from)
	lines := bytes.Split(buf[:n], []byte("\n"))
	if from > 0 && len(lines) > 0 {
		lines = lines[1:] // cut by where the read began
	}
	for i := len(lines) - 1; i >= 0 && len(t.events) < watchMost; i-- {
		var rec map[string]any
		if json.Unmarshal(lines[i], &rec) != nil { // a line still being written, or not an event
			continue
		}
		kind, _ := rec["kind"].(string)
		text := decide.WatchEvent(kind, rec)
		ts, _ := rec["ts"].(string)
		at, err := time.Parse(time.RFC3339Nano, ts)
		if text == "" || err != nil {
			continue
		}
		t.events = append(t.events, watchEvent{At: at, Text: text})
	}
	slices.Reverse(t.events)
	return t.events
}

// lines is the frame, each line at most width cells wide (0: no limit) and, with height, as many lines as
// fit a terminal height lines high, leaving out the oldest events first.
func (v watchView) lines(width, height int) []string {
	head := []string{decide.WatchHead(v.At, len(v.Runs))}
	if len(v.Runs) == 0 && len(v.Unreadable) == 0 {
		head = append(head, decide.WatchNoRun)
	}
	head = append(head, v.Unreadable...)
	runs := make([][]string, len(v.Runs))
	fixed := len(head)
	for i, r := range v.Runs {
		runs[i] = r.lines(v.At, width)
		fixed += 1 + len(runs[i]) // a blank line before each run
	}
	// The room left goes to the runs' events a line at a time, each run in turn, the newest first.
	shown := make([]int, len(v.Runs))
	limited, room := height > 0, height-1-fixed
	for more := true; more; {
		more = false
		for i, r := range v.Runs {
			cost := 1
			if shown[i] == 0 {
				cost = 2 // its section's head too
			}
			if shown[i] < len(r.Events) && (!limited || room >= cost) {
				shown[i]++
				room -= cost
				more = true
			}
		}
	}
	out := head
	for i, r := range v.Runs {
		out = append(out, "")
		out = append(out, runs[i]...)
		if n := shown[i]; n > 0 {
			out = append(out, decide.WatchEvents)
			for _, e := range r.Events[len(r.Events)-n:] {
				out = append(out, "  "+e.At.Local().Format("15:04:05")+"  "+e.Text)
			}
		}
	}
	if height > 0 && len(out) > height-1 {
		out = out[:max(height-1, 1)]
	}
	for i, l := range out {
		out[i] = fit(l, width)
	}
	return out
}

// lines is the run in the frame, but for its events: its board (fitted to width), its jobs and its
// landings.
func (r watchRun) lines(now time.Time, width int) []string {
	out := r.Board.Lines(width)
	if !r.Unread.IsZero() {
		out = append(out, decide.WatchUnread(r.Unread))
	}
	if len(r.Jobs) > 0 {
		out = append(out, decide.WatchJobs)
		for _, j := range r.Jobs {
			out = append(out, "  "+j.Head())
			for _, l := range []string{j.Stand(now), j.StageLine(), j.FailLine()} {
				if l != "" {
					out = append(out, indent(l, "      ", width)...)
				}
			}
		}
	}
	if len(r.Landings) > 0 {
		out = append(out, decide.WatchLandings)
		for i, wk := range r.Landings {
			out = append(out, indent(decide.WatchLanding(i+1, wk, r.Here, now), "  ", width)...)
		}
	}
	return out
}

// indent is s broken between words into lines at most width cells wide (0: no limit), each starting with
// pad, and the lines after the first with two spaces more. A word too wide for a line is left to fit.
func indent(s, pad string, width int) []string {
	line, more := pad, pad+"  "
	if width <= 0 || utf8.RuneCountInString(pad+s) <= width {
		return []string{pad + s}
	}
	var out []string
	for _, w := range strings.Fields(s) {
		switch n := utf8.RuneCountInString(line); {
		case line == pad || line == more:
			line += w
		case n+1+utf8.RuneCountInString(w) <= width:
			line += " " + w
		default:
			out, line = append(out, line), more+w
		}
	}
	return append(out, line)
}

// fit cuts s to width cells (0: no limit), taking each character for one, as the board does, and marks the
// cut with "…".
func fit(s string, width int) string {
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return s
	}
	r := []rune(s)
	return string(r[:max(width-1, 0)]) + "…"
}
