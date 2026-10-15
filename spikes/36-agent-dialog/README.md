# 36 — a phase agent's permission prompt on the lead's screen, and how a stopped agent looks (Claude Code 2.1.300, Haiku 5.5, macOS arm64, 2026-10-15)

Question: baton holds the lead's compaction, its deliveries and its switching while any dialog is open, and a phase
agent's permission prompt opens on the lead's screen, so one agent's prompt holds the whole run until the human
answers it (v0.8.4's item 2). Where the mod runs, may baton go on under an agent's prompt? With a phase agent's
permission prompt open: does `$.session.compact()` from the mod compact the lead, and does the prompt survive it? Does
`$.prompt.submit` reach the lead, and does the lead's turn run? What tells an agent's prompt from the lead's? And for
v0.8.4's item 10: is an agent "stopped by the user" told from one `/clear` ended, from the lead's Ctrl-C, and from
baton's own TaskStop?

Harness: spike 35's. `run.py <scenario> <workroot>` runs claude in a PTY in a fresh git repo (raw bytes kept, so
`pyte` renders the screen as drawn), with the mod (`mod/hooks/register.js`, spike 35's with more logging, a copy per
session) and spike 35's settings hook on every event (`../35-mod-drives/hook.py`, logging every hook's input to
`hooks.jsonl`, with the asyncRewake `PostCompact` hook that wakes the lead with a brief whose codeword is OSPREY). The
mod logs every tool call's verdict (`tool.check`, with the loop's `agentId`) and end (`tool.call`), every
`turn.complete` in full, the classic `PermissionRequest`, `Notification`, `SubagentStop` and `SessionEnd`, and each
draw of its band above the prompt (invalidated every second, so a gap means the band was not drawn). `SPK_ON_ASK` is
a plan of timed actions that starts when one of its agents' calls is put to the human (`tool.check` → `ask`), since
keys typed while that dialog is up would go to it: compact, submit (`asUser`), read the box, ask from the band's render
hook (`renderask`), as baton's mod does. `/spk-go <name>:<template>` spawns a background agent from a hook, `/spk-msg`
sends one a message (`$.tool.call` SendMessage, as baton's mod sends), `/spk-at <ms> stop <name>` stops one with
TaskStop, `/spk-list` lists them. Templates: A runs `sh ask.sh` then `sh ask2.sh`, which no rule allows (two
permission prompts); L runs a 60-second command three times; Q runs `pwd`. `digest.py <work>` merges both logs in
time order.

| scenario | what | result |
|---|---|---|
| D (a) | default mode: a phase agent's permission prompt open (`sh ask.sh`), the lead idle; 5 s after it opened, `$.session.compact()` from the mod's timer | **It compacts, and the agent's prompt survives.** `PreCompact`, `SessionStart` `source: compact` and `PostCompact` (`trigger: manual`) ran as in spike 35, the promise resolved 5.7 s after the call (45,126 → 3,485 tokens), the boundary was written 133 ms after `PostCompact`, and the asyncRewake hook **woke the lead with its brief**, whose turn ran (10 s) with the prompt still open below it. The prompt stayed on screen, unanswered, through the compaction and every turn after; answered 5.5 minutes later, its command ran and the agent went on. |
| D (b) | the prompt still open: a question from the band's render hook (`renderask`), 30 s after it opened, as baton's mod asks | **Not asked until the prompt closed: the band is not drawn under a dialog.** The `AbovePrompt` render hook ran every second until the agent's call was put to the human (its `tool.check` → `ask`), then not once in the 5.5 minutes the prompt was open; nothing else draws it, so the question set at 30 s waited. The first render after the prompt was answered asked it at once (60 ms after the key); it opened as the lead's `AskUserQuestion` dialog (`PermissionRequest` with no `agent_id`), and the agent's next prompt, 2.5 s later, queued behind it. The same gap in every scenario: the band is not drawn while any permission prompt or question is on screen, the lead's or an agent's. |
| D (c) | the prompt still open: `$.prompt.submit({text, asUser: true})` 45 s after it opened, a prompt whose command (`pwd`) a rule allows | **The lead's turn runs under the agent's prompt.** The promise resolved 66 ms after the call, as the turn started; the lead ran `pwd` and answered PWDONE in 7 s, the agent's prompt still open below. |
| D (d) | the prompt still open, the lead idle for 4.5 minutes; `$.prompt.read()` at 3 s, 75 s and 180 s | **No `idle_prompt` notification while the prompt is open:** none in the 274 s the lead was idle under it, and one 60 s after the session's last turn once nothing was open. **The `permission_prompt` notification names nobody:** "Claude needs your permission" came 6 s after each prompt that stayed open (the agent's and the mod's question alike), with no `agent_id`. Each read gave the empty box in 1 ms. |
| E (e1) | default mode, an agent's prompt open: `/compact` typed and Enter pressed, as serial mode types | **Enter approves the agent's command.** Typing left the prompt on screen and nothing in the box, and Enter chose its selected option, `1. Yes`: the agent's `sh ask.sh` ran 2.7 s later. Nothing compacted. |
| E (e2) | the agent's next prompt open: one of baton's lines typed (`[baton] Reply with only the word BACK.`) and Enter | **The same: Enter approved the command** (`sh ask2.sh` ran 0.5 s after the key), and the line was lost. |
| E (f) | the lead's own prompt (`sh lead.sh`), no agent at work | Its `PermissionRequest` has no `agent_id` or `agent_type`; an agent's has both (`agent_type: "spk:phase"`), and so has the mod's `tool.check` (`agentId`). On screen the lead's prompt reads `Bash command` with four options (don't ask again, switch to auto mode); an agent's reads `Bash command · from the spk plugin`, "… in the worktree", with two. |
| E (g) | an agent's prompt open, then the mod submits a lead prompt whose command needs one too | **The prompts queue in the order they opened, one on screen**, with a counter (`1 of 2`; `2 of 3` once the agent's next prompt queued). The lead's turn started under the agent's prompt and its own call waited behind it: the first `1` answered the agent's, the second the lead's, which had opened before the agent's next one. |
| X1 | acceptEdits mode: `x` on a running agent's row in the panel | `turn.complete` `aborted` at the key (10 ms), its row `killed`, no `SubagentStop`, no prompt to the lead. **A message after is refused:** `{"success": false, "message": "Agent \"…\" was stopped by the user and was not resumed. Treat its work as cancelled; only start a new agent for it if the user explicitly asks."}` |
| X7 | `x` on an idle agent's row | A finished agent has **no row**: 25 s after its end the panel was gone and `x` went into the box. A message **resumed** it (`"Resuming agent …"`): a new `SubagentStart`, its answer, and a `task-notification` to the lead. |
| X2 | ctrl+x ctrl+k at the prompt | The first press asks "Press ctrl+x ctrl+k again to stop background agents"; the second stops every background agent: `aborted`, `killed`, the same refusal as X1. **The lead is told**, as a `task-notification` prompt (`Background agent "X2 — spike phase" was stopped by the user.`) that started a lead turn. |
| X3 | Ctrl-C at the idle prompt, the box empty | As spike 35: `aborted` 20 ms after the key, `killed`, the same refusal as X1. Nothing reaches the lead. |
| X4 | Esc in the agent's transcript view (Enter on its row opens it: `Message @X4…`) | The first Esc moved the focus from the panel to the view's box; **the second stopped the agent** (`aborted` 80 ms after it, `killed`), and the mod's message after was refused as in X1. Spike 32 (2.1.296) resumed an agent stopped this way by typing in its view's box, a route baton has not; to SendMessage it is a stop for good. |
| X5 | the mod's TaskStop (`$.tool.call`), as baton retires an agent | `Successfully stopped task`, `aborted`, `killed`, no `SubagentStop`: the same as a human's stop up to here. **But a message resumes it** (`"Resuming agent …"`), and it answered. |
| X6 | `/clear`, an agent at work | `SessionEnd` `clear` and `SessionStart` `clear`; **the agent's running command is killed** (exit 137, `PostToolUseFailure`), **but the agent goes on**: its row stays `running`, its model ran the command again (60 s), and its hooks carry the new session's id. A message meanwhile is queued (`"Message queued for delivery … at its next tool round"`) and delivered: it answered. Once its turn ended (`SubagentStop` under the new session), **a message is refused:** `"Agent \"…\" is stopped (completed) and could not be resumed: This agent cannot be resumed: its worktree is not recorded for this isolated agent, and the fallback directory is not covered by the session's isolation fences."` |

Each scenario ran once. D's own (e) to (g) ran with a slip in the harness (it matched each call's end by its command,
which `tool.call`'s event does not carry), so its keys went astray there; E ran them again with each end matched by
the call's id.

Every `turn.complete` of a stopped agent was `{reason: "aborted", isAborted: true}`, with nothing saying who stopped
it, for the human's four ways and the mod's TaskStop alike; none gave a hook. `$.agent.list()` gave `killed` for
all five.

## What it means for baton (v0.8.4)

- **Where the mod runs, an agent's permission prompt need not hold the lead.** The mod's compaction runs under it
  and the prompt survives; a submitted line starts the lead's turn, which runs under it; the brief's rewake runs too.
  The lead's own call that needs a prompt waits behind the agent's, which is the human's to answer either way. So
  the gates on `Dialogs.AnyOpen()` (compaction, deliveries, switching) can let an agent's dialog by. The record must
  keep whose it is: the `PermissionRequest` hook's `agent_id` says (the lead's has none), and baton records it
  (`state.Dialog.Agent`).
