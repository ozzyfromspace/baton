// Package config reads baton's user settings from ~/.baton/config.json ($BATON_HOME/config.json).
// Environment variables override the file, so a single session can be tuned without editing it. baton
// never edits Claude Code's own settings files.
package config

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
}

// Defaults.
const (
	DefaultAutocompact   = "810k"
	DefaultCheckpointPct = 60.0
	DefaultWarnPct       = 90.0
	DefaultNtfyServer    = "https://ntfy.sh"
)

// Path is where the config file lives, under baton's root directory (~/.baton, or $BATON_HOME).
func Path(root string) string { return filepath.Join(root, "config.json") }

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

// DesktopOn reports whether desktop notifications are enabled.
func (c Config) DesktopOn() bool { return c.Desktop == nil || *c.Desktop }
