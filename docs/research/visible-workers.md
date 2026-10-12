# Workers you can see: phases as subagents of the lead

Measured 2026-10-12 against **Claude Code 2.1.296** (macOS, arm64) with Haiku, from Claude Code's documentation and
two spikes: [`spikes/31-mod-agents`](../../spikes/31-mod-agents) (can a mod drive phase agents at all; eight
scenarios) and [`spikes/32-phase-agents`](../../spikes/32-phase-agents) (what baton's design for it assumes; fourteen).
Each spike's README has its table and harness.

## The question

v0.5 put each worker in a hidden `claude` of baton's own, in a pseudo-terminal nobody sees
([`workers.md`](workers.md)). That made workers easy for baton to drive: it types into them, reads their screens and
ends them. It made the run hard for the human to follow. In practice the human could not tell how many agents ran,
on which phases, which ran at once and which waited on which, or whether anything ran at all, and ended up asking
the lead's model, which only knows what baton tells it. `baton view` shows one worker in another terminal, which
does not answer "is the run moving".

Claude Code shows its own agents in the terminal the human already watches: background subagents in a panel under
the prompt and in `/tasks`, workflows in `/workflows`, each with its status. Could a phase run as one of those, with
baton still in control, deterministically and with no model judgment, and its requests still reaching the human?

## What Claude Code offers (2.1.296)

- **Background subagents.** Each runs in its own context and shows as a row in the panel below the prompt (name,
  description, time, status), in the @-mention typeahead and in `/tasks`, where the human can open its transcript.
  A permission prompt in one is shown in the main session. The human can type into a subagent's transcript to steer
  it while its row is listed. A subagent compacts by itself. It can be resumed by a message (`SendMessage`) with its
  conversation, also after the session is resumed. Every settings hook fires inside it with `agent_id` and
  `agent_type`; `SubagentStart` can add context, and a `SubagentStop` block runs it on with the reason as its next
  instruction. An agent definition can set `isolation: worktree`, and a `WorktreeCreate` hook replaces Claude
  Code's own worktree creation with whatever the hook makes. At most 20 run at once by default.
- **Dynamic workflows.** A script (`agent()`, `parallel()`, `pipeline()`, `phase()`) runs agents in the background,
  and `/workflows` shows each `phase()` group with its agents, their prompts, tool calls and results. A run pauses
  for a usage limit, restarts a stalled agent, and can be resumed in the same session. It takes no input while it
  runs, its agents end when their turn ends, the script cannot read files, and it is started by the model's
  `Workflow` call (a saved workflow's `/<name>` is a prompt that asks the model to make it).
- **Agent teams** (experimental): teammates the human can message, but no worktree isolation, and in-process
  teammates are not restored by `--resume` or a rewind.
- **Agent view** (`claude agents`, research preview): background sessions on a screen of their own, outside the
  lead's terminal.
- **Mods** (on by default since 2.1.287): a plugin's JavaScript module that Claude Code runs in its own process, on
  events (tool calls, prompts, turns, agents, compaction, drawing). Through its API a mod can spawn and list
  agents, message one, compact the conversation, submit a prompt, ask the human in Claude Code's own question
  dialog, run a process, read files, and draw a pane, a band above the prompt or a status line. It can drop a
  prompt before the model reads it, and it is told where each prompt came from (the human's keyboard, a
  notification, a plugin).

## What the spike found

- **A mod starts phase agents with no model in the loop.** `$.agent.spawn` starts a named background agent of a
  given type on the session's model, and `$.agent.list()` gives each one's status (`running`, `waiting` for its own
  background work, `idle`, `completed`). Its `cwd` is ignored, and a mod may not call the Agent tool or the Workflow
  tool itself ("`$.agent.spawn` is the door").
- **baton can place each one in its own worktree.** An agent type the mod registers with `isolation: worktree`
  runs in the path baton's `WorktreeCreate` hook returns, named `agent-<agentId>`: outside the checkout, on a branch
  baton names. Claude Code keeps a hook-made worktree when the agent finishes. The merge queue can land it as it
  lands a worker's branch today.
- **baton's hooks reach every phase agent.** `SubagentStart`'s context reached the agent, a `SubagentStop` block ran
  it on, and every tool call came with the agent's id. A permission prompt's `PermissionRequest` came with the
  agent's id while `$.agent.list()` still said `running`.
- **A message resumes an idle agent**, with its conversation, in its worktree: also after `kill -9` and
  `--resume`, when `$.agent.list()` no longer lists it.
- **Its report would wake the lead.** An agent resumed by the mod reports to the lead's model as a
  `<task-notification>` prompt, and the lead acted on it. The mod's `prompt.submit` hook sees it with
  `origin.kind: task-notification` and can drop it.
- **The lead can be compacted and woken without typing.** `$.session.compact()` and `$.prompt.submit()` work from a
  mod timer (they are refused from inside a slash command's own event). Compacting the lead while phase agents ran
  left them running.
- **A question can reach the human without a model.** `$.ui.ask` opens Claude Code's question dialog in the main
  session, and the answer comes back to the mod.
- **A phase agent compacts by itself, but its compaction looks like the lead's.** `PreCompact`, `SessionStart(compact)`
  and `PostCompact` came with no `agent_id` and the lead's transcript; only `prompt_id` was the agent's. The mod's
  own `session.compact` event names the agent. The agent kept its brief through the summary, not because Claude Code
  gave it again.
- **A workflow agent cannot wait.** Hooks reach workflow agents too, but a workflow agent's turn is its whole life:
  one whose command went to the background ended its turn "waiting…", and that was its result.

## What spike 32 found

- **Where the mod spawns from decides what it sees.** A module never sees the events that come from inside its own
  calls: Claude Code's re-entry skip. Called from a hook, only that hook is skipped. Called from a timer, which runs
  outside any event, the whole module is. So of an agent the mod spawned from a timer, as a command poll would, it
  sees only its `turn.complete` and its reports to the lead: not its tool calls, its settings events or its
  compactions. Spawned inside a hook (a command's, or the band's render), it sees all of them. But the only hook
  baton could trigger is the render, and the band is not drawn while a dialog or `/tasks` is open.
- **A message waits for the agent's next tool round.** Sent while the agent runs a command, while it compacts, or
  while it is idle (which resumes it), it was delivered after the command, after the compaction, or at once. None
  was lost. `$.session.send` and the SendMessage tool behave alike; the first is the second underneath, and the
  lead's `PreToolUse` sees it.
- **A compaction's proof is in the loop's own transcript.** Over four compactions, the lead's boundary was written
  154–175 ms after `PostCompact`. An agent's goes to its own `subagents/agent-<id>.jsonl`, never the lead's, so a
  rewake waiting for the lead's boundary did not wake the lead for an agent's compaction. The mod's `classic.*` hooks
  can add a field the settings hooks receive. But the mod's view of the compaction events has no agent id either, so
  it could name the loop only from its own bookkeeping, which two compactions at once would confuse.
- **One rewake carries the brief after any compaction of the lead.** Registered for every trigger, waiting for the
  boundary, it reached an idle lead at once and a lead mid-turn at its next tool round, and the model followed it.
  It reaches the mod as a `task-notification`, the same kind as an agent's report.
- **The human's keys and agents.**
  - Esc and Ctrl-C in the lead abort the lead's turn alone. A rewind that restores the conversation fires no hook
    and leaves agents alone. With no code changes, the rewind menu's second option is "Summarize from here", which
    compacts.
  - **`/clear` ends every agent.** It kills their running commands. Afterwards an idle agent cannot be resumed, and
    one that was running finishes its turn (its hooks now with the new session's id) and cannot be messaged again.
  - Esc in an agent's transcript view interrupts the agent: a later message resumes it, and it takes the
    interruption for a rejection of its task. x on its row stops it, and every later message is refused ("was
    stopped by the user … only start a new agent for it if the user explicitly asks"). Both end with `aborted` and
    no `SubagentStop`. A message typed to an agent raises no hook; it waits for the agent's next tool round.
- **baton's question beside other dialogs.** `$.ui.ask` opens during a lead turn without stopping it. Opened while a
  permission prompt is up, its hooks fire at once, but it waits behind the prompt, and a key goes to the prompt in
  front. **In 2.1.296 a permission prompt's third option is "Yes, and switch to auto mode"**: the 3 baton's host
  types at a deadline approved the command and turned auto mode on.
- **Errors.** Claude Code retries an overload and a lone bad request inside the agent's turn. A usage limit ends the
  turn at once: `turn.complete` `error`, `StopFailure` with the agent's `agent_id` and `rate_limit`, status `failed`.
  A message after the limit resumes it.
- **Ending one.** `TaskStop` from the mod ends a running agent (`aborted`), and a later message would resume it.
- **Claude Code's own forks of an agent**, about every 30 s while it runs, fire hooks with ids of their own, an empty
  `agent_type` and the agent's worktree as `cwd`, once a `PreToolUse` that never ran.
- **Reloads, failures, the store.** A saved mod file reloads the module: timers restart, `$.store` keeps its values,
  running agents go on. A hook that throws lets its event through. `$.store` is one file per plugin, shared by every
  session that loads it.
- **An agent's context** is in its transcript's last assistant entry (within a tool result of the compaction's
  count), and it compacts where the lead does.
- **Four agents for 20 minutes:** 156 turns and 151 messages without a failure, 152 reports dropped before the
  lead's model, and claude's memory flat (286–445 MB).

## What spike 33 found

- **`/baton board` is the skill's command, answered by the mod.** Claude Code refuses a mod command named
  like its plugin's skill (`"/baton" refused: it is the plugin's /baton:baton`), but a mod's `command.run`
  hook sees the skill's command as `baton:baton` with its arguments, and can answer it itself: the skill
  never runs and no model is called ([`spikes/33-board-command`](../../spikes/33-board-command)). A skill's
  command waits for the turn to end, so the board also has a command of its own, `/baton-board`, which is
  `immediate`.

## What spike 34 found

- **baton's question can be settled at its deadline with no key typed**
  ([`spikes/34-ask-deadline`](../../spikes/34-ask-deadline)). The mod's own `tool.call` hook for
  `AskUserQuestion` sees a question the mod asks from inside its band's render hook (never one asked from a
  timer), and the model's own; returning at the deadline, with the tool's output shape, closes the dialog, and the
  question resolves to that answer (the model reads it as the human's). No `PostToolUse` fires then, so the answer
  reaches baton through the mod's report. A question asked from the render is never asked while a permission prompt
  or any other dialog is open: the band is not drawn then.

## What it means for baton

The control baton needs exists, and none of it is model judgment. A mod in baton's plugin does what only something
inside the session can do (spawn, message, compact, submit, ask, draw, drop a notification), on baton's say-so; the
settings hooks baton already has do the rest, now told apart by `agent_id`. Phases become subagents of the lead:

| baton's worker today | A phase as a subagent |
|---|---|
| a hidden `claude` in baton's PTY, `--session-id` linked to a sub-run | a background agent the mod spawns, of a `baton:phase` type; its `agentId` linked to the sub-run |
| baton's worktree, made before launch | the same worktree, made by baton's `WorktreeCreate` hook (`agent-<agentId>`) |
| brief at `SessionStart(startup)` | brief at `SubagentStart`, and again after the agent compacts |
| the worker's Stop hook refuses a stop without a status | `SubagentStop` blocks, with the same text |
| mid-turn requests as PostToolUse feedback | unchanged, routed by `agent_id` |
| a gate's result typed or given at the next tool call | the idle agent resumed by a message carrying the result |
| a worker's question relayed through the lead's model | `$.ui.ask` from the mod, or the lead as today |
| a permission prompt seen only with `baton view` | shown in the lead's terminal by Claude Code, and known to baton by `agent_id` |
| a crashed worker resumed by baton | the session resumed (v0.7), then each agent by a message |
| `baton view P3` in another terminal | the agent's row and transcript in the same terminal |

What the human sees, in the terminal they already watch: one row per phase agent with its status, its transcript a
keystroke away, its permission prompts and questions in place, and a band or pane of baton's own with the run as a
whole: the steps of the plan (the phases that may run at once), each phase's state, and what the lead is doing.

Unchanged: the plan and its graph, lanes and jobs, the merge queue and its rules, decisions and escalation, and the
lead hosted in baton's PTY (crash recovery, the passthrough).

### What spike 32 changes

- **The mod spawns from its poll timer** and reports what it sees from there: each agent's turns, its reports to the
  lead (which it drops), and the lead's own compactions. It needs no more.
- **An agent's compaction is proven by its own transcript.** A new boundary in `subagents/agent-<id>.jsonl` is that
  agent's and no one else's, as one in the lead's transcript is the lead's. The stamp is not used.
- **Every delivery to an agent is a message, whenever it is due:** Claude Code holds it for the agent's next tool
  round, so nothing waits for an agent's compaction.
- **The host never types 3, or any option's key, into a dialog.** A phase agent's permission prompt now shows in the
  lead's terminal beside baton's own question, so v0.8 settles a question's deadline without typing into it.
- **`/clear` ends every phase agent.** baton starts each unfinished phase again, in its worktree, from its notes, and
  links agents by id, not by session.
- **An interrupted agent** (`aborted`) gets one message to go on, saying the phase continues. If Claude Code refuses
  it as stopped by the user, the phase is the human's: baton sets it aside and says so.
- **Hook events from Claude Code's forks of an agent** (an id baton did not spawn, in that agent's worktree) change
  nothing, and never count for the lead.
- **`$.store` is shared by every session**, so the mod keys what it keeps there by run.

### Why not workflows

Workflows give the view the human asked for (groups of agents that run at once, then the ones after them), but not
the control. A phase waits on gates, questions and the human; a workflow agent's turn is its whole life, it takes no
input, and the script cannot change when the plan does. A workflow starts only through the model's `Workflow` call,
which a mod may not make. baton can draw the same grouping in its own band or pane, from the plan's graph.

## Open questions

- **A mod is JavaScript that Claude Code runs.** baton's rule is no runtime dependencies for users: one static
  binary. A mod adds none (Claude Code is the runtime, as it is for the skill), but it is code baton ships in
  another language. The human accepted it on 2026-10-12. Where an organization turns user mods off
  (`allowManagedModsOnly`), baton runs the plan serially in the lead.
- **Compaction hooks without `agent_id`.** Answered for baton by the transcripts (spike 32), but worth reporting to
  Claude Code: a settings hook cannot tell whose compaction it is.
- **A deadline on baton's question** with no key typed: answered by spike 34, where baton's mod runs. Where it
  does not (serial mode), v0.7's typed answer stays, guarded by baton's question being the only dialog open.
- **Not yet seen:** phases that run for hours, and a real usage limit (spike 32 injected one).
- **One process.** Every phase agent lives in the lead's claude: one crash takes them all down (and v0.7 brings them
  back), and they share its memory and its rate limits.
