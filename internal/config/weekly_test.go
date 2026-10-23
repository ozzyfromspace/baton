package config

import (
	"os"
	"strings"
	"testing"
)

// weekly_reserve_pct: 2 by default, 0 turns the wind-down off, at most 50, and the environment wins.
func TestWeeklyReserve(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		name, file string
		env        map[string]string
		want       float64
		err        string
	}{
		{"the default", `{}`, nil, 2, ""},
		{"in the file", `{"weekly_reserve_pct":5}`, nil, 5, ""},
		{"a decimal", `{"weekly_reserve_pct":1.5}`, nil, 1.5, ""},
		{"off", `{"weekly_reserve_pct":0}`, nil, 0, ""},
		{"the most", `{"weekly_reserve_pct":50}`, nil, 50, ""},
		{"env wins", `{"weekly_reserve_pct":5}`, map[string]string{"BATON_WEEKLY_RESERVE_PCT": "0.5"}, 0.5, ""},
		{"off in the env", `{}`, map[string]string{"BATON_WEEKLY_RESERVE_PCT": "0"}, 0, ""},
		{"over 50", `{"weekly_reserve_pct":51}`, nil, 2, "weekly_reserve_pct 51 is not valid"},
		{"negative", `{}`, map[string]string{"BATON_WEEKLY_RESERVE_PCT": "-1"}, 2, "weekly_reserve_pct -1 is not valid"},
		{"not a number", `{}`, map[string]string{"BATON_WEEKLY_RESERVE_PCT": "some"}, 2, `BATON_WEEKLY_RESERVE_PCT "some" is not valid`},
	} {
		os.WriteFile(Path(root), []byte(c.file), 0o644)
		got, err := Load(root, func(k string) string { return c.env[k] }).WeeklyReserve()
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: %v, want an error mentioning %q", c.name, err, c.err)
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case got != c.want:
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
