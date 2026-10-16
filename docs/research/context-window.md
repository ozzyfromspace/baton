# Measuring the context (status line input and `--autocompact`)

**In short:** the status line's `current_usage` is the exact context size, and `context_window_size` is the
model's window, not baton's cap. Claude Code compacts on its own 33k tokens below `min(window, --autocompact cap)`.
baton's heads-up to the model (60%), its checkpoint (80%, 60% until v0.8.6) and its question to the human (90%) are
tokens against that limit, moved earlier when a small window would put them after Claude Code's own compaction. The
checkpoint moved to 80% because, in a real run, a lead restarted at about 180k tokens after each compaction (about
32k of them the plan file Claude Code attaches, which baton now moves out of its way), had about 300k of room before
the ask at 60%, and checkpointed 9 times mid-phase against 7 phase boundaries.

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
- Thresholds are in tokens against that limit: the heads-up to the model (`notice_pct`, default 60%, since v0.8.6), the checkpoint nudge (`checkpoint_pct`, default 80%, 60% until v0.8.6), and the question to the human (`warn_pct`, default 90%, never under 200k). When a small window would put any of them at or after Claude Code's own compaction point, every one becomes its percentage of that point instead, so each still comes, and in order: on a 200k model, 100.2k, 133.6k and 150.3k, before Claude Code's 167k.
- baton validates its own `autocompact` setting before launching `claude`. A cap given on the command line wins and is left for `claude` to validate.

## Room before the checkpoint (v0.8.6)

A read of a real run's last 15½ hours (a 57-phase plan, an 810k cap) found the lead had checkpointed 9 times
mid-phase, against 7 phase boundaries.

- **Each stretch had about 300k tokens.** The checkpoint ask came at 60% (486k). Every compaction restarted the lead
  at about 180k: the summary, baton's brief (the phase's section, the standing rules, the notes), and, about 32k
  tokens of it, the plan file, which Claude Code attaches in full after every compaction while the session's
  plan-mode file exists (`plan_file_reference`; [spike 38](../../spikes/38-plan-file-reference/README.md)). No
  setting turns that off; only the file's absence does, so baton now moves an attached plan out of that path.
- **The ask moved to 80%.** With an 810k cap: the heads-up at 486k, the checkpoint at 648k, the human's question at
  729k, Claude Code's own compaction at 777k. A stretch from a 180k floor has about 470k tokens of room, about 55%
  more, so about a third fewer mid-phase checkpoints. The 129k left between the ask and Claude Code's own compaction
  is about 14 minutes at that run's pace, and models acted within 1 to 10 minutes of the ask. Claude Code itself
  starts precomputing its summary from 80% of its effective window (above).
- **The heads-up asks nothing.** At 60% the model is told where its context stands, that nothing is asked of it
  now, and to make the room last: a line range or a grep rather than a whole file, and short command output. It
  comes beside a tool result and never blocks. Phase agents get it and the checkpoint ask too; for them a
  checkpoint is a fresh agent from their notes, so the room matters more.
