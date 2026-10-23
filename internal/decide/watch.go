package decide

import (
	"fmt"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// What `baton watch` shows besides the board: a live view of each run a baton host drives in the project,
// for a second terminal. It reads the run's records and changes nothing.

// WatchHead is the view's first line: "◆ baton watch · 14:02:11 · 2 live runs".
func WatchHead(at time.Time, runs int) string {
	head := BandMark + " watch · " + at.Local().Format("15:04:05")
	if runs > 1 {
		head += fmt.Sprintf(" · %d live runs", runs)
	}
	return head
}

// WatchNoRun is the view of a project where no baton host drives a run.
const WatchNoRun = "No baton host drives a run in this project right now. This view shows each run one drives, as it runs."

// WatchUnread is the line under a run whose record could not be read this time (it was being written), so
// the view shows how it stood at, the last reading it has.
func WatchUnread(at time.Time) string {
	return "(its record could not be read just now: this is how it stood at " + at.Local().Format("15:04:05") + ")"
}

// WatchUnreadable is the line of a run whose record could not be read, with no earlier reading to show.
func WatchUnreadable(run string, err error) string {
	return fmt.Sprintf("run %s: its record could not be read just now (%v); the view tries again", short8(run), err)
}

// The view's section heads.
const (
	WatchJobs     = "jobs:"
	WatchLandings = "landing (first finished, first landed):"
	WatchEvents   = "lately:"
)

// WatchedJob is a job queued or running, as the view shows it.
type WatchedJob struct {
	Job state.Job
	// Earlier is how long earlier runs of the same command took, newest first.
	Earlier []time.Duration
	// Stages are the job's stages that have ended (lane.Stage), and Stage the one running now, from its
	// log's markers.
	Stages []lane.Stage
	Stage  string
	// Still is how long its log has not grown (0: not known, or not running).
	Still time.Duration
	// Failure is the first failure its log shows (state.Failing.Line), in stage FailStage if known.
	Failure   string
	FailStage string
}

// Head is the job's first line: its id, its phase and what it runs, on which commit.
//
//	P2.j1  P2  `go test ./internal/lane` on 72b7740
func (w WatchedJob) Head() string {
	j := w.Job
	what := "`" + j.Command + "`"
	if j.Commit != "" {
		what += " on " + short7(j.Commit)
	}
	if j.Here {
		what += " in the working tree"
	}
	return j.ID + "  " + j.Phase + "  " + what
}

// watchStill is how long a running job's log must stand still before the view says so.
const watchStill = 30 * time.Second

// Stand is how the job stands, and how its time compares with earlier runs of its command.
//
//	running 12m in lane 1 · earlier runs took 8m–10m · got its lane after 2m · log still 4m
func (w WatchedJob) Stand(now time.Time) string {
	j := w.Job
	if j.Status == state.JobQueued {
		return "queued " + since(j.Queued, now)
	}
	if !j.LaneWait.IsZero() {
		return "waiting " + since(j.LaneWait, now) + " for a lane"
	}
	began := w.Began()
	parts := []string{words("running", since(began, now))}
	if j.Lane > 0 {
		parts[0] += fmt.Sprintf(" in lane %d", j.Lane)
	}
	if e := earlier(w.Earlier, now.Sub(began)); e != "" && !began.IsZero() {
		parts = append(parts, e)
	}
	if !j.Ran.IsZero() && !j.Started.IsZero() && j.Ran.Sub(j.Started) >= watchStill {
		parts = append(parts, "got its lane after "+Span(j.Ran.Sub(j.Started)))
	}
	if w.Still >= watchStill {
		parts = append(parts, "log still "+Span(w.Still))
	}
	return strings.Join(parts, " · ")
}

// Began is when the job's command began: when it got its lane (state.Job.Ran), or else when its runner
// started.
func (w WatchedJob) Began() time.Time {
	if !w.Job.Ran.IsZero() {
		return w.Job.Ran
	}
	return w.Job.Started
}

// earlier compares a run of took so far with earlier runs of the same command (newest first).
func earlier(runs []time.Duration, took time.Duration) string {
	if len(runs) == 0 {
		return ""
	}
	lo, hi := runs[0], runs[0]
	for _, d := range runs {
		lo, hi = min(lo, d), max(hi, d)
	}
	switch {
	case took > hi && len(runs) == 1:
		return "past the " + Span(hi) + " its earlier run took"
	case took > hi:
		return "past the " + Span(hi) + " its longest earlier run took"
	case len(runs) == 1:
		return "its earlier run took " + Span(hi)
	case Span(lo) == Span(hi):
		return fmt.Sprintf("its %d earlier runs took %s", len(runs), Span(hi))
	}
	return "earlier runs took " + Span(lo) + "–" + Span(hi)
}

// StageLine is the job's stages so far, the one running last: "stages: lint ✓ 12s · unit ✗ 2m14s · e2e
// running". "" for a job that names no stages.
func (w WatchedJob) StageLine() string {
	var parts []string
	for _, s := range w.Stages {
		switch s.Status {
		case lane.StagePassed:
			parts = append(parts, s.Name+" ✓ "+Stopwatch(s.Took()))
		case lane.StageFailed:
			part := s.Name + " ✗ " + Stopwatch(s.Took())
			if n := len(s.Failures); n > 0 {
				part += fmt.Sprintf(" (%d failed)", n)
			}
			parts = append(parts, part)
		case lane.StageCarried:
			parts = append(parts, s.Name+" passed earlier")
		default:
			parts = append(parts, s.Name+" "+s.Status)
		}
	}
	if w.Stage != "" {
		parts = append(parts, w.Stage+" running")
	}
	if len(parts) == 0 {
		return ""
	}
	return "stages: " + strings.Join(parts, " · ")
}

// FailLine is the first failure the job's log shows, "" with none.
func (w WatchedJob) FailLine() string {
	switch {
	case w.Failure == "":
		return ""
	case w.FailStage != "":
		return "first failure, in " + w.FailStage + ": " + w.Failure
	}
	return "first failure: " + w.Failure
}

// WatchFailMarker is how the view gives a fail marker of the gate protocol (lane.MarkFail) as a failure.
func WatchFailMarker(id string) string { return id + " failed" }

// WatchLanding is phase agent w's work in the merge queue, n-th in it, and what holds it, if anything:
// here is the gate running in the run's working tree, which holds every landing there (state.HereGate).
// "1. P7 finished 14:02 · waits to land: the lead's changes to the same files (since 14:03, 4m)".
func WatchLanding(n int, w state.Worker, here *state.Job, now time.Time) string {
	m := w.Merge
	line := fmt.Sprintf("%d. %s finished %s", n, w.Phase, m.Queued.Local().Format("15:04"))
	switch {
	case m.Reverify != "":
		return line + " · verifying again with the run's branch merged in (job " + m.Reverify + ")"
	case m.Deferred != "":
		return line + " · waits to land: " + m.Deferred + " (since " + m.DeferredAt.Local().Format("15:04") + ", " + since(m.DeferredAt, now) + ")"
	case here != nil:
		return line + " · waits for gate " + here.ID + ", which runs in the working tree"
	case m.Merged != "":
		return line + " · merged with " + short7(m.Onto) + ", landing"
	}
	return line + " · landing"
}

// WatchEvent is an event of a run's events.jsonl in words, or "" for one the view leaves out: the many that
// only say how baton goes about its work (locks, the mod's commands, turns, hooks).
func WatchEvent(kind string, f map[string]any) string {
	ph, job := field(f, "phase"), field(f, "job")
	if ph == "" {
		ph = field(f, "worker")
	}
	who := ph
	if who == "" {
		who = "the lead"
	}
	switch kind {
	case "phase_started":
		return ph + " started"
	case "phase_resumed":
		return ph + " taken up again"
	case "phase_verifying":
		return ph + " done, verifying (job " + job + ")"
	case "phase_done":
		return ph + " done"
	case "phase_parked":
		return words(ph, "set aside", ParkedWhy(field(f, "reason")))
	case "job_queued":
		return job + " queued for " + ph + ": `" + field(f, "command") + "`"
	case "job_started":
		return job + " started (" + ph + ")"
	case "lane_acquired":
		if w := seconds(f, "waited_seconds"); w >= time.Minute {
			return fmt.Sprintf("%s got lane %s after %s waiting", job, field(f, "lane"), Span(w))
		}
		return ""
	case "job_finished":
		return job + " " + finished(f)
	case "job_failing":
		if s := field(f, "stage"); s != "" {
			return job + " failing in " + s + ": " + field(f, "line")
		}
		return job + " failing: " + field(f, "line")
	case "stage_finished":
		return job + " stage " + field(f, "stage") + " " + field(f, "status") + " in " + Stopwatch(seconds(f, "seconds"))
	case "worker_started":
		return "a phase agent started on " + ph
	case "worker_ended":
		return words(ph+"'s phase agent ended", dash(field(f, "why")))
	case "worker_deferred":
		return words("no phase agent on "+ph+" yet", dash(field(f, "why")))
	case "land_queued":
		return ph + " queued to land"
	case "reverify_queued":
		return ph + " verifying again before it lands (job " + job + ")"
	case "qa_confirm_queued":
		return ph + " verifying with one full run (job " + job + ")"
	case "reverify_skipped":
		return ph + " lands without verifying again: " + field(f, "qa") + " checks it"
	case "merged_in":
		return "the run's branch merged into " + ph + "'s"
	case "landed":
		return ph + " landed"
	case "compact_started":
		return "the lead's context compacting"
	case "compact_finished":
		return "the lead's context compacted"
	case "note":
		return who + " decided: " + field(f, "what")
	case "proposed":
		return who + " proposes: " + field(f, "action")
	case "blocked":
		return who + " blocked: " + field(f, "reason")
	case "run_stalled":
		if d, err := time.ParseDuration(field(f, "quiet")); err == nil {
			return "nothing moved for " + Span(d)
		}
		return "nothing moved for a while"
	case "plan_replanned":
		return "the plan was replanned"
	case "plan_complete":
		return "the plan is complete"
	case "stopped_phase_taken":
		return "the lead takes " + ph + " up, its agent stopped"
	}
	return ""
}

// finished is how a job_finished event says the job ended, after how long its command ran (since v0.8.7;
// before, with its wait for a lane): "passed in 9m", "failed (exit 1) after 12m".
func finished(f map[string]any) string {
	took := Span(seconds(f, "seconds"))
	if _, ok := f["ran_seconds"]; ok {
		took = Span(seconds(f, "ran_seconds"))
	}
	switch st := field(f, "status"); st {
	case state.JobPassed:
		return "passed in " + took
	case state.JobFailed:
		return fmt.Sprintf("failed (exit %s) after %s", field(f, "exit"), took)
	case state.JobTimeout:
		return "ran out of time after " + took
	default:
		return words("ended", st, dash(field(f, "reason")))
	}
}

// field is an event's field as text, "" when it has none.
func field(f map[string]any, key string) string {
	switch v := f[key].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	default:
		return fmt.Sprint(v)
	}
}

// seconds is an event's field of seconds, as a duration.
func seconds(f map[string]any, key string) time.Duration {
	v, _ := f[key].(float64)
	return time.Duration(v * float64(time.Second))
}

// dash is why, after a dash, or "".
func dash(why string) string {
	if why == "" {
		return ""
	}
	return "— " + why
}

// short8 is the start of a run's id, enough to tell it apart.
func short8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
