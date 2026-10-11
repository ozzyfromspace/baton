# Design note: graduated escalation — a run must not stop for something it can fix

Written 2026-10-05, from a live incident on a 17-phase plan (the "quiet campaign" in a separate repo).
**Accepted the same day, and implemented in v0.2.0.** The first draft was a proposal with no spikes behind
it. The spikes in [`research/escalation.md`](research/escalation.md) settled its open question, and
changed two of its central mechanisms. This version describes what baton does.

**v0.4.0 removes the declared wait,** and its command with it: a long command moves to the background
by itself and wakes the session when it ends, a long gate goes to `baton verify`, and a wait on something
outside the project goes to `baton await`, whose result baton delivers like a gate's. A stop without a
status now offers `await` alongside `done`, `propose` and `blocked`. `blocked` is unchanged, and still
stops the whole run: the jobs baton runs keep going while it waits, and the run picks up once the human
answers. The diagnosis below describes the statuses as they were at the time.

**v0.5.0 adds workers,** hidden sessions that work on other phases beside the lead in worktrees of their
own. A worker's proposals, blocks and reviews are put to the human by the lead, one at a time, and a
worker's block stops only its phase (section 10). Every request baton makes of the model mid-turn now
arrives as hook feedback rather than `additionalContext`, which Haiku sometimes refused as possible
injection.

It extends [`docs/research/reliability.md`](research/reliability.md), which asked *what can stall or
loop a run*. That review looked for stalls caused by Claude Code's own non-determinism. This one is
about a stall **baton's interface invited**: a run that halted over a recoverable problem, with the
phase's work verified and sitting uncommitted.

## The incident

At the end of a phase, every gate green (`14/14` hover, `560` a11y, `30` preflight gates,
typecheck clean), the model went to commit and GPG signing timed out. The user had hard-restarted
the machine.

The model ran `baton blocked "…"`, which paged the human and stopped the run. The work, about eight
hours of it, stayed **staged but uncommitted** for as long as the block lasted. The human happened
to be watching, and answered eleven minutes later (`events.jsonl`: blocked 15:28:35, answered 15:39:36).
Their words:

> "This theoretically isn't a blocker. why is baton getting in the way here? … isn't this something
> you can technically do on your own? if I missed the notification, I could have lost 8 hours of
> work."

They were right twice over. An unsigned commit is recoverable in one command
(`git commit --amend --no-edit -S`), so the degraded path was cheap and reversible. And the run's own
standing rule, quoted into every phase brief baton writes, is *every phase ends committed*. The halt cost
eleven minutes only because someone was looking. What was at stake was everything after: unwatched, the
run would have sat uncommitted until somebody noticed.

(The model's diagnosis, "gpg-agent lost its cached passphrase", turned out to be wrong. Signing kept failing
for another reason: a `pubring.db.lock` left behind two weeks earlier named a process id that the restart
handed to a system daemon, so gpg waited on a live "holder" forever. No model should be expected to find
that mid-run, which is the point: the run has to keep moving without the diagnosis.)

## Diagnosis: one token, three meanings

baton refused a stop without a status and offered four:
`done` · `blocked` · a declared wait · `checkpoint`.

The model's actual state was none of them. The phase was finished and verified, so `done` would have
been a lie; a declared wait was for external work with a duration; `checkpoint` compacts mid-phase.
**The only remaining token was `blocked`**, and `blocked` is defined to halt the run and page a human.

Three distinct meanings were collapsed into it:

| what the model needs to say | what it had to use | what it uses now |
|---|---|---|
| I cannot proceed without you | `blocked` | `blocked --tried` |
| I want to do something you might not; stop me if so | `blocked` | `propose` |
| I proceeded, but you should know | `blocked` | `note` |

It is worse than naming, because **`AskUserQuestion` is refused while a run is live**. So `blocked`
was not merely the cheapest way to reach the human; it was the only way. An interface in which
*notify* and *halt* are the same operation will be used to halt for things that needed only a
notification. `--notes` is not an escape hatch: it feeds the **next phase's brief**, so it reaches
the next context rather than the human, and is compacted away unread.

