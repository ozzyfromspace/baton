package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// models, escalate and escalate_after: the defaults leave Fable out and raise a phase two steps after three
// red runs; the environment wins over the file; a value out of bounds is an error, with the defaults.
func TestTiering(t *testing.T) {
	for _, c := range []struct {
		name, file string
		env        map[string]string
		want       Tiers
		err        string
	}{
		{"defaults", `{}`, nil, Tiers{Models: []string{"haiku", "sonnet", "opus"}, Steps: 2, After: 3}, ""},
		{"fable allowed, in the families' order", `{"models": ["fable", "Opus", "sonnet"]}`, nil, Tiers{Models: []string{"sonnet", "opus", "fable"}, Steps: 2, After: 3}, ""},
		{"no escalation", `{"escalate": 0, "escalate_after": 5}`, nil, Tiers{Models: []string{"haiku", "sonnet", "opus"}, Steps: 0, After: 5}, ""},
		{"the environment wins", `{"models": ["opus"], "escalate": 1}`, map[string]string{"BATON_MODELS": "haiku,opus", "BATON_ESCALATE": "3", "BATON_ESCALATE_AFTER": "1"},
			Tiers{Models: []string{"haiku", "opus"}, Steps: 3, After: 1}, ""},
		{"a model id is no family", `{"models": ["claude-opus-5-5"]}`, nil, Tiers{}, `models: "claude-opus-5-5" is not a model family`},
		{"too many steps", `{"escalate": 9}`, nil, Tiers{}, "escalate 9 is not valid"},
		{"after one red run, in the file", `{"escalate_after": 1}`, nil, Tiers{}, "escalate_after 1 is not valid"},
		{"a word for a number", `{}`, map[string]string{"BATON_ESCALATE": "lots"}, Tiers{}, `BATON_ESCALATE "lots" is not valid`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var cfg Config
			if err := json.Unmarshal([]byte(c.file), &cfg); err != nil {
				t.Fatal(err)
			}
			cfg.envModels, cfg.envEscalate, cfg.envEscalateAfter = c.env["BATON_MODELS"], c.env["BATON_ESCALATE"], c.env["BATON_ESCALATE_AFTER"]
			got, err := cfg.Tiering()
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				if !reflect.DeepEqual(got, Tiers{Models: DefaultModels, Steps: DefaultEscalateSteps, After: DefaultEscalateAfter}) {
					t.Errorf("a bad value gave %+v, not the defaults", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
	tiers := Tiers{Models: []string{"sonnet", "opus", "fable"}}
	tiers.Steps, tiers.After = 1, 4
	if env := tiers.Env(); !reflect.DeepEqual(env, []string{"BATON_MODELS=sonnet,opus,fable", "BATON_ESCALATE=1", "BATON_ESCALATE_AFTER=4"}) {
		t.Errorf("env %v", env)
	}
	passed := map[string]string{}
	for _, kv := range tiers.Env() {
		k, v, _ := strings.Cut(kv, "=")
		passed[k] = v
	}
	if got := TiersFromEnv(func(k string) string { return passed[k] }); !reflect.DeepEqual(got, tiers) {
		t.Errorf("passed down and read back: %+v", got)
	}
	read := func(v string) func(string) string { return func(string) string { return v } }
	if got := ModelsFromEnv(read("sonnet,opus,fable")); !reflect.DeepEqual(got, tiers.Models) || !tiers.Allows("fable") || (Tiers{Models: DefaultModels}).Allows("fable") {
		t.Errorf("from the environment: %v", got)
	}
	if got := ModelsFromEnv(read("")); !reflect.DeepEqual(got, DefaultModels) {
		t.Errorf("none passed down: %v", got)
	}
	if got := ModelsFromEnv(read("gpt")); !reflect.DeepEqual(got, DefaultModels) {
		t.Errorf("a bad value passed down: %v", got)
	}
}
