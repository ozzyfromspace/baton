# Tiers: the model and effort each phase runs on

**In short:** Claude Code lets baton set a phase's model and effort deterministically, read back what every request
was served, and change both from one phase to the next, up or down, without touching the human's settings. A phase
agent's tier is set as it is spawned and stays with it. The lead's is its launch flags, so changing it means a
restart in place, which ends every agent of that claude.

- **What a request was served is written on it.** Every assistant entry of a transcript, the lead's and each agent's
  own (`subagents/agent-<id>.jsonl`), carries `message.model` (the model that answered), `requestedModel` and
  `effort` (the effort sent). Real runs' phase agents read `claude-opus-5-5` at `max` on 2.1.310 and at `xhigh` on
  2.1.316, the human's own effort each time, since they inherited it.
- **Hooks and the status line say it before a request.** Every hook input but `SessionStart` and `SubagentStart`
  carries `effort: {level}`, an agent's its own; `SessionStart` carries `model` at startup (not at a resume); the
  status line carries `model.id` and `effort.level`; `$.agent.spawn` answers the model the agent got.
- **An agent's effort comes from its type, its model from its spawn.** The mod's `$.agent.spawn` forwards `model`
  (an alias: `haiku`, `sonnet`, `opus`, `fable`) but drops `effort`; `$.agent.register` takes both. A type's effort and
  model are read again at every resume, the spawn's model never is ([spike 43](../../spikes/43-model-effort/)).
- **The lead's tier is `--model` and `--effort` at launch,** for that session only. A resume with no `--effort` falls
  back to the model's default effort, with no `--model` to the conversation's last model. Nothing in an interactive
  session switches either for the session alone: `/model` and `/effort` save the choice as the human's default.
- **Effort costs tokens.** Claude Code's own cost index, per token, against `high`: Opus 5.5 0.48 (low), 0.66, 1,
  1.66 (xhigh), 3.44 (max); Sonnet 5.5 0.49, 0.54, 1, 1.87, 5.5; Haiku 5.5 0.38, 0.52, 1, 2.03, 4.34; Fable 5.1 0.75,
  0.86, 1, 1.38, 1.74. Fable's price per token is 2.5 times Opus's, Sonnet's half.

## What Claude Code 2.1.318 does

Read from its binary, then measured in [spike 43](../../spikes/43-model-effort/).

### Effort

- The levels are `low`, `medium`, `high`, `xhigh` and `max` (`ultracode` is an alias of `xhigh`, `med` of
  `medium`). Each model has its own default: Opus 5.5 and Sonnet 5.5 `medium`, Fable 5.1 `high`. The current
  families all take every level.
- A request's effort is resolved in order: `CLAUDE_CODE_EFFORT_LEVEL`; the session's own level (`--effort`, or an
  `/effort` pick); the settings' `effortLevel` and `modelSettings.<model>.effortLevel`; the model's default. It is
  then clamped to `maxEffortLevel` (the lowest across settings files), the organization's cap and the model's
  maximum, with no word to the model. So the effort baton asks for and the one served can differ, and only the
  transcript says which was sent.
- **A subagent's effort** is its type's `effort` when the type sets one, else `CLAUDE_CODE_SUBAGENT_EFFORT`, else the
  session's. When none of the first two is set, a server-side flag can lower an inheriting subagent's effort below
  the session's. A type with its own effort, or `CLAUDE_CODE_EFFORT_LEVEL`, keeps that from happening.
- `/effort <level>` saves the level as the human's default for new sessions (`effortLevel` in
  `~/.claude/settings.json`) unless `CLAUDE_CODE_EFFORT_LEVEL` is set, in which case it changes nothing. Changing it
  mid-conversation can open a confirmation about the prompt cache. `--effort` sets the session's level and saves
  nothing.

### Model

- `--model` takes an alias (`fable`, `opus`, `sonnet`, `haiku`, which resolve to the newest of the family) or a full
  id. `/model <x>` sets the model and saves it as the human's default (`model` in `~/.claude/settings.json`), and the
  `/config` row does the same. A session-only switch exists, but only on the SDK's and Remote Control's control
  channel (`set_model`, `apply_flag_settings`), which a session hosted in a terminal does not have.