The replay confirms it. Run through baton v0.1.1 with nobody at the terminal, Sonnet in auto mode reproduces
the incident 4/4: "NOTES.md requires signed commits so I did not use --no-gpg-sign", then `blocked`.

## Why this belongs in the host

From [`CLAUDE.md`](../CLAUDE.md):

> **Control flow lives in host code, not in model judgment.** If a step only happens because the
> model remembered to do it, it is a bug waiting to happen; make a hook or the host do it.

"Before hard-blocking, look for a reversible alternative" is exactly such a step. It was left to model
judgment, and the judgment went wrong on its first real test. Fixing it with a better instruction would
leave every other user's model free to make the same call. It is a host problem.

Note also what baton *did* enforce versus what it did not: it policed the **reporting** protocol
rigorously (a stop without a status is refused), and never checked the **durability** invariant it
exists to protect. `baton blocked` accepted a dirty tree without comment.

## The design

### 1. The default is the action: `propose`

The first draft said: make the model compute a reversible **fallback** before it may escalate, and let
soft-versus-hard fall out of whether one exists. The replay disproved it. Given `ask "<question>"
--fallback "<default>"`, the models put the degraded path in the *question* ("May I commit unsigned?")
and inaction in the *default* ("leave it staged and report blocked"). Nobody answered, so they did nothing,
and blocked: 4/4. "Do nothing" is always reversible, so requiring reversibility alone forces nothing.

So the shape is action-first:

```
baton propose "<what you will do to keep the plan moving>" --because "<what went wrong>" --undo "<how to reverse it>"
```

