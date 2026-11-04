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