- **A subagent's model** is the Agent call's `model` (an alias only), else its type's `model`, else
  `CLAUDE_CODE_SUBAGENT_MODEL`, else the session's. Spike 43: the spawn's model wins over the type's, and stays with
  the agent through every resume; a type's model is read again at each resume.
- **Fable is metered on its own.** On a subscription it can need the human's consent to usage credits, or credits,
  and bill beyond the plan (the binary's `needs_consent` and `needs_credits`). Nothing baton does may spend that
  without the human's word.

### What the mod can and cannot do

- `$.agent.register({name, description, prompt, effort, model, …})` registers a type with its own effort and model.
  Registering a name again replaces it, for new agents and, at their next resume, every agent of it.
- `$.agent.spawn({subagentType, prompt, description, name, model})` forwards those fields alone: no `effort`. Its
  answer carries the model the agent got.
- `$.command.run({command: 'model', args})` and `$.config.set({key: 'model', value})` would switch the lead, but both
  save the choice as the human's default, as `/model` does. baton uses neither.
- `$.session.model()` reads the lead's model.

### Ruled out for the lead

- Typing or running `/model` and `/effort`: they would change every new session the human starts.
- Writing `effortLevel` into a settings file: the human's own files are theirs, and baton's `--settings` is inline
  JSON fixed at launch.
- `CLAUDE_CODE_EFFORT_LEVEL`: it would fix the effort of the lead and of every agent alike for the whole claude.

## What it means for baton

- **One agent type per effort, never registered again with other settings.** Since a resumed agent reads its type
  again (spike 43, A1 and A3), a type changed under a live agent would move it at its next message; a fixed type per
  effort keeps every agent on the effort it started with.
- **The model goes with the spawn.** A spawn's model stays; a type's would follow the type.
- **An agent's tier is fixed for its life.** Changing it means a fresh agent on the same worktree.
- **The lead's tier is both flags, on every launch.** A restart in place (`--resume`, the same conversation) changes
  it, and ends every agent of that claude, so it waits for none to be at work, as an upgrade's does.
- **Each phase's tier can be checked from facts:** the first reply of the phase in that loop's transcript, against
  the plan. A cap Claude Code applied is told once, never retried.

## What v0.8.18 built

- **A phase's tier in its plan:** `Model:` and `Effort:` lines under its heading. The planner is asked for every
  phase's, with rules that err on the side of caution and quote the cost index above, once at its first
  `ExitPlanMode`.
- **Phase agents on their tier:** one agent type per effort, never registered again with another, and the model on
  the spawn. Each agent is checked against its tier once its first reply says what it was served.
- **The lead on its phase's tier:** a restart in place with both flags at the phase's boundary, or mid-phase once
  idle, waiting for no agent to be at work; while agents can take them, the lead passes over phases on other tiers.
  Once no plan runs, it goes back on the human's own tier.
- **Raised when it goes badly:** three red runs of a phase's own gate raise it a step, at most two steps; never down
  mid-phase, and never on a refusal.

Measured end to end on Claude Code 2.1.318 with Haiku 5.5 (`test/e2e/tiers_test.go`):

- **The lead, high to low to medium:** a three-phase plan in serial mode restarted the lead at each boundary whose
  phase named another effort. Every reply of each phase in the lead's transcript was served that phase's effort,
  about a minute in all.
- **Phase agents, low and max:** beside a lead phase at high, the phases at low and max went to phase agents. Each
  was spawned as the type of its effort and was served it, and the lead never restarted.
- **A red phase raised:** a phase at low whose gate failed once was raised to medium (`escalate_after` 1 there), and
  the lead, idle while the gate ran, restarted on medium before it was given the result. Its replies after the
  restart were served medium.

Two things the first runs taught, both fixed before the release:

- **An agent can be done in seconds.** Its loop read its transcript every 10 seconds, so a three-second phase was
  never checked. It now reads at every tick until the first reply says what it was served, whether its phase is
  done or not.
- **A raise has to come before the result is delivered.** A red whole run counts once its environment has been
  read, and that happens in the same tick, after the lead's state was read. The result woke the lead first, and it
  finished the phase on its old tier in one turn. The lead's tick now reads its state again when a red run may count,
  and raises before it delivers anything.

## What v0.8.19 fixed

The first long run on tiers (a 74-phase plan, every phase on opus at `xhigh` or `max`) found two gaps in its first
hour:

- **A tier edited in the plan file was followed silently.** The human edited P0's tier from `high` to `xhigh` a
  minute after the plan was attached, while the lead, restarted onto `high` for P0, was at work. baton followed the
  file at P0's `baton done`, five minutes later, and the lead restarted on `xhigh` at the next boundary, but nothing
  said so, and the human and the lead's model had both taken the edit as applied at once. A phase agent at work on a
  phase whose tier changed would have kept its old tier for its life, since only a raise replaced one. Now every
  following of the file says each phase whose tier changed and what follows for it (`tier_changed`), and an agent at
  work is replaced on the new tier as after a raise. A tier written after a raise is the phase's: a raise records
  the plan's tier it was made from, and stands only while the plan names it.
- **A release would have gone to a phase agent.** Its release phase, `After: all` on `max`, while the lead ran
  `xhigh`: v0.8.18 left a phase on another tier to an agent, but the release merges `origin/main` in, which no phase
  agent may do. Now a phase that waits for every phase before it, and is not a QA phase, is the lead's whatever its
  tier; by its boundary no agent is at work, so the lead restarts on its tier there.

Still open: the plan file is followed only at the points baton reads it, so an edit made while the lead is idle,
holding no phase and waiting on agents, waits for the lead's next turn.

## What v0.8.20 fixed

The same 74-phase campaign, on v0.8.19, a day later. P33, the lead's phase, had been raised from opus/xhigh to
opus/max after 3 red runs. Its boundary, from `events.jsonl` (UTC, 2026-11-05):

| Time | What happened |
|---|---|
| 10:04:37 | P33's boundary compaction is queued; the lead waits to restart on opus/max (`tier_waits`, 6 agents at work, and two read-only research subagents the human had asked the lead's model to start, Study AU and Study AV). |
| 10:14:37 | `tier_given_up` after `Timing.TierWait`: P33 is to go on on opus/xhigh. The compaction is then held by the gate's own case for the model's background subagents (`gateAgents`). |
| 10:27:18 | Study AV ends. |
| 10:36:00 | Study AU ends; the compaction is asked for at once, and P33 resumes at 10:37:27 on opus/xhigh, not the max it was raised to. |