The default *is* the action, and it must make progress. Waiting, doing nothing or stopping is not a
proposal: that is `blocked`. And the model is told plainly what a proposal is for: **when a rule or
convention of the plan cannot be followed right now, propose the closest reversible way around it — the
human gets a deadline to veto it, and an undo after.** That is the case the incident was. With it, the
replay completes the plan 4/4: each run proposes "commit unsigned; re-sign with `git commit --amend
--no-edit -S`", nobody objects, and the run goes on.

### 2. Three tiers

| tier | command | when | behaviour |
|---|---|---|---|
| **act + notify** | `baton note "<what you did>" [--undo …]` | fully reversible, default obvious, or a repeat of something already let through | Do it, then tell baton. Announced and recorded; the run continues. |
| **timed proposal** | `baton propose "<action>" --because … --undo …` | reversible, but a human might choose differently, or a rule can't be followed right now | baton puts the proposal to the human with a deadline. No answer means the model does it. |
| **hard ask** | `baton blocked "<why>" --tried "<what you tried>"` | irreversible, outward-facing, or needs a secret only the human holds | Announce and stop. No timer. |

One proposal covers the case in front of the model. If the same degradation recurs (the next phase's
commit fails the same way), the model notes it each time, so the human's list is complete.

### 3. The timed proposal

- **The question.** `baton propose` prints the exact `AskUserQuestion` call to make:
  - question: `baton: <because>. Unless you answer by HH:MM, I will <action>.`
  - three options in this order: `Wait for me` · `Pause baton` · `Go ahead`, single-select.

  Until the model has asked it, baton holds every other tool and refuses every other status.
- **Reach.** The question reaches every device the human uses: Claude's own question notification is the
  one channel that reaches a phone even without an ntfy topic. baton also pushes its own notice.
- **The timer.** It starts when the dialog opens. Default 5 minutes (`escalation_timeout` in
  `~/.baton/config.json`). It never fires sooner than the printed deadline, nor within a minute of the
  question appearing.
  - **A key press restarts the timer, rather than cancelling it.** A human at the keyboard is there, but a
    stray key must not turn the proposal into a hold that lasts forever.
  - Opening a notification is not a signal baton can see, and is not one it waits for.
- **No answer.** baton answers the question itself, `Go ahead`, by typing `3`, and the model does what it
  proposed. The record says baton decided on the timer, not the human.
- **Hold.** `Wait for me` stops the clock: the proposal becomes a hard ask, with reminders, and the run
  waits for the human's answer in the session. `Pause baton` hands the session over. A typed answer is
  relayed to the model as the human's instruction.
- **Declined.** If the question disappears without one of these answers (Esc, an interrupted turn), baton
  treats it as held.
- **A late answer still works.** When the human next types, baton hands the model every decision made
  without them, each with its undo. That comes from baton's state, not the model's memory, so it survives
  compaction (and `/clear`). "Did X — want it undone?" is answerable at any time.
- **No timer at all.** A proposal whose action looks outward-facing or irreversible (push, publish, deploy,
  delete, force, …) gets no timer: it waits for the human like a hard ask.

### 4. Answering its own question safely

The first draft's gating question: may baton answer its own `AskUserQuestion`, given that typing into the
wrong dialog could approve something nobody approved? The spikes found this was already a live hazard.

- Claude Code queues dialogs first-in, first-out, and fires `PermissionRequest` when a dialog joins the
  queue, not when it is shown.
- A background subagent's permission prompt requested before baton's question sits **in front** of it.
- In a Bash permission prompt, `2` is "Yes, and always allow".
- v0.1.1 remembered only the last dialog requested, and answered its context question with `2`.

So baton now:

- **Tracks open dialogs as a queue.**
  - Entries close only on an exact match (agent, tool, input).
  - Turn ends and idle notices sweep stale ones.
  - A stale entry only ever holds baton back.
- **Answers only when its own question is the only dialog open,** and only when that question has exactly
  the shape baton issued (one question, its labels in order, single-select). A model that rewords or
  reorders baton's question is refused and shown the exact call.
- **Puts the default at option 3 and types `3`.** In a Bash permission prompt that key means "No", so even
  a wrong guess denies rather than approves. The context question became
  `Checkpoint now · Pause baton · Keep going` for the same reason.

The one gate that is never overridden is unchanged: an open permission prompt.

### 5. Generative refusals

A refusal that only says what the model may not do invites it to reach for the one exit it remembers.
So every refusal names the way forward, from one playbook kept in baton:

```
A run must not stop for something it can work around. Before hard-blocking:
  · uncommitted work?   commit it, degraded if need be (e.g. --no-gpg-sign); never discard or unstage work
  · a step failed?      is there a worse-but-working version? do that
  · a rule or convention of the plan can't be followed right now?
                        that is what baton propose is for: the human gets a deadline to veto your way around it
Then pick the lightest status that fits:
  · baton note …        you already took a reversible path; the human should know
  · baton propose …     a human might choose differently; no answer means you do it
  · baton blocked …     only if every way forward is irreversible, or needs a secret only the human holds
