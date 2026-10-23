package config

import (
	"fmt"
	"math"
	"strconv"
)

// The weekly wind-down's reserve (weekly_reserve_pct): near the end of the account's weekly allowance, the
// run finishes what it started and starts nothing new until the weekly reset (state/winddown.go).
const (
	DefaultWeeklyReservePct = 2.0
	MaxWeeklyReservePct     = 50.0
)

// WeeklyReserve checks weekly_reserve_pct (the environment's value wins) and fills in the default: the
// percent of the weekly allowance the run leaves, at which it winds down, or 0 for no wind-down. A value
// out of bounds is an error.
func (c Config) WeeklyReserve() (float64, error) {
	const valid = "is not valid: use a percent up to 50, or 0 for no wind-down"
	f := DefaultWeeklyReservePct
	if c.WeeklyReservePct != nil {
		f = *c.WeeklyReservePct
	}
	if v := c.envWeeklyReserve; v != "" {
		var err error
		if f, err = strconv.ParseFloat(v, 64); err != nil {
			return DefaultWeeklyReservePct, fmt.Errorf("BATON_WEEKLY_RESERVE_PCT %q %s", v, valid)
		}
	}
	if math.IsNaN(f) || f < 0 || f > MaxWeeklyReservePct {
		return DefaultWeeklyReservePct, fmt.Errorf("weekly_reserve_pct %v %s", f, valid)
	}
	return f, nil
}
