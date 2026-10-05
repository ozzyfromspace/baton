# Measuring the context (status line input and `--autocompact`)

Measured 2026-10-04 against Claude Code 2.1.289 on macOS (arm64), with [`spikes/14-context-window`](../../spikes/14-context-window). The thresholds come from reading the 2.1.289 binary.

baton's context valves need two numbers the model cannot be trusted with: how big the context is, and where Claude Code will compact it on its own.

## What the status line input reports

The probe logged every status line input from two sessions. Abridged, after the first response:

| Session | `context_window_size` | `current_usage` (input + cache creation + cache read) | `total_input_tokens` | `used_percentage` |
|---|---|---|---|---|
| `haiku`, `--autocompact 100k` | 200000 | 10 + 11101 + 24883 = 35994 | 35994 | 18 |
| `opus[1m]`, `--autocompact 810k` | 1000000 | 2 + 1962 + 40616 = 42580 | 42580 | 4 |

- **`context_window_size` is the model's window, not the cap.** baton has to apply the cap itself. It knows the cap because it launched `claude` with it.
- **`used_percentage` is relative to the model's window, and rounded to a whole point.** On a 1M model one point is 10k tokens.
- **`current_usage` is exact.** It is the last request's input, cached or not, which is the size of the context. It is `null` before the first response.
- **`total_input_tokens`** matched the last request in these runs, but Claude Code documents it as a session total. That is the same kind of number the model mistakes for its context window, so baton does not use it.

## Where Claude Code compacts on its own

From the binary (2.1.289):

- the compaction window is the model's window, or the `--autocompact` value when that is smaller (`min(window, cap)`);
- the effective window is that minus the output reserve, `min(max output tokens, 20k)`;
- auto-compaction triggers 13k below the effective window, which is **33k below the compaction window** for current models. An 810k cap compacts at about 777k; a 200k model compacts at about 167k.
- A summary is precomputed in the background from 80% of the effective window, so `PreCompact` (trigger `auto`) can fire well before the swap.
- `--autocompact` accepts `auto` or 100k–1M (`810k`, `1m`, `810000`, or `810` as shorthand). Anything else makes `claude` refuse to start.
- `CLAUDE_CODE_AUTO_COMPACT_WINDOW` in the environment takes precedence over the flag and settings.

## Consequences for baton

- The status line records `current_usage` as exact tokens (falling back to the percentage) together with the model's window. baton computes the limit as `min(window, cap)`.
- Thresholds are in tokens against that limit: the checkpoint nudge (default 60%), and the question to the human (default 90%, never under 200k). Both are moved before Claude Code's own compaction point when a small window would put them after it.
- baton validates its own `autocompact` setting before launching `claude`. A cap given on the command line wins and is left for `claude` to validate.
