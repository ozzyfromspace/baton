# Verification round 2 (P1)

Measured 2026-10-04 against Claude Code 2.1.289 on macOS (arm64). These runs checked the assumptions the v0.1 plan depends on before any baton code was written. Scripts are in [`/spikes`](../../spikes) (09–13). They are driven by `spikes/common/scenario.py`, a scripted PTY driver, and `spikes/common/hooklog.py`, a hook that logs every event's full input.

## Results

| Question | Answer | Consequence for baton |
|---|---|---|
| Do hooks passed via `--settings` merge with project and plugin hooks? | ✅ They merge. Both the `--settings` and the project `SessionStart` hook fired. | baton passes its session hooks per launch with `--settings`. Plain sessions carry zero baton hooks. |
| Does the post-compact brief land before an un-delayed `PostCompact` asyncRewake? | ✅ `SessionStart(compact)` runs ~30ms before `PostCompact`, and the model acted on the brief. | No artificial sleep is needed in the rewake path. |
| How does a rewake turn look to hooks? | It fires `UserPromptSubmit` with a prompt starting `<task-notification>`. | baton can tell a human-started turn from a rewake or nudge. |
| `/compact` typed while the model is busy? | ✅ It is queued and runs right after the turn's `Stop`. | Typing while busy is not destructive. baton still waits for `Stop`. |
| `/compact` when a `/compactor` command exists (autocomplete)? | ✅ The real `/compact` runs. | No special handling. |
| Which keys empty a non-empty input box? | A single **Ctrl-U** clears only the current line. **Ctrl-U ×4** cleared a two-line draft. **Escape** does not clear. | baton **never types over a human draft**: it tracks the human's keystrokes since their last Enter and holds off (then escalates) while a draft exists. Ctrl-U ×N is used only to wipe baton's own residue. |
| Dialog signals? | Permission prompts **and** `AskUserQuestion` both fire `PermissionRequest`; the dialog closes at `PostToolUse` (or `PermissionDenied`). A `Notification` did not fire within 3s. | `PermissionRequest` marks a dialog as open; the injector never types while one is open. |
| `SessionEnd` input | `reason: "prompt_input_exit"` for `/exit`. | Mid-plan `SessionEnd` → push with the reason. |
| `StopFailure` input (from the binary's schema) | `error` (`rate_limit`, `overloaded`, `server_error`, …), `error_details`, `last_assistant_message`. | Rate limit → one push, then wait for reset. Overloaded → back off and nudge. |
| **`Stop` input** | Includes `last_assistant_message`, `permission_mode`, **`background_tasks`** (`{id, type: shell\|subagent\|monitor\|workflow, status, description}`) and **`session_crons`**. | Deterministic "is it really waiting?": in-flight shells, subagents or workflows mean a legitimate wait with a deadline, while monitors-only means idle. Elevation refuses while any background work is in flight. |
| `CLAUDE_PID`, `CLAUDE_CODE_SESSION_ID` | ✅ Present in hook processes and in the Bash tool's environment. | Elevation can find the `claude` process and session without guessing. |
| Plugin named `baton` with skill `baton` | ✅ `/baton hello world` resolves to `baton:baton` with `command_args: "hello world"`; `/baton:baton …` also works. | The skill is invoked as `/baton <subcommand> …`. |
| Is the plugin's `bin/` on the Bash tool's PATH? | ✅ Yes. | The model runs `baton …` directly. |
| Auto mode and baton's CLI | Sonnet, auto mode: 1 denial out of 11 identical probe runs (`"[Code from External]"`), so the classifier is non-deterministic. The binary shows that Bash **allow rules apply in auto mode** unless `classifyAllShell` is set. | baton passes `permissions.allow: ["Bash(baton:*)"]` in its `--settings`. Plain sessions (elevation) need the user to add that rule once; `/baton setup` says so. |
| Hook start-up latency (spike 13) | Go binary: p50 3.2ms / p95 3.6ms. sh launcher → Go: p95 9.9ms. Python: p95 25.6ms. | Generated hooks call the Go binary directly via `BATON_BIN`. |

## Not verified here (flagged)

- Linux and Windows: PTY behavior, `CLAUDE_PID` and the Bash PATH. These are covered by CI unit tests and a later manual check; Windows elevation is out of scope for v0.1.
- Whether `autoMode.allow` entries in `--settings` merge with or replace the user's own. baton avoids relying on it and uses `permissions.allow` instead.
