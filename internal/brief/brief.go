// Package brief writes what the model reads right after a compaction: where the plan stands, the phase
// it is on (quoted from the plan document), the standing rules, the notes earlier phases left, and the
// decisions the run made without the human. The text is assembled by code from files, never summarized
// by a model, so it is the same every time.
package brief

import (
	"fmt"
	"strings"

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

	switch {
	case in.Kind == Boundary && first(pl, st):
		fmt.Fprintf(&b, "[baton] The plan %q (%s) was attached, and the conversation before it compacted. You are running it now, phase by phase.\n\n", pl.Title, pl.File)
	case in.Kind == Boundary:
		fmt.Fprintf(&b, "[baton] The context was compacted at a phase boundary. You are running the plan %q (%s).\n\n", pl.Title, pl.File)
	case in.Kind == Switch:
		from := "the phase in hand waits on its job, so baton set it aside"
		if sw := st.Run.Compaction.Switch; sw != nil {
			from = fmt.Sprintf("%s waits on its job %s, so baton set %s aside", sw.From, sw.Job, sw.From)
		}
		fmt.Fprintf(&b, "[baton] The context was compacted to switch phases: %s and takes it up again once the job ends. You are running the plan %q (%s).\n\n", from, pl.Title, pl.File)
	case in.Kind == Resume:
		fmt.Fprintf(&b, "[baton] The context was compacted, and you are taking up a phase that was set aside unfinished. You are running the plan %q (%s).\n\n", pl.Title, pl.File)
	case in.Kind == Checkpoint:
		fmt.Fprintf(&b, "[baton] The context was compacted at your checkpoint, mid-phase. You are running the plan %q (%s).\n\n", pl.Title, pl.File)
	default:
		fmt.Fprintf(&b, "[baton] Claude Code compacted the context automatically, mid-phase. You are running the plan %q (%s).\n\n", pl.Title, pl.File)
	}

	b.WriteString("Progress:\n")
	for _, ph := range pl.Phases {
		mark, extra := "·", ""
		if ps := st.Phases[ph.ID]; ps != nil {
			switch ps.Status {
			case state.PhaseDone:
				mark = "✓"
			case state.PhaseVerifying:
				mark, extra = "⧗", " (verifying)"
			case state.PhaseParked:
				mark, extra = "⏸", " (parked)"
			}
		}
		if ph.ID == st.Current {
			mark, extra = "▶", ""
		}
		fmt.Fprintf(&b, "  %s %s — %s%s\n", mark, ph.ID, ph.Title, extra)
	}
	b.WriteString("\n")

	var park *state.Park
	if ps := st.Phases[st.Current]; ps != nil {
		park = ps.Park
	}
	switch {
	case in.Kind == Boundary || in.Kind == Switch:
		fmt.Fprintf(&b, "Now: %s. Begin it now.\n\n", title)
	case in.Kind == Resume && park != nil && park.Reason == state.ParkRed:
		fmt.Fprintf(&b, "Now: resume %s. It was finished, but its verification failed: fix what failed, then finish it again.\n\n", title)
	case in.Kind == Resume && park != nil && park.Reason == state.ParkUnverified:
		fmt.Fprintf(&b, "Now: resume %s. It was finished, but its verification ended without a verdict: check it again (in place if the lane cannot run it), then finish it again.\n\n", title)
	case in.Kind == Resume:
		fmt.Fprintf(&b, "Now: resume %s from where it stood. Other phases may have changed the repository since: check its state before you go on.\n\n", title)
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
	if park != nil && strings.TrimSpace(park.Notes) != "" {
		fmt.Fprintf(&b, "This phase's park notes (where %s stood when it was set aside):\n\n%s\n\n", st.Current, clip(strings.TrimSpace(park.Notes), maxNotes))
	}
	if r := strings.TrimSpace(in.Result); r != "" {
		fmt.Fprintf(&b, "The result of the job %s was set aside for:\n\n%s\n\n", st.Current, r)
	}
	if red := decide.RedPhases(st.Red(), st.Current); red != "" {
		b.WriteString(red + "\n\n")
	}
	if notes := strings.TrimSpace(in.Handoff); notes != "" {
		fmt.Fprintf(&b, "Notes left by earlier phases (most recent last):\n\n%s\n\n", tail(notes, maxNotes))
	}
	if ds := decide.WithoutHuman(st.Decisions); len(ds) > 0 {
		b.WriteString(decide.RecordSection(ds) + "\n")
	}

	b.WriteString(in.Words.Closing(st.Current))
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
