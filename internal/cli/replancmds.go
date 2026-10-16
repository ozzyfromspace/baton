package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/replan"
	"github.com/ozzyfromspace/baton/internal/state"
)

// The human's /baton replan has the lead's model restructure the phases not started (package replan):
// `baton replan` opens it and prints the brief the model replans from, and `baton replanned` hands the
// model's draft back, for baton to check and follow, or closes the replan with the plan unchanged.

func init() {
	register("replan", "replan the phases not started, so more can run at once; prints the brief to follow (the lead session's): replan [what to consider]", leadOnly("replan", cmdReplan))
	register("replanned", "finish the open replan: baton checks the draft and follows it: replanned --notes \"<what changed and why>\" | --none \"<why nothing should change>\"", leadOnly("replanned", cmdReplanned))
}

func cmdReplan(args []string, io IO) (code int) {
	defer failedMidway(io, "replan", &code)
	p, err := parseArgs(args, nil, nil)
	if err != nil {
		return fail(io, "usage: baton replan [\"<what to consider>\"]%s", errSuffix(err))
	}
	words := decide.Sanitize(strings.Join(p.pos, " "))
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	if !hosted(io) {
		s.Event("refused", map[string]any{"command": "replan", "why": "not hosted"})
		return fail(io, "%s", notHostedNote(io, "no replan opened: this session is not hosted by baton, so nothing would hold phase agents while it is open, or close it if it is left"))
	}
	o, err := replan.Open(s, state.ReplanHuman, words, io.Now(), replan.MaxFrom(io.Env))
	if err != nil {
		s.Event("refused", map[string]any{"command": "replan", "why": firstLine(err)})
		return fail(io, "%v", err)
	}
	if o.Again {
		fmt.Fprintln(io.Out, decide.ReplanAgain(o.Replan.By, o.Replan.Since))
	}
	fmt.Fprintln(io.Out, o.Brief)
	return 0
}

func cmdReplanned(args []string, io IO) (code int) {
	defer failedMidway(io, "replanned", &code)
	p, err := parseArgs(args, []string{"notes", "none"}, nil)
	rawNotes, hasNotes := p.vals["notes"]
	rawWhy, hasNone := p.vals["none"]
	notes, why := decide.Sanitize(rawNotes), decide.Sanitize(rawWhy)
	if err != nil || len(p.pos) > 0 || hasNotes == hasNone || hasNotes && notes == "" || hasNone && why == "" {
		return fail(io, "usage: baton replanned --notes \"<what changed and why>\" | --none \"<why nothing should change>\"%s", errSuffix(err))
	}
	s, _, ok := storeAndPlan(io)
	if !ok {
		return 1
	}
	if hasNone {
		if _, err := replan.Decline(s, why, io.Now()); err != nil {
			return fail(io, "%v", err)
		}
		_, tell := decide.ReplanDeclined(why, false)
		fmt.Fprintln(io.Out, tell)
		return 0
	}
	f, err := replan.Finish(s, notes, io.Now())
	var refused replan.Refused
	switch {
	case errors.As(err, &refused):
		return fail(io, "%s", refused.Text)
	case err != nil:
		return fail(io, "%v", err)
	case f.Unchanged:
		_, tell := decide.ReplanDeclined("", true)
		fmt.Fprintln(io.Out, tell)
		return 0
	}
	st, took := f.State, ""
	if f.Took != "" {
		took = phaseLabel(f.Plan, f.Took)
	}
	next := decide.ReplanNext(took, phaseLabel(f.Plan, st.Current), st.Mode, st.BoundaryOwed, st.InHand())
	fmt.Fprintln(io.Out, decide.ReplannedTell(f.Replanned.Changed, f.Decision.ID, next))
	return 0
}

// failedMidway turns a panic in command into a failure the model reads (exit 1), as a hook's fails open:
// what the command changed is left as it stood (a replan's plan file is put back by replan.Finish).
func failedMidway(io IO, command string, code *int) {
	if r := recover(); r != nil {
		*code = fail(io, "%s failed midway (%v); nothing more changed. Carry on with the plan as it is.", command, r)
	}
}

// printReplans lists the replan open, if one is, and the replans baton followed since the human approved
// the plan, each with what it changed and what the plan after it changed of the approved one.
func printReplans(io IO, s *state.Store, st state.State) {
	if r := st.Replan; r != nil {
		fmt.Fprintln(io.Out, decide.ReplanOpenLine(*r, replan.MaxFrom(io.Env)))
	}
	if len(st.Replans) == 0 {
		return
	}
	fmt.Fprintf(io.Out, "replans (the plan as approved is kept in %s):\n", shown(s, s.ReplanText(0)))
	for _, r := range st.Replans {
		fmt.Fprintln(io.Out, "  "+decide.ReplanLine(r))
	}
}

// firstLine is the first line of err.
func firstLine(err error) string {
	first, _, _ := strings.Cut(err.Error(), "\n")
	return first
}
