// Package config reads baton's user settings from ~/.baton/config.json. Environment variables override
// the file, so a single session can be tuned without editing it. baton never edits Claude Code's own
// settings files.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
	// Autocompact is the launch-time --autocompact cap, e.g. "400k"; "off" disables it (BATON_AUTOCOMPACT).
	Autocompact string `json:"autocompact,omitempty"`
	// CheckpointPct is the context fill (percent of the compaction window) at which baton asks the model
	// to checkpoint at its next safe point (BATON_CHECKPOINT_PCT). 0 disables the nudge.
	CheckpointPct *float64 `json:"checkpoint_pct,omitempty"`
}

// Defaults.
const (
	DefaultAutocompact   = "400k"
	DefaultCheckpointPct = 60.0
	DefaultNtfyServer    = "https://ntfy.sh"
)

// Path is where the config file lives for a home directory.
func Path(home string) string { return filepath.Join(home, ".baton", "config.json") }

// Load reads the config file (a missing or unreadable file is an empty config) and applies the
// environment overrides and defaults.
func Load(home string, env func(string) string) Config {
	var c Config
	if b, err := os.ReadFile(Path(home)); err == nil {
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
	return c
}

// DesktopOn reports whether desktop notifications are enabled.
func (c Config) DesktopOn() bool { return c.Desktop == nil || *c.Desktop }
