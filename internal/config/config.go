// Package config reads baton's user settings from ~/.baton/config.json ($BATON_HOME/config.json).
// Environment variables override the file, so a single session can be tuned without editing it. baton
// never edits Claude Code's own settings files.
package config

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is ~/.baton/config.json.
type Config struct {
	// NtfyTopic enables push notifications through ntfy (BATON_NTFY_TOPIC). Treat it like a password.
	NtfyTopic string `json:"ntfy_topic,omitempty"`
	// NtfyServer defaults to https://ntfy.sh (BATON_NTFY_SERVER).
	NtfyServer string `json:"ntfy_server,omitempty"`
	// Desktop notifications are on unless set to false (BATON_NOTIFY_DESKTOP=0).
	Desktop *bool `json:"desktop,omitempty"`
	// Details puts the reason text into pushes; off by default so pushes are content-free.
	Details bool `json:"details,omitempty"`
	// Autocompact is the launch-time --autocompact cap, e.g. "810k"; "off" leaves it to Claude Code's own
	// settings (BATON_AUTOCOMPACT).
	Autocompact string `json:"autocompact,omitempty"`
	// CheckpointPct is the context fill (percent of the compaction window) at which baton asks the model
	// to checkpoint at its next safe point (BATON_CHECKPOINT_PCT). 0 disables the nudge.
	CheckpointPct *float64 `json:"checkpoint_pct,omitempty"`
	// WarnPct is the context fill (percent of the compaction window, but never under 200k tokens) at which
	// baton asks the human, through AskUserQuestion, whether to checkpoint (BATON_WARN_PCT). 0 disables it.
	WarnPct *float64 `json:"warn_pct,omitempty"`
	// EscalationTimeout is how long a proposal waits for the human before baton goes ahead with it: a
	// duration from 1m to 2h, "5m" by default (BATON_ESCALATION_TIMEOUT, which may go down to 10s).
	EscalationTimeout string `json:"escalation_timeout,omitempty"`
	// MaxAutoDecisions is how many decisions a phase may make without the human (notes, and proposals
	// nobody answered) before baton stops for them to review; 5 by default, 0 for no limit
	// (BATON_MAX_AUTO_DECISIONS).
	MaxAutoDecisions *int `json:"max_auto_decisions,omitempty"`
	// AutoRestart moves the sessions baton hosts onto a newer compatible baton by themselves once it is
	// installed (each restarts at a safe point, resuming the same conversation). On unless set to false
	// (BATON_AUTO_RESTART=0).
	AutoRestart *bool `json:"auto_restart,omitempty"`

	// The environment's values for the two above, which Escalation checks against their own bounds.
	envTimeout, envMaxAuto string
}

// Defaults.
const (
	DefaultAutocompact   = "810k"
	DefaultCheckpointPct = 60.0
	DefaultWarnPct       = 90.0
	DefaultNtfyServer    = "https://ntfy.sh"

	DefaultEscalationTimeout = 5 * time.Minute
	DefaultMaxAutoDecisions  = 5
)

// Bounds for escalation_timeout. A proposal that goes ahead within seconds gives nobody a chance to
// veto it, so only the environment, for tests, may go below a minute.
const (
	MinEscalationTimeout    = time.Minute
	MaxEscalationTimeout    = 2 * time.Hour
	MinEnvEscalationTimeout = 10 * time.Second
)

// Path is where the config file lives, under baton's root directory (~/.baton, or $BATON_HOME).
func Path(root string) string { return filepath.Join(root, "config.json") }

// Root is baton's root directory: $BATON_HOME, or ~/.baton.
func Root(env func(string) string) string {
	if r := env("BATON_HOME"); r != "" {
		return r
	}
	home := env("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".baton")
}

// Load reads the config file (a missing or unreadable file is an empty config) and applies the
// environment overrides and defaults.
func Load(root string, env func(string) string) Config {
	var c Config
	if b, err := os.ReadFile(Path(root)); err == nil {
		json.Unmarshal(b, &c)
	}
	if v := env("BATON_NTFY_TOPIC"); v != "" {
		c.NtfyTopic = v
	}
	if v := env("BATON_NTFY_SERVER"); v != "" {
		c.NtfyServer = v
	}
	if v := env("BATON_NOTIFY_DESKTOP"); v != "" {
		on := v != "0" && v != "false"
		c.Desktop = &on
	}
	if v := env("BATON_AUTO_RESTART"); v != "" {
		on := v != "0" && v != "false"
		c.AutoRestart = &on
	}
	if v := env("BATON_AUTOCOMPACT"); v != "" {
		c.Autocompact = v
	}
	if v := env("BATON_CHECKPOINT_PCT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.CheckpointPct = &f
		}
	}
	if v := env("BATON_WARN_PCT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.WarnPct = &f
		}
	}
	c.envTimeout, c.envMaxAuto = env("BATON_ESCALATION_TIMEOUT"), env("BATON_MAX_AUTO_DECISIONS")
	if c.NtfyServer == "" {
		c.NtfyServer = DefaultNtfyServer
	}
	if c.Autocompact == "" {
		c.Autocompact = DefaultAutocompact
	}
	if c.Autocompact == "off" {
		c.Autocompact = ""
	}
	if c.CheckpointPct == nil {
		d := DefaultCheckpointPct
		c.CheckpointPct = &d
	}
	if c.WarnPct == nil {
		d := DefaultWarnPct
		c.WarnPct = &d
	}
	return c
}

