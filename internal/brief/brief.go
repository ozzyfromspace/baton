// Package brief writes what the model reads right after a compaction, or as a conversation starts
// without its phase's context: where the plan stands, the phase it is on (quoted from the plan document),
// the standing rules, the notes earlier phases left, and the decisions the run made without the human. The text is assembled by code from files, never summarized
// by a model, so it is the same every time.
package brief

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Kind of compaction the brief follows.
const (
	Boundary   = "boundary"   // a phase finished; the next one starts now
	Switch     = "switch"     // the phase in hand was set aside while its job runs; another starts now
	Resume     = "resume"     // a phase that was set aside unfinished is taken up again
	Checkpoint = "checkpoint" // the model asked to compact mid-phase
	Auto       = "auto"       // Claude Code compacted on its own mid-phase
	Worker     = "worker"     // a worker's session starts on its phase (no compaction: it starts afresh)
	Fresh      = "fresh"      // a conversation started mid-phase without the phase's context (no compaction)
)

// Limits keep the brief a brief: sections are quoted in full only up to these sizes.
const (
	maxSection = 24000
	maxRules   = 8000
	maxNotes   = 8000
)

// Input is everything the brief is built from.
type Input struct {
	Kind    string
	Plan    plan.Plan
	Doc     []byte // the plan document's current text
	State   state.State
	Handoff string // .baton/handoff.md
	Words   decide.Words
	// Result is the result of the job a resumed phase was set aside for (decide.JobResult), "" for none.
	Result string
	// A worker's brief (Words.Worker) names its worktree (Tree) and branch, and quotes only the notes of
	// the phases its phase builds on (Needs, from the run's plan).
	Tree, Branch string
	Needs        []string
	// Again: the worker's brief is for an agent taking up its phase where an agent before it left it;
	// Checkpointed: the agent before it checkpointed, and it goes on from its notes.
	Again, Checkpointed bool
	// Decisions is the run's decisions, its workers' too (Store.Decisions); nil: the state's own.
	Decisions []state.Decision
	// Facts is the run's environment facts (Store.Facts: a worker's sub-run keeps none of its own); nil:
	// the state's own.
	Facts []state.Fact
}

