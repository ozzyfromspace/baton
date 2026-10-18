package config

import (
	"encoding/json"
	"testing"
)

// advisor is on unless the file or the environment turns it off, and advisor_model is haiku unless either
// names another; the environment wins both, and Load reads both from the file.
func TestAdvisorSettings(t *testing.T) {
	no, yes := false, true
	for _, c := range []struct {
		name          string
		file          Config
		env, envModel string
		on            bool
		model         string
	}{
		{"defaults", Config{}, "", "", true, "haiku"},
		{"off in the file", Config{Advisor: &no}, "", "", false, "haiku"},
		{"on in the file", Config{Advisor: &yes}, "", "", true, "haiku"},
		{"off by the environment", Config{Advisor: &yes}, "0", "", false, "haiku"},
		{"on by the environment", Config{Advisor: &no}, "1", "", true, "haiku"},
		{"off, in words", Config{}, "off", "", false, "haiku"},
		{"nonsense", Config{}, "nonsense", "", true, "haiku"},
		{"a model in the file", Config{AdvisorModel: " sonnet "}, "", "", true, "sonnet"},
		{"a model by the environment", Config{AdvisorModel: "sonnet"}, "", "claude-haiku-4-5", true, "claude-haiku-4-5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := func(k string) string {
				switch k {
				case "BATON_ADVISOR":
					return c.env
				case "BATON_ADVISOR_MODEL":
					return c.envModel
				}
				return ""
			}
			if got := c.file.Advises(env); got != c.on {
				t.Errorf("advisor %v, want %v", got, c.on)
			}
			if got := c.file.AdvisorModelFor(env); got != c.model {
				t.Errorf("advisor_model %q, want %q", got, c.model)
			}
		})
	}
	var c Config
	if err := json.Unmarshal([]byte(`{"advisor": false, "advisor_model": "opus"}`), &c); err != nil || c.Advises(func(string) string { return "" }) ||
		c.AdvisorModelFor(func(string) string { return "" }) != "opus" {
		t.Errorf("the file's advisor settings: %+v, %v", c, err)
	}
}
