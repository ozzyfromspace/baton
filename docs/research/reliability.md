# Reliability review: what can stall or loop a run

Measured and read on 2026-10-04 against Claude Code 2.1.289 (macOS, arm64), with [`spikes/15-gates-and-signals`](../../spikes/15-gates-and-signals) and the 2.1.289 binary. This is the evidence behind the "nothing waits forever" rework of the host loop and hooks.

The question was which non-deterministic behaviors could stall an unattended run silently, loop and burn tokens, or otherwise waste hours.

## Claude Code behaviors that matter

| Behavior | Evidence | Consequence for baton |
|---|---|---|
| A project's `statusLine.refreshInterval` merges into the status line baton passes with `--settings`. | With `refreshInterval: 1` in the project's settings, baton's status line command ran 21 times in 20 idle seconds. | A status line whose text changes every second (a clock) redraws the screen every second. The longest quiet gap was **1.0s**, under baton's 1.5s quiet gate, so baton could never type. A static status line redrew once in 20s. baton now ignores the quiet gate after 30s with every other gate open. Typing into Claude Code while it redraws is safe, because input is buffered. |
| `Notification` with `notification_type: idle_prompt` fires about **60s after a turn ends**, once per idle stretch, and not while a dialog is on screen (`isDialogOnScreen`). | The spike logged `Stop` at 2.9s and `idle_prompt` at 63.0s. No second notification came in the next 190s, including with a draft typed. | This is an authoritative sign that no turn or dialog is open. It corrects flags left stale by a missing `Stop` (Claude Code's docs say Stop does not run when the user interrupts a turn). |
| Typed `/compact` does not fire `UserPromptSubmit`. | Spike 9 hook log: `PreCompact(manual)`, then `SessionStart(compact)`, then `PostCompact`, with no `UserPromptSubmit`. | The compaction itself never makes a turn look open. |
| `PreCompact` with `trigger: auto` fires when Claude Code starts **precomputing** a summary in the background, from about 80% of the effective window, well before (or without) the compaction itself. | Binary: the precompute path calls the PreCompact hooks with `trigger: "auto"`. | It must not count as the owed compaction. Only `SessionStart(compact)` proves that a compaction happened. |
| `PostToolUse` for `AskUserQuestion` carries the answers: `tool_response.answers` is `{question: chosen label}`. | Spike 10 hook log. | baton acts on the answers to its own questions (Continue, Pause baton, Checkpoint now, Keep going) instead of relying on the model to run a command. |
| `StopFailure.error` takes these values: `rate_limit`, `overloaded`, `server_error`, `unknown`, `max_output_tokens`, `authentication_failed`, `oauth_org_not_allowed`, `account_on_hold`, `verification_required`, `billing_error`, `invalid_request`, `model_not_found` and `cloud_credential_error`. | Binary: hook matcher metadata. | Only the first five are retried. The rest are reported at once. |
| `Stop.background_tasks[].type` takes these values: `subagent`, `workflow`, `shell` (which also covers the Monitor tool's shell watchers), `monitor`, `MCP task`, `teammate`, `dream`, `auto-mode scan`, `memory import` and `cloud session`. | Binary: the task-type map. | `dream`, `auto-mode scan`, `memory import` and `monitor` are not the model's work and never make a session look busy. A `shell` may run forever (a dev server, a log watcher), so it is no excuse for a stop without a status. |
| A subagent that ends in an API error never fires `SubagentStop`. | Binary: the query loop returns on an API error before the stop-hook runner. | A running count of subagents drifts. baton recounts at every `Stop` and does not gate on the count. |
| Go's monotonic clock stops during sleep on macOS. | Go runtime on darwin. | Sleep has to be detected on the wall clock (`Round(0)`). Ownership must not depend on a fresh heartbeat in the seconds after waking. |

## What was fixed

- **Gates no longer wait forever.**
  - A stale open turn (no hook activity for 15 minutes) stops gating.
  - The quiet-screen gate expires after 30s.
  - Background subagents that hold baton back for 30 minutes are reported.
  - A draft is reported after 2 minutes, with how to clear it.
- **Escalations clear themselves** when their cause is gone. Unresolved ones are pushed again after 30 minutes, 2 hours and 6 hours. A failed push is retried.
- **A failed compaction is retried** at 5, 15 and 45 minutes, not abandoned. One that finishes late is accepted.
- **API errors are classified.**
  - A usage limit is retried every 15-30 minutes, so the run resumes soon after it resets (before, the backoff grew to 4 hours).
  - Persistent overloads are reported.
- **Background work needs a declared wait**, and waits are capped at 2 hours. Reminders that get answered without the plan moving escalate after two.
- **The heartbeat runs on its own goroutine.** A slow notification or a long typed message can no longer let ownership lapse. A panic in the controller is logged instead of taking `claude` down.
- **Draft tracking follows the input box.**
  - Ctrl-C, Enter, backspace back to empty, and Ctrl-U on one line all clear it.
  - Mouse reports, arrows and mode keys don't create one.
- **baton's own CLI is allowed deterministically** in `PreToolUse`. A command with shell substitution in its notes (backticks inside double quotes) is refused, with the fix.
- **A hook that fails open is recorded** in `events.jsonl` (`hook_failed`).

## Left as decisions

- **The 90% context question waits for an answer.** It is pushed and re-pushed, and the answer acts directly. If nobody answers, the run waits.
- **Questions the model asks on its own (`AskUserQuestion`) also wait for an answer.** They get the same push and reminders.
