# 40 — writing to a phase agent from the lead's prompt (Claude Code 2.1.301, Haiku, macOS arm64, 2026-10-16)

Question: the human wants to type `@P23 <words>` at the lead's prompt and have the words reach P23's phase agent. In
2.1.301, `@name` at the main prompt routes only to agent-team teammates (an experimental flag); for a subagent the
whole line goes to the lead's model as an ordinary prompt. So baton would route it itself, from the lead's
`UserPromptSubmit` hook, and send the words through its mod. Does that hold on the screen and in the transcript? What
do messages do to a running, a completed, a stopped and a retired agent? Who owns an agent's name?

Harness: `run.py H <workroot>`, spike 36's: an interactive claude in a PTY (raw bytes kept, rendered through `pyte`) in
a fresh git repo with `plansDirectory` set, no user settings (`--setting-sources project,local`), acceptEdits with
exact allow rules, a mod (`mod/`, spike 36's cut down: it spawns agents as baton's mod does, a registered type with
`isolation: worktree` and a name, sends with `$.tool.call` SendMessage and TaskStop, and drops agents' reports to the
lead) and `hook.py` on the settings events, logging to `hooks.jsonl`. `hook.py` keeps a prompt `@<name> <words>` from
the model at `UserPromptSubmit` in three shapes (below). One session, two runs; run 1 also had a `WorktreeCreate` hook
that only logged, which replaced Claude Code's own worktree and failed every spawn ("hook succeeded but returned no
worktree path"), so its agent steps ran again in run 2 without it; its prompt steps agree with run 2's.

