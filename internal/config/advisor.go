package config

import "strings"

// DefaultAdvisorModel is the advisor's model when advisor_model is not set.
const DefaultAdvisorModel = "haiku"

// Advises reports advisor: the environment's BATON_ADVISOR (0, false, off; or 1, true, on) wins, then the
// file, then on.
func (c Config) Advises(env func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(env("BATON_ADVISOR"))) {
	case "0", "false", "off", "no":
		return false
	case "1", "true", "on", "yes":
		return true
	}
	return c.Advisor == nil || *c.Advisor
}

// AdvisorModelFor reports advisor_model: the environment's BATON_ADVISOR_MODEL wins, then the file, then
// DefaultAdvisorModel.
func (c Config) AdvisorModelFor(env func(string) string) string {
	if m := strings.TrimSpace(env("BATON_ADVISOR_MODEL")); m != "" {
		return m
	}
	if m := strings.TrimSpace(c.AdvisorModel); m != "" {
		return m
	}
	return DefaultAdvisorModel
}
