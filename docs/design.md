# baton's design

Until v0.8, two documents described baton's design: the v0.1 implementation plan and the v0.2 note on graduated
escalation, each with a note for every version since. This note replaces both, and describes baton as it works
now. The evidence behind each part is in [`research/`](research). How it got here is in `git log` and the
[changelog](../CHANGELOG.md).

## Principles

- **The model does the work; host code decides.** When to compact, resume, start a phase, land work and call the
  human is decided by deterministic code, never by a model remembering to. A step that happens only because a model
  remembered it is a bug waiting to happen.
- **One live terminal.** baton hosts `claude` in a pseudo-terminal it owns and passes every byte through. Nothing
  draws over or replaces Claude Code's interface.
- **Fail open.** A baton hook that errs lets Claude Code go on, and the watchdog is the backstop.
- **Dormant unless hosted.** Hooks, the CLI and the mod do nothing in a session baton doesn't host.
- **A run must not stop for something it can work around,** and must never get past what only the human may decide.

## Architecture

```
terminal ──bytes──▶ baton host (PTY owner) ──bytes──▶ claude (interactive TUI)
                    │ loop · coordinator · watchdog       │ hooks (via --settings) ──▶ baton hook <event>
                    │                                     │ model ──Bash──▶ baton done|verify|propose|…
                    │                                     │ mod (plugin JavaScript) ──▶ baton mod report
                    └── .baton/runs/<run>/: locked state, events.jsonl, mod/ commands ◀──┘
```

- **The host** starts `claude` with baton's hooks, its status line, an allow rule for `baton` and the
  `--autocompact` cap, all in `claude --settings`, so a plain session carries none of them. It sets `BATON_HOST=1`.
  It types only when no turn or dialog is open and the human hasn't typed for a few seconds. Escape, Ctrl-C or a
  prompt from the human hands that turn to the human.
- **Hooks record facts:** who started a turn, the open dialogs (as a queue), the subagents, and the context size from
  the status line. `baton hook` recovers panics and exits 0 on any internal error. Only the rewake path exits 2,
  which is Claude Code's "block".
- **A run per session.** `.baton/runs/<id>/` holds the plan's anchors, the state, the notes and `events.jsonl`. An
  anchor is a verbatim snippet checked to occur once in the plan file, so baton needs no heading parser. Every write
  is locked and atomic. A run follows its conversation through `--resume` and `/clear`.
- **Handing a session over.** `/baton start` records the session. Once its turn ends, the plugin's Stop hook ends
  claude, and the `baton init` shell hook relaunches the same conversation under baton. A host that finds a newer
  compatible baton installed restarts its session on it in place, resuming the same conversation.
- **The skill** is a stub that loads its instructions from the binary (`baton skill`), so they always match the baton
  that carries them out.

## A phase boundary

1. The model runs `baton done P3` and ends its turn. The Stop table allows the stop when the phase is done or the
   plan complete, when the run is paused or blocked, and when the phase waits on a job or on background work. It
   refuses a stop without a status, even in a turn the human started (to talk without the plan, the human pauses
   baton), and names the way forward. After three refusals in a row, it escalates.
2. The host types `/compact` once nothing is in the way: no turn, no dialog, none of the lead's own subagents. An
   unsent draft is saved and cleared after a grace period.
3. **Only proof moves a compaction.** A phase agent's compaction fires `PreCompact`, `SessionStart(compact)` and
   `PostCompact` with no `agent_id` and the lead's transcript, so those hooks cannot tell whose compaction it is.
   They log `compact_seen` and change nothing. `state.Proof` has one constructor per source of proof:
   - the mod's report of the lead's compaction, which it sees before and after;
   - a new `compact_boundary` entry in a loop's own transcript: the lead's, or a phase agent's
     `subagents/agent-<id>.jsonl`.

   Every transition past "typed" takes a Proof.
4. `PostCompactRewake`, registered for every trigger, waits a bounded time for the proof. Then it wakes the model
   with the next phase's brief: the phase's section of the plan, the standing rules, the notes, and the decisions
   made without the human.

