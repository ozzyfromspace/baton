package valve

import (
	"strings"
	"testing"
	"time"
)

func TestLimits(t *testing.T) {
	def := Settings{Cap: 810_000, NoticePct: 60, CheckpointPct: 80, WarnPct: 90}
	for _, c := range []struct {
		name   string
		s      Settings
		window int
		want   Limits
	}{
		// The default on a 1M model: a notice at 60% of the cap, the checkpoint at 80%, the human asked at 90%,
		// Claude Code compacts at 777k.
		{"1M model, 810k cap", def, 1_000_000, Limits{Window: 810_000, AutoAt: 777_000, Notice: 486_000, Checkpoint: 648_000, Warn: 729_000}},
		// A 200k model under the same cap: the window is the model's, and 90% of it (or the 200k floor)
		// would come after Claude Code compacts at 167k, so every line is a percentage of 167k.
		{"200k model, 810k cap", def, 200_000, Limits{Window: 200_000, AutoAt: 167_000, Notice: 100_200, Checkpoint: 133_600, Warn: 150_300}},
		// No cap: the model's window.
		{"1M model, no cap", Settings{NoticePct: 60, CheckpointPct: 80, WarnPct: 90}, 1_000_000, Limits{Window: 1_000_000, AutoAt: 967_000, Notice: 600_000, Checkpoint: 800_000, Warn: 900_000}},
		// A lower warning percentage: 50% of 300k is under the 200k floor, so the floor applies.
		{"floor", Settings{Cap: 300_000, CheckpointPct: 40, WarnPct: 50}, 1_000_000, Limits{Window: 300_000, AutoAt: 267_000, Checkpoint: 120_000, Warn: 200_000}},
		// Small caps: the warning (90%, or the floor) would come after Claude Code compacts, so every line moves before.
		{"90% past auto-compaction", Settings{Cap: 250_000, NoticePct: 60, CheckpointPct: 80, WarnPct: 90}, 1_000_000, Limits{Window: 250_000, AutoAt: 217_000, Notice: 130_200, Checkpoint: 173_600, Warn: 195_300}},
		{"floor past auto-compaction", Settings{Cap: 220_000, NoticePct: 60, CheckpointPct: 80, WarnPct: 90}, 1_000_000, Limits{Window: 220_000, AutoAt: 187_000, Notice: 112_200, Checkpoint: 149_600, Warn: 168_300}},
		// With no warning, the checkpoint alone past auto-compaction moves the lines.
		{"checkpoint past auto-compaction", Settings{Cap: 150_000, NoticePct: 60, CheckpointPct: 80}, 1_000_000, Limits{Window: 150_000, AutoAt: 117_000, Notice: 70_200, Checkpoint: 93_600}},
		// A percentage near 100 moves every line on any window.
		{"checkpoint near 100%", Settings{Cap: 810_000, NoticePct: 60, CheckpointPct: 99, WarnPct: 90}, 1_000_000, Limits{Window: 810_000, AutoAt: 777_000, Notice: 466_200, Checkpoint: 769_230, Warn: 699_300}},
		{"window not reported yet", def, 0, Limits{Window: 810_000, AutoAt: 777_000, Notice: 486_000, Checkpoint: 648_000, Warn: 729_000}},
		{"no cap, no window", Settings{NoticePct: 60, CheckpointPct: 80, WarnPct: 90}, 0, Limits{}},
		{"notice off", Settings{Cap: 810_000, CheckpointPct: 80, WarnPct: 90}, 1_000_000, Limits{Window: 810_000, AutoAt: 777_000, Checkpoint: 648_000, Warn: 729_000}},
		{"all off", Settings{Cap: 810_000}, 1_000_000, Limits{Window: 810_000, AutoAt: 777_000}},
		{"test override", Settings{Cap: 810_000, WarnPct: 90, WarnTokens: 5_000}, 200_000, Limits{Window: 200_000, AutoAt: 167_000, Warn: 5_000}},
	} {
		if got := c.s.Limits(c.window); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestEnvRoundTrip(t *testing.T) {
	s := Settings{Cap: 810_000, NoticePct: 45, CheckpointPct: 55, WarnPct: 0, WarnTokens: 12, WarnTimeout: 45 * time.Second}
	m := map[string]string{}
	for _, kv := range s.Env() {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	if got := FromEnv(func(k string) string { return m[k] }); got != s {
		t.Fatalf("%+v, want %+v", got, s)
	}
	if got := FromEnv(func(string) string { return "" }); got != (Settings{NoticePct: 60, CheckpointPct: 80, WarnPct: 90}) {
		t.Fatalf("defaults: %+v", got)
	}
}

func TestTokens(t *testing.T) {
	for n, want := range map[int]string{950: "950", 1_000: "1k", 36_176: "36k", 729_000: "729k", 999_499: "999k", 1_000_000: "1M", 1_500_000: "1.5M", 1_234_567: "1.23M"} {
		if got := Tokens(n); got != want {
			t.Errorf("%d: %s, want %s", n, got, want)
		}
	}
}
