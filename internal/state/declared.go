package state

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DeclaredEnd is the run time a background task's description declares, 0 for none: a number and a unit
// (`~15m`, `15 min`, `2 hours`, `1.5h`, `90s`), or a duration as Go writes it (`1h30m`). One marked with ~
// wins; else the first. A task that runs PastEnd times as long is a suspect (SignalPastEnd). An end declared as
// a condition (`until P12's checks pass`) has no run time.
func DeclaredEnd(description string) time.Duration {
	var first time.Duration
	for _, m := range declaredRun.FindAllStringSubmatch(description, -1) {
		d := runTime(m[2])
		if d <= 0 {
			continue
		}
		if m[1] != "" {
			return d
		}
		if first == 0 {
			first = d
		}
	}
	return first
}

// declaredRun is a run time in a description: an optional ~ (m[1]), then a duration as Go writes it, or a
// number and a unit, plural or not (m[2]). The unit must end its word: 5mb is no run time.
var declaredRun = regexp.MustCompile(`(?i)(~\s*)?\b(\d+h\d+m(?:\d+s)?|\d+m\d+s|\d+(?:\.\d+)?\s*(?:hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s))\b`)

// runTime reads one match of declaredRun's run time.
func runTime(s string) time.Duration {
	s = strings.ToLower(strings.TrimSpace(s))
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	i := strings.IndexFunc(s, func(r rune) bool { return !('0' <= r && r <= '9' || r == '.') })
	if i <= 0 {
		return 0
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	unit := time.Second
	switch strings.TrimSpace(s[i:])[0] {
	case 'h':
		unit = time.Hour
	case 'm':
		unit = time.Minute
	}
	return time.Duration(n * float64(unit))
}