**Mid-phase valves.** The status line gives the exact context size. At 60% of the limit (the model's window, or the
`--autocompact` cap if smaller), the model is asked to checkpoint at its next safe point, which goes through the same
loop. At 90%, but never under 200k tokens, the human is asked whether to checkpoint now; unanswered for 20 minutes,
baton keeps going. Claude Code's own compaction, 33k tokens below the limit, is the backstop.

**The watchdog** catches a run that has gone quiet: idle turns, stale gates, unanswered prompts, usage limits and API
errors. Everything baton waits on either clears by itself or expires into action, except an open permission prompt.

## Gates

A long gate goes to baton:
- `baton verify` runs it on `HEAD` in a lane: a detached worktree under `~/.baton/lanes/`, set up by the project's
  `.baton-lane`.
- `verify --here` runs it in the checkout, and holds edits and landings there until it ends.
- `baton await` waits on something outside the project.

The job runs detached (`baton lane-run`), so it outlives claude. Its result reaches the model at its next tool call
or its stop, or wakes it. A finished phase is "verifying" while its gate runs: it is done when the gate passes, and
reopens when it fails. A phase that can't go on without its result is set aside after `switch_after` for another phase
that can start, and later resumes from its notes.

## Phases at once

The plan's `After:` lines make a graph. The lead, the session in the human's terminal, works one phase at a time.
Each other phase that can start runs as a **phase agent**: a background agent of the lead's session.

- **The mod is a thin executor.** `plugin/hooks/baton.js` is JavaScript that Claude Code runs in the lead's claude.
  It spawns and messages `baton:phase` agents, asks the human in Claude Code's own dialog, drops the agents' reports
  before the lead's model reads them, draws the band, and reports what it sees. It decides nothing and words
  nothing: the host does both.
- **The bridge** is files and the CLI:
  - host to mod: numbered command files under `.baton/runs/<run>/mod/`, each addressed to one host instance,
    idempotent, and retried until acked;
  - mod to host: `baton mod report`, with each report recorded once, under the run's lock.
- **Placing.** The mod spawns one agent at a time, and baton's `WorktreeCreate` hook makes its worktree (under
  `~/.baton/worktrees/`, on the branch `baton/<run>/<phase>`). A model's own isolated subagent gets a plain worktree,
  as Claude Code would make it.