| step | what | result |
|---|---|---|
| a0 | `@P` typed, P1 running | The typeahead lists the agent by name: `❯ @P1   send message · running`. Choosing it only puts the text in the box. |
| a1 | `@P1 please write …`, the hook answering `{"decision": "block", "reason"}` | **No turn, and nothing reaches the model.** The screen shows `⏺ UserPromptSubmit operation blocked by hook:`, the reason, and `Original prompt: @P1 …`. The transcript gets one `system` entry of subtype `informational` with that text, and no API request carries the words or the notice (every `api-request-blob` of both runs checked). **The box keeps the text:** `$.prompt.read()` gave the whole prompt at 0, 0.2, 1 and 2.5 s, and a second Enter submitted it again (a second block). |
| a2 | the same with a `systemMessage` beside the block | The `systemMessage` is shown nowhere (screen, transcript): only the reason is. |
| a3 | `{"continue": false, "stopReason"}` instead | The box is emptied, but **the prompt enters the conversation**: a `user` entry with the words, an `isMeta` user entry `Operation stopped by hook: …`, and the next API request carried both. Not a way to keep words from the model. |
| a5 | the block, then the mod empties the box from a timer 1 s later (`$.prompt.fill({text: '', mode: 'replace'})`, only while `$.prompt.read()` still gives that prompt) | `isFilled: true`, the box empty; Enter after did nothing. |
| a4 | `@P1 note four …` typed while the lead runs a 25-second command | `UserPromptSubmit` fired at the Enter, with the command still 22 s from its end (run 1), not at the next tool round; blocked, the box kept it, and the lead's turn went on to its answer untouched. The mod's `prompt.submit` saw the prompt with `turnId` set (typed over that turn) and `origin: {kind: "composer"}`, and `next(e)` resolved `{drop: "UserPromptSubmit operation blocked by hook: …"}`: the settings hook runs inside the mod's chain. |
| b1 | the mod's SendMessage to P1, running a 60 s command | `{"success": true, "message": "Message queued for delivery to <id> at its next tool round. If it finishes first, it is resumed with the message and notifies again, …"}`. P1 ran the asked `echo FROM-MOD` 1 s after its command ended (35 s after the send), then went on with its task. |
| b2 | to P1 once completed | `"Resuming agent a3f1c30"` (`resumedAgentId`): `SubagentStart` again, and it answered. Its worktree, which it left unchanged, Claude Code had removed at its end (`worktreeCleanlyRemoved: true` in `agent-<id>.meta.json`), and **it was resumed in the lead's checkout**: the resumed `SubagentStart` has the project root as `cwd`. |
| b3 | to P2, stopped by the human with `x` on its row | `{"success": false, "message": "Agent \"<id>\" was stopped by the user and was not resumed. Treat its work as cancelled; only start a new agent for it if the user explicitly asks."}` |
| b4 | the human types in P2's view (Enter on its row; `Message @P2…`) | **Resumed** (`revivedAt` in its record), answered HUMANBACK, in the lead's checkout too (its unchanged worktree removed). Typing in the view raises no `UserPromptSubmit` and no `prompt.submit`. |
| c | P3 commits in its worktree and finishes, its view open; the worktree is then removed (`git worktree remove --force`, as baton retires one); the human types in its view; then the mod's SendMessage | Claude Code kept P3's worktree (it held a commit): its record names `worktreePath`. After the removal, **the human's words show in the view, and nothing else happens**: no hook with its `agent_id`, no turn, its row stays `idle`, no error on screen. **The mod's message is refused:** `Agent "<id>" is stopped (completed) and could not be resumed: This agent cannot be resumed: its worktree no longer exists, and the fallback directory is not covered by the session's isolation fences.` |
| d | a second agent spawned with the name P4 while the first P4 runs (run 1 asked the lead's model to name an agent of its own: its Agent tool has no `name` parameter, and the call it made had none) | **The later agent takes the name.** `$.agent.list()` gives the first P4 no name at all (`name` absent, `running`), the second `name: "P4"`; the typeahead lists one `@P4 … send message · completed`, the second; SendMessage to `"P4"` resumed the second (`"Resuming agent P4"`). Messages by id are unaffected: the mod's TaskStop by the first's id stopped the first. |
| e | the lead's turns over the whole session | Two: the first prompt and the slow.sh prompt. None for any blocked prompt, typed idle or mid-turn. P1's transcript has none of the blocked words: blocking routes nothing by itself. |

From the code (2.1.301, read only): a resume of an isolated agent whose recorded worktree no longer exists falls back
to the record's `cwd` or the session's own directory only when the session itself runs in a worktree whose fences
cover it ("Resumed worktree … falling back … under the session worktree's fences"); otherwise it is refused, as in
(c). An agent whose worktree Claude Code removed cleanly has no worktree on record, and resumes in the session's
directory, as in (b2) and (b4). Agent names: `/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/`, refusing `main`, `team-lead`,
`user`, `system` and agent-id shapes; the registry maps a name to the latest agent given it; SendMessage resolves a
name exactly, then as an id, then ignoring case.

## What it means for baton (P16)

- **(a) holds: a block keeps the words from the lead's model at no cost.** The lead's `UserPromptSubmit` hook answers
  `{"decision": "block", "reason": …}` for `@<phase> <words>`: no turn, nothing in any request, whether the lead is
  idle or mid-turn (the hook fires at the Enter, so the words go at once, not at the lead's next tool round). The
  reason is the human's only confirmation, under Claude Code's own frame (`UserPromptSubmit operation blocked by
  hook:` and `Original prompt:`), so it says plainly what baton did: "baton: sent to P23's agent …". A
  `systemMessage` beside it is not shown; `continue: false` lets the words into the conversation, so it is never used.
- **The box keeps the prompt, and a second Enter would send it again.** baton has the mod empty the box once the
  words are queued (`$.prompt.fill` with empty text, `replace`), only while `$.prompt.read()` still gives exactly
  that prompt, so a draft the human started meanwhile is never touched. Where the mod does not run there are no
  phase agents, and nothing is routed.
- **The hook cannot tell who typed the prompt.** Its input carries no origin (`cwd`, `hook_event_name`,
  `permission_mode`, `prompt`, `prompt_id`, `scratchpad_dir`, `session_id`, `transcript_path`); the mod sees
  `origin.kind: "composer"` for typed prompts, but the routing is the host's. The shape decides: a leading `@<id>` and
  whitespace, where the id is one of the run's phases. baton's own lines start `[baton]`, agents' reports
  `<task-notification>` or `<agent-message`, so none of them is ever routed.
- **(b) The words go as baton's other deliveries go**, through `bridge.Send`, by agent id: a running agent gets them
  at its next tool round (the send answers at once, "queued … at its next tool round"); an idle or completed one
  that baton still has is resumed by them. An agent the human stopped refuses them ("was stopped by the user and
  was not resumed"); only the human's own typing in its view brings it back (b4), so baton tells the human that,
  and gives the words to the lead.
- **(c) A retired agent is refused by Claude Code itself** where baton removed a worktree Claude Code kept: a message
  is refused, and the human's typing in its view does nothing, with no word on screen. baton forgets a retired agent
  (`bridge.Forget`), so it never sends to one; `@P23` for a done phase goes to the lead, and the relay line says that
  typing in a done agent's view does nothing once baton has landed its work. **The retired-agent guard stays**: an
  agent with no worktree on record (one Claude Code removed as unchanged), or any agent of a lead that itself runs in
  a git worktree, is resumed in the lead's own checkout (b2, b4), where a woken agent's edits would land among the
  lead's. The guard refuses every tool call of an agent baton has retired, whatever its directory.
- **(d) A name is a pointer to the latest agent given it.** Messages by id are safe, and baton sends by id; but the
  typeahead, the view's `Message @P4…` and SendMessage by name follow the newest agent, and the first loses its name
  in `$.agent.list()`. The lead's model cannot name an agent in 2.1.301 (its Agent tool has no `name`); another
  plugin's spawn, or a teammate where agent teams are on, can. P15's refusal of a model's Agent call named after a
  phase covers the configurations that offer the parameter, and costs nothing where none does.
- **(e) A blocked prompt costs nothing:** no turn, no tokens, one display-only line in the transcript.
