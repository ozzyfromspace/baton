# Workers you can see: phases as subagents of the lead

Measured 2026-10-12 against **Claude Code 2.1.296** (macOS, arm64) with Haiku, from Claude Code's documentation and
one spike, [`spikes/31-mod-agents`](../../spikes/31-mod-agents) (eight scenarios; its README has the table and the
harness).

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

### Why not workflows

Workflows give the view the human asked for (groups of agents that run at once, then the ones after them), but not
the control. A phase waits on gates, questions and the human; a workflow agent's turn is its whole life, it takes no
input, and the script cannot change when the plan does. A workflow starts only through the model's `Workflow` call,
which a mod may not make. baton can draw the same grouping in its own band or pane, from the plan's graph.

## Open questions

- **A mod is JavaScript that Claude Code runs.** baton's rule is no runtime dependencies for users: one static
  binary. A mod adds none (Claude Code is the runtime, as it is for the skill), but it is code baton ships in
  another language, and an organization can turn user mods off (`allowManagedModsOnly`), where baton would need the
  hidden-session workers still.
- **Compaction hooks without `agent_id`.** baton must not take a phase agent's compaction for the lead's. The mod
  knows; the settings hooks would need its word, or the `prompt_id` test. Worth reporting to Claude Code.
- **Not yet seen:** how the agent panel, `/tasks` and baton's own pane look to a person (the spike's screens did not
  render cleanly), typing into a phase agent's transcript, a usage limit in a background agent, many agents for
  hours, and how a rewind or `/clear` in the lead treats its agents.
- **One process.** Every phase agent lives in the lead's claude: one crash takes them all down (and v0.7 brings them
  back), and they share its memory and its rate limits.
