package state

import (
	"slices"
	"testing"
	"time"
)

// A session with no plan of its own carries on the one run of the same plan file that no live host owns:
// not one a live host drives, not one of several, not a run of another plan, nor a finished one.
func TestCarry(t *testing.T) {
	const file = "/work/plan.md"
	type run struct {
		session, file string
		live          bool // its host is alive
		done          bool // its plan is complete
	}
	for _, c := range []struct {
		name string
		runs []run
		mine bool   // the session's own run has a plan
		want string // the run Orphans lists alone, carried on; "" for none
	}{
		{"one left with no host", []run{{"old", file, false, false}}, false, "old"},
		{"one a live host drives", []run{{"old", file, true, false}}, false, ""},
		{"two left with no host", []run{{"old", file, false, false}, {"older", file, false, false}}, false, ""},
		{"one of another plan", []run{{"old", "/work/other.md", false, false}}, false, ""},
		{"one finished", []run{{"old", file, false, true}}, false, ""},
		{"one left, beside one of another plan", []run{{"old", file, false, false}, {"else", "/work/other.md", false, false}}, false, "old"},
		{"the session has a plan of its own", []run{{"old", file, false, false}}, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, clk := newProject(t)
			for _, r := range c.runs {
				s := bind(t, p, Binding{Session: r.session, Instance: "host-" + r.session})
				pl := threePhases()
				pl.Title, pl.File = "Plan "+r.session, r.file
				if err := s.SavePlan(pl); err != nil {
					t.Fatal(err)
				}
				s.Update(func(st *State) error {
					*st = Reattach(*st, pl, t0, Origin{})
					st.Phases["P0"].Status = PhaseDone
					if r.done {
						st.Mode = ModeComplete
					}
					return nil
				})
			}
			clk.t = clk.t.Add(OwnerTTL + time.Second) // every host went quiet…
			for _, r := range c.runs {
				if r.live { // …but these
					s, _ := p.Run(r.session, "")
					s.Update(func(st *State) error { return Claim(st, "host-"+r.session, 1, clk.t) })
				}
			}
			mine := bind(t, p, Binding{Session: "bg", Instance: "host-bg"})
			if c.mine {
				withPlan(t, mine, "My own")
			}
			var ids []string
			for _, r := range p.Orphans(file, "bg") {
				ids = append(ids, r.ID)
			}
			if c.mine {
				if err := p.Carry("bg", "old", file); err == nil {
					t.Fatal("carried a run on in a session with a plan of its own")
				}
				return
			}
			if (len(ids) == 1) != (c.want != "") || c.want != "" && ids[0] != c.want {
				t.Fatalf("Orphans = %v, want %q alone", ids, c.want)
			}
			if c.want == "" {
				for _, r := range c.runs { // of several, the caller carries none on (Orphans lists them all)
					if r.live || r.done || r.file != file {
						if err := p.Carry("bg", r.session, file); err == nil {
							t.Errorf("carried on %s (live %v, done %v, file %s)", r.session, r.live, r.done, r.file)
						}
					}
				}
				return
			}
			if err := p.Carry("bg", c.want, file); err != nil {
				t.Fatal(err)
			}
			if p.RunOf("bg") != c.want {
				t.Fatalf("bg belongs to %q", p.RunOf("bg"))
			}
			// The session's next hook makes its host the run's driver, and lets its empty run go.
			s := bind(t, p, Binding{Session: "bg", Instance: "host-bg"})
			if s.Ref != c.want || p.HostRun("host-bg") != c.want || ownerOf(t, s) != "host-bg" || ownerOf(t, mine) != "" {
				t.Errorf("after the carry, bg binds %s, its host drives %q, owners %q and %q", s.Ref, p.HostRun("host-bg"), ownerOf(t, s), ownerOf(t, mine))
			}
			st, _ := s.Load()
			if st.Phases["P0"].Status != PhaseDone || st.Current != "P1" && st.Current != "P0" {
				t.Errorf("the run did not go on where it stood: %+v", st.Phases["P0"])
			}
			info := slices.IndexFunc(p.Runs(), func(r RunInfo) bool { return r.ID == c.want })
			if left := p.Runs()[info].Left("bg"); !slices.Equal(left, []string{c.want}) {
				t.Errorf("Left = %v", left)
			}
		})
	}
}
