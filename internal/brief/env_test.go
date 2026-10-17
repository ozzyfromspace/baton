package brief

import (
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// Every brief lists the run's environment facts: the lead's from its own state, a worker's from the run's
// (Input.Facts), since its sub-run keeps none. With none, the brief says nothing of them.
func TestTheBriefListsTheRunsEnvironmentFacts(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	facts := []state.Fact{{Text: "leave host port 5432 free: the dev stack holds it", Phase: "P0", At: t0}, {Text: "start colima before docker compose", At: t0}}
	list := "- leave host port 5432 free: the dev stack holds it (P0)\n- start colima before docker compose (the lead)"

	pl, doc, st := setup(t)
	if b := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st}); strings.Contains(b, "baton env") {
		t.Errorf("a run with no facts:\n%s", b)
	}
	st.Facts = facts
	lead := Write(Input{Kind: Boundary, Plan: pl, Doc: doc, State: st})
	if !strings.Contains(lead, decide.EnvFacts(facts)) || !strings.Contains(lead, list) {
		t.Errorf("the lead's brief lacks the facts:\n%s", lead)
	}

	wpl, wdoc, wst := worker(t)
	words := decide.Words{Git: true, Timeout: 5 * time.Minute, Worker: true}
	in := Input{Kind: Worker, Plan: wpl, Doc: wdoc, State: wst, Words: words, Tree: "/w/R1", Branch: "baton/run/R1"}
	if b := Write(in); strings.Contains(b, "baton env") {
		t.Errorf("a worker's brief with no facts:\n%s", b)
	}
	in.Facts = facts
	if b := Write(in); !strings.Contains(b, list) {
		t.Errorf("the worker's brief lacks the run's facts:\n%s", b)
	}
}
