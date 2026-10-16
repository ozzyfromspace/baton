package replan

import (
	"testing"

	"github.com/ozzyfromspace/baton/internal/state"
)

// sharedDoc is a plan whose phases name files: once P0 is done and P1 in hand, P2 and P3 may run at once
// and both name the CLI's main.go; P4 waits for P1, which names go.mod as P4 does.
const sharedDoc = `# Files

## Standing rules

- Keep ` + "`go.mod`" + ` tidy.

## P0 — Schema

After: none

Change ` + "`internal/state/state.go`" + `.

## P1 — API

After: P0

Change ` + "`internal/api/api.go`" + ` and ` + "`go.mod`" + `.

## P2 — CLI

After: P0

Change ` + "`cmd/baton/main.go`" + ` and ` + "`internal/state/state.go`" + `.

## P3 — Docs

After: P0

Change ` + "`docs/design.md`" + ` and ` + "`./cmd/baton/main.go`" + `.

## P4 — Web

After: P1

Change ` + "`web/app.ts`" + ` and ` + "`go.mod`" + `.

## Verification

All green.
`

// A replan's brief lists the phases not started that may run at once and name the same files, for the plan
// as it stands, when it opens and when it is given again; replan_opened counts the pairs. A phase that
// started or is done is in no pair, nor is a phase that waits for another. A plan whose phases name no
// shared file has no list.
func TestTheBriefNamesSharedFiles(t *testing.T) {
	const head = "Phases not started that may run at once and name the same files"
	r := rigOf(t, sharedDoc, "P0")
	if st := r.state(); st.Current != "P1" {
		t.Fatalf("in hand: %q", st.Current)
	}
	o := r.open("")
	wants := []string{head, "  P2 and P3: cmd/baton/main.go\n"}
	lacks := []string{"P0 and", "P1 and", "and P4"}
	checkBrief(t, o, wants, lacks)
	if e := r.event("replan_opened"); e == nil || e["shared"] != float64(1) {
		t.Errorf("replan_opened: %v", e)
	}
	checkBrief(t, r.open("the CLI first"), wants, lacks)

	r = newRig(t)
	checkBrief(t, r.open(""), nil, []string{head})
	if e := r.event("replan_opened"); e == nil || e["shared"] != nil || e["by"] != state.ReplanHuman {
		t.Errorf("replan_opened, no shared file: %v", e)
	}
}
