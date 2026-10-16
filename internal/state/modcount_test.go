package state

import "testing"

// A report of the mod's is counted once: the same report again (a report process killed before the bridge
// recorded it, sent anew) changes nothing; any other report counts, a mod in another claude's included.
func TestCountOnceCountsAModReportOnce(t *testing.T) {
	first := ModSeq{Instance: "i", Seq: 4, Kind: "ack", Command: 7}
	for _, c := range []struct {
		name  string
		next  ModSeq
		count bool
	}{
		{"the same report again", first, false},
		{"the next report", ModSeq{Instance: "i", Seq: 5, Kind: "compacted", Command: 7}, true},
		{"another report under its number", ModSeq{Instance: "i", Seq: 4, Kind: "compacting"}, true},
		{"another claude's mod", ModSeq{Instance: "j", Seq: 4, Kind: "ack", Command: 7}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := New()
			n := 0
			count := func(*State) bool { n++; return true }
			if !CountOnce(&st, first, count) || n != 1 {
				t.Fatalf("first: counted %d times", n)
			}
			if got := CountOnce(&st, c.next, count); got != c.count || n != map[bool]int{false: 1, true: 2}[c.count] {
				t.Errorf("next: %v, counted %d times", got, n)
			}
			if *st.Run.ModCounted != c.next {
				t.Errorf("counted last: %+v", st.Run.ModCounted)
			}
		})
	}
}
