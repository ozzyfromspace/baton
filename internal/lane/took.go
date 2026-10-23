package lane

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// How long a failed test ran, as its runner's failure line says it (FailedTest.Took), v0.8.15: go's `---
// FAIL: TestX (300.00s)`, Playwright's `✘ … (1.2m)`, vitest's `× name 5ms`. The other runners' failure lines
// say none.

// Same reports whether f and o are the same test, whatever time each says it took.
func (f FailedTest) Same(o FailedTest) bool {
	f.Took, o.Took = 0, 0
	return f == o
}

// tookRE is a runner's time: a number and its unit, as go ("300.00s"), Playwright ("500ms", "30.0s",
// "1.2m", "1.3h") and vitest ("5ms", "1.20s") print it.
var tookRE = regexp.MustCompile(`^(\d+(?:\.\d+)?)(ms|s|m|h|d)$`)

var tookUnits = map[string]time.Duration{"ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}

// parseTook reads a runner's time ("300.00s", "1.2m", "5ms"), reporting whether s is one.
func parseTook(s string) (time.Duration, bool) {
	m := tookRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(n * float64(tookUnits[m[2]])), true
}

// parenTook is the time a line ends with in parentheses, as go's and Playwright's failure lines do ("---
// FAIL: TestX (300.00s)", "✘ 4 … › checkout (retry #1) (30.0s)"); 0 for none.
func parenTook(line string) time.Duration {
	line = strings.TrimRight(line, " \t")
	rest, ok := strings.CutSuffix(line, ")")
	if !ok {
		return 0
	}
	open := strings.LastIndexByte(rest, '(')
	if open < 0 {
		return 0
	}
	d, _ := parseTook(rest[open+1:])
	return d
}

// cutTook cuts the time a vitest test line ends with ("5ms", "1.20s") from name, and returns it too (0 for
// none). vitest gives milliseconds or seconds alone, so a name that ends with a word like "5m" keeps it.
func cutTook(name string) (string, time.Duration) {
	if i := strings.LastIndexByte(name, ' '); i > 0 && strings.HasSuffix(name, "s") {
		if d, ok := parseTook(name[i+1:]); ok {
			return strings.TrimSpace(name[:i]), d
		}
	}
	return strings.TrimSpace(name), 0
}
