# 35 — what a mod can do in baton's place, and what baton's keys do to agents (Claude Code 2.1.300 and 2.1.296, Haiku 5.5, macOS arm64, 2026-10-15)

Question: in a real v0.8.1 run (2026-10-13), baton's draft rescue cleared the input box with Ctrl-C at an idle prompt,
and 42 ms later a phase agent ended "stopped by the user"; and Claude Code handed agents' reports to the lead as a
peer message ("Another Claude session sent a message"), which the mod did not drop. v0.8.2 wants baton to type
nothing where the mod runs: compactions and baton's lines through the mod, the human's box never touched, and every
agent's report kept from the lead. What does each key do to agents, and what do `$.session.compact`,
`$.prompt.submit`, `$.prompt.read` and `$.prompt.fill` do when the mod calls them from a timer, as baton's command
poll runs? How does a report reach the lead, and what can the mod and the settings hooks tell of it?

Harness: spike 32's. `run.py <scenario> <workroot>` runs claude in a PTY in a fresh git repo, with the mod
(`mod/hooks/register.js`, spike 32's cut down, a copy per session) and a settings hook on every event (`hook.py`,
spike 32's unchanged: it logs every hook's input to `hooks.jsonl`, notes when a new `compact_boundary` appears in
the lead's transcript, and has an asyncRewake `PostCompact` hook for every trigger that waits for that boundary, then
wakes the lead with a 5,935-character brief whose codeword is OSPREY). `SPK_CLAUDE` names the claude binary, so a
scenario runs on 2.1.300 (current) or 2.1.296 (the incident's). The mod's `/spk-at <ms> <action>` runs one action
from a `$.clock.after` timer (compact, submit, read, fill, stash, restore, spawn), never awaiting a compaction's or
a submit's promise in the timer, and logs when it started and how its promise ended. A phase agent of the mod's
`spk:phase` type runs three 60-second commands beside the lead in each scenario but P, spawned inside a hook so the
mod sees its events (spike 32, S0). `digest.py <work>` merges both logs in time order. Sessions run in acceptEdits
mode unless the row says auto.

| scenario | what | result |
|---|---|---|
| K | Ctrl-C and Ctrl-U at an idle prompt while an agent runs: with a draft in the box and with it empty; a fresh agent each time, then a message to each one that ended | **Ctrl-C stops every background agent, draft or no draft.** "⏺ All background agents stopped", the agent's `turn.complete` `aborted` 10 to 20 ms after the key, its row `killed`, no `SubagentStop`. With a draft, the same key also empties the box; either way "Press Ctrl-C again to exit" follows. **A message after is refused:** `{"isDelivered":false,"reason":"Agent … was stopped by the user and won't be resumed. Treat its work as cancelled; only launch a new agent if the user explicitly asks."}`. This is the incident, reproduced. **Ctrl-U stops nothing**, with a draft (which it deletes) or without. |
| Kb | Ctrl-C at an idle prompt, no phase agent: a background Bash command (`sleep 90`) and a background agent the model started, as in serial mode | The model's agent was stopped (`aborted`, `killed`; the model said so when it next woke). **The background command ran on** and finished with exit code 0, notified as usual. The key stops agents, not shells. |
| C | `$.session.compact()` from the mod's timer, an agent at work: (a) idle, the box empty; (b) a draft in the box; (c) while a lead turn runs; (d) while the mod's question (`$.ui.ask`) is open | (a) **It compacts, and it looks like `/compact` to every settings hook:** `PreCompact`, `SessionStart` `source: compact` and `PostCompact`, each with **`trigger: manual`** (not `plugin`). The promise resolved just after `PostCompact` (5.3 to 7.5 s), `{messages, tokensBefore, tokensAfter}` (45,121 → 3,491). The boundary was written 127 to 136 ms after `PostCompact`; the asyncRewake hook found it and **woke the lead with its brief**, as a `task-notification` prompt ("Stop hook feedback"), and the model followed it (OSPREY). **The mod's own `session.compact` hook does not run for its call**; its `classic.*` hook sees `PreCompact` and `PostCompact`. The agent ran on through all three compactions and finished its task. (b) **The draft is kept:** `$.prompt.read()` gave `my draft during the compaction` (cursor 30) at four reads, from 25 s to 115 s after, through the compaction and the rewake's turn. (c) **Rejected at once**: `HooksError: spk: $.session.compact: a turn is running (<turnId>); the conversation compacts between turns, so call it from turn.complete or later`. Nothing compacted at the turn's end. (d) **It compacts under the dialog**, and the rewake's turn ran (OSPREY) while the question stayed open; the question was answered afterwards as usual. |
| S | `$.prompt.submit({text})` from the timer, an agent at work: (a) a draft in the box, the lead idle; (b) while a lead turn runs | (a) **A turn of its own at once, and the draft kept:** the promise resolved 163 ms after the call, as the turn started (`{text, origin: {kind: plugin, name: spk}}`); reads 10 s and 30 s later gave the draft back with its cursor. The settings `UserPromptSubmit` gets the text bare (`prompt: "Reply with only the word SUBMITTED."`); the model reads it framed ("The spk plugin sent a message:\n…\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns — it starts this turn in the user's place. Address the message above."), and the transcript stamps `origin: {kind: plugin}`. The mod's own `prompt.submit` hook does not see it. (b) **Not rejected: it waits.** The promise resolved 25 s later, when the running turn ended, and the prompt ran as the next turn. |
| Su | `$.prompt.submit({text, asUser: true})`, then without | **`asUser` reads as typed:** the model's prompt and the transcript's text are the bare line, origin `{kind: plugin, name: spk, asUser: true}`; without it, the plugin frame of S. Both resolved within 60 ms of the call, with the lead idle; `UserPromptSubmit` got the same bare text either way, with no `source`. |
| F | `$.prompt.read()` / `fill()` from the timer, an agent at work: (a) a stash (read, then fill `''`) and, 8 s later, a restore, around a typed draft; (b) `fill` while the mod's question is open | (a) **The round trip is exact:** read gave the draft and cursor 42, `fill({text: '', mode: 'replace'})` gave `{isFilled: true}` and an empty box on screen, the restore put the same text back with the cursor at its end (42), on screen too. Each call took 1 to 10 ms. (b) **`fill` under a dialog is refused** without a throw: `{isFilled: false, refusal: "dialog", text: "", cursor: 0}`. |
| P_auto | auto mode: how agents' reports reach the lead: (a) a fresh agent ends while the lead is idle; (b) one ends while a lead turn runs; (c) an idle agent resumed by the mod's message ends; (d) one at work for a minute ends; the mod drops nothing | **In auto mode a report comes as a peer hand-back.** (a), (c), (d): a prompt with `origin: {kind: "peer"}` and text `<agent-message from="A1">\n[Subagent hand-back] The text below is the final report of a subagent this session delegated to. …`, which starts a lead turn, just before the agent's `SubagentStop`. (b) The same, delivered into the running turn at its next tool round (`turnId` set). (c) A `task-notification` (with the agent's id) followed 2 s later, into the turn the hand-back started; (a), (b) and (d) had none. **The mod's event carries `origin: {kind: "peer"}` alone**: the sender, its task id and `handback: true` are only in the transcript (`origin: {kind, from, senderTaskId, name, body, handback, handbackReportText}`). `from` is the agent's name when it has one, else its id. The settings `UserPromptSubmit` had **no `source` field** for any prompt: typed, plugin, peer, task-notification, rewake. |
| P_accept | P_auto in acceptEdits mode | **No hand-back at all.** (a), (b), (d): an agent's end gave the lead no prompt of any kind. (c): the resumed agent's end gave one `task-notification`, as in spike 32. So the hand-back follows the permission mode, not the agent. |
| P_auto_drop | P_auto with the mod dropping each peer prompt that hands back one of its agents, matched by the text's frame (`<agent-message from="<its name or id>">` then `[Subagent hand-back]`), and its agents' task-notifications | **The drop works.** All four hand-backs and the one task-notification were dropped: the lead had no turn after its first, and the settings `UserPromptSubmit` did not run for any of them. The hand-back that came into a running turn (b) was dropped there, and the turn ended as usual. |
| 2.1.296 | K, P_auto and C again on 2.1.296, the incident's version | **The same in every case.** Ctrl-C ended both agents (20 ms after the key) and each was refused a message after; Ctrl-U ended neither. Every agent's end was a peer hand-back, into the running turn in (b), with a task-notification as well for the resumed agent (c). Three compactions from the mod's timer (`trigger: manual`, boundary 117 to 136 ms after `PostCompact`, the lead woken with OSPREY), the draft read back unchanged four times, the call during a turn rejected naming it. |

