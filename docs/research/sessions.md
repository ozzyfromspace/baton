# Plan approval and session identity

Measured 2026-10-07 against Claude Code 2.1.292 on macOS (arm64), with `--model haiku` and [`spikes/17-sessions`](../../spikes/17-sessions). Every hook event's full input was logged (`hooklog.py`), and a scripted terminal (`common/scenario.py`) entered plan mode, approved or sent back a plan, cleared and resumed sessions.

Two questions decided v0.3:

- **What tells baton that a plan was approved, and which file holds it?** baton attaches an approved plan itself, instead of asking the model to remember to.
- **What is a session?** baton keeps one run per session, so it has to know when a session's id changes and which plan file belongs to it.

## Plan approval

| Path | Hooks, in order | What baton can read |
|---|---|---|
| Approve ("Yes, auto-accept edits", Enter) | `PreToolUse` → `PermissionRequest` → (dialog) → **`PostToolUse(ExitPlanMode)`**; the model carries on in the same turn, with no `UserPromptSubmit` | `PreToolUse` and `PermissionRequest` carry `tool_input = {plan, planFilePath}`. `PostToolUse` has an **empty `tool_input`** and `tool_response = {plan, isAgent: false, filePath}`. |
| Send back (Esc, or "Tell Claude what to change") | `PreToolUse` → `PermissionRequest` → (dialog) → **nothing** | No `PostToolUse`, `PostToolUseFailure`, `PermissionDenied` or `Stop`. Typed feedback goes back as the tool result and the model continues in the same turn. |
| Approve with "clear context" (`showClearContextOnPlanAccept: true`) | `PermissionRequest` → **`SessionEnd{reason: clear}`** → **`SessionStart{source: clear}`** with a new session id → the model works by itself → `Stop` | **No `PostToolUse(ExitPlanMode)` and no `UserPromptSubmit`.** The new session's first message is "Implement the following plan: … read the full transcript at: <old transcript>", which names no plan file. The new session keeps the old session's plan file. |

- `permission_mode` reads `plan` up to the dialog, and `acceptEdits` (or the chosen mode) from `PostToolUse` on. `SessionStart` carries no `permission_mode`.
- A dialog left open for about 6 seconds fires `Notification{permission_prompt}` ("Claude Code needs your approval for the plan").
- The "clear context" option is off unless that setting is on. When it is, it is option 1.

## Session identity

| Action | Session id | Plan file |
|---|---|---|
| `/clear` | **new** (`SessionEnd{reason: clear}` then `SessionStart{source: clear}`) | **new** file, new slug |
| Approve a plan with "clear context" | **new** | **same** file as before |
| `claude --resume <id>` (same directory) | same | same: planning again **overwrites** it |
| Two sessions started in one directory | distinct | distinct |

- `CLAUDE_CODE_SESSION_ID` follows the new id after `/clear`, both in hook processes and in the Bash tool's environment.
- Every hook input now also carries `scratchpad_dir` and `prompt_id`.

## Consequences for baton

- **Plan approval.** baton attaches on `PostToolUse(ExitPlanMode)` from the main agent, reading `tool_response.filePath`. It never treats a `PermissionRequest` alone as approval: a plan sent back looks the same up to that point.
- **The "clear context" approval** is recognised as `SessionEnd{clear}` while a plan dialog is open, confirmed by the next main-agent tool call outside plan mode before any prompt. Nothing needs compacting then: the new session gets P0's instructions straight away.
- **Dialog tracking.** An approval's `tool_input` is empty, so the plan dialog is keyed by its tool alone. A plan sent back fires no hook, so asking again replaces the dialog on record instead of adding a second one.
- **Runs are per session.** One plan file belongs to one session; `/clear` and a "clear context" approval in the same terminal carry the run into the new session id; `--resume` finds the run by its id. Two sessions in one directory never share a run.
- **Re-planning in a running session rewrites the running plan's file.** baton keeps no copy of the plan (it reads the file Claude Code writes). It watches the file's hash instead: phases still found means baton follows the new text; phases missing means the run pauses and says why.
