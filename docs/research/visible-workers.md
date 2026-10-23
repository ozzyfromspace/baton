# Workers you can see: phases as subagents of the lead

**In short:** since v0.8, each phase beside the lead runs as a background subagent of the lead's own session, which
Claude Code shows in its agent panel. A mod in baton's plugin spawns and messages the agents, asks the human and drops
the agents' reports, on the host's say-so; baton's hooks, told apart by `agent_id`, brief and hold them. An agent's
compaction hooks look like the lead's, so only a proof (the mod's report, or a loop's own transcript) moves a
compaction. Every delivery to an agent is a message, `/clear` ends every agent, and nothing types into a dialog where
the mod runs. A phase's work lands by a merge, never a rebase (v0.8.1). A Ctrl-C at an idle prompt stops every
background agent, so where the mod runs baton types nothing: the mod compacts and submits, and the human's draft
stays in the box; in auto mode agents' reports come back as peer prompts, which the mod drops (v0.8.2). A real run
taught that nothing may wait on a phase agent's compaction: a sub-run never owes one, so an agent's red gate goes
back to it; agents start 2 minutes apart, their `--here` gates take lane slots, and a run that stops moving tells the
human (v0.8.2). A compaction Claude Code refuses while the lead's turn ends is run from the mod's `turn.complete`
hook (v0.8.5). Workflows were ruled out: a workflow agent cannot wait. Since v0.8.16 baton runs every merge itself:
a landing's merge that conflicts is left in progress in the agent's worktree for it to resolve and commit, since auto
mode refused a real run's agent its own `git merge` twice, and every phase left waited 4h50m.