Also found, in the 903 session transcripts on this machine (spike sessions left out): all 190 peer hand-backs, in 66
sessions on 2.1.278 to 2.1.296, came while the session's permission mode was `auto`; none in `plan` mode. Agents'
task notifications came in both (107 in auto mode, 28 in plan mode). In the v0.8.1 run, each phase agent's end gave
a hand-back first and a task-notification 0.1 s later; the mod dropped the second, and the first reached the lead's
model as the human's turn. The run of v0.8.2's own plan (v0.8.1, auto mode) did the same while this spike ran: its
P1 agent's report reached the lead as a hand-back, and baton told the lead its decisions as if the human had written.

## What it means for baton (v0.8.2)

- **Never Ctrl-C while any agent may be live**, in every mode: it ends every background agent, baton's and the
  model's, for good. Ctrl-U leaves agents alone but deletes the human's draft.
- **The mod can compact the lead without a key**, from its poll's timer, with the draft left in the box. It must not
  await the promise in its poll (5 to 8 s here, minutes in a long session), and it must report the compaction from
  the promise: its own `session.compact` hook does not run for its call. The settings hooks see `trigger: manual`
  and the rewake runs as for a typed `/compact`, so baton's proof and brief need no new path. A call while a turn
  runs is refused at once, naming the turn: baton issues it again after the turn. A dialog does not stop it, so
  baton's own gate on an open dialog stays.
- **The mod can submit baton's lines**, with the draft left alone. A submit during a turn waits for it rather than
  failing, so the mod must not await that promise either. With `asUser: true` the model reads a line exactly as it
  reads one baton types today; the transcript still names the plugin.
- **The fallback works:** `read`, `fill('')`, and a `fill` back restore a draft exactly, and `fill` says when a dialog
  is in the way.
- **A report's peer hand-back is told by its text**, not its origin's fields, in the mod's `prompt.submit`; and the
  settings hooks have no `source` to tell machine prompts from typed ones, so `TurnSource` needs the text forms:
  `<agent-message from=` with `[Subagent hand-back]`, and `<task-notification>`. The mod can drop a hand-back by that
  frame, and a dropped prompt never reaches a settings hook.

Consequences: [`docs/research/visible-workers.md`](../../docs/research/visible-workers.md), "What spike 35 found".
