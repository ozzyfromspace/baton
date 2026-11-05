package config

import (
	"encoding/json"
	"testing"
)

// task_review is on unless the file or the environment turns it off, and review_model is sonnet unless either
// names another; the environment wins both, and Load reads both from the file.
func TestTaskReviewSettings(t *testing.T) {
	no, yes := false, true
	for _, c := range []struct {
		name          string
		file          Config
		env, envModel string
		on            bool
		model         string
	}{
		{"defaults", Config{}, "", "", true, "sonnet"},
		{"off in the file", Config{TaskReview: &no}, "", "", false, "sonnet"},
		{"off by the environment", Config{TaskReview: &yes}, "0", "", false, "sonnet"},
		{"on by the environment", Config{TaskReview: &no}, "1", "", true, "sonnet"},
		{"nonsense", Config{}, "nonsense", "", true, "sonnet"},
		{"a model in the file", Config{ReviewModel: " opus "}, "", "", true, "opus"},
		{"a model by the environment", Config{ReviewModel: "opus"}, "", "haiku", true, "haiku"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := func(k string) string {
				switch k {
				case "BATON_TASK_REVIEW":
					return c.env
				case "BATON_REVIEW_MODEL":
					return c.envModel
				}
				return ""
			}
			if got := c.file.ReviewsTasks(env); got != c.on {
				t.Errorf("task_review %v, want %v", got, c.on)
			}
			if got := c.file.ReviewModelFor(env); got != c.model {
				t.Errorf("review_model %q, want %q", got, c.model)
			}
		})
	}
	var c Config
	none := func(string) string { return "" }
	if err := json.Unmarshal([]byte(`{"task_review": false, "review_model": "opus"}`), &c); err != nil || c.ReviewsTasks(none) || c.ReviewModelFor(none) != "opus" {
		t.Errorf("the file's review settings: %+v, %v", c, err)
	}
}
