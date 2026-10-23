package lane

import (
	"slices"
	"time"
)

// Failures that waited out a timeout (v0.8.15). When several of a red gate's failures ran for about the
// same long time, they did not fail on what they checked: they waited for a timeout to end them, and every
// red run of the gate pays that wait again. A failing test should fail in seconds; the gate's result says
// so (decide.JobResult), and job_finished logs it.

const (
	// TimeoutLeast is the fewest failures that make a cluster: fewer can share a time by chance.
	TimeoutLeast = 3
	// TimeoutLong is the least time each failure of a cluster ran.
	TimeoutLong = 30 * time.Second
	// TimeoutSpread is how far each time of a cluster may be from their median, as a share of it.
	TimeoutSpread = 0.10
	// TimeoutShare is the least share of the gate's run its cluster's times add up to: below it, the wait
	// cost too little to speak of.
	TimeoutShare = 0.25
)

// TimedOut are the failures of fs whose times cluster at one long duration, in the order of fs: at least
// TimeoutLeast of them, each TimeoutLong or more, all within TimeoutSpread of their median, and adding up to
// TimeoutShare or more of ran, how long the gate's command ran. Of two clusters, the one of more time, then
// of more failures, is taken: it is the wait that costs most. None when no cluster meets all of it, or ran
// is not known.
func TimedOut(fs []FailedTest, ran time.Duration) []FailedTest {
	if ran <= 0 {
		return nil
	}
	var long []time.Duration
	for _, f := range fs {
		if f.Took >= TimeoutLong {
			long = append(long, f.Took)
		}
	}
	slices.Sort(long)
	var best []time.Duration // a run of long: the cluster
	for _, d := range long {
		c := around(long, d)
		for range len(long) { // settle on the times within the spread of their own median
			next := around(long, median(c))
			if len(next) == 0 || len(next) == len(c) && next[0] == c[0] {
				break
			}
			c = next
		}
		if len(c) < TimeoutLeast || len(around(c, median(c))) != len(c) {
			continue // too few, or never settled
		}
		if sum(c) > sum(best) || sum(c) == sum(best) && len(c) > len(best) {
			best = c
		}
	}
	if best == nil || float64(sum(best)) < TimeoutShare*float64(ran) {
		return nil
	}
	lo, hi := best[0], best[len(best)-1]
	var out []FailedTest
	for _, f := range fs {
		if f.Took >= lo && f.Took <= hi {
			out = append(out, f)
		}
	}
	return out
}

// Times are what the failures fs took: the median of their times, which each took about, and all of them
// added up.
func Times(fs []FailedTest) (each, all time.Duration) {
	ds := make([]time.Duration, len(fs))
	for i, f := range fs {
		ds[i] = f.Took
	}
	slices.Sort(ds)
	return median(ds), sum(ds)
}

// around is the run of the sorted times ds within TimeoutSpread of m.
func around(ds []time.Duration, m time.Duration) []time.Duration {
	spread := time.Duration(TimeoutSpread * float64(m))
	from, _ := slices.BinarySearch(ds, m-spread)
	to := from
	for to < len(ds) && ds[to] <= m+spread {
		to++
	}
	return ds[from:to]
}

// median is the middle of the sorted times ds, or the mean of the two in the middle; 0 for none.
func median(ds []time.Duration) time.Duration {
	n := len(ds)
	switch {
	case n == 0:
		return 0
	case n%2 == 1:
		return ds[n/2]
	}
	return (ds[n/2-1] + ds[n/2]) / 2
}

func sum(ds []time.Duration) time.Duration {
	var all time.Duration
	for _, d := range ds {
		all += d
	}
	return all
}
