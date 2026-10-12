package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/term"

	"github.com/ozzyfromspace/baton/internal/board"
	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// baton board is baton's view of the run (internal/board). baton's mod runs `baton board --band` every
// second for the band above the lead's prompt, and `baton board` for the /baton board pane; the human can
// run either by hand. It reads the run, takes no lock, and changes nothing.

func init() {
	register("board", "show the plan step by step, each phase and the lead: --band for one line, --width N to fit, --json for machines", cmdBoard)
}

func cmdBoard(args []string, io IO) int {
	p, err := parseArgs(args, []string{"width"}, []string{"band", "json"})
	if err != nil {
		return fail(io, "%v", err)
	}
	width := termWidth(io)
	if w, ok := p.vals["width"]; ok {
		if width, err = strconv.Atoi(w); err != nil || width < 0 {
			return fail(io, "--width takes a number of columns (0: no limit)")
		}
	}
	b, err := loadBoard(io)
	if err != nil {
		return fail(io, "%v", err)
	}
	switch {
	case p.bools["json"]:
		out, _ := json.MarshalIndent(b, "", "  ")
		fmt.Fprintln(io.Out, string(out))
	case p.bools["band"]:
		if band := b.Band(width); band != "" {
			fmt.Fprintln(io.Out, band)
		}
	default:
		for _, l := range b.Lines(width) {
			fmt.Fprintln(io.Out, l)
		}
	}
	return 0
}

// loadBoard builds the board of the run the command is about: with no plan, an empty one.
func loadBoard(io IO) (board.Board, error) {
	in := board.Input{Now: io.Now()}
	s, p, err := boardRun(io)
	switch {
	case errors.Is(err, state.ErrNoPlan):
		return board.Build(in), nil
	case err != nil:
		return board.Board{}, err
	}
	if in.State, err = s.Peek(); err != nil {
		return board.Board{}, err
	}
	pl, perr := s.LoadPlan()
	in.Plan, in.HavePlan = pl, perr == nil
	in.Subs = p.Workers(s.Ref)
	if in.Serial = in.State.SerialWhy(); in.Serial == "" {
		in.Serial = serialByConfig(io)
	}
	return board.Build(in), nil
}

// boardRun is the run baton board shows: the session's (in a phase agent, the run its phase is of), or
// from a shell the run the command can only mean (state.Project.Pick). It binds nothing.
func boardRun(io IO) (*state.Store, *state.Project, error) {
	p, err := project(io)
	if err != nil {
		return nil, nil, err
	}
	if sub, ok := inPhase(io, p); ok {
		s, err := sub.ParentRun()
		return s, p, err
	}
	sid, inst := io.Env("CLAUDE_CODE_SESSION_ID"), instance(io)
	if s, ok := p.Lookup(sid, inst); ok {
		return s, p, nil
	}
	if sid != "" || inst != "" {
		return nil, p, state.ErrNoPlan
	}
	s, err := p.Pick(inst)
	return s, p, err
}

// serialByConfig is why the run's phases go one at a time in the lead when baton's config runs no phase
// agents, or "".
func serialByConfig(io IO) string {
	n, auto, err := config.Load(batonRoot(io), io.Env).WorkerCap(io.reading)
	if err != nil || n > 0 {
		return ""
	}
	return decide.SerialConfig(auto)
}

// termWidth is the width of the terminal the command writes to, or 0 when it writes elsewhere.
func termWidth(io IO) int {
	f, ok := io.Out.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return 0
	}
	w, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return w
}
