package state

import "time"

// Fact is one thing the gates' environment needs, as a model found it while it ran the plan (baton env):
// a port to leave free, a service to start first. Every later brief lists the run's facts, so no phase finds
// the same thing again.
type Fact struct {
	Text  string    `json:"text"`
	Phase string    `json:"phase,omitempty"` // the phase that found it
	At    time.Time `json:"at"`
}