// Bounds Claude Code accepts for --autocompact (2.1.289); it refuses to start with anything else.
const (
	MinAutocompact = 100_000
	MaxAutocompact = 1_000_000
)

// AutocompactTokens reads an --autocompact value the way Claude Code does: "810k", "1m", "810000", or
// "810" as shorthand for 810k. "auto" (the window Claude Code picks for the model) is valid but has no
// size baton can know, so it gives 0 tokens. ok is false for anything claude would refuse.
func AutocompactTokens(v string) (tokens int, ok bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "auto" {
		return 0, true
	}
	var f float64
	var err error
	switch {
	case strings.HasSuffix(v, "m"):
		f, err = strconv.ParseFloat(strings.TrimSuffix(v, "m"), 64)
		f *= 1e6
	case strings.HasSuffix(v, "k"):
		f, err = strconv.ParseFloat(strings.TrimSuffix(v, "k"), 64)
		f *= 1e3
	default:
		f, err = strconv.ParseFloat(v, 64)
		if f >= 100 && f <= 1000 {
			f *= 1e3
		}
	}
	if err != nil || math.IsNaN(f) || f < MinAutocompact || f > MaxAutocompact {
		return 0, false
	}
	return int(math.Round(f)), true
}

// Escalation is how baton treats the decisions a run makes without the human.
type Escalation struct {
	Timeout time.Duration // how long a proposal waits for the human before baton goes ahead with it
	MaxAuto int           // decisions without the human a phase may make before a review; 0: no limit
}

// Escalation checks escalation_timeout and max_auto_decisions (the environment's values win) and fills
// in the defaults. A value out of bounds is an error, so baton refuses to start rather than run on a
// setting the human did not choose.
func (c Config) Escalation() (Escalation, error) {
	e := Escalation{Timeout: DefaultEscalationTimeout, MaxAuto: DefaultMaxAutoDecisions}
	for _, v := range []struct {
		name, value string
		min         time.Duration
	}{{"escalation_timeout", c.EscalationTimeout, MinEscalationTimeout}, {"BATON_ESCALATION_TIMEOUT", c.envTimeout, MinEnvEscalationTimeout}} {
		if v.value == "" {
			continue
		}
		d, err := time.ParseDuration(v.value)
		if err != nil || d < v.min || d > MaxEscalationTimeout {
			return e, fmt.Errorf("%s %q is not valid: use a duration from %s to %s, such as \"5m\"", v.name, v.value, short(v.min), short(MaxEscalationTimeout))
		}
		e.Timeout = d
	}
	if n := c.MaxAutoDecisions; n != nil {
		if *n < 0 {
			return e, fmt.Errorf("max_auto_decisions %d is not valid: use 0 (no limit) or more", *n)
		}
		e.MaxAuto = *n
	}
	if v := c.envMaxAuto; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return e, fmt.Errorf("BATON_MAX_AUTO_DECISIONS %q is not valid: use 0 (no limit) or more", v)
		}
		e.MaxAuto = n
	}
	return e, nil
}

// Env is the environment the host gives the session, so the CLI, the hooks and the host agree.
func (e Escalation) Env() []string {
	return []string{"BATON_ESCALATION_TIMEOUT=" + e.Timeout.String(), "BATON_MAX_AUTO_DECISIONS=" + strconv.Itoa(e.MaxAuto)}
}

// EscalationFromEnv reads the settings the host passed down; anything missing takes its default.
func EscalationFromEnv(env func(string) string) Escalation {
	e := Escalation{Timeout: DefaultEscalationTimeout, MaxAuto: DefaultMaxAutoDecisions}
	if d, err := time.ParseDuration(env("BATON_ESCALATION_TIMEOUT")); err == nil && d > 0 {
		e.Timeout = d
	}
	if n, err := strconv.Atoi(env("BATON_MAX_AUTO_DECISIONS")); err == nil && n >= 0 {
		e.MaxAuto = n
	}
	return e
}

// short prints a whole number of hours or minutes without the zero fields: 2h, 1m, 10s.
func short(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// DesktopOn reports whether desktop notifications are enabled.
func (c Config) DesktopOn() bool { return c.Desktop == nil || *c.Desktop }

// Restarts reports whether hosted sessions restart on a newer baton by themselves.
func (c Config) Restarts() bool { return c.AutoRestart == nil || *c.AutoRestart }
