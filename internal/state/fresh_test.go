package state

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

// A new plan starts fresh: of the run it is attached over, it keeps only what is the session's, the host's or
// the account's. Every field of State and of Runtime is sorted here, kept or fresh, and one in neither list
// fails, so a field added later must be sorted. Each is then shown kept, or fresh, over a run whose every
// field was set.
func TestANewPlanStartsFresh(t *testing.T) {
	// What the session, the host and the account still have under a new plan (freshRun says why of each).
	runKept := []string{
		"SessionID", "PermissionMode", "TurnOpen", "TurnBy", "TurnStarted", "LastStop", "LastActivity", "History", "HumanAt",
		"Dialogs", "Gate",
		"Subagents", "Background", "BackgroundTold", "BackgroundToldAt", "Monitors", "Crons", "Tools", "Spawning", "AgentCalls", "Plain",
		"Compaction", "ModReports", "ModCounted", "Serial", "Fleet", "Proven", "Notices", "Announce",
		"Context", "ContextNoticed", "ContextNudged", "ContextNudgedAt", "ContextWarned", "WarnQuestion", "WarnOwed",
		"LastError", "PendingDone", "PlanFile", "ClearedPlan", "ExitAt", "Restart", "NewerNoted", "StaleSkill", "Ended", "Mark", "Recoveries",
	}
	// The replaced plan's: its questions asked once, its counts, its escalation, briefs and alarms, its end
	// told, and a sub-run's agent.
	runFresh := []string{
		"DirectivesAsked", "UnbuiltAsked", "DirectivesPending", "GatesQuestioned", "LeftoverAsked", "BackgroundQuestioned",
		"StopBlocks", "IdleNudges", "Escalation", "Briefing", "Stalled", "Review", "CompleteNotified",
		"Briefed", "Transcript",
	}
	// What a run keeps under a new plan (attachKeeping, Reattach): its owner, a pause and where the context
	// stood at it, the workers still at work (the replaced plan's, which the host retires), the numbers its
	// jobs and ids go on from, its generations, and of its runtime what runKept says.
	stKept := []string{"Owner", "Mode", "PauseContext", "PauseCompacted", "Workers", "JobSeq", "IDPrefix", "Generation", "Run"}
	// The replaced plan's: its phases and jobs, what it owed, its decisions and questions, replans, branch,
	// landings, usage and holds, mail and look-ins, records, facts and risk, and the human's wrap of it. (The
	// callers set the new plan's branch after attaching it.)
	stFresh := []string{
		"Version", "Current", "Phases", "Blocked", "Jobs", "KeptLogs",
		"BoundaryOwed", "CheckpointOwed", "CheckpointAsked", "CheckpointDue", "OwedAt", "Covered",
		"Decisions", "ReviewDue", "ReviewAt", "PlanQuestion", "PlanDrift", "PlanLooked",
		"Replan", "Replans", "RulesVersion", "ReplanCued",
		"RunBranch", "Relay", "Landed", "Usage", "Hold", "Conflicts", "BranchNote", "BranchTold", "BranchFrom",
		"Inbox", "LookIns", "Conflicted", "ConflictCue", "Advice", "Records", "ToAgents", "PlanText", "Facts", "Recorded", "Risk",
		"Wrap",
	}
	sorted(t, reflect.TypeFor[Runtime](), runKept, runFresh)
	sorted(t, reflect.TypeFor[State](), stKept, stFresh)

	var prev State
	fill(t, reflect.ValueOf(&prev).Elem())
	prev.Mode = ModePaused // a pause stays: the human has the wheel
	p, now := threePhases(), t0.Add(time.Hour)
	got, blank := Reattach(prev, p, now, Origin{}), Attach(p, now, Origin{})

	want := map[string]any{"Generation": prev.Generation + 1, "Workers": prev.LiveWorkers()}
	gv, pv, bv := reflect.ValueOf(got), reflect.ValueOf(prev), reflect.ValueOf(blank)
	for _, name := range stKept {
		if name == "Run" {
			continue // field by field, below
		}
		w, ok := want[name]
		if !ok {
			w = pv.FieldByName(name).Interface()
		}
		if g := gv.FieldByName(name).Interface(); !reflect.DeepEqual(g, w) {
			t.Errorf("State.%s = %+v, want it kept: %+v", name, g, w)
		}
	}
	for _, name := range stFresh {
		if g, w := gv.FieldByName(name).Interface(), bv.FieldByName(name).Interface(); !reflect.DeepEqual(g, w) {
			t.Errorf("State.%s = %+v, want it fresh, as a first plan has it: %+v", name, g, w)
		}
	}

	want = map[string]any{"Compaction": Compaction{Epoch: prev.Run.Compaction.Epoch}}
	gv, pv = reflect.ValueOf(got.Run), reflect.ValueOf(prev.Run)
	for _, name := range runKept {
		w, ok := want[name]
		if !ok {
			w = pv.FieldByName(name).Interface()
		}
		if g := gv.FieldByName(name).Interface(); !reflect.DeepEqual(g, w) {
			t.Errorf("Runtime.%s = %+v, want it kept: %+v", name, g, w)
		}
	}
	for _, name := range runFresh {
		if f := gv.FieldByName(name); !f.IsZero() {
			t.Errorf("Runtime.%s = %+v, want it fresh", name, f.Interface())
		}
	}
}

// sorted fails t for a field of typ in neither kept nor fresh, or in both, and for a name in either that
// is no field of typ.
func sorted(t *testing.T, typ reflect.Type, kept, fresh []string) {
	t.Helper()
	for f := range typ.Fields() {
		switch k, fr := slices.Contains(kept, f.Name), slices.Contains(fresh, f.Name); {
		case !k && !fr:
			t.Errorf("%s.%s is neither kept nor fresh under a new plan: sort it (freshRun, attachKeeping)", typ.Name(), f.Name)
		case k && fr:
			t.Errorf("%s.%s is both kept and fresh", typ.Name(), f.Name)
		}
	}
	for _, name := range slices.Concat(kept, fresh) {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("%s has no field %s", typ.Name(), name)
		}
	}
}

// fill sets every exported field of the struct v to a value that is not zero, and fails t for one it cannot.
func fill(t *testing.T, v reflect.Value) {
	t.Helper()
	filled(v, 0)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Fatalf("%s.%s could not be filled", v.Type().Name(), v.Type().Field(i).Name)
		}
	}
}

// filled sets v, and what it points to or holds, to values that are not zero, down to depth 5.
func filled(v reflect.Value, depth int) {
	if depth > 5 {
		return
	}
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.String:
		v.SetString("x")
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		filled(p.Elem(), depth+1)
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		filled(s.Index(0), depth+1)
		v.Set(s)
	case reflect.Map:
		m, k, e := reflect.MakeMap(v.Type()), reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		filled(k, depth+1)
		filled(e, depth+1)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(t0))
			return
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				filled(v.Field(i), depth+1)
			}
		}
	}
}
