package state

import (
	"testing"
	"time"
)

// A fact is recorded once: the same words again, whatever their case and spacing, are the fact on record.
func TestAddFact(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	st := New()
	for _, c := range []struct {
		text, phase string
		fresh       bool
		want        Fact
	}{
		{"leave host port 5432 free", "P2", true, Fact{Text: "leave host port 5432 free", Phase: "P2", At: t0}},
		{"Leave  host port 5432 FREE", "P3", false, Fact{Text: "leave host port 5432 free", Phase: "P2", At: t0}},
		{"start colima first", "", true, Fact{Text: "start colima first", At: t0.Add(2 * time.Minute)}},
	} {
		got, fresh := AddFact(&st, c.text, c.phase, t0.Add(time.Duration(len(st.Facts))*2*time.Minute))
		if fresh != c.fresh || got != c.want {
			t.Errorf("AddFact(%q) = %+v, %v; want %+v, %v", c.text, got, fresh, c.want, c.fresh)
		}
	}
	if len(st.Facts) != 2 {
		t.Errorf("facts %+v", st.Facts)
	}
	if _, ok := KnownFact(st, "start colima"); ok {
		t.Error("a fact is known by part of its words")
	}
}

// The run's facts are its own state's; a worker's sub-run keeps none, and reads its run's.
func TestTheRunsFactsAreReadFromAWorker(t *testing.T) {
	p, _ := newProject(t)
	lead := bind(t, p, Binding{Session: "lead", Instance: "host"})
	withPlan(t, lead, "Plan")
	w := newWorker(t, p, lead, "P1")
	lead.Update(func(st *State) error {
		AddFact(st, "leave host port 5432 free", "P1", time.Now())
		return nil
	})
	st, _ := lead.Load()
	if f := lead.Facts(st); len(f) != 1 || f[0].Text != "leave host port 5432 free" {
		t.Errorf("the run's facts %+v", f)
	}
	sub, _ := w.Load()
	if f := w.Facts(sub); len(f) != 1 || f[0].Phase != "P1" || len(sub.Facts) != 0 {
		t.Errorf("read from the worker: %+v (its own %+v)", f, sub.Facts)
	}
}
