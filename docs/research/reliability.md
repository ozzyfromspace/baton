# Reliability review: what can stall or loop a run

**In short:** which Claude Code behaviours could stall or loop an unattended run, and the "nothing waits forever"
rework they led to: every gate baton waits on expires (a stale turn, a screen that keeps redrawing, background work, a
draft), escalations clear themselves or are pushed again, a failed compaction is retried, API errors are classified,
and the 90% context question goes ahead with "Keep going" when nobody answers it (after 20 minutes then; since v0.8.3
at `escalation_timeout`, 10 minutes by default). Its "only `SessionStart(compact)` proves a compaction" became v0.8's
proof ([`visible-workers.md`](visible-workers.md)), and its declared waits became v0.4's jobs
([`parallel.md`](parallel.md)). Since v0.8.21 a background shell that may do nothing more for the run is reviewed
and stopped only on the reviewer's verdict, and a newer baton holds new agents for 10 minutes at most
([What v0.8.21 fixed](#what-v0821-fixed)).

Measured and read on 2026-10-04 against Claude Code 2.1.289 (macOS, arm64), with [`spikes/15-gates-and-signals`](../../spikes/15-gates-and-signals) and the 2.1.289 binary. This is the evidence behind the "nothing waits forever" rework of the host loop and hooks.

The question was which non-deterministic behaviors could stall an unattended run silently, loop and burn tokens, or otherwise waste hours.

## Claude Code behaviors that matter

| Behavior | Evidence | Consequence for baton |
|---|---|---|
| A project's `statusLine.refreshInterval` merges into the status line baton passes with `--settings`. | With `refreshInterval: 1` in the project's settings, baton's status line command ran 21 times in 20 idle seconds. | A status line whose text changes every second (a clock) redraws the screen every second. The longest quiet gap was **1.0s**, under baton's 1.5s quiet gate, so baton could never type. A static status line redrew once in 20s. baton now ignores the quiet gate after 30s with every other gate open. Typing into Claude Code while it redraws is safe, because input is buffered. *Revised for v0.8.20* ([spike 44](../../spikes/44-statusline-refresh/README.md), Claude Code 2.1.318): that merge holds only while the flag's status line sets no interval of its own. baton now sets `refreshInterval` (10 seconds, or the user's own passed on as it is), and the flag's value wins over a project's, slower (30) and faster (5) alike. In 60 idle seconds no interval ran the command 0 times, and 10 ran it 6 times; text that had not changed was not drawn again, and text that changed every run redrew 10 seconds apart, far above the quiet gate. |
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
  - Background subagents that hold baton back for 30 minutes are reported. (*Revised for v0.8.20:* the model's own background subagents and workflows no longer gate a compaction, a result or a line, since none of those ends them, as phase agents never did. Only what would end them waits for them: a restart in place, at most 10 minutes, and a fresh session after an API error, at most 20.)
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

## What v0.8.21 fixed

A 74-phase campaign on v0.8.19 lost about 8 agent-hours, and probably 1.5 to 2 hours of its finish, to one background
shell. Its events, in UTC:

- **2026-11-04 ~18:09.** In P23, a background subagent of the lead's model started a background zsh poll loop,
  `until [ -s …/tasks/<id>.output ] && tail -2 … | grep -qE "passed…"; do sleep 5; done`. Its target, a pytest task,
  ended at 18:25 without the pattern, so the loop never ended: it polled every 5 seconds for 22 hours, writing
  nothing. baton's hooks did not record a subagent's starts then.
- **2026-11-05 13:34.** v0.8.20 was installed; the host logged `newer_baton` at 13:52. `restartable` counted the loop
  as background work (`BusyBackground`), so no restart could come, and `upgradeHolds` held every new phase agent
  while the lead held a phase, with no deadline.
- **13:46 → 16:10.** No agent spawned (the last, P60's, at 13:18). P40 was suspended at 13:46 for a fresh agent and
  P60 at 14:28; P78 was ready from 13:58 and P56 from 14:16. From 14:40, when P52 landed, the lead worked alone.
- **16:10.** The loop was killed by hand. The host restarted on v0.8.20 at 16:24, at P55's boundary; P40 and P60
  resumed, and P56 and P78 spawned.

What changed:

- **A newer baton never holds a run.** New agents wait for an upgrade restart only while phase agents alone keep it
  away (the model's own background work, a wakeup or a monitor drops the hold, since holding agents cannot clear
  them), and for `Timing.UpgradeWait` (10 minutes) at most (`upgrade_given_up`). A restart held 10 minutes tells the
  human what holds it, each background task with its description, age and phase (`upgrade_waits`). This alone would
  have shown the 22-hour loop at 14:02.
- **Bad background tasks are found, and dropped only on a reviewer's verdict.** Signals (`phase_over`, `quiet` for
  30 minutes, `idle` as evidence only, `past_end`, `waits_on_ended`, `long`) make a background shell a suspect; a
  model call (`sonnet` by default) reads the evidence and answers STOP, KEEP or UNSURE; only STOP stops, through
  `TaskStop`, or the shell's processes where that cannot. Unsure asks the model that started it, and tells the human.
  Each task is reviewed at most three times. The campaign's loop had `phase_over`, `quiet`, `waits_on_ended` and
  `long`, and would have been reviewed and stopped on 2026-11-04.
- **A loop that only waits is questioned once** before it starts, and every brief asks for background work that
  declares its end and prints as it goes.

The facts behind the stop ([spike 45](../../spikes/45-stop-background/README.md), Claude Code 2.1.318, Haiku, macOS
arm64, 2026-11-05):

| Behavior | Evidence | Consequence for baton |
|---|---|---|
| No hook names a background task's output file. Every hook carries `scratchpad_dir`, and tasks write to `tasks/<id>.output` beside it, under `/tmp/claude-<uid>/<project>/<session>/`, or under `$CLAUDE_CODE_TMPDIR` when set. | `PostToolUse(Bash).tool_response` of a background command is `{stdout, stderr, interrupted, isImage, noOutputExpected, backgroundTaskId}`; the task notification names `<output-file>`, but only as the task ends. | baton takes the folder from `scratchpad_dir` and stats `<id>.output`. |
| A phase agent's and a subagent's background tasks write to the lead session's folder. | A phase agent's command, in its worktree, wrote to `…/<the lead's session>/tasks/<id>.output`; a subagent's job ran on after it stopped, and its notification reached neither the lead's hooks nor the mod. | One folder holds every loop's tasks; a subagent's start is recorded with its `agent_id`. |
| A task's output file ends `[exited with code N]`, or `[killed]` after `TaskStop`. `Stop.background_tasks` lists a shell's `command`. | The spike's files and Stop inputs. | An output's last line says a task ended, whatever a Stop says. |
| `TaskStop` (the mod's `stop`) ends a background shell's whole process group within 3 seconds, without telling or waking the model. | `loop.sh`, its `sleep 600 &` child and its `sleep 2` all ended; no notification in the next 45 seconds; the next Stop did not list it. | The quiet way to stop one; the model is told by baton instead. |
| A background shell is a direct child of claude, a `zsh -c` that evals the model's command quoted in `eval '…'`, leading a process group of its own. Ending it by hand wakes the model with a failure, exit code 144. | `kill -TERM -<pgid>` ended every process within 3 seconds; the notification said "failed with exit code 144". | The fallback finds the shell by its arguments (`procs.Proc.Args`) and signals its processes one by one, never claude; the owner is told what exit 144 means. |

## Decided with the user (2026-10-05)

- **The 90% context question times out.** After 20 minutes unanswered, the host answers "Keep going" itself. In a probe, a digit selected that option of a single-question dialog at once, with no review step (`spikes/15-gates-and-signals/ask_keys.py`), and a digit doesn't depend on where the highlight is. The answer arrives through the usual `PostToolUse` hook.
  - *Revised for v0.2* (`docs/research/escalation.md`). v0.1.1 typed `2`, into the dialog it recorded last. Dialogs form a queue, and a background subagent's permission prompt can be on screen in front of the question; there `2` means "Yes, and always allow". So the options became `Checkpoint now` · `Pause baton` · `Keep going`, the default third, and the host types `3`, which in a Bash permission prompt means "No".
  - *Revised since.* Since v0.8.3 the question waits `escalation_timeout` (10 minutes by default, at most 20), as every question of baton's does. Since v0.8 baton's mod settles it at that deadline where the mod runs, with no key typed. Since v0.8.6, where it does not, the host dismisses it with Esc and records Keep going itself: on Claude Code 2.1.30x a Bash prompt's `3` is "Yes, and switch to auto mode" ([`escalation.md`](escalation.md), spike 39).
  - The host types only when its question is the **only** dialog open, and only into an entry the hooks marked as baton's own question. The clock starts when the question is first alone, and any key the human presses starts it over. A draft in the input box no longer holds it: keys go to the dialog.
- **Questions the model asks on its own are refused while a plan runs**, unless the human started the turn. The refusal tells the model to decide, note its assumption, or run `baton blocked`, which notifies the human.
  - *Revised for v0.2.* baton's own questions are allowed only exactly as issued: the same text (whitespace aside), the same options in the same order, single-select. v0.1 let through anything that started with `baton:`. A question that starts with `baton:` while baton has one waiting must be that one, word for word, even in a turn the human started. In an end-to-end run, Haiku dropped the context question's last sentence; the host did not recognize the dialog and never answered it. Refused with the exact call, the model asks it again as issued.
