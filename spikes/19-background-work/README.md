# 19 — background work across compaction and restart

2026-10-10, Claude Code 2.1.296, `--model haiku`, interactive in a PTY with every hook logged
([write-up](../../docs/research/parallel.md)).

- `run.py q1`: does a `run_in_background` Bash job survive a typed `/compact`, does its completion wake an idle
  model, and does the model still know what the job was for?
- `run.py q3`: SIGTERM claude while a background job runs (as baton's restart and elevation do), then
  `claude --resume`: does the job survive, and is the resumed session told?
- `run.py q4`: do a Monitor watcher and a one-shot CronCreate survive `/compact`?
- `run.py q5`: what does PostToolUse(Monitor) report, and does a Monitor that ends leave `background_tasks`?

Each run uses `../common/scenario.py`, creates `work/<q>/` (a scratch git repo with `job.sh`), and logs hooks
to `work/<q>/hooks-*.log` through `hooklog.py`. A run costs a few cents.

Results:

| | Result |
|---|---|
| q1 | Survives (same pid). After the compact boundary Claude Code re-attaches a `task_status` record per live task (id, `local_bash`, the model's description, command, output file). The `<task-notification>` wakes the idle model with no human input. |
| q1 | No hook fires when a background task completes. Signals: `UserPromptSubmit` whose prompt starts with `<task-notification>`; the task leaving `Stop.background_tasks`; `PostToolUse(Bash).tool_response.backgroundTaskId` at start. |
| q3 | Lost. The job shares claude's process group and dies with it. On `--resume` the transcript gets a `stopped` notification, which does not wake the model. |
| q4 | Both survive. A Monitor appears in `background_tasks` as `type: "shell"`, not `"monitor"`. |
| q5 | `PostToolUse(Monitor).tool_response` is `{taskId, timeoutMs, persistent}`; `taskId` is the task's `id` at Stop. A Monitor whose command ends leaves `background_tasks` at the next Stop, and its end woke the model. |
