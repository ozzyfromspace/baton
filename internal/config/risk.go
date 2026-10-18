package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Risk is the risk ledger's settings (package risk): risk, on unless set to false (BATON_RISK=0), and its
// two lines in points, risk_review (BATON_RISK_REVIEW), where baton asks for an assessment of the work no
// full check has passed yet, and risk_full (BATON_RISK_FULL), where it asks again whatever the last one
// said. Read apart from Config, from the same file, with the environment's values winning.
type Risk struct {
	On     bool
	Review int
	Full   int
}

// The default lines, calibrated by a replay of two real runs (internal/risk's replay test).
const (
	DefaultRiskReview = 24
	DefaultRiskFull   = 40
)

// riskFile is the risk ledger's part of the config file.
type riskFile struct {
	Risk   *bool `json:"risk"`
	Review *int  `json:"risk_review"`
	Full   *int  `json:"risk_full"`
}

// LoadRisk reads the risk ledger's settings from the config file under root and the environment, and
// checks them: each line at least 1 point, the hard line above the review line. A value out of bounds is
// an error, and gives the defaults.
func LoadRisk(root string, env func(string) string) (Risk, error) {
	r := Risk{On: true, Review: DefaultRiskReview, Full: DefaultRiskFull}
	var f riskFile
	if b, err := os.ReadFile(Path(root)); err == nil {
		json.Unmarshal(b, &f)
	}
	if f.Risk != nil {
		r.On = *f.Risk
	}
	switch strings.ToLower(strings.TrimSpace(env("BATON_RISK"))) {
	case "0", "false", "off", "no":
		r.On = false
	case "1", "true", "on", "yes":
		r.On = true
	}
	def := Risk{On: r.On, Review: DefaultRiskReview, Full: DefaultRiskFull}
	if f.Review != nil {
		r.Review = *f.Review
	}
	if f.Full != nil {
		r.Full = *f.Full
	}
	for _, v := range []struct {
		name string
		to   *int
	}{{"BATON_RISK_REVIEW", &r.Review}, {"BATON_RISK_FULL", &r.Full}} {
		s := strings.TrimSpace(env(v.name))
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return def, fmt.Errorf("%s %q is not valid: use a whole number of points", v.name, s)
		}
		*v.to = n
	}
	switch {
	case r.Review < 1:
		return def, fmt.Errorf("risk_review %d is not valid: use 1 point or more", r.Review)
	case r.Full <= r.Review:
		return def, fmt.Errorf("risk_full %d is not valid: use more points than risk_review (%d)", r.Full, r.Review)
	}
	return r, nil
}

// String is the settings as baton setup shows them.
func (r Risk) String() string {
	if !r.On {
		return "off"
	}
	return fmt.Sprintf("on: an assessment at %d points since the last full check, again at %d", r.Review, r.Full)
}