- **The lead's guarantees, by host code:**
  - `SubagentStart` briefs the agent, and `SubagentStop` refuses its stop without a status.
  - Every hook carries `agent_id`, so it acts for that phase's sub-run (`.baton/runs/<run>/workers/<phase>/`).
  - Every delivery (a gate's result, an answer, a conflict) is a message, which Claude Code holds for the agent's
    next tool round.
  - A resumed agent is given nothing of `SubagentStart`, so what baton has for it comes at its next tool call or its
    stop.
  - The agent's context is read from its own transcript, and its checkpoint starts a fresh agent from its notes.
  - A refused, errored or interrupted turn is the host's to handle.
  - A model's own call to the `baton:phase` type is refused.
- **Room.** `workers: auto` sizes the fleet to the machine. A new agent also waits while the machine is busy, the
  lanes fall behind, landings keep conflicting, or a usage limit holds. Every hold is host code.
- **Landing.** A finished phase joins the merge queue. If the run's branch has moved on, it is merged into the phase's
  branch in its worktree, and a conflict goes back to its agent, which merges and resolves it itself. Nothing baton
  runs, or asks an agent to run, rewrites a commit: a rebase can drop a resolved conflict without a word, and Claude
  Code's auto mode may refuse one as destructive. A merge starts only on a worktree with nothing uncommitted, and one
  that stops is aborted, leaving everything as it was. The phase is verified again if the run's branch moved (at most
  once for the checkout's own commits). Then the run's branch is fast-forwarded to it with `git merge --ff-only`
  between the lead's tool calls, never over a file the lead has uncommitted. The lead is told what moved, and never
  to rewrite it.
- **Serial mode.** With no hello from the mod, there are no phase agents: the lead works the phases in turn, as
  `workers: 0` does.

## Reaching the human

It began with an incident. At the end of a phase, with every gate green, GPG signing timed out. The model ran `baton
blocked`, the only status that fit, and eight hours of verified work sat uncommitted until the human, who happened
to be watching, answered eleven minutes later. An unsigned commit would have been a reversible, one-command repair.
Replayed with nobody there, Sonnet blocked 4 times out of 4 ([`research/escalation.md`](research/escalation.md)). The
interface had made *notify* and *halt* the same operation.

**Three tiers**, and the model uses the lightest that fits:

| Command | When | What happens |
|---|---|---|
| `baton note "<what it did>" --undo …` | reversible, and already done | announced and recorded; the run goes on |
| `baton propose "<action>" --because … --undo …` | reversible, but the human might choose otherwise, or a rule can't be followed right now | asked with a deadline; no answer means the model does it |
| `baton blocked "<why>" --tried …` | irreversible, outward-facing, or needs a secret only the human holds | the run stops, and only the human clears it |

- **The default is the action.** A question with a fallback failed in the replay: the models put the degraded path
  in the question and inaction in the default, then blocked, 4 times in 4. With `propose`, whose default *is* the
  action and must make progress, the replay finished the plan 4 times in 4.
- **Every refusal names the way forward,** from one playbook: commit degraded rather than lose work, take a
  worse-but-working path, propose a way around a rule. `blocked` needs `--tried`.
- **The timed proposal** is the question `baton: <because>. Unless you answer by HH:MM, I will <action>.`, with the
  answers Wait for me · Pause baton · Go ahead. It goes ahead at its deadline (`escalation_timeout`, 5 minutes), never
  sooner than a minute after it appears, and a key press restarts the clock. A proposal that names an outward or
  irreversible act (push, publish, deploy, delete, force, …) has no deadline.
- **Answering its own question.** Where the mod runs, its `tool.call` hook answers baton's question at the deadline,
  so nothing types into a dialog. That matters because dialogs queue, and in Claude Code 2.1.296 a permission
  prompt's third option is "Yes, and switch to auto mode". Where the mod doesn't run (serial mode), the host types
  `3`, and only while baton's question, in exactly the shape baton issued, is the only dialog open.
- **A phase agent's question** is asked by the mod in the lead's terminal, without stopping the lead's turn, and the
  answer goes to the agent as a message. A phase agent's block stops only its phase.
- **Durability.** `blocked` and `done` refuse fresh uncommitted work unless the model says why. Whenever the run
  halts, the host also snapshots that work into `refs/baton/snapshots/`: staged, unstaged and new files, leaving the
  working tree, the index, `HEAD` and gpg untouched.
- **Every decision made without the human is recorded,** with its undo, in baton's state. It is announced, listed by
  `baton status` and `baton done`, carried in each brief, and handed to the model when the human next types, so a
  late answer works after any compaction. After `max_auto_decisions` in one phase, baton stops for a review. The
  model cannot clear its own block.
- **Without git** there is no gate and no snapshot, and baton says "complete" rather than "committed".

## Recovery

- **A refused turn is escalated, never retried.** baton reads the refusal from the transcript (read-only), types
  nothing into that conversation, starts no fresh one, and never switches models. The human rewinds or clears.
- **A crash is recovered without asking.** The host resumes the conversation in place, at most twice in 30 minutes,
  then each phase agent by a message. A conversation that cannot be resumed gets one fresh session per phase.
- **A rewind leaves baton's record standing.** baton marks the transcript's last entry at each prompt, and checks that
  the next prompt descends from it. After a rewind, the model is told what still stands.
- **`/clear`** gets the phase's brief. It ends every phase agent, so baton starts each unfinished phase again in its
  worktree.

## What never changes

- An open permission prompt is never answered or typed through.
- Nothing irreversible or outward-facing goes ahead on a timer.
- A secret only the human holds stops the run.
- A turn the model's safeguards refused gets no retry, no rephrasing and no other model.
- Anything baton cannot classify is treated as hard.
