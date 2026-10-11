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
	// VerifyTimeout is how long a job baton runs for the session (a gate in a lane, or a wait) may run
	// before baton stops it and counts it as failed: a duration from 1m to 24h, "3h" by default
	// (BATON_VERIFY_TIMEOUT, which may go down to 1s).
	VerifyTimeout string `json:"verify_timeout,omitempty"`
	// SwitchAfter is how long the phase in hand waits on its job before baton sets it aside for another
	// phase that can go on, if there is one: a duration from 1m to 2h, "5m" by default
	// (BATON_SWITCH_AFTER, which may go down to 1s). A command whose earlier runs took longer than that
	// switches at once.
	SwitchAfter string `json:"switch_after,omitempty"`
	// ForegroundMax is how long a command may run in the foreground before Claude Code moves it to the
	// background, where it keeps running and wakes the session when it ends: a duration from 10s to 10m,
	// "2m" by default, or "off" to leave it to Claude Code (BATON_FOREGROUND_MAX, which may go down to 1s).
	// baton passes it to claude as CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS, unless that is set already.
	ForegroundMax string `json:"foreground_max,omitempty"`
	// Workers is how many hidden worker sessions may work on other phases of a plan beside the visible one,
	// each in a git worktree of its own: 0 to 3, 1 by default; 0 runs one phase at a time
	// (BATON_WORKERS).
	Workers *int `json:"workers,omitempty"`
	// WorkerUsageMax is how much of the account's five-hour usage allowance, in percent, may be used before
	// baton starts no new worker: up to 100, 80 by default; 0 turns the check off
	// (BATON_WORKER_USAGE_MAX).
	WorkerUsageMax *float64 `json:"worker_usage_max,omitempty"`

	// The environment's values for escalation_timeout, max_auto_decisions, verify_timeout, switch_after,
	// foreground_max, workers and worker_usage_max, which are checked against bounds of their own.
	envTimeout, envMaxAuto, envVerifyTimeout, envSwitchAfter, envForegroundMax string
	envWorkers, envUsageMax                                                    string
}

// Defaults.
const (
	DefaultAutocompact   = "810k"
	DefaultCheckpointPct = 60.0
	DefaultWarnPct       = 90.0
	DefaultNtfyServer    = "https://ntfy.sh"

	DefaultEscalationTimeout = 5 * time.Minute
	DefaultMaxAutoDecisions  = 5
	DefaultVerifyTimeout     = 3 * time.Hour
	DefaultSwitchAfter       = 5 * time.Minute
	DefaultForegroundMax     = 2 * time.Minute

	DefaultWorkers        = 1
	MaxWorkers            = 3
	DefaultWorkerUsageMax = 80.0
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
	c.envVerifyTimeout, c.envSwitchAfter = env("BATON_VERIFY_TIMEOUT"), env("BATON_SWITCH_AFTER")
	c.envForegroundMax = env("BATON_FOREGROUND_MAX")
	c.envWorkers, c.envUsageMax = env("BATON_WORKERS"), env("BATON_WORKER_USAGE_MAX")
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

// Bounds for verify_timeout, switch_after and foreground_max. Only the environment, for tests, may go lower.
// Claude Code runs a command in the foreground for at most 10 minutes whatever it is asked.
const (
	MinVerifyTimeout = time.Minute
	MaxVerifyTimeout = 24 * time.Hour
	MinSwitchAfter   = time.Minute
	MaxSwitchAfter   = 2 * time.Hour
	MinForegroundMax = 10 * time.Second
	MaxForegroundMax = 10 * time.Minute
	MinEnvDuration   = time.Second
)

// JobTimeout checks verify_timeout (the environment's value wins) and fills in the default. A value out of
// bounds is an error.
func (c Config) JobTimeout() (time.Duration, error) {
	return within(DefaultVerifyTimeout, MinVerifyTimeout, MaxVerifyTimeout, "3h", "verify_timeout", c.VerifyTimeout, c.envVerifyTimeout)
}

// SwitchWait checks switch_after (the environment's value wins) and fills in the default. A value out of
// bounds is an error.
func (c Config) SwitchWait() (time.Duration, error) {
	return within(DefaultSwitchAfter, MinSwitchAfter, MaxSwitchAfter, "5m", "switch_after", c.SwitchAfter, c.envSwitchAfter)
}

// ForegroundWait checks foreground_max (the environment's value wins) and fills in the default. 0 means
// off: Claude Code keeps its own. A value out of bounds is an error.
func (c Config) ForegroundWait() (time.Duration, error) {
	file, env := c.ForegroundMax, c.envForegroundMax
	if env == "off" || env == "" && file == "off" {
		return 0, nil
	}
	if file == "off" {
		file = "" // the environment's duration wins
	}
	return within(DefaultForegroundMax, MinForegroundMax, MaxForegroundMax, "2m", "foreground_max", file, env)
}

// within reads a duration setting: the file's value (at least min) or the environment's (at least
// MinEnvDuration), which wins, up to max; def when neither is set. A value out of bounds is an error, and
// gives def.
func within(def, min, max time.Duration, example, name, file, env string) (time.Duration, error) {
	d := def
	for _, v := range []struct {
		name, value string
		min         time.Duration
	}{{name, file, min}, {"BATON_" + strings.ToUpper(name), env, MinEnvDuration}} {
		if v.value == "" {
			continue
		}
		t, err := time.ParseDuration(v.value)
		if err != nil || t < v.min || t > max {
			return def, fmt.Errorf("%s %q is not valid: use a duration from %s to %s, such as \"%s\"", v.name, v.value, short(v.min), short(max), example)
		}
		d = t
	}
	return d, nil
}

// WorkerCount checks workers (the environment's value wins) and fills in the default. A value out of
// bounds is an error.
func (c Config) WorkerCount() (int, error) {
	n := DefaultWorkers
	if c.Workers != nil {
		n = *c.Workers
	}
	if v := c.envWorkers; v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil {
			return DefaultWorkers, fmt.Errorf("BATON_WORKERS %q is not valid: use a number from 0 to %d", v, MaxWorkers)
		}
	}
	if n < 0 || n > MaxWorkers {
		return DefaultWorkers, fmt.Errorf("workers %d is not valid: use a number from 0 to %d", n, MaxWorkers)
	}
	return n, nil
}

// WorkerUsage checks worker_usage_max (the environment's value wins) and fills in the default: the
// percent of the five-hour allowance above which no new worker starts, or 0 for no check. A value out of
// bounds is an error.
func (c Config) WorkerUsage() (float64, error) {
	f := DefaultWorkerUsageMax
	if c.WorkerUsageMax != nil {
		f = *c.WorkerUsageMax
	}
	if v := c.envUsageMax; v != "" {
		var err error
		if f, err = strconv.ParseFloat(v, 64); err != nil {
			return DefaultWorkerUsageMax, fmt.Errorf("BATON_WORKER_USAGE_MAX %q is not valid: use a percent up to 100, or 0 for no check", v)
		}
	}
	if math.IsNaN(f) || f < 0 || f > 100 {
		return DefaultWorkerUsageMax, fmt.Errorf("worker_usage_max %v is not valid: use a percent up to 100, or 0 for no check", f)
	}
	return f, nil
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
