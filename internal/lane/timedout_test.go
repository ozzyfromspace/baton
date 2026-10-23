package lane

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// Failures that waited out a timeout are those whose times cluster at one long duration: at least three,
// each 30s or more, within a tenth of their median, adding up to a quarter or more of the gate's run.
func TestTimedOut(t *testing.T) {
	s, m := time.Second, time.Minute
	tests := func(took ...time.Duration) []FailedTest {
		fs := make([]FailedTest, len(took))
		for i, d := range took {
			fs[i] = FailedTest{Runner: RunPlaywright, ID: fmt.Sprintf("t.spec.ts:%d", i+1), Took: d}
		}
		return fs
	}
	ids := func(fs []FailedTest) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.ID)
		}
		return out
	}
	cases := []struct {
		name string
		fs   []FailedTest
		ran  time.Duration
		want []string // ids
	}{
		{"twelve of fourteen at 3m", tests(3*m, 3*m+2*s, 2*s, 179*s, 3*m, 3*m, 3*m+1*s, 3*m, 5*s, 3*m, 3*m, 182*s, 3*m, 3*m), 40 * m,
			[]string{"t.spec.ts:1", "t.spec.ts:2", "t.spec.ts:4", "t.spec.ts:5", "t.spec.ts:6", "t.spec.ts:7", "t.spec.ts:8", "t.spec.ts:10",
				"t.spec.ts:11", "t.spec.ts:12", "t.spec.ts:13", "t.spec.ts:14"}},
		{"three, just enough", tests(30*s, 31*s, 32*s), 6 * m, []string{"t.spec.ts:1", "t.spec.ts:2", "t.spec.ts:3"}},
		{"side by side, more than the run", tests(2*m, 2*m, 2*m, 2*m), 3 * m, []string{"t.spec.ts:1", "t.spec.ts:2", "t.spec.ts:3", "t.spec.ts:4"}},
		{"of two clusters, the one of more time", tests(30*s, 30*s, 30*s, 30*s, 5*m, 5*m, 5*m), 20 * m, []string{"t.spec.ts:5", "t.spec.ts:6", "t.spec.ts:7"}},
		{"of two of as much time, the one of more failures", tests(m, m, m, m, m, m, 2*m, 2*m, 2*m), 20 * m,
			[]string{"t.spec.ts:1", "t.spec.ts:2", "t.spec.ts:3", "t.spec.ts:4", "t.spec.ts:5", "t.spec.ts:6"}},
		{"two long ones are not a cluster", tests(30*s, 30*s, 30*s, 10*m, 10*m), 5 * m, []string{"t.spec.ts:1", "t.spec.ts:2", "t.spec.ts:3"}},
		{"spread times", tests(40*s, 55*s, 70*s, 90*s, 2*m, 3*m), 10 * m, nil},
		{"too few", tests(3*m, 3*m, 2*s), 10 * m, nil},
		{"too short", tests(25*s, 25*s, 25*s, 25*s), m, nil},
		{"too small a share", tests(30*s, 30*s, 30*s), 7 * m, nil},
		{"no times", tests(0, 0, 0, 0), 10 * m, nil},
		{"the run's time unknown", tests(m, m, m), 0, nil},
	}
	for _, c := range cases {
		if got := ids(TimedOut(c.fs, c.ran)); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// What failures took: about the median each, and their sum.
func TestTimes(t *testing.T) {
	s := time.Second
	for _, c := range []struct {
		took      []time.Duration
		each, all time.Duration
	}{
		{[]time.Duration{30 * s, 32 * s, 31 * s}, 31 * s, 93 * s},
		{[]time.Duration{40 * s, 30 * s, 32 * s, 34 * s}, 33 * s, 136 * s},
		{nil, 0, 0},
	} {
		var fs []FailedTest
		for _, d := range c.took {
			fs = append(fs, FailedTest{ID: "x", Took: d})
		}
		if each, all := Times(fs); each != c.each || all != c.all {
			t.Errorf("%v: each %s, all %s; want %s, %s", c.took, each, all, c.each, c.all)
		}
	}
}
