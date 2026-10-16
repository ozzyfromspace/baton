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
- **Dormant unless hosted.** Hooks, the CLI and the mod do nothing in a session baton doesn't host: one with a live
  baton host above it, not only baton's variables.
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
  `--autocompact` cap, all in `claude --settings`, so a plain session carries none of them. It sets `BATON_HOST=1`
  and records its pid. A session counts as hosted only with that host among its ancestors (package `hostcheck`),
  since Claude Code's background service passes baton's variables on to sessions no host drives. Such a session is
  told once how to come back (`baton --resume <session id>` in a terminal), and `/baton run` of its plan there
  carries the old session's run on, where it stood, when no live host owns it.
  Where its mod runs, it types nothing: the mod compacts the lead and submits baton's lines, and the human's draft
  stays in the input box. In serial mode it types only when no turn or dialog is open and the human hasn't typed for
  a few seconds, and never sends Ctrl-C while an agent may be live: one at an idle prompt stops every background
  agent for good ([`research/visible-workers.md`](research/visible-workers.md)). Escape, Ctrl-C or a prompt from the
  human hands that turn to the human.
- **Hooks record facts:** who started a turn, the open dialogs (as a queue), the subagents, and the context size from
  the status line. `baton hook` recovers panics and exits 0 on any internal error. Only the rewake path exits 2,
  which is Claude Code's "block".
- **A run per session.** `.baton/runs/<id>/` holds the plan's anchors, the state, the notes and `events.jsonl`. An
  anchor is a verbatim snippet checked to occur once in the plan file, so baton needs no heading parser. Every write
  is locked and atomic, and a lock is held for that write alone: commands, process and transcript reads and the
  network come before it or after it. A hold over a second, or a waiter that gives up after 5, names the holder in
  the log. A run follows its conversation through `--resume` and `/clear`.
- **Handing a session over.** `/baton start` records the session. Once its turn ends, the plugin's Stop hook ends
  claude, and the `baton init` shell hook relaunches the same conversation under baton. A host that finds a newer
  compatible baton installed restarts its session on it in place, resuming the same conversation.
- **The skill** is a stub that loads its instructions from the binary (`baton skill`), so they always match the baton
  that carries them out.

## Plans

A plan is a Markdown file with a heading per phase (`## P0 — …`). The line under each heading says what the phase
needs done first (`After: P1, P3`, `After: none` or `After: all`); a phase without it follows the one before it. Beside
it, `Type: qa` marks a QA phase (below), and `Replan: after` a phase whose findings should reshape the phases after it.
One phase is enough: a small task can be a plan of one phase. A plan with no phase headings is ordinary plan-mode
work, and baton leaves it alone. Each anchor is the phase's heading, or more of its lines where the plan quotes its
own headings, and fenced code is passed over, so the anchor occurs once.

- **The shape sets how long a plan runs.** Phase agents work beside the lead only on phases whose dependencies are
  done, so a plan's longest chain of `After:` lines, not its number of phases, sets how long it takes. In a real run,
  a plan of 57 phases had a longest chain of 42, and of the 12 phases it ran in 13 hours, 2 ran beside the lead;
  eight of ten plans saved on one machine had a longest chain over 60% of their phases, and six were one chain. So
  `/baton plan` builds a graph by six rules:
  1. A dependency is a need: a phase waits for another only for something it makes (code, a schema, a file, a
     decision), never because the other's gate checks it.
  2. Split enabling work: the small part others need goes first, in a phase of its own; the rest becomes a leaf.
  3. Contract first: fix an interface in a small phase, then build both sides at once.
  4. Split by files: divide work by the files each phase owns, and chain only what truly shares files.
  5. Outside waits off the main chain: waiting on CI, a deploy or an outside service belongs in a leaf, a QA phase
     or the release. In that run, a phase on the main chain that awaited CI went red five times in 3 hours, and the
     phase after it waited all that time.
  6. A small task can be one phase.
- **Checked before the human sees it.** At the lead's `ExitPlanMode`, each check once per plan, and none of them
  blocking (the same plan again goes on):
  - a plan with phase headings that baton could not attach as written (a duplicate id, an `After:` naming an unknown
    or later phase) is turned back with what is wrong;
  - the plan's first `ExitPlanMode` is turned back with baton's request to check it against CLAUDE.md, memory, skills
    and settings, and address in it anything that competes (a rule to run the full suite before every commit, say);
  - in the same turn-back, a plan that is mostly one chain (6 phases or more, the longest chain over 60% of them) is
    asked to check each link of that chain against the shape rules. Where every link holds, the model puts the plan
    to the human unchanged, saying in it why the chain is real. On those ten plans, the threshold flags the eight
    and passes the two at 43% and 58%.

  A stop in plan mode before the plan reaches the human is turned back once too.
