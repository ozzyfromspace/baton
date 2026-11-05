# 45 — a background shell's output file, and stopping one (Claude Code 2.1.318, Haiku, macOS arm64, 2026-11-05)

Question: a 74-phase campaign lost about 8 agent-hours to a background `until` loop that polled another task's output
for 22 hours, writing nothing. Before baton judges and stops such a shell (v0.8.21): where does a background task's
output go, and can baton learn that from a hook; does the mod's `stop` command (`TaskStop` with the task's id) end a
background shell and its children, and what do the model and the next `Stop` see; and does ending the shell's process
group by hand, found among claude's children by its command line, do the same?

Harness: `run.py <workroot> [where] [taskstop] [killpg] [tmpdir]`, spike 43's on `common/scenario.py`: an interactive
claude on Haiku in a PTY, in a fresh git repo, no user settings (`--setting-sources project,local`), acceptEdits with
exact allow rules, `hook.py` logging every hook's input (`hooks.jsonl`), and a mod (`mod/`) with one command,
`/spk-stop <task id>`, that calls `TaskStop` from a timer as baton's mod runs its `stop` command
(`$.tool.call({ tool: 'TaskStop', task_id })`), and that logs every prompt Claude Code submits, task notifications
too, letting each through. `job.sh NAME SECS` prints a line a second; `loop.sh NAME` polls for a file that never
appears, writing nothing, with a `sleep 600 &` child of its own. A run costs a few cents. `out/results.json` is the
run's digest.

| Question | What the run did |
|---|---|
| `where` | The lead started `./job.sh A 20` in the background, and a general-purpose subagent of its started `./job.sh B 20`. |
| `taskstop` | The lead started `./loop.sh L1` in the background; `/spk-stop` ended it. |
| `killpg` | The lead started `./loop.sh L2`; the driver found its shell among claude's children by its command line and sent its process group SIGTERM. |
| `tmpdir` | A second session with `CLAUDE_CODE_TMPDIR` set started `./job.sh C 5`. |

## Findings

**Where a task's output goes.**

- `PostToolUse(Bash).tool_response` of a background command names no file: `{stdout: "", stderr: "", interrupted:
  false, isImage: false, noOutputExpected: false, backgroundTaskId}`. The text the model gets ("Output is being written
  to: …") does name it, but no hook sees that text.
- Every hook's input carries `scratchpad_dir`, `<root>/claude-<uid>/<project>/<session>/scratchpad`, and each
  background task writes its output to the folder beside it: `<root>/claude-<uid>/<project>/<session>/tasks/<id>.output`.
  `<project>` is the folder the session's transcript is kept in under `~/.claude/projects`, and `<session>` the
  session's id. The campaign's were the same. baton takes the folder from `scratchpad_dir` (`hooks.tasksDir`).
- `<root>` is `/tmp` (`/private/tmp` on macOS, as the notification writes it), or `$CLAUDE_CODE_TMPDIR` when set: then
  the output, and `scratchpad_dir` with it, went under it, and nothing under `/tmp`. macOS's own `TMPDIR` was not used.
- The task notification names the file (`<output-file>`), but only as the task ends, as a prompt: `UserPromptSubmit`
  and the mod's `prompt.submit` (`origin.kind: task-notification`) see it.
- A subagent's background command: its `PostToolUse` carries the subagent's `agent_id` and `agent_type`, and the
  lead's `session_id`, `transcript_path` and `scratchpad_dir`; its output goes to the same session folder. Job B ran
  on after its subagent stopped, and finished; its notification reached neither the lead's prompt hooks nor the mod,
  and a second `SubagentStop` of that subagent came two seconds after B ended, as if the notification woke it.
  The campaign's loop was such a task: started by a background subagent of the lead's model, which baton's hooks did
  not record before v0.8.21.
- A phase agent's tasks are the lead session's too: this phase's own agent, in its worktree, had its background
  command's output written to `…/-Users-ozzy-Projects-claude-baton/<the lead's session>/tasks/<id>.output`.
- The folder also holds `<agentId>.output` for a background subagent.
- A task that ends gets a last line in its file: `[exited with code N]`, or `[killed]` once `TaskStop` ended it.
- Claude Code 2.1.318's `Stop.background_tasks` lists a shell's command: `{id, type: "shell", status, description,
  command}` (`state.Task.Command`).

**`TaskStop`, as the mod's `stop` command calls it.**

- It returned `{message: "Successfully stopped task: <id> (./loop.sh L1)", task_id, task_type: "local_bash",
  command}`.
- It ended the shell's whole process group within 3 seconds: the `zsh -c` wrapper, `loop.sh`, its `sleep 600 &`
  child, and the loop's `sleep 2`.
- The model was not told and not woken: no task notification in the 45 seconds after, and the next `Stop` no longer
  listed the task. Its output file ends `[killed]`.
- The mod's call fires `PreToolUse` and `PostToolUse` like any tool call (`tool_use_id` `toolu_plugin_…`), so baton's
  hooks see it as a mod call (`modCall`).

**Ending the process group by hand.**

- A background shell is a direct child of claude: `/bin/zsh -c source <shell snapshot> 2>/dev/null || true && … &&
  eval '<the model's command>' < /dev/null && pwd -P >| /tmp/claude-XXXX-cwd`, the command quoted inside `eval '…'`
  (a `'` in it written `'"'"'`). It leads a process group of its own (its pgid is its pid, claude's is claude's), so
  `kill -TERM -<pgid>` ends it and every child, and never claude. `procs.Proc.Args` reads that command line
  (`kern.procargs2` on macOS, which hides a platform binary's environment but not its arguments; `/proc/<pid>/cmdline`
  on Linux).
- It ended every process of the group within 3 seconds.
- Unlike `TaskStop`, it wakes the model: a task notification with `status: failed`, "failed with exit code 144", and
  the output file ends `[exited with code 144]`. The next `Stop` no longer listed it. So `TaskStop` is the quiet way,
  and the process group the fallback, after which the model hears of a failure baton should explain.

**Not examined:** what effort the mod's `$.model.complete` sends (P2 asks; the mod is not edited in v0.8.21).