- **baton's own questions cannot be drawn under any dialog.** The band is not drawn while a prompt is on screen, and
  the mod asks from the band's render, so a question waits for every open prompt to close, an agent's included. A
  relay question issued meanwhile is not issued again while a dialog is in front of it (`relay.go`), and its
  deadline runs, so nothing waits on it; the relay and the stall alarm must go on seeing an agent's dialog.
- **Serial mode keeps today's hold.** Enter in a permission prompt picks `Yes`: baton typing `/compact` or a line
  would approve an agent's command and lose the line. In serial mode every open dialog holds baton's typing, as now.
- **The notifications stay as they are.** `idle_prompt` never comes while a prompt is open (as `notification`
  assumes), and `permission_prompt` names no agent, so it can say nothing about whose prompt is open.
- **A human's stop is told only by a message's refusal.** x on a running row, ctrl+x ctrl+k, Ctrl-C and Esc in its
  view all end in the same refusal to the mod's SendMessage ("stopped by the user and was not resumed"), and no hook
  says which. An aborted turn baton did not cause is probed with a message (`fleet.go`), and `sendAcked` reads the
  refusal, as now. baton's own TaskStop leaves the agent resumable, so it is never taken for the human's. Only
  ctrl+x ctrl+k tells the lead (a `task-notification` prompt).
- **`/clear` does not end an agent at work:** it kills the command it runs, and the agent goes on to the end of its
  turn under the new session, taking messages meanwhile; only then is it refused, "could not be resumed". baton
  already waits for that turn's end before a fresh agent takes the phase up (`cleared`), so never two agents work in
  one worktree.