- **The shape line.** As the plan goes to the human, they see its shape in one line: `baton: 12 phases · longest
  chain 5 (P1 → P7 → P8 → P9 → P11) · up to 7 at once`.

### Replans

What a run learns (a phase that turned out to wait on CI, phases that turned out independent, a newer baton's
planning rules) used to stop at the phases already done. A replan carries it to the phases not started: the lead's
model restructures them, in the session where the human sees it, and baton checks the result. **The model decides
how; the host decides when, and whether the result stands.**

- **Only on a cue,** never on a timer or the model's own initiative:
  - the human's `/baton replan "<what to consider>"`, done at once in that turn;
  - a run carried across an upgrade whose planning rules changed (`plan.RulesVersion`), once;
  - a QA phase under way while the phases not started are mostly one chain, once per QA phase;
  - a phase marked `Replan: after` (a spike, an exploration), once it is done.

  baton's own replans open at the lead's phase boundary, in its fresh context: the replan's brief comes before the
  next phase's, and the lead replans, runs `baton replanned`, then begins the phase. `auto_replan: false` turns them
  off.
- **A draft, checked.** `baton replan` copies the plan file to a draft and prints the brief: the phases not started
  and those that started, the shape now, the shape rules, the human's words, and what this replan may change. The
  model edits only the draft, and ends with `baton replanned --notes "<what changed and why>"`, or `--none "<why>"`.
  baton checks the draft before the plan file is touched: one that fails is refused with each thing it may not
  change, and the model fixes it.
- **What each kind may change.** Phases that started or are done never change, and the order and `After:` lines of
  the rest always may.
  - After an upgrade or at a QA phase, that is all: every other byte of each phase stays as approved, so however
    many such replans run, each phase's goal, steps and "done when" are what the human approved. One stands only if
    the phases not started got a shorter longest chain or more of them can run at once; otherwise the plan stays as
    it was, and the model is told why.
  - After a marked phase, the steps of the phases not started may also take in what it found, and a phase may be
    split, but no work is dropped or merged.
  - The human's may also add, split and reword phases, and, as far as their words ask, drop or merge phases or
    change the plan's goal sections (its title and opening, the standing rules, the end section).
- **Recorded, and undone in one step.** A draft that stands is written over the plan file and followed: the phase
  agents at work hear of their part, as of any change to the plan. It is a decision without the human, with its
  undo: copy the text the replan found back over the plan file. baton keeps the text the human approved, and
  `baton status` lists every replan with what it changed against that text, so drift across many replans stays
  visible.
- **Bounded.** While a replan is open, or owed at the lead's boundary, no new phase agent starts, since it could
  take a phase the replan restructures; the agents at work go on. One not finished in 20 minutes lapses, and the plan
  goes on as it was.

## A phase boundary

1. The model runs `baton done P3` and ends its turn. The Stop table allows the stop when the phase is done or the
   plan complete, when the run is paused or blocked, and when the phase waits on a job or on background work. It
   refuses a stop without a status, even in a turn the human started (to talk without the plan, the human pauses
   baton), and names the way forward. After three refusals in a row, it escalates.
   The model hears of its own background work (a command run in or moved to the background, a Monitor, a
   background agent) with the result of the call that starts it: the work wakes it when it ends, so it keeps working
   on what does not depend on that, and with nothing left, ends its turn. Its stop over that work is then allowed;
   the refusal that says the same is left for work whose start baton did not see.
   The boundary that the model's own `done` owes holds the rest of its turn, and its stop queues the compaction; so
   does the lead's `done` of a worker's phase (below).
2. The host asks the mod to compact the lead once nothing is in the way: no turn, no dialog, none of the lead's own
   subagents at work. The human's draft stays in the box. The host asks once the lead's Stop hook has run, a
   moment before Claude Code counts the turn over, and Claude Code compacts only between turns: so the mod holds a
   compaction it refuses while the lead's turn runs, and calls it from that turn's end (`turn.complete`), where it
   is taken ([spike 37](../spikes/37-compact-at-turn-end/)). One still refused 30 seconds later is asked again after
   the turn, using up no try. In serial mode the host types `/compact`, and an unsent draft is saved and
   cleared after a grace period, unless an agent may be live: then the draft holds the compaction, and the human is
   told why.
