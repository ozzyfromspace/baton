package decide

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/leftover"
	"github.com/ozzyfromspace/baton/internal/procs"
	"github.com/ozzyfromspace/baton/internal/state"
)

// what a phase left in /w/P3: a dev server with a shell in its group, and a container compose started.
var leftP3 = leftover.Left{
	Procs:      []procs.Proc{{PID: 4123, PGID: 4120, Comm: "node", Cwd: "/w/P3/web"}},
	Hidden:     []procs.Proc{{PID: 4120, PGID: 4120, Comm: "sh", Cwd: "/w/P3"}},
	Containers: []leftover.Container{{Name: "p3-db-1", Status: "Up 3 minutes", Project: "p3", Dir: "/w/P3"}},
}

// Each message lists what was left, a line each with the command that stops it, and asks nothing it would
// wait on: the done ask goes through the second time, and the rest is information.
func TestWhatAPhaseLeftIsListedWithTheCommandsThatStopIt(t *testing.T) {
	lines := []string{
		"  - process 4123 (node) in /w/P3/web: kill 4123\n",
		"  - process 4120 (sh) in /w/P3, in the process group of 4123: kill 4120\n",
		"  - container p3-db-1 (Up 3 minutes), which compose started in /w/P3: docker rm -f p3-db-1\n",
	}
	j := state.Job{ID: "P5.j2", Phase: "P5", Command: "make e2e"}
	for _, c := range []struct {
		name, got string
		want      []string
	}{
		{"the done ask", LeftoverAsk("P3", "/w/P3", leftP3), append([]string{
			"not done yet — P3 leaves this running, in its worktree (/w/P3) or a lane its gates used, and it would go on using the machine beside the phases after it:\n",
			"Stop what P3 started (for containers, `docker compose down` where compose ran stops them with their network), then run this `baton done` again.",
			"run this `baton done` again as it is: baton asks this once.",
		}, lines...)},
		{"the lead's look-in at retiring", LeftoverRetired("P3", "/w/P3", leftP3), append([]string{
			"[baton] P3's agent has ended, but it left this running in its worktree (/w/P3), which baton does not stop by itself: it removes no container",
			"baton asks nothing about them.",
		}, lines...)},
		{"the human's line at retiring", LeftoverRetiredSaid("P3", leftP3), []string{
			"baton: P3 left a container and 2 processes running after its agent ended (p3-db-1, node (pid 4123) and sh (pid 4120))",
		}},
		{"what retiring stopped", LeftoverStoppedSaid("P3", leftP3.Procs), []string{"baton: stopped what P3's agent left running in its worktree: node (pid 4123)"}},
		{"the look-in on a slow gate", LeftoverLookIn(j, 42*time.Minute, "the machine is busy (load 9.1 on 8 CPUs)", []PhaseLeft{{"P3", leftP3}}), []string{
			"[baton] Job P5.j2 (P5, `make e2e`) has run 42m, past its slowest earlier pass, while the machine is busy (load 9.1 on 8 CPUs); and phases that finished left this running:\n  P3:\n",
			"    - container p3-db-1 (Up 3 minutes), which compose started in /w/P3: docker rm -f p3-db-1\n",
			"It may be what slows your gate.",
			"baton stops none of it by itself, and asks nothing.",
		}},
		{"the human's line on a slow gate", LeftoverLookInSaid(j, []PhaseLeft{{"P3", leftP3}, {"P4", leftP3}}), []string{
			"baton: job P5.j2 (`make e2e`, P5) runs past its history on a busy machine, beside what P3 and P4 left running",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, want := range c.want {
				if !strings.Contains(c.got, want) {
					t.Errorf("lacks %q:\n%s", want, c.got)
				}
			}
		})
	}
	if ask := LeftoverAsk("P3", "/w/P3", leftover.Left{Procs: leftP3.Procs}); strings.Contains(ask, "docker compose down") {
		t.Errorf("the ask names compose with no container: %s", ask)
	}
}

// A long list is cut at maxLeftListed, saying how many more.
func TestALongListOfWhatWasLeftIsCut(t *testing.T) {
	var many leftover.Left
	for i := range maxLeftListed + 3 {
		many.Containers = append(many.Containers, leftover.Container{Name: fmt.Sprintf("c%d", i), Status: "Up", Dir: "/w/P3"})
	}
	ask := LeftoverAsk("P3", "/w/P3", many)
	if n := strings.Count(ask, "docker rm -f"); n != maxLeftListed || !strings.Contains(ask, "  - and 3 more\n") {
		t.Errorf("%d listed:\n%s", n, ask)
	}
	if said := LeftoverRetiredSaid("P3", many); !strings.Contains(said, "15 containers") || !strings.HasSuffix(said, "c11 and 3 more)") {
		t.Errorf("said %q", said)
	}
}
