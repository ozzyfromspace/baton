package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/host"
	"github.com/ozzyfromspace/baton/internal/state"
)

func init() {
	register("view", "watch a worker and type into it, in a terminal of its own (Ctrl-] leaves): view [phase]", cmdView)
}

// viewable is a worker of one of the project's runs, as baton view finds it.
type viewable struct {
	run   state.RunInfo
	w     state.Worker
	title string // its phase's title
	host  string // the instance of the host that runs its session; "" if no host does
}

// cmdView shows a worker's screen in this terminal, and passes the human's keys to it, until they press
// Ctrl-]. The worker's host runs it whether or not anyone watches; with no phase, baton view lists the
// workers there are. It finds them through the project's runs, from a plain shell; inside a hosted session
// it only lists them, since showing one would take over the terminal claude runs in.
func cmdView(args []string, io IO) int {
	p, err := parseArgs(args, nil, nil)
	if err != nil {
		return fail(io, "view: %v", err)
	}
	if len(p.pos) > 1 {
		return fail(io, "view takes one phase, such as baton view P3")
	}
	proj, err := project(io)
	if err != nil {
		return fail(io, "%v", err)
	}
	ws := viewables(proj, io.Now())
	if len(p.pos) == 0 {
		printViewables(io, ws)
		return 0
	}
	if hosted(io) {
		return fail(io, "baton view takes over the terminal it runs in, so it cannot run inside a session. "+
			"Run baton view %s in another terminal, in this project.", p.pos[0])
	}
	w, err := pickViewable(ws, p.pos[0])
	if err != nil {
		return fail(io, "%v", err)
	}
	in, inOK := io.In.(*os.File)
	out, outOK := io.Out.(*os.File)
	if !inOK || !outOK {
		return fail(io, "%v", host.ErrNoTerminal)
	}
	why, err := host.Watch(host.ViewSocket(w.host), w.w.Phase, in, out)
	var ne *net.OpError
	switch {
	case errors.As(err, &ne):
		return fail(io, "cannot reach the baton that runs %s's worker: %v", w.w.Phase, err)
	case err != nil:
		return fail(io, "%v", err)
	case why != "":
		fmt.Fprintf(io.Out, "baton: %s\n", why)
	default:
		fmt.Fprintf(io.Out, "baton: left %s's worker, which carries on. baton view %s shows it again.\n", w.w.Phase, w.w.Phase)
	}
	return 0
}

// viewables lists the workers of the project's runs that are not done with: landed and failed workers
// are left out.
func viewables(proj *state.Project, now time.Time) []viewable {
	var out []viewable
	for _, r := range proj.Runs() {
		var titles map[string]string
		for _, w := range r.State.Workers {
			if !w.Live() {
				continue
			}
			if titles == nil {
				titles = phaseTitles(proj, r.ID)
			}
			v := viewable{run: r, w: w, title: titles[w.Phase]}
			if r.Live(now) && w.Status != state.WorkerSuspended && w.Instance == state.WorkerInstance(r.State.Owner.Instance, w.Phase) {
				v.host = r.State.Owner.Instance
			}
			out = append(out, v)
		}
	}
	return out
}

// phaseTitles maps the phases of run's plan to their titles.
func phaseTitles(proj *state.Project, run string) map[string]string {
	titles := map[string]string{}
	if s, err := proj.ExistingRun(run, ""); err == nil {
		if pl, err := s.LoadPlan(); err == nil {
			for _, ph := range pl.Phases {
				titles[ph.ID] = ph.Title
			}
		}
	}
	return titles
}

// pickViewable finds the worker arg names, by its phase or, among runs with a worker on the same phase,
// by its ref (run/phase), and makes sure a host runs it. Of several on the phase, the one running is
// meant: a run that ended keeps its workers suspended until it is hosted again, and plans number their
// phases alike.
func pickViewable(ws []viewable, arg string) (viewable, error) {
	var found []viewable
	for _, v := range ws {
		if v.w.Phase == arg || v.w.Ref == arg {
			found = append(found, v)
		}
	}
	if len(found) > 1 {
		switch running := slices.DeleteFunc(slices.Clone(found), func(v viewable) bool { return v.host == "" }); len(running) {
		case 0:
			found = found[:1] // none can be shown, as for one
		default:
			found = running
		}
	}
	switch {
	case len(found) == 0 && len(ws) == 0:
		return viewable{}, fmt.Errorf("no worker is working on %s: this project has no workers", arg)
	case len(found) == 0:
		var names []string
		for _, v := range ws {
			names = append(names, v.w.Phase)
		}
		return viewable{}, fmt.Errorf("no worker is working on %s. The workers here: %s (baton view lists them)", arg, strings.Join(names, ", "))
	case len(found) > 1:
		var refs []string
		for _, v := range found {
			refs = append(refs, "baton view "+v.w.Ref)
		}
		return viewable{}, fmt.Errorf("several runs have a worker on %s; name the one you mean: %s", arg, strings.Join(refs, ", or "))
	case found[0].host == "":
		return viewable{}, fmt.Errorf("%s's worker is not running: its host has ended. It resumes once its run's session is hosted by baton again", found[0].w.Phase)
	}
	return found[0], nil
}

func printViewables(io IO, ws []viewable) {
	if len(ws) == 0 {
		fmt.Fprintln(io.Out, "No worker is working in this project.")
		return
	}
	now := io.Now()
	fmt.Fprintln(io.Out, "Workers:")
	several := map[string]int{} // running workers by phase: a phase alone names the only one running
	for _, v := range ws {
		if v.host != "" {
			several[v.w.Phase]++
		}
	}
	for _, v := range ws {
		name := v.w.Phase
		if several[name] > 1 {
			name = v.w.Ref
		}
		label := v.w.Phase
		if v.title != "" {
			label += " — " + v.title
		}
		status := v.w.Status + " " + decide.Span(now.Sub(v.w.Since))
		switch {
		case v.host == "":
			fmt.Fprintf(io.Out, "  %s  %s, not running: it resumes with its run's next host\n", label, status)
		default:
			fmt.Fprintf(io.Out, "  %s  %s  baton view %s\n", label, status, name)
		}
	}
	if hosted(io) {
		fmt.Fprintln(io.Out, "baton view takes over the terminal it runs in: run it in another terminal, in this project. Ctrl-] leaves.")
	} else {
		fmt.Fprintln(io.Out, "Ctrl-] leaves a worker's screen; the worker carries on.")
	}
}
