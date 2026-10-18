package config

import (
	"os"
	"strings"
	"testing"
)

// The risk ledger's settings: on by default with the calibrated lines; the file sets them, the environment
// wins, and a line out of bounds is an error that gives the defaults.
func TestLoadRisk(t *testing.T) {
	def := Risk{On: true, Review: DefaultRiskReview, Full: DefaultRiskFull, Model: DefaultRiskModel}
	for _, c := range []struct {
		name, file string
		env        map[string]string
		want       Risk
		err        string
	}{
		{"defaults", "", nil, def, ""},
		{"the file", `{"risk":true,"risk_review":10,"risk_full":30,"risk_model":"sonnet"}`, nil, Risk{On: true, Review: 10, Full: 30, Model: "sonnet"}, ""},
		{"off in the file", `{"risk":false}`, nil, Risk{Review: DefaultRiskReview, Full: DefaultRiskFull, Model: DefaultRiskModel}, ""},
		{"the environment wins", `{"risk":false,"risk_review":10,"risk_full":30,"risk_model":"sonnet"}`,
			map[string]string{"BATON_RISK": "1", "BATON_RISK_REVIEW": "12", "BATON_RISK_FULL": "50", "BATON_RISK_MODEL": " claude-opus-5-5 "},
			Risk{On: true, Review: 12, Full: 50, Model: "claude-opus-5-5"}, ""},
		{"off by the environment", "", map[string]string{"BATON_RISK": "0"}, Risk{Review: DefaultRiskReview, Full: DefaultRiskFull, Model: DefaultRiskModel}, ""},
		{"no assessment in the file", `{"risk_model":"off"}`, nil, Risk{On: true, Review: DefaultRiskReview, Full: DefaultRiskFull}, ""},
		{"no assessment by the environment", `{"risk_model":"haiku"}`, map[string]string{"BATON_RISK_MODEL": "none"},
			Risk{On: true, Review: DefaultRiskReview, Full: DefaultRiskFull}, ""},
		{"not a number", "", map[string]string{"BATON_RISK_FULL": "lots"}, def, "BATON_RISK_FULL"},
		{"no review line", `{"risk_review":0}`, nil, def, "risk_review"},
		{"the hard line under the review line", `{"risk_review":30,"risk_full":30}`, nil, def, "risk_full"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if c.file != "" {
				os.WriteFile(Path(root), []byte(c.file), 0o644)
			}
			got, err := LoadRisk(root, func(k string) string { return c.env[k] })
			if got != c.want || (err == nil) != (c.err == "") || err != nil && !strings.Contains(err.Error(), c.err) {
				t.Errorf("LoadRisk = %+v, %v; want %+v, an error naming %q", got, err, c.want, c.err)
			}
		})
	}
	if s := (Risk{}).String(); s != "off" {
		t.Errorf("off: %q", s)
	}
	if s := def.String(); !strings.Contains(s, "an assessment by opus at 56 points") {
		t.Errorf("on: %q", s)
	}
	if s := (Risk{On: true, Review: 56, Full: 90}).String(); !strings.Contains(s, "no assessment") {
		t.Errorf("no assessment: %q", s)
	}
}