// Write returns the brief.
func Write(in Input) string {
	var b strings.Builder
	pl, st := in.Plan, in.State
	cur := pl.Index(st.Current)
	title := st.Current
	if cur >= 0 {
		title = st.Current + " — " + pl.Phases[cur].Title
	}

	running := fmt.Sprintf("You are running the plan %q (%s).", pl.Title, pl.File)
	if in.Words.Worker {
		running = decide.WorkerRole(pl.Title, pl.File, st.Current, in.Tree)
	}
	switch {
	case in.Kind == Worker && in.Again && in.Checkpointed:
		fmt.Fprintf(&b, "[baton] %s %s\n\n", decide.WorkerIntro(pl.Title, pl.File, st.Current, in.Tree, in.Branch), decide.WorkerCheckpointed(st.Current))
	case in.Kind == Worker && in.Again:
		fmt.Fprintf(&b, "[baton] %s %s\n\n", decide.WorkerIntro(pl.Title, pl.File, st.Current, in.Tree, in.Branch), decide.WorkerTakesUp(st.Current))
	case in.Kind == Worker:
		fmt.Fprintf(&b, "[baton] %s\n\n", decide.WorkerIntro(pl.Title, pl.File, st.Current, in.Tree, in.Branch))
	case in.Kind == Boundary && first(pl, st):
		fmt.Fprintf(&b, "[baton] The plan %q (%s) was attached, and the conversation before it compacted. You are running it now, phase by phase.\n\n", pl.Title, pl.File)
	case in.Kind == Boundary:
		fmt.Fprintf(&b, "[baton] The context was compacted at a phase boundary. %s\n\n", running)
	case in.Kind == Switch:
		from := "the phase in hand waits on its job, so baton set it aside"
		if sw := st.Run.Compaction.Switch; sw != nil {
			from = fmt.Sprintf("%s waits on its job %s, so baton set %s aside", sw.From, sw.Job, sw.From)
		}
		fmt.Fprintf(&b, "[baton] The context was compacted to switch phases: %s and takes it up again once the job ends. %s\n\n", from, running)
	case in.Kind == Resume:
		fmt.Fprintf(&b, "[baton] The context was compacted, and you are taking up a phase that was set aside unfinished. %s\n\n", running)
	case in.Kind == Checkpoint:
		fmt.Fprintf(&b, "[baton] The context was compacted at your checkpoint, mid-phase. %s\n\n", running)
	case in.Kind == Fresh:
		fmt.Fprintf(&b, "[baton] %s\n\n", decide.FreshStart(running))
	default:
		fmt.Fprintf(&b, "[baton] Claude Code compacted the context automatically, mid-phase. %s\n\n", running)
	}

	b.WriteString("Progress:\n")
	var working []string
	for _, ph := range pl.Phases {
		mark, extra := "·", ""
		if ps := st.Phases[ph.ID]; ps != nil {
			switch ps.Status {
			case state.PhaseDone:
				mark = "✓"
				// Its notes were left while its gates ran, and may well say so: they are out of date.
				var passed []string
				for _, j := range st.Standing(ph.ID) {
					if j.Status == state.JobPassed {
						passed = append(passed, j.ID)
					}
				}
				switch len(passed) {
				case 0:
				case 1:
					extra = fmt.Sprintf(" (verified: job %s passed)", passed[0])
				default:
					extra = fmt.Sprintf(" (verified: jobs %s passed)", strings.Join(passed, " and "))
				}
			case state.PhaseVerifying:
				mark, extra = "⧗", " (verifying)"
			case state.PhaseParked:
				mark, extra = "⏸", " (parked)"
			case state.PhaseWorking:
				mark, extra = "⚙", " (a worker's)"
				working = append(working, ph.ID+" — "+ph.Title)
			}
		}
		if ph.ID == st.Current {
			mark, extra = "▶", ""
		}
		fmt.Fprintf(&b, "  %s %s — %s%s\n", mark, ph.ID, ph.Title, extra)
	}
	b.WriteString("\n")
	if len(working) > 0 {
		b.WriteString(decide.WorkingPhases(working) + "\n\n")
	}

	var park *state.Park
	if ps := st.Phases[st.Current]; ps != nil {
		park = ps.Park
	}
	switch {
	case in.Kind == Worker && in.Again && in.Result != "" && park != nil && decide.ReopenedNow(title, park.Reason) != "":
		// Its verification reopened the phase after the agent before it ended: the result comes below.
		b.WriteString(decide.ReopenedNow(title, park.Reason) + "\n\n")
	case in.Kind == Worker && in.Again && in.Checkpointed:
		b.WriteString(decide.CheckpointNow(title) + "\n\n")
	case in.Kind == Worker && in.Again:
		b.WriteString(in.Words.FreshNow(title) + "\n\n")
	case in.Kind == Boundary || in.Kind == Switch || in.Kind == Worker:
		fmt.Fprintf(&b, "Now: %s. Begin it now.\n\n", title)
	case in.Kind == Resume && park != nil && park.Reason == state.ParkRed:
		fmt.Fprintf(&b, "Now: resume %s. It was finished, but its verification failed: fix what failed, then finish it again.\n\n", title)
	case in.Kind == Resume && park != nil && park.Reason == state.ParkUnverified:
		fmt.Fprintf(&b, "Now: resume %s. It was finished, but its verification ended without a verdict: check it again (in place if the lane cannot run it), then finish it again.\n\n", title)
	case in.Kind == Resume:
		fmt.Fprintf(&b, "Now: resume %s from where it stood. Other phases may have changed the repository since: check its state before you go on.\n\n", title)
	case in.Kind == Fresh:
		b.WriteString(in.Words.FreshNow(title) + "\n\n")
	default:
		fmt.Fprintf(&b, "Now: continue %s from where you left off (see your notes below and the repository's state).\n\n", title)
	}

	if rules := pl.Rules(in.Doc); rules != "" {
		fmt.Fprintf(&b, "Standing rules (from the plan):\n\n%s\n\n", clip(rules, maxRules))
	}
	if cur >= 0 {
		if sec, err := pl.Section(in.Doc, cur); err == nil {
			fmt.Fprintf(&b, "Phase %s, from the plan:\n\n%s\n\n", st.Current, clip(sec, maxSection))
		} else {
			fmt.Fprintf(&b, "(baton could not quote phase %s from the plan document: %v. Read %s directly.)\n\n", st.Current, err, pl.File)
		}
	}
	if q := qaGuide(pl, st.Current, in.Words); q != "" {
		b.WriteString(q + "\n\n")
	}
	if park != nil && strings.TrimSpace(park.Notes) != "" {
		fmt.Fprintf(&b, "This phase's park notes (where %s stood when it was set aside):\n\n%s\n\n", st.Current, clip(strings.TrimSpace(park.Notes), maxNotes))
	}
	if r := strings.TrimSpace(in.Result); r != "" {
		fmt.Fprintf(&b, "The result of the job %s was set aside for:\n\n%s\n\n", st.Current, r)
	}
	if red := decide.RedPhases(st.Red(), st.Current); red != "" {
		b.WriteString(red + "\n\n")
	}
	facts := in.Facts
	if facts == nil {
		facts = st.Facts
	}
	if text := decide.EnvFacts(facts); text != "" {
		b.WriteString(text + "\n\n")
	}
	switch notes := strings.TrimSpace(in.Handoff); {
	case notes == "":
	case in.Words.Worker:
		if needs := notesOf(notes, in.Needs); needs != "" {
			fmt.Fprintf(&b, "Notes left by the phases %s builds on (most recent last):\n\n%s\n\n", st.Current, tail(needs, maxNotes))
		}
		var started time.Time
		if ps := st.Phases[st.Current]; ps != nil {
			started = ps.StartedAt
		}
		if own := notesSince(notes, st.Current, started); own != "" {
			fmt.Fprintf(&b, "Notes %s left so far (most recent last):\n\n%s\n\n", st.Current, tail(own, maxNotes))
		}
	default:
		fmt.Fprintf(&b, "Notes left by earlier phases (most recent last):\n\n%s\n\n", tail(notes, maxNotes))
	}
	all := in.Decisions
	if all == nil {
		all = st.Decisions
	}
	if ds := decide.WithoutHuman(all); len(ds) > 0 {
		b.WriteString(decide.RecordSection(ds) + "\n")
	}

	if in.Kind == Worker {
		// A worker's session starts with this brief alone: it gets the whole of how to report, as the lead
		// does when its session starts (hooks.Primer).
		b.WriteString(in.Words.Reporting())
	} else {
		b.WriteString(in.Words.Closing(st.Current))
	}
	return b.String()
}

