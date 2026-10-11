# 20 — steering tool calls from a PreToolUse hook

2026-10-10, Claude Code 2.1.296, headless `claude -p` ([write-up](../../docs/research/parallel.md)).

Can a hook move a long command to the background, or rewrite it, without the model's cooperation? And what
does Claude Code itself do with a long foreground command?

```sh
./run.sh <mode> <permission-mode> <tag> "<prompt>"   # MODEL=sonnet ./run.sh … for auto mode
python3 tx.py <session-id>                           # print that session's transcript
```

`hook.py` logs every Bash Pre/PostToolUse to `logs/<tag>.jsonl` and acts per `MODE`: `bg` (force
`run_in_background`), `bg-ctx` (the same, plus `additionalContext` telling the model), `wrap` / `wrap-pass`
(rewrite the command, with and without `permissionDecision`), `deny` (refuse with an instruction).
Scratch work happens in `proj/`. The whole spike cost $0.16.

Results:

- `updatedInput` rewrites the command or forces `run_in_background: true` in default, acceptEdits,
  bypassPermissions and auto mode (Sonnet), with or without `permissionDecision`. PostToolUse shows the
  rewritten input. The transcript keeps the model's original call, so tell the model with `additionalContext`.
- A deny that says how to run the command instead: Haiku complied on its next call, 3 of 3.
- Foreground Bash: default timeout 2 min, maximum 10 min. At the timeout a command is moved to the background
  (`timedOutAfterMs` plus a `backgroundTaskId`), except one starting with `sleep`, which is killed (exit 143);
  a standalone `sleep` of 25 s or more is refused before it runs.
- `CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS` (main agent only) caps the foreground timeout: at 3000, a 12 s
  command was backgrounded after about 4 s.
- From the bundle: subagents auto-compact on their own; `Agent` `isolation: "worktree"` creates
  `<repo>/.claude/worktrees/…` and never merges back; `CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS` defaults to 20;
  the Workflow tool is gated by policy, settings and a feature flag, and may be called only on the user's
  explicit opt-in (which a skill the user invokes can carry, and a hook's `additionalContext` cannot).