```

`blocked` without `--tried` is refused with this text. That turns "find another way" from a virtue the
model must remember into a field it cannot leave blank. (In a project without git, the "uncommitted work"
line is dropped. baton never tells the model to commit, or to create a repository.)

### 6. Durability: a gate, and a guarantee

The first draft's "single highest-value item" was a clean-tree gate on the halting statuses. The replay
showed the limit of any gate the model can satisfy itself. Refused, the models passed `--keep-dirty` with
a principled reason (the signing rule). One got past it by unstaging its new file. So there are two layers:

- **The gate prompts.** `blocked` and `done` refuse dirt that is new or changed since the phase started,
  unless `--keep-dirty "<why>"`. Dirt that was already there when the phase began only warns. `checkpoint`
  warns.
  - `await` is not gated: a wait often comes before the commit (the incident run declared two waits on
    its own test runs, both legitimate). `verify` refuses fresh uncommitted work instead, since it runs
    the gate on `HEAD`.
  - `propose` is not gated: the uncommitted work is often what the proposal is about.
- **The snapshot guarantees.** Whenever the run halts with a dirty tree, the host saves the work into a
  private ref, `refs/baton/snapshots/<phase>-<time>`. It covers staged and unstaged changes and new
  untracked files, and leaves out ignored files. It touches nothing: the working tree, the index, `HEAD`
  and gpg are all left alone. It takes about 60ms.
  - **When:** an escalation, a dialog waiting on the human, a usage-limit wait, a pause, and the session
    ending.
  - **Where it shows:** `baton status` lists the snapshots with the command to restore one.
  - It is taken by the host, never by the model, so it cannot be routed around.

### 7. Record every decision made without the human

Yielding control has a price: **a degradation that used to page the human becomes invisible unless it is
recorded.** So every note and proposal is kept in baton's state for the whole run, with its undo. It is
surfaced in:

- the session (`systemMessage`) and `events.jsonl`, like every action baton takes;
- `baton status`: the list of decisions made without the human, with how to undo each;
- `baton done`: that phase's decisions;
- the plan-complete notice: the count;
- the brief after each compaction: the most recent ones, so later phases know about the unsigned commit;
- the human's next message: the ones they have not been shown yet (section 3).

The model cannot clear its own block. While a block or a held proposal waits for the human, `run` (to
resume), `done`, `checkpoint`, `verify`, `await` and `blocked` are refused until the human has taken part since:
typed in the session, or answered one of baton's questions. If the model could silently clear its own
blocked state, `blocked` would stop being a trustworthy signal.

### 8. Budget the decisions made without the human

Each decision being individually reasonable does not make a dozen of them reasonable. When a phase reaches
`max_auto_decisions` (default 5; notes plus proposals that went ahead on the timer), baton stops for a
review before anything else, a phase boundary included. Further notes and proposals are refused until the
human picks Continue. Without a cap, a long unattended run can drift a long way on individually defensible
calls, and the human only finds out at the end.

### 9. Projects without git

A plan runs the same in a folder that is not a git repository, or where git is not installed. One check
decides it: is there a `.git` at the project root, and `git` on the `PATH`? Without git:

- there is no gate and no snapshot;
- there is no "no commit since the phase started" warning;
- baton's wording says "complete" and "saved" rather than "committed", and never asks the model to commit
  (or to create a repository);
- `events.jsonl` records which mode the run was in.

### 10. Workers: questions relayed through the lead, blocks per phase

Added in v0.5.0. A worker is a hidden session that works on one phase in a git worktree of its own, beside
the lead session in the human's terminal ([README](../README.md#working-on-phases-at-once)). Nobody
watches a worker, so nothing it needs from the human may wait in its own terminal. The spikes behind this
section are in [`research/workers.md`](research/workers.md).

- **Queued, not asked.** A worker's `note`, `propose`, `blocked`, and a review that falls due are recorded
  in its own sub-run, exactly as in the lead. A note is announced in the lead. Anything that needs an
  answer is queued for the lead instead of being asked, and the worker is told that baton is asking the
  human: it works on what does not depend on the answer, or ends its turn, and that stop is let through.
  While its proposal waits, the worker may not do it, report the phase done, or propose again.
- **Asked by the lead, one question at a time,** and only while the lead has no question of its own open.
  The question names the phase: *baton: worker P3: gpg signing timed out. Unless you answer by 15:33, I
  will commit P3 unsigned.*
- **Hook feedback, not context.** The lead's model is given the question as hook feedback, which it asks
  word for word: with the result of its next tool call (PostToolUse `decision: block`), by a refusal of
  its stop, or, when it is idle, with the prompt of a short line baton types
  (`[baton] worker P3 has a question for you`). Its other tool calls are held until it has asked. Spike 24
  found that `additionalContext`, baton's channel for mid-turn requests until then, was refused by Haiku 2
  times in 5 as possible injection; hook feedback was asked 12 times in 12. Since v0.5.0, every request
  baton makes mid-turn goes that way, the context-warning question of section 4 included.
- **The deadline starts when the question is issued.** The third option is what baton picks when nobody
  answers, by typing `3` under the conditions of section 4:
  - a timed proposal: `Wait for me` · `Pause baton` · `Go ahead`, and baton picks Go ahead at the time the
    question names;
  - a proposal with no timer (outward-facing or irreversible): `Go ahead` · `Pause baton` · `Wait for me`;
  - a block, or a review: `Continue` · `Pause baton` · `Wait for me`.

  For the last two, baton picks Wait for me after `escalation_timeout`.
- **Wait for me never holds the lead.** It takes the question off the lead's screen and leaves it in the
  worker's own session, where the human answers it by typing into the worker (`baton view P3`, in another
  terminal). A proposal waiting there is not done; a block or a review is answered by what the human
  writes. It is pushed, naming `baton view P3`, and the status line shows `! P3 needs you`.
- **Answers go to the worker's sub-run,** as its own answer would have: Go ahead resolves the proposal (a
  Go ahead on the timer counts toward `max_auto_decisions` for the worker's phase), Continue ends the block
  or the review, an answer in the human's own words reaches the worker as their instruction, and Pause
  baton pauses the whole run. The worker gets it like a job's result: with its next tool call, at its stop,
  or with a line typed into its idle session. A question settled meanwhile in the worker's own session, or
  whose worker is gone, is withdrawn from the lead.
- **A worker's block stops only its phase.** The lead and any other worker carry on, and the phases that
  need the blocked one wait. A block in the lead still stops the whole run: no worker starts and nothing
  lands until the human answers, though workers already at work carry on.
- **Decisions stay with their phase.** A worker's decisions are numbered with its phase (`P3.d1`), so ids
  never clash across the run, and the cap is per phase as before. `baton status`, the lead's next prompt
  after the human types, and the plan-complete notice include them.
- **Never relayed: a permission prompt, or any other dialog, in a worker's session.** baton never answers
  one and never types through it, in a worker as in the lead. The lead is told, the human is pushed, and
  `baton status` names `baton view P3`. Only that phase waits.

## What this must not become

Graduated escalation must be **scoped by reversibility**, or it is the worse failure in the opposite
direction: a model told to keep moving will eventually route around a failing test, a confirmation,
or a permission prompt.

Unchanged, and not negotiable:

- **An open permission prompt.** baton cannot answer one and must not type through it. If a run waits on
  one, the human is the only way forward.
- **Irreversible acts**: deleting data, pushing, publishing, anything outward-facing. A proposal that looks
  like one gets no timer.
- **A secret only the human holds.** The incident is the boundary case worth noting: the *signature*
  needed the human, and committing did not. The way around existed because the two were separable. Where
  they are not, it is a hard block.
- **Anything baton cannot classify.** Default to hard.

## The open questions, answered

**1. May baton answer its own `AskUserQuestion`?** Yes, under the conditions in section 4. Never typing
into dialogs (option b) was safer on paper. But without an ntfy topic, Claude's question notification is
the only thing that reaches the human away from the machine, and the timer needs someone to answer when
nobody does.

**2. Does an open dialog suppress the signals a timer needs?** It does (`idle_prompt` does not fire while a
dialog is on screen). The timer is the host's own clock, adjusted for sleep, and a key press restarts it.

**3. Who classifies the tier?** The model proposes; it chose the action. baton withholds the timer when the
action matches a list of outward or irreversible verbs. A false match costs a wait (today's behaviour),
never an unapproved act.

**4. Should a per-case timer override the default?** No. There is one default (`escalation_timeout`, 5m),
and outward-looking proposals have no timer at all.

## What this would have done to the incident

1. The commit fails. The model proposes: "commit P4 unsigned; re-sign with
   `git commit --amend --no-edit -S`", because "gpg signing times out after the restart".
2. The human's phone shows the question: *unless you answer by 15:33, I will commit P4 unsigned.*
3. Nobody answers. At 15:33 baton picks Go ahead, and the model commits, unsigned.
4. The run continues into P5. `baton done P4` lists the one decision, with its undo. The next time the human
   types, the model is reminded of it, so "re-sign that" just works.

Had the model still reached for a hard block, `--tried` would have been required, the gate would have asked
it to commit first, and the snapshot would have saved the staged work regardless.

Nothing is lost if the human is asleep, and the one-command repair is in their hands when they wake.