// first reports that the brief starts the plan's first phase: nothing is done yet.
func first(pl plan.Plan, st state.State) bool {
	for _, ph := range pl.Phases {
		if ps := st.Phases[ph.ID]; ps != nil && ps.Status == state.PhaseDone {
			return false
		}
	}
	return len(pl.Phases) > 0 && st.Current == pl.Phases[0].ID
}

// notesOf picks, from the notes phases left (handoff.md: a "## <phase> — <time>" heading for each), the
// latest notes of each phase in ids, in the order they were left.
func notesOf(handoff string, ids []string) string {
	sections := strings.Split("\n"+strings.TrimSpace(handoff), "\n## ")[1:]
	latest := map[string]int{}
	for i, sec := range sections {
		if id, _, _ := strings.Cut(sec, " "); slices.Contains(ids, id) {
			latest[id] = i
		}
	}
	var out []string
	for i, sec := range sections {
		if id, _, _ := strings.Cut(sec, " "); latest[id] == i && slices.Contains(ids, id) {
			out = append(out, "## "+strings.TrimSpace(sec))
		}
	}
	return strings.Join(out, "\n\n")
}

// notesSince picks, from the notes phases left, those phase left since it started (its checkpoints, and
// its done notes if its verification reopened it), in the order it left them: a plan run before may have
// had a phase of the same id, whose notes are older. A heading's time is the minute the notes were left,
// read in since's location (both are the local time where baton runs).
func notesSince(handoff, phase string, since time.Time) string {
	if since.IsZero() {
		return ""
	}
	from := since.Truncate(time.Minute)
	var out []string
	for _, sec := range strings.Split("\n"+strings.TrimSpace(handoff), "\n## ")[1:] {
		head, _, _ := strings.Cut(sec, "\n")
		id, _, _ := strings.Cut(head, " ")
		_, at, ok := strings.Cut(head, " — ")
		if id != phase || !ok {
			continue
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(at), since.Location()); err == nil && !t.Before(from) {
			out = append(out, "## "+strings.TrimSpace(sec))
		}
	}
	return strings.Join(out, "\n\n")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n…(%d more characters — read the plan document for the rest)", len(s)-n)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[len(s)-n:]
	if i := strings.Index(cut, "\n## "); i >= 0 {
		cut = cut[i+1:]
	}
	return "…(earlier notes omitted)\n" + cut
}

// qaGuide says, in short, how phase id checks its work. While the plan aggregates its heavy checks in QA
// phases (w.AggregateQA), a QA phase is told it runs them (decide.QAPhase), and a phase before one that it
// runs scoped checks only (decide.QAFocus). Every phase then gets the gate rules in short
// (decide.GateHabits): one with no QA phase after it, or with the setting off, checks itself as it ends,
// by the same rules.
func qaGuide(pl plan.Plan, id string, w decide.Words) string {
	i := pl.Index(id)
	if i < 0 {
		return ""
	}
	habits := decide.GateHabits(w.Git)
	if !w.AggregateQA {
		return habits
	}
	if pl.Phases[i].QA() {
		since := pl.SinceQA(id)
		names := ""
		switch n := len(since); {
		case n == 1:
			names = since[0]
		case n > 1:
			names = strings.Join(since[:n-1], ", ") + " and " + since[n-1]
		}
		return decide.QAPhase(names) + "\n\n" + habits
	}
	if qa := pl.NextQA(id); qa != nil {
		return decide.QAFocus(qa.ID+" ("+qa.Title+")") + "\n\n" + habits
	}
	return habits
}
