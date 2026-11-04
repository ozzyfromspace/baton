# 43 — the model and effort of a session and of its agents (Claude Code 2.1.318, Haiku 5.5 and Sonnet 5.5, macOS arm64, 2026-11-04)

Question: can baton set the model and effort of each phase deterministically, read back what each request was served,
and change them from one phase to the next (up or down) without touching the human's settings? Reading Claude Code's
binary first ([write-up](../../docs/research/tiers.md)) left these to measure:

- Does an agent type registered through `$.agent.register` with `effort` (and `model`) set its agents' effort (and
  model)? The mod's `$.agent.spawn` passes `model` but drops `effort`.
- Which wins between the spawn's `model` and the type's?
- Does an agent resumed by a message keep what it was spawned with, or read its type again?
- Do an agent's hooks carry its own effort, or the lead's?
- Does a lead resumed with new `--model` and `--effort` flags run on them, with no dialog and nothing written to
  `~/.claude/settings.json`? And resumed with neither?

Harness: `run.py <workroot>` and `run2.py <workroot>`, spike 40's without `pyte`: an interactive claude in a PTY in a
fresh git repo, no user settings (`--setting-sources project,local`), acceptEdits with one exact allow rule, a mod
(`mod/`) that registers four agent types (`t-max` with effort max, `t-low` with effort low, `t-none` with neither,
`t-sonnet` with model sonnet and effort medium), spawns and messages agents as baton's mod does and drops their
reports to the lead, and `hook.py` on the settings events and as the status line. Every answer is read from what the
requests carried: `message.model`, `requestedModel` and `effort` on each assistant entry of the lead's transcript and
of each agent's own.

| step | what | result |
|---|---|---|
| A | lead `--model haiku --effort low` | Served haiku/low. The status line said `model.id: claude-haiku-5-5`, `effort: {level: low}`. |
| A1 | type `t-max`, no spawn model | **haiku/max**: the type's effort, on the lead's model. `$.agent.spawn` answered `model: claude-haiku-5-5`. |
| A2 | type `t-low`, spawn model `sonnet` | **sonnet/low**: the spawn's model with the type's effort. |
| A3 | type `t-none`, no spawn model | haiku/low: the lead's model and effort. |
| A4 | type `t-sonnet` (model sonnet), spawn model `haiku` | **haiku/medium**: the spawn's model wins over the type's. |
| A5 | type `t-sonnet`, no spawn model | sonnet/medium: the type's model and effort. |
| A1, again | `t-max` registered again at `low`, then A1 messaged once completed | `Resuming agent …`, and **every request after it was low**. A resumed agent reads its type's effort again. |
| A3, again | `t-none` registered again at `high`, then A3 messaged | Low before, **high** after. |
| B1 | a new agent of `t-max`, registered at `low` | low: the type as registered now. |
| A6, again | spawned of `t-low` with spawn model `sonnet`; messaged once completed | **sonnet** before and after: a spawn's model stays with the agent. |
| A7, again | spawned of `t-sonnet` with no spawn model; the type registered again with model `haiku`; messaged | sonnet before, **haiku** after: a resumed agent reads its type's model again, unless its spawn named one. |
| hooks | every hook of A, its agents', B, C | Every hook input but `SessionStart` and `SubagentStart` carries `effort: {level}`, and an agent's carries **its own** (A1's `max`, A5's `medium`, A3's `high` after its resume). `SessionStart` carries `model` at startup, none at a resume. |
| B | `--resume <id> --model sonnet --effort high` | Served **sonnet/high**. No dialog on the screen at the resume; the status line said sonnet and high at once. |
| C | `--resume <id>`, neither flag | Served **sonnet/medium**: the conversation's last model, and that model's default effort (no user settings here). A resume keeps the model, not the effort. |
| D, E | `--model haiku --effort max`, then `--resume <id> --model haiku --effort low` | max, then **low**: a lead goes down a step from one session to the next as cleanly as up. |
| settings | `~/.claude/settings.json` before A and after A, B, C and E | Unchanged (`model: opus`, `effortLevel: xhigh`): launch flags write nothing. |

What it means for baton:

- **Agent types are fixed per effort.** A type's effort and model are read again at every resume, so a type
  registered again with another effort would move every agent of it at its next message. baton registers one type
  per effort (`phase-low` … `phase-max`) and never registers one again with other settings.
- **The model goes with the spawn, always.** It stays with the agent through every resume; a type's model would not.
- **The lead's tier is its launch flags, both, every time.** A resume with no `--effort` falls back to the model's
  default effort; with no `--model`, to the conversation's last model. baton passes both on every launch, restart
  and recovery while a plan sets the tier.
- **What a request was served is in its transcript entry**, and an agent's hooks carry its own effort, so baton can
  check every phase's tier from facts.

Cost: two runs, about 4 minutes and 3 minutes, on Haiku 5.5 with five short Sonnet 5.5 requests.
