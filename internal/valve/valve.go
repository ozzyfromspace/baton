// Package valve works out the context sizes baton's mid-phase valves act on.
//
// Claude Code reports the model's context window and the size of the last request through the status
// line input; it does not report the --autocompact cap, but baton launched claude with it, so it knows.
// From those (Claude Code 2.1.289):
//
//   - the compaction window is the model's window, or the cap when that is smaller;
//   - Claude Code compacts on its own 33k tokens below it (up to 20k reserved for output, plus a 13k
//     buffer), so an 810k cap compacts at about 777k;
//   - baton tells the model where its context stands, asking nothing, at NoticePct of the compaction
//     window;
//   - baton asks the model to checkpoint at CheckpointPct of the compaction window;
//   - baton asks the human, through AskUserQuestion, at WarnPct of the compaction window or 200k
//     tokens, whichever is larger.
//
// When any of these lines would come after Claude Code has already compacted (a small window, or a
// percentage near 100), every line is that percentage of the point where Claude Code compacts instead,
// so each still fires, and in order.
package valve

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
)

// AutoCompactMargin is how far below the compaction window Claude Code compacts on its own.
const AutoCompactMargin = 33_000

// WarnFloor is the smallest context baton asks the human about, unless Claude Code would compact first.
const WarnFloor = 200_000

// Settings are the inputs that do not change during a session.
type Settings struct {
	Cap           int     // the --autocompact cap in tokens; 0 when off, "auto", or unknown
	NoticePct     float64 // 0: never tell
	CheckpointPct float64 // 0: never nudge
	WarnPct       float64 // 0: never warn
	WarnTokens    int     // a fixed warning line that replaces the computed one (tests)
	// WarnTimeout is how long the context question waits for the human before baton answers it
	// (0: the default). The host answers; the question says when.
	WarnTimeout time.Duration
}

// DefaultWarnTimeout is how long the context question waits for the human by default.
const DefaultWarnTimeout = 10 * time.Minute

// Limits are the context sizes, in tokens, for one model window. A zero threshold is off.
type Limits struct {
	Window     int // the compaction window
	AutoAt     int // where Claude Code compacts on its own
	Notice     int // where baton tells the model where its context stands, asking nothing
	Checkpoint int // where baton asks the model to checkpoint at its next safe point
	Warn       int // where baton asks the human
}

// Limits computes the thresholds for a model whose context window is modelWindow tokens (0: unknown).
func (s Settings) Limits(modelWindow int) Limits {
	w := modelWindow
	if s.Cap > 0 && (w <= 0 || s.Cap < w) {
		w = s.Cap
	}
	if w <= 0 {
		return Limits{}
	}
	at := w - AutoCompactMargin
	l := Limits{Window: w, AutoAt: at}
	// The lines are percentages of the window, the warning at least WarnFloor, unless one of them would
	// come at or after Claude Code's own compaction: then all are percentages of where it compacts.
	base, moved := w, false
	for _, line := range []int{pct(w, s.NoticePct), pct(w, s.CheckpointPct), s.warnLine(w)} {
		if line > 0 && line >= at {
			base, moved = at, true
		}
	}
	if s.NoticePct > 0 {
		l.Notice = pct(base, s.NoticePct)
	}
	if s.CheckpointPct > 0 {
		l.Checkpoint = pct(base, s.CheckpointPct)
	}
	switch {
	case s.WarnTokens > 0:
		l.Warn = s.WarnTokens
	case s.WarnPct > 0 && moved:
		l.Warn = pct(at, s.WarnPct)
	case s.WarnPct > 0:
		l.Warn = s.warnLine(w)
	}
	return l
}

// warnLine is where the warning comes on a window of w tokens, before any move: WarnPct of it, and at least
// WarnFloor. A fixed line (WarnTokens) never moves, and is none.
func (s Settings) warnLine(w int) int {
	if s.WarnTokens > 0 || s.WarnPct <= 0 {
		return 0
	}
	return max(pct(w, s.WarnPct), WarnFloor)
}

func pct(n int, p float64) int { return int(float64(n) * p / 100) }

// Env is the environment the host gives the session, so hooks and the status line work from the same
// settings as the host.
func (s Settings) Env() []string {
	env := []string{
		"BATON_COMPACT_CAP=" + strconv.Itoa(s.Cap),
		fmt.Sprintf("BATON_NOTICE_PCT=%g", s.NoticePct),
		fmt.Sprintf("BATON_CHECKPOINT_PCT=%g", s.CheckpointPct),
		fmt.Sprintf("BATON_WARN_PCT=%g", s.WarnPct),
	}
	if s.WarnTokens > 0 {
		env = append(env, "BATON_WARN_TOKENS="+strconv.Itoa(s.WarnTokens))
	}
	if s.WarnTimeout > 0 {
		env = append(env, "BATON_WARN_TIMEOUT="+s.WarnTimeout.String())
	}
	return env
}

// FromEnv reads the settings the host passed down; anything missing takes its default.
func FromEnv(env func(string) string) Settings {
	s := Settings{NoticePct: config.DefaultNoticePct, CheckpointPct: config.DefaultCheckpointPct, WarnPct: config.DefaultWarnPct}
	if n, err := strconv.Atoi(env("BATON_COMPACT_CAP")); err == nil && n > 0 {
		s.Cap = n
	}
	if f, err := strconv.ParseFloat(env("BATON_NOTICE_PCT"), 64); err == nil {
		s.NoticePct = f
	}
	if f, err := strconv.ParseFloat(env("BATON_CHECKPOINT_PCT"), 64); err == nil {
		s.CheckpointPct = f
	}
	if f, err := strconv.ParseFloat(env("BATON_WARN_PCT"), 64); err == nil {
		s.WarnPct = f
	}
	if n, err := strconv.Atoi(env("BATON_WARN_TOKENS")); err == nil && n > 0 {
		s.WarnTokens = n
	}
	if d, err := time.ParseDuration(env("BATON_WARN_TIMEOUT")); err == nil && d > 0 {
		s.WarnTimeout = d
	}
	return s
}

// Tokens formats a token count the way people say it: 810k, 1M, 1.5M, 950.
func Tokens(n int) string {
	switch {
	case n >= 1_000_000 && n%100_000 == 0:
		return strconv.FormatFloat(float64(n)/1e6, 'f', -1, 64) + "M"
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	default:
		return strconv.Itoa(n)
	}
}
