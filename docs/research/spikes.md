# What can trigger and resume a compaction? (spike findings)

Measured on 2026-10-04 against **Claude Code 2.1.289** (macOS, arm64). Scripts are in [`/spikes`](../../spikes). Most runs used `--model haiku`. Interactive runs drove a real `claude` TUI through a pseudo-terminal.

## The question

Over a long multi-phase plan, compaction should happen **at every phase boundary**, decided by something deterministic rather than by the model's memory. The model should then **resume on its own**, in **one live interactive session** a human can watch, **with no external tools**.

Earlier attempts in this space had the model run a script that typed `/compact` into a terminal emulator via AppleScript. They failed in characteristic ways:
- the model forgot to run the script;
- it read the `<total_tokens>` session budget as its context window and decided compaction wasn't needed;
- a backgrounded script's exit code was swallowed;
- long focus text arrived as a paste and was sent as a message;
- after compaction, nothing woke the session up.

## Results

| # | Mechanism | Result | Notes |
|---|---|---|---|
| 1 | Stop hook returns `{"decision":"block","reason":"…"}` | ✅ | The model continues; the reason arrives as "Stop hook feedback". |
| 2 | Lower `autoCompactWindow` (settings) mid-session | ❌ | No compaction: not on a block continuation, not on a rewake turn, not on a real typed user turn. The status bar *did* show the new window. |
| 3 | `autoCompactWindow` in settings at launch, or `--autocompact 100k` | ✅ | Auto-compaction fires at the threshold, and the model **continues the task by itself** afterwards. Usable as a hard cap, not as a boundary trigger. |
| 4 | `SessionStart` hook, matcher `compact`, `additionalContext` | ✅ | The text reaches the model after compaction (it used a codeword that existed only in the brief). |
| 5 | `PreCompact` / `PostCompact` hook inputs | ✅ | PreCompact gets `trigger` (`manual`/`auto`) and `custom_instructions`. PostCompact gets `compact_summary`. |
| 6 | Hook option `"asyncRewake": true`, exit code 2 | ✅ | Runs in the background; on exit 2 it wakes an idle model with its stderr. Works on `Stop`, `PostCompact` and `SessionStart`. The resulting turn counts as a stop-hook continuation (`stop_hook_active: true`). |
| 7 | CronCreate one-shot with prompt `/compact …` | ❌ | Fires on time but is delivered as a **plain user message**. The command does not run. |
| 8 | `/compact` as a user message over `--input-format stream-json` (headless) | ✅ | Runs as a real command. But headless loses the live terminal, which is a hard requirement. |
| 9 | `/compact` typed (as keystrokes) into an interactive session | ✅ | With PostCompact asyncRewake and the compact brief, this gives a full unattended phase handoff in ~26s, compaction included. |
| 10 | A transparent PTY wrapper that hosts `claude` and types `/compact` when a hook asks | ✅ | Stop hook queues a request → wrapper waits for a quiet screen and idle hands → types `/compact` → PreCompact acks in under 1s → brief → rewake → next phase. ~25s end to end, 18s of it compaction. |
| 11 | Elevation: plain session → wrapper, same conversation | ✅ | SIGTERM exits `claude` cleanly in under 1s. A zsh `precmd` hook keyed by tty relaunches `claude --resume <id>` under the wrapper (same session id, full context). A `SessionStart` asyncRewake hands over the pending command. ~9s, zero keystrokes. |
| 12 | `claude --settings '{"statusLine":{…}}'` | ✅ | Overrides the user's status line **for that session only**. No settings file is edited. |

## Conclusions that shaped the design

- **Only two things start a compaction:** a keystroke in an interactive session, or a message on a headless session's stdin. Settings changes and scheduled tasks don't. With live-terminal and no-dependency requirements, baton has to **own the terminal**: a PTY passthrough that can type.
- **Resuming should never depend on the model.** `asyncRewake` hooks wake it deterministically after compaction, after elevation, and as a watchdog nudge.
- **Stopping should never depend on the model either.** A Stop hook that refuses the stop (fixed reason, counted attempts) turns "the model forgot" into "the model is told, every time".
- **Measure context outside the model.** The `<total_tokens>` figure the model sees is a session budget, not its context window. Baton reads real usage from the status line input.
- **Plugins can't do it all.** A plugin can't set the main status line, can't grant permissions, and (outside allowlisted marketplaces) can't use the manifest's `binaries` field. So baton ships a launcher that downloads a sha256-pinned binary, and it passes its session hooks and status line through `--settings`.

## Other observations

- Haiku refuses to run an unexplained "run this script" instruction, and calls it a hidden instruction sequence. Skill text has to say plainly what each command does.
- Auto mode is unavailable on Haiku, which falls back to manual. End-to-end tests that need auto mode must use Sonnet or Opus.
- A `claude` started from inside another Claude Code session inherits `CLAUDE_CODE_CHILD_SESSION` and doesn't save transcripts. Test harnesses must scrub `CLAUDE*` variables.
- Auto-compaction precomputes its summary in the background: `PreCompact` can fire several times before the swap actually happens.