3. **Only proof moves a compaction.** A phase agent's compaction fires `PreCompact`, `SessionStart(compact)` and
   `PostCompact` with no `agent_id` and the lead's transcript, so those hooks cannot tell whose compaction it is.
   They log `compact_seen` and change nothing. `state.Proof` has one constructor per source of proof:
   - the mod's report of the lead's compaction, which it sees before and after, or of the one it ran on baton's
     command, counted once by that command whatever trigger Claude Code gives it;
   - a new `compact_boundary` entry in a loop's own transcript: the lead's, or a phase agent's
     `subagents/agent-<id>.jsonl`.

   Every transition past "typed" (the host has asked, by the mod or its keys) takes a Proof.
4. `PostCompactRewake`, registered for every trigger, waits a bounded time for the proof. Then it wakes the model
   with the next phase's brief: the phase's section of the plan, the standing rules, the notes, and the decisions
   made without the human.

**A boundary baton queues itself** (a gate's verdict, a landing, a switch to a phase that can go on) never cuts a
turn short. A turn under way when it was queued, or one the human started, runs to its end: the model is told once,
with a call's result, that the next phase comes after this turn and not to start it there, and the compaction waits
for the turn's stop. A boundary that does hold a turn says what comes next: the phase begins, resumes, or reopens
after its verification failed.

**Mid-phase valves.** The status line gives the exact context size. At 60% of the limit (the model's window, or the
`--autocompact` cap if smaller), the model is asked to checkpoint at its next safe point, which goes through the same
loop. At 90%, but never under 200k tokens, the human is asked whether to checkpoint now; unanswered for 10 minutes,
baton keeps going. Claude Code's own compaction, 33k tokens below the limit, is the backstop.

**The watchdog** catches a run that has gone quiet: idle turns, stale gates, unanswered prompts, usage limits and API
errors. Everything baton waits on either clears by itself or expires into action, except an open permission prompt.
Each loop's watchdog stays quiet while it waits on something that ends by itself, so **the stall alarm** looks at the
run as a whole, about once a minute: with the plan running and phases left, nothing busy (a tool call, the model's
own background work, a gate whose log still grows), and nothing in the lead's run or any phase agent's sub-run moved
for 20 minutes, it pushes the human one notice naming what each phase waits on. It holds nothing, and the run moving
arms it again. A usage limit, an open dialog and a machine just woken are never a stall.

## Gates

A long gate goes to baton:
- `baton verify` runs it on `HEAD` in a lane: a detached worktree under `~/.baton/lanes/`, set up by the project's
  `.baton-lane`.
- `verify --here` runs it in the checkout, and holds edits and landings there until it ends. In a phase agent's
  worktree it also holds one of the project's lanes while it runs, with `BATON_LANE` set to it, so here and lane
  gates together never run more at once than the lanes allow, and per-lane resources (a test database, a Compose
  project) stay apart. With no `.baton-lane` there is one lane, so agents' here gates take turns. A gate waiting for a
  lane is announced with how many are ahead of it.
- `baton await` waits on something outside the project.

A phase runs one gate and one wait on something outside at a time, side by side: a local gate and an outside wait
never check the same thing, so a `verify` never stops an `await`, nor the reverse. In a real run, a QA phase's wait on
CI replaced its running suite, and queuing the suite again replaced the CI watch: of that run's 16 replacements, 12
swapped one check for another, and phases took to gluing gates into one command. Within a kind, the same command
queued again replaces the one that runs (the gate on a newer commit, or the wait again). Another command is refused,
naming the job that runs: the model stops it with `--replace`, folds both into one command, or queues its own once
the result comes. The phase stands on its latest job of each kind: it is done once both passed, and reopens on the
first red.

A gate that hangs in silence is stopped: a `verify` job whose log has not grown and whose processes used no CPU for 20
minutes ends unverified, with the reason, and the model is told. Log silence alone never stops one, so where the CPU
can't be read (Windows), a gate runs to `verify_timeout`.

The job runs detached (`baton lane-run`), so it outlives claude. Its result reaches the model at its next tool call
or its stop, or wakes it. A finished phase is "verifying" while its jobs run: it is done when they pass, and
reopens when one fails. A phase that can't go on without its result is set aside after `switch_after` for another phase
that can start, and later resumes from its notes.

## Phases at once

The plan's `After:` lines make a graph. The lead, the session in the human's terminal, works one phase at a time.
Each other phase that can start runs as a **phase agent**: a background agent of the lead's session.

- **The mod is a thin executor.** `plugin/hooks/baton.js` is JavaScript that Claude Code runs in the lead's claude.
  It spawns and messages `baton:phase` agents, asks the human in Claude Code's own dialog, compacts the lead and
  submits baton's lines to it, drops the agents' reports before the lead's model reads them (a task notification, or
  in auto mode a peer hand-back), draws the band, and reports what it sees. It decides nothing and words nothing:
  the host does both. A mod of another baton release speaks another protocol, and the run is serial.
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
  - Nothing compacts a phase agent at a boundary, so its sub-run never owes one. When its gate fails after its
    `baton done`, or a landing's verification fails, the phase is taken up again in place, and the result goes to
    the agent; one that ended is started afresh, with the result in its brief.
  - A change to the plan reaches the agents at work: when the standing rules or an agent's own section read
    differently from what it was given, it gets the new text as a message, once.
  - A refused, errored or interrupted turn is the host's to handle. An API error a fresh context avoids starts the
    agent afresh from its brief, with the lead's backoff (below).
  - An agent the human stopped from the agent panel is asked about in the lead's terminal: Restart it · Leave it
    stopped · Pause baton. Unanswered at `escalation_timeout`, baton decides from its record: an agent whose work is
    done (its phase done and not red, or landed) is let go and its work lands; otherwise the first stop restarts it
    and tells the human, and a second takes it down and asks the human what to do with the phase, with no deadline,
    since they are the one in the way. That question steps aside after `escalation_timeout` and comes back when they
    next write; only that phase and those that need it wait. Leave it stopped, and `/baton run` starts a fresh agent.
  - An agent's permission prompt holds that agent alone where the mod runs: the lead's compaction, its deliveries and
    its switching go on under it, and the prompt survives them ([spike 36](../spikes/36-agent-dialog/)). In serial
    mode any dialog holds the lead, since a typed key would answer the agent's prompt.
  - A model's own call to the `baton:phase` type is refused.