Measured 2026-10-12 against **Claude Code 2.1.296** (macOS, arm64) with Haiku, from Claude Code's documentation and
two spikes: [`spikes/31-mod-agents`](../../spikes/31-mod-agents) (can a mod drive phase agents at all; eight
scenarios) and [`spikes/32-phase-agents`](../../spikes/32-phase-agents) (what baton's design for it assumes; fourteen);
and 2026-10-15 against 2.1.300 and 2.1.296 by [`spikes/35-mod-drives`](../../spikes/35-mod-drives) (what a mod can do
in place of baton's keys, and what the keys do to agents). Each spike's README has its table and harness.

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

## What the end-to-end runs found (P7)

Real phase agents on Claude Code 2.1.296 with Haiku, in baton's own e2e scenarios:

- **A hook's `prompt_id` is the session's latest prompt, not the call's.** The lead's first prompt landed between
  the mod's spawn and its `WorktreeCreate`, so prompt ids cannot tell a phase's worktree from one the model's own
  agent asks for; baton tells them apart by which Agent calls are open (`Runtime.AgentCalls`).
- **The worktree-isolation guard refuses shell text handed to a program it does not know.** In an agent isolated in
  a worktree, a Bash command whose argument is several words starting with a shell or git (`baton await 'sh
  hold.sh'`, `baton verify 'bash x.sh'`) is refused as "cannot be shown not to be git", after `PreToolUse`, as a
  `PostToolUseFailure`. The same command as words after `--` runs (`baton verify --notes '…' -- sh hold.sh`, also
  `-- sh -c '…'`), and so does a quoted command that starts with anything else (`'make test'`). The guard reads
  the command as a `PreToolUse` hook rewrote it (`updatedInput`): baton's own hook gives a phase agent's quoted
  gate as words there, so nothing depends on how the agent writes it. A gate that names git is refused in either
  form.
- **An agent of a type the session does not offer cannot be resumed.** A message to a completed `baton:phase`
  agent failed ("Agent type 'baton:phase' is not offered in this session") while the mod hid the type
  (`agent.offer` → `isOffered: false`); spike 32's agents resumed because their type was offered. The type stays
  offered, and baton's `PreToolUse` refuses a model's own call to it, as it refuses a model's message to one of
  baton's agents.
- **A resumed agent is given nothing of `SubagentStart`.** When a message starts an idle agent again, its
  `SubagentStart` hook runs, but Claude Code does not put the hook's `additionalContext` in front of the model (an
  agent's first start gets it): an answer baton gave there never reached the agent, which said so and waited.
  What baton has for a resumed agent comes with its next tool call (`PostToolUse`) or its stop (`SubagentStop`,
  refused with it), and the message that wakes it says so.

## Why landing merges (v0.8.1)

In a real v0.6 run (a worker session in auto mode, on a large private project), the merge queue's rebase of a
phase's branch conflicted, and the worker was told to `git rebase` the run's branch itself. Claude Code's auto-mode
classifier refused that as "[Git Destructive]". The worker replayed its commits onto a side branch by cherry-pick,
which passed its gate, then asked to move its phase branch there (`git checkout -B`) and delete the side branch. The
word "delete" made the proposal untimed, so the phase waited for the human. The same conflict scenario on a toy
repository (P7's, without its `Bash(git:*)` allow rule) went through: the classifier judges by context, and a small
case does not show its refusal.

Checked by hand with git 2.50 (`git rebase` without `--rebase-merges`): rebasing a branch that holds a merge drops the
merge commit and replays the branch's own commits. Usually that meets the same conflict again; when the run's branch
has since taken its side back, the replay goes through and the resolution is gone, without a word. `git merge`
rewrites nothing. A merge that stops on a conflict, or on a merge commit that cannot be signed, leaves `MERGE_HEAD`,
and `git merge --abort` gives back the branch, index and files exactly; an untracked file in its way makes git
refuse before it changes anything; `merge --abort` cannot always give back an uncommitted change to a tracked file,
so baton merges only on a worktree with none. So baton merges the run's branch into a phase's branch, never rebases
it, and a conflict goes back to the agent as a merge.

## Why baton runs the merge (v0.8.16)

A real run on v0.8.14 (2026-10-23, phase agents in auto mode, the human asleep) lost 4h50m to one agent's merge, and
every phase left waited behind it:

- **05:23:** the agent proposed merging the run's branch into its own, to pick up work another phase had landed.
  `decide.Outward` counts "merge" as outward, so the proposal's deadline answered Not now, and its brief said to
  merge only when baton asked.
- **05:35:** its landing conflicted in two files. baton's merge was aborted, as v0.8.1 had it, and the agent was told
  to run `git merge <run branch>` itself.
- **Claude Code's auto-mode classifier refused that twice:** as "[Modify Shared Resources]", then the retry as
  "[Auto-Mode Bypass]". It misread the direction: the merge changes only the agent's own branch.
- **05:36 to 10:27:** the agent blocked. The block lapsed every 30 minutes and came back each time, and every
  question told the human that only that phase waited, while nothing else in the run could move.
- **10:27:** the human, just awake, answered Continue with no context, and with that answer in the agent's context
  the classifier let the same command through.

It is the second refusal of its kind, after v0.6's `git rebase` (above). The classifier judges a command by its
context, reviews every command a model runs, and changes between releases; baton cannot know in advance what it
will refuse, and must not work around it. What baton can do is not ask. A merge of the run's branch into a phase's
branch is baton's own landing step, which its host already ran for every clean landing. So since v0.8.16 baton runs
every merge, and a model only resolves files, `git add`s them and commits:

- A landing's merge that stops on conflicts is left in progress in the agent's worktree (`MERGE_HEAD`, and git's
  markers in the files) instead of aborted. The agent resolves each file, adds it and runs `git commit --no-edit`,
  or `baton done`, which commits the merge once nothing is unmerged and no conflict marker is staged.
- An agent that needs another phase's landed work first runs `baton sync`, the same merge on its say-so.
- Every word to an agent says never to run `git merge`, `rebase`, `reset` or `merge --abort`, and none asks it to.

Nothing can be lost by leaving the merge to the agent:

- baton starts a merge only on a worktree with no uncommitted change to a tracked file, and git refuses before
  changing anything when an untracked file is in the way. So a merge in progress holds nothing but the merge, and
  `git merge --abort` would give back the branch, index and files exactly.
- baton never runs that abort once a model may have resolved files, which would throw the resolution away. A merge
  that stops for another reason (a merge commit that cannot be signed) stops before any model sees it, and is
  aborted as before.
- A phase reported done with its merge unfinished goes back to its agent, never landed half done. A fresh agent is
  told of the merge in its worktree first, since the message about it may have gone to the agent it replaced.
- The merge commit keeps both parents: the agent's commits stay as they were, and the run's branch only
  fast-forwards to it.

## What spike 35 found (v0.8.2)

A real v0.8.1 run (2026-10-13) showed two things the earlier spikes had not: a phase agent ended "stopped by the
user" 42 ms after baton's draft rescue cleared the input box with Ctrl-C at an idle prompt, and agents' reports
reached the lead's model as peer messages, which baton took for the human's turns.
[`spikes/35-mod-drives`](../../spikes/35-mod-drives) measured both, and what the mod can do in place of baton's keys,
on Claude Code 2.1.300 and 2.1.296 with Haiku:

- **One Ctrl-C at an idle prompt stops every background agent**, with a draft in the box or without ("All
  background agents stopped"; the agent's turn `aborted` 10 to 20 ms later). A message afterwards is refused: "was
  stopped by the user and won't be resumed". This covers the model's own background agents too; background shells
  run on. Ctrl-U stops nothing, but deletes the draft. Spike 32 had tried Ctrl-C only during a lead turn, where it
  stops that turn alone.
- **The mod can compact the lead.** `$.session.compact()` from its timer runs every settings compaction hook with
  `trigger: manual`, writes the boundary about 130 ms after `PostCompact`, and the asyncRewake hook wakes the lead
  with its brief, as for a typed `/compact`. The promise resolves just after `PostCompact`. The mod's own
  `session.compact` hook does not run for its call, so it reports from the promise. **The human's draft stays in the
  box** through the compaction and the turn the brief starts. While a turn runs the call is refused at once, naming
  the turn; a dialog does not stop it.
- **The mod can submit baton's lines.** `$.prompt.submit` starts a turn of its own with the draft left in place, and
  during a turn it waits for the turn's end. With `asUser: true` the model reads the line bare, as it reads a typed
  one; without it, framed as the plugin's message. `$.prompt.read` and `fill` make an exact round trip of a draft, and
  `fill` is refused under a dialog (`refusal: "dialog"`).
- **In auto mode, an agent's report is handed back as a peer prompt** (`<agent-message from="P2">` then `[Subagent
  hand-back]`), into a running turn or as a turn of its own, at every end of every background agent; an agent resumed
  by a message also gives a `task-notification`. In other modes there is no hand-back: a fresh agent's end sends the
  lead nothing, and a resumed one's a `task-notification`. All 190 hand-backs in this machine's transcripts
  (2.1.278 to 2.1.296) came in auto mode, the mode the human's sessions run in. The mod's `prompt.submit` event
  carries only `origin: {kind: "peer"}`, so the mod tells baton's hand-backs by that frame, and its drop works; the
  settings hooks received no `source` for any prompt, so the host tells machine prompts by their text.

So in v0.8.2, baton types nothing where the mod runs: compactions and baton's lines go through the mod, and the box is
never touched. In serial mode, where it still types, it never sends Ctrl-C while an agent may be live.

## What spike 36 found (v0.8.4)

baton held the lead's compaction, its deliveries and its switching while any dialog was open, so one phase agent's
permission prompt held the whole run until the human answered it. [`spikes/36-agent-dialog`](../../spikes/36-agent-dialog)
measured, on Claude Code 2.1.300 with Haiku, what goes on under an agent's prompt, and how each way of stopping an
agent looks:

- **An agent's permission prompt holds nothing of the lead's where the mod runs.** With one open, the mod's
  `$.session.compact()` compacted the lead (the hooks, the boundary and the rewake's brief as in spike 35) and the
  prompt stayed on screen, unanswered, through the compaction and every turn after; `$.prompt.submit` started a lead
  turn that ran beneath it. Prompts queue in the order they open, one on screen: the lead's own call that needs one
  waits behind the agent's. The `PermissionRequest` hook of an agent's call has `agent_id` and `agent_type`; the
  lead's has neither.
- **No band, so no question, under any dialog.** The mod's `AbovePrompt` band is not drawn while a prompt is on
  screen, the lead's or an agent's, and the mod asks from the band's render, so baton's questions wait for every open
  prompt to close. Their deadlines run meanwhile.
- **Keys typed into an agent's prompt approve it.** `/compact` or a line, then Enter, chose the prompt's `1. Yes`:
  the agent's command ran, and nothing compacted. Serial mode, which types, keeps holding on every dialog.
- **The notifications:** `idle_prompt` never came while a prompt was open (none in 4.5 idle minutes); the
  `permission_prompt` notification, 6 s after a prompt opens, has no `agent_id`.
- **A human's stop looks the same every way, and only to a message.** x on a running row, ctrl+x ctrl+k (pressed
  twice), Ctrl-C at the prompt and Esc in the agent's view each end its turn `aborted` and its row `killed`, with no
  hook, and the mod's SendMessage after is refused: "was stopped by the user and was not resumed". baton's own
  TaskStop looks the same up to there, but a message resumes the agent. Only ctrl+x ctrl+k tells the lead, as a
  `task-notification` prompt. A finished agent has no row to stop.
- **`/clear` kills the command an agent runs, not the agent.** It goes on to the end of its turn under the new
  session, takes messages meanwhile, and only then is refused: "could not be resumed: … its worktree is not recorded
  for this isolated agent". baton's `cleared` already waits for that.

So v0.8.4 lets a phase agent's dialog hold only that agent where the mod runs, while the relay and the stall alarm
still see it; serial mode keeps its hold; and a stopped agent is still told by the refusal its next message gets.

## What spike 37 found (v0.8.5)

In a real run on v0.8.3 and v0.8.4, the lead's compaction went the same way 15 times in 13 hours: the host asked
for it within 100 ms of the lead's Stop hook, Claude Code refused it ("a turn is running …; the conversation compacts
between turns, so call it from turn.complete or later"), and the host asked again about 3 seconds later, which
worked. The Stop hook runs a moment before Claude Code counts the turn over.
[`spikes/37-compact-at-turn-end`](../../spikes/37-compact-at-turn-end) measured, on Claude Code 2.1.301 with Haiku,
where a mod's call is taken:

- **From the poll timer while the Stop hooks run**, it is refused at once, as in the run. The lead's `turn.complete`
  came 4 to 6 ms after the classic `Stop` ended.
- **From the lead's `turn.complete` hook** (before or after `next(e)`, or a timer started there), it is taken and
  compacts. A timer started in a classic `Stop` hook carries that hook's context: before `next(e)` it is refused.
- **The mod's own `session.compact` hook runs for a call made inside one of its hooks** (`trigger: plugin`), unlike
  one from its timer (spike 35), so the mod tells its own call apart.

So in v0.8.5 the mod holds a compaction refused while the lead's turn runs (by its own `turn.start` and
`turn.complete`), and calls it from that turn's `turn.complete`, once. Past 30 seconds it acks the refusal, and the
host asks again after the turn, as before.

## What a real run taught (v0.8.2)

The same v0.8.1 run, on a large private project with four phase agents beside the lead in auto mode, stopped for
hours with nobody told. Its events and Claude Code's transcripts showed eight faults, each since confirmed in the code
and replayed by an end-to-end scenario on real Claude Code (`test/e2e/nostall_test.go`):

- **A phase agent's red gate stalled the run for three hours.** The agent ran `baton done` while its gate ran, and the
  gate failed. Its sub-run reopened the phase as the lead's run does, with a boundary owed and a compaction queued.
  But nothing compacts a phase agent: while that compaction was in flight the sub-run delivered nothing, every tool
  call of the agent was held, and each loop's watchdog stayed quiet, since a compaction ends by itself. A landing's
  re-verification that fails fell into the same trap. So a sub-run never owes a boundary: the phase is taken up
  again in place, and the result goes to its agent, or to a fresh one in its brief. And since every watchdog had a
  reason to wait, the run as a whole is now watched too: nothing moved for 30 minutes, with nothing at work, pushes
  the human one notice.
- **baton's Ctrl-C ended a phase agent** (spike 35, above) and threw the human's draft away. baton then took the
  agent's end for the human's doing and held its phase for over an hour, said only in the session. Now a held phase
  is pushed and shown on the board.
- **A compaction while paused did not count.** The human paused at a boundary and ran `/compact`; `/baton run`
  compacted again a minute later. It now covers the compaction the run owed.
- **Agents' reports were taken for the human's turns** (spike 35): each hand-back released baton's holds and handed
  the lead the run's decisions as if the human had typed.
- **Agents started faster than the machine showed their load.** Four started within seconds, at a load above the
  machine's CPU count: the one-minute load average baton read had not caught up. Their `verify --here` gates, each
  bringing up services of its own, ran at once outside any lane. A linter was killed for memory, browsers crashed,
  and two test runs shared one test database. So agents start 2 minutes apart (v0.6 had a one-minute ramp, which
  v0.8 dropped), and an agent's here gate holds a lane slot, with `BATON_LANE` set.
- **The human's edit to the plan never reached the agents at work:** a sub-run's plan was fixed when its agent
  started. Now each change to the standing rules or an agent's section reaches it as a message.
- **Work landed by hand could not be recorded.** The lead's `baton done` of a worker's phase was refused, so the lead
  attached the plan again over the run, which took the finished phases off the board and kept nothing of the
  replaced plan's record. Now that `done` records the phase landed when its branch is on the run's branch, a
  replaced plan is archived, and attaching while paused stays paused.
- **A turn the human asked for was cut short:** a gate's verdict made baton owe a boundary at once, and every tool
  call in the human's turn was refused, with a sentence saying the phase was done when it had just failed. Sentences
  built from the phase in hand also had a blank where it went when the lead held none.

The end-to-end runs added two things. Claude Code takes the permission mode from the user's own settings, so every
scenario ran in auto mode, and every agent's end came back as a hand-back, which the mod dropped. And with the mod
compacting it, the lead's compaction is over within seconds of baton's ask; a restarted host waits for its new mod's
hello before it asks, and a mod of another release (another protocol) makes the run serial, where baton types
`/compact`, as designed.

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
| a worker's question relayed through the lead's model | `$.ui.ask` from the mod, settled at its deadline by the mod's own `tool.call` hook (spike 34) |
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
  does not (serial mode), v0.7's typed answer stayed, guarded by baton's question being the only dialog open, until
  v0.8.6 replaced its `3` with an Esc that picks no option anywhere
  ([spike 39](../../spikes/39-escape-answers/README.md)).
- **Not yet seen:** phases that run for hours, and a real usage limit (spike 32 injected one).
- **One process.** Every phase agent lives in the lead's claude: one crash takes them all down (and v0.7 brings them
  back), and they share its memory and its rate limits.