Thirty-one minutes of the lead doing nothing, two stacked clocks (10 minutes of tier wait, then up to
`Timing.BackgroundMax`, 20), and the raise lost. All the while nothing recorded the hold (`Run.Gate` stayed empty)
and the status line read "compacting…"; Claude Code runs a status line only as the conversation changes, so it still
read that after the band had moved on. At 11:43 P50, raised to max too, waited its 10 minutes the same way and went
on on xhigh.

- **The model's own background agents hold only what would end them.** A compaction, a result, mail and baton's lines
  leave a subagent running, as they leave a phase agent, so none waits for one now. A restart in place (tier, upgrade,
  idle) still does, as before, and so does a fresh session after an API error, by a wait of its own of at most
  `BackgroundMax`; serial mode's Ctrl-C never comes while one may be live.
- **A tier wait says why.** The boundary's hold is published (`decide.TierHold`): "restart on opus/max waits for 6
  agents", or the gate's reason, on the status line ("compact held: …") and the board.
- **The status line runs while idle**, every 10 seconds ([spike 44](../../spikes/44-statusline-refresh/README.md)).
- **A raised phase goes to a phase agent on its tier** when the lead cannot restart for it (the human's option (a)):
  mid-phase, once the lead is idle with its work committed and its checkout on the run's branch, the phase is parked
  for an agent and the lead takes its next phase; a parked phase off the lead's tier, as P33 was at its boundary, is
  taken by an agent the same way. The agent starts from the run's branch, which holds the lead's commits, and its
  brief gives why, those commits, the lead's notes and the red gate's failures. A phase that waits for every phase
  before it stays the lead's (v0.8.19), and so does one whose work was left uncommitted (`baton done --keep-dirty`)
  or whose `--here` gate runs. A boundary already owed to such a phase in a run carried from v0.8.19 goes to the
  next phase instead (`boundary_redirected`).

Still open: the record file and the risk ledger count only the agent's landing of a handed phase, not the lead's
commits before it, and time saved counts its work from the agent's start.