- **Room.** `workers: auto` sizes the fleet to the machine. New agents start at least 2 minutes apart, since the
  machine's readings lag the load an agent brings. A new agent also waits while the machine is busy, the lanes fall
  behind, landings keep conflicting, a usage limit holds, or a replan is open or owed ([Replans](#replans)). Every
  hold is host code.
- **Landing.** A finished phase joins the merge queue. If the run's branch has moved on, it is merged into the phase's
  branch in its worktree, and a conflict goes back to its agent, which merges and resolves it itself. Nothing baton
  runs, or asks an agent to run, rewrites a commit: a rebase can drop a resolved conflict without a word, and Claude
  Code's auto mode may refuse one as destructive. A merge starts only on a worktree with nothing uncommitted, and one
  that stops is aborted, leaving everything as it was. The phase is verified again if the run's branch moved (at most
  once for the checkout's own commits). Then the run's branch is fast-forwarded to it with `git merge --ff-only`
  between the lead's tool calls, never over a file the lead has uncommitted. The lead is told what moved, and never
  to rewrite it. A landing held for 10 minutes by the lead's uncommitted changes to the same files tells the lead's
  model to commit those files by path; one held because the checkout is off the run's branch is pushed to the human
  once.
- **Landed some other way.** When every commit of a phase's branch is on the run's branch already (merged by hand),
  the lead's `baton done <phase>` records it landed, as a decision without the human with its undo, and the host
  retires the agent, saving what it had not committed as a snapshot first. Otherwise the refusal says how to finish
  the phase: its agent's own `done`, or a merge into the run's branch first.
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
| `baton blocked "<why>" --tried …` | irreversible, outward-facing, or needs a secret only the human holds | the phase stops; unanswered for 30 minutes, the model looks again |

- **The default is the action.** A question with a fallback failed in the replay: the models put the degraded path
  in the question and inaction in the default, then blocked, 4 times in 4. With `propose`, whose default *is* the
  action and must make progress, the replay finished the plan 4 times in 4.
- **Every refusal names the way forward,** from one playbook: commit degraded rather than lose work, take a
  worse-but-working path, propose a way around a rule. `blocked` needs `--tried`.
- **Nothing waits on the human for what baton can decide** (v0.8.3, v0.8.4). With no deadline, baton waits on them
  only where they chose it (Pause baton, `/baton pause`, a `max_auto_decisions` they set, a phase agent they stopped
  twice whose work is not done) or where nothing in baton can answer (a permission prompt, the usage limit, a login
  or billing error, a refusal, which is never retried). Every other wait has a deadline, and the deadline picks the
  model's side: the human said they would rather correct an assumption later than find a run idle for hours, and
  that nothing should wait more than a few minutes, 20 at most. Only a block waits longer, 30 minutes, as below.
- **Wait for me lapses.** A hold the human chose (Wait for me, on a proposal or on one of baton's questions) lasts
  until they write, or for 20 minutes with no word from them. Then it lapses into the question's default, and the
  model is told.
- **The timed proposal** is the question `baton: <because>. Unless you answer by HH:MM, I will <action>.`, with the
  answers Wait for me · Pause baton · Go ahead. It goes ahead at its deadline, never sooner than a minute after it
  appears, and a key press restarts the clock. The model sets the wait (`--wait`, 5 to 20 minutes), told to give the
  shortest that fits since every minute waiting is time the run loses; with none, or one out of range, it waits
  `escalation_timeout` (10 minutes). A question that goes away unanswered (Esc, a restart) is nobody's choice: the
  proposal goes ahead at its deadline all the same (the lead's loop settles it, `Loop.lapse`), unless the human
  writes first. A phase agent's proposal counts its wait from when it was made, so one the human never saw (the
  lead's own question went first) goes ahead too.
- **An outward proposal is answered Not now.** One that names an outward or irreversible act (push, publish, deploy,
  delete, force, …) gets a deadline like any other, with Not now among its answers, third, where the default sits.
  At the deadline baton answers Not now for the human, and the model is told: do it only if it is safely reversible
  and stays inside the project (pushing its own branch, a draft pull request, deleting files it generated), and
  `baton note` it; otherwise leave it undone and carry on with everything that does not need it. baton's word list
  cannot tell a push to the model's own branch from a deploy, so the model judges which it is. What was left so is
  listed by `baton status`, and the run's summary names it as left for the human.
- **baton's own questions answer themselves.** The stall question (Pause baton · Wait for me · Continue, when the
  model stopped without a status) and the question about a plan approved mid-run (Wait for me · Start the new plan
  from P0 · Continue with the revised plan, or for another plan Keep the current run; Wait for me first, where a
  stray Enter lands, so no single key sets a run's progress aside) wait `escalation_timeout`; then the third
  answer: Continue, and for the plan, a revision of the running plan is followed and another plan is left for
  `/baton run`. The mod answers one on screen (`hooks.Bound`); the host's loop answers one that never reached it
  (`hooks.Lapse`); in serial mode the host types its `3`. A review the human opted into is never answered for them.
- **The model's own question** (AskUserQuestion, in a turn the human started while a plan runs) is answered at
  `escalation_timeout` where the mod runs: the option the model marked Recommended, else its first, and the model is
  told that baton chose for the human. Serial mode leaves it to the human, since no typed key can pick an option
  safely.
- **A block looks again.** A block is the model's judgement that only the human can answer, so it waits longer than
  any question, but not for good: unanswered for 30 minutes (from when it began, or the human last wrote), it is
  lifted, and the model is told to look again for a safe, independent way to go on (the parts that don't need the
  answer first, or a reversible way around) and to propose it, which goes ahead at its deadline. Blocked again for
  the same reason, the same loop runs again.
- **Nothing gives up.** A failed compaction is retried every 15 minutes for as long as it fails; a session the
  watchdog has down as stalled still gets its results, and a reminder every 15 minutes; a plan file baton can't
  follow is reported, and the run goes on with the plan as attached; a lead's block holds the lead alone.
- **Answering its own question.** Where the mod runs, its `tool.call` hook answers baton's question at the deadline,
  so nothing types into a dialog. That matters because dialogs queue, and in Claude Code 2.1.296 a permission
  prompt's third option is "Yes, and switch to auto mode". Where the mod doesn't run (serial mode), the host types
  `3`, and only while baton's question, in exactly the shape baton issued, is the only dialog open.
- **A phase agent's question** is asked by the mod in the lead's terminal, without stopping the lead's turn, and the
  answer goes to the agent as a message. A phase agent's block stops only its phase, and looks again after 30
  minutes as the lead's does (its question's default is Look again).
- **Durability.** `blocked` and `done` refuse fresh uncommitted work unless the model says why. Whenever the run
  halts, the host also snapshots that work into `refs/baton/snapshots/`: staged, unstaged and new files, leaving the
  working tree, the index, `HEAD` and gpg untouched.
- **Every decision made without the human is recorded,** with its undo, in baton's state. It is announced, listed by
  `baton status` and `baton done`, carried in each brief, and handed to the model when the human next types, so a
  late answer works after any compaction. Only a human who set `max_auto_decisions` gets a review after that many
  in one phase (it is off by default); a decision made with no phase in hand is the run's, and counts toward no
  phase's review. The model cannot clear its own block.
- **QA phases carry the heavy checks** (`Type: qa`, `aggregate_qa`, on by default). Dense checks at every phase
  burn hours and hold everything behind them, so a phase before a QA phase is briefed to run focused checks only
  (the tests of what it changes and what that likely affects) and may end in a work-in-progress state; the QA
  phase runs the full suites and lints once, and fixes what the phases before it left incoherent. baton cannot
  tell a focused check from a full one, so it asks the model once of each `verify` or `await` queued before a QA
  phase (a wait on CI is often a full check in disguise), and the job runs either way; `--qa "<why now>"` flags a
  full check run early on purpose. A red result there says that other phases' work in progress may be why: the
  model fixes what is its phase's, and finishes with `baton done --despite-red "<why>"` if the rest is not.
- **Competing instructions are flagged before approval,** with the plan's shape ([Plans](#plans)).
- **Without git** there is no gate and no snapshot, and baton says "complete" rather than "committed".

## When the human steps in

- **A turn the human starts runs to its end,** even when baton takes up a phase meanwhile (above).
- **`/baton pause`** hands the session over: baton stops compacting, nudging and escalating until `/baton run`. A
  compaction proven while paused, the human's own `/compact` or one baton queued before the pause, covers the one
  the run owed. At `/baton run` the owed phase starts from its brief with no compaction of baton's, unless the
  conversation has grown by more than 20k tokens since, when the model checkpoints first.
- **A plan attached while paused** stays paused until `/baton run`, unless the human's answer to baton's question
  about it is to start it now.
- **A plan's record is kept.** Before another plan replaces it, or `baton drop` sets it aside, its state, anchors and
  handoff are copied into `.baton/runs/<run>/gen-<N>/`, and `baton status` lists the run's earlier plans with how
  many phases each finished.
- **The plan file may change while it runs.** baton follows the new text, and the phase agents at work hear of a
  change to their part of it (above). An edit made while a replan is open is followed once the replan closes, and
  the replan's draft, made from the older text, is refused.
- **`/baton replan "<what to consider>"`** has the lead's model restructure the phases not started at once, as far
  as the human's words ask ([Replans](#replans)).

## Recovery

- **A refused turn is escalated, never retried.** baton reads the refusal from the transcript (read-only), types
  nothing into that conversation, starts no fresh one, and never switches models. The human rewinds or clears.
- **A crash is recovered without asking, for as long as it takes.** The host resumes the conversation in place, then
  each phase agent by a message. Two crashes in 30 minutes are brought back at once; past them the host waits 5
  minutes, then 15 between tries, and never exits while the plan runs (a Ctrl-C in that wait stops it). A
  conversation that cannot be resumed gets a fresh session from the phase's brief.
- **An API error a fresh context avoids** (a prompt too long, a request too large, a broken tool history) starts the
  lead, or the phase agent, afresh from its brief, with the same backoff. A refusal, an expired login, billing and
  an unknown model stay the human's.
- **A rewind leaves baton's record standing.** baton marks the transcript's last entry at each prompt, and checks that
  the next prompt descends from it. After a rewind, the model is told what still stands.
- **`/clear`** gets the phase's brief. It ends every phase agent, so baton starts each unfinished phase again in its
  worktree.

## What never changes

- An open permission prompt is never answered or typed through.
- Nothing irreversible or outward-facing goes ahead on a timer.
- Nothing that needs a secret only the human holds goes ahead without them.
- A turn the model's safeguards refused gets no retry, no rephrasing and no other model.
- Anything baton cannot classify is treated as hard.
