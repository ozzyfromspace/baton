# Design note: graduated escalation — a run must not stop for something it can fix

Written 2026-10-05, from a live incident on a 17-phase plan (the "quiet campaign" in a separate
repo). This is a proposal, not shipped behaviour. Nothing here has a spike behind it yet; the one
question that gates the whole design is named in [Open questions](#open-questions).

It extends [`docs/research/reliability.md`](research/reliability.md), which asked *what can stall or
loop a run*. That review looked for stalls caused by Claude Code's own non-determinism. This one is
about a stall **baton's interface invited**: a run that halted for four hours over a recoverable
problem, with the phase's work verified and sitting uncommitted.

## The incident

At the end of a phase, every gate green (`14/14` hover, `560` a11y, `30` preflight gates,
typecheck clean), the model went to commit and GPG signing timed out — the user had hard-restarted
the machine and `gpg-agent` had lost its cached passphrase.

The model ran `baton blocked "…"`, which paged the human and stopped the run. The work — about eight
hours of it — stayed **staged but uncommitted** for as long as the block lasted. The human happened
to be watching and answered. Their words:

> "This theoretically isn't a blocker. why is baton getting in the way here? … isn't this something
> you can technically do on your own? if I missed the notification, I could have lost 8 hours of
> work."

They were right twice over. An unsigned commit is recoverable in one command
(`git commit --amend --no-edit -S`), so the degraded path was cheap and reversible. And the run's own
standing rule, quoted into every phase brief baton writes, is *every phase ends committed*.

## Diagnosis: one token, three meanings

baton refuses a stop without a status and offers four:
`done` · `blocked` · `waiting` · `checkpoint`.

The model's actual state was none of them. The phase was finished and verified, so `done` would have
been a lie; `waiting` is for external work with a duration; `checkpoint` compacts mid-phase. **The
only remaining token was `blocked`**, and `blocked` is defined to halt the run and page a human.

Three distinct meanings are collapsed into it:

| what the model needs to say | what it must use today |
|---|---|
| I cannot proceed without you | `blocked` |
| I proceeded, but you should know | `blocked` |
| You should look at this; work continues | `blocked` |

It is worse than naming, because **`AskUserQuestion` is forbidden while a run is live**. So `blocked`
is not merely the cheapest way to reach the human — it is the only way. An interface in which
*notify* and *halt* are the same operation will be used to halt for things that needed only a
notification. `--notes` is not an escape hatch: it feeds the **next phase's brief**, so it reaches
the next context rather than the human, and is compacted away unread.

## Why this belongs in the host

From [`CLAUDE.md`](../CLAUDE.md):

> **Control flow lives in host code, not in model judgment.** If a step only happens because the
> model remembered to do it, it is a bug waiting to happen; make a hook or the host do it.

"Before hard-blocking, look for a reversible alternative" is exactly such a step. It happened to be
left to model judgment, and the judgment went wrong on its first real test. Fixing it with a better
instruction would leave every other user's model free to make the same call. It is a host problem.

Note also what baton *does* enforce versus what it does not: it polices the **reporting** protocol
rigorously (a stop without a status is refused) and never checks the **durability** invariant it
exists to protect. `baton blocked` accepted a dirty tree without comment.

## The design

### 1. Compute the fallback first — it is the classifier

Require the model to compute a reversible alternative **before** the escalation is accepted. This is
not an optimization; it decides which kind of escalation this is:

- a fallback exists ⇒ **soft**: notify, state the intended action, proceed if nobody answers
- no fallback computable ⇒ **hard**: this genuinely stops

The soft/hard distinction then falls out of the attempt rather than out of a judgment the model has
to get right. In the incident the model never tried to compute an alternative, so it never
discovered the case was trivial; making the computation mandatory makes that specific
misclassification unreachable.

It also improves the question. Not *"what should I do?"* but *"committing unsigned in 10 minutes
unless you say otherwise"* — reviewable on a watch, with no laptop.

### 2. Three tiers, not two

A two-tier split (ask-with-timer / hard-block) still mishandles the incident: a timed ask before an
unsigned commit is ceremony for something nobody would veto.

| tier | when | behaviour |
|---|---|---|
| **act + notify** | fully reversible, default obvious | do it, announce it, log it. No ask, no timer. |
| **timed ask** | reversible, but a human might choose differently | announce the intended fallback, start a timer, proceed on expiry |
| **hard ask** | irreversible, or no fallback computable | announce and stop. No timer. |

The incident was tier 1 played as tier 3 — two steps off, which is what made the overreach expensive.
The timed tier defaults to **5 minutes**; where the cost of reversal is high, longer is right, which is
the residual question in [Open questions](#open-questions).

### 3. The timed ask

Lifecycle:

1. Model computes the fallback and declares it: `baton soft-block "<why>" --fallback "<what I will do>" --tried "<what I already tried>"`.
2. baton announces it in the session (`systemMessage`), logs it to `events.jsonl`, and pushes it (ntfy).
3. A timer runs — **5 minutes by default**, settable in `~/.baton/config.json` like `ntfy_topic`
   (`{"escalation_timeout": "5m"}`). On expiry, baton tells the model to proceed with the stated
   fallback.
4. The decision and its outcome are recorded as a resolved escalation, not as a cleared flag.

Three rules the timer needs:

- **Opening a notification is not the cancel signal.** Reading it and not answering means *I saw it,
  I am busy*; the timer should keep running. What should pause it is the human **typing in the
  session** (`UserPromptSubmit`) — that is the authoritative "I am here".
- **Offer a one-tap Hold** that extends the timer without demanding a decision. Without it, silence
  cannot be told apart from *let me think*, and auto-proceeding overrides a human who is
  deliberately considering.
- **A late answer must still work.** If the human replies after the timer fired, that answer has to
  remain actionable — which turns "pick a *reversible* fallback" from a preference into a hard
  requirement of the mechanism. The escalation should not close on expiry; it should re-frame to
  *"did X — want it undone?"*

### 4. Generative refusals

Every refusal baton prints today is terminal: *a stop with no status is refused* says what the model
may not do and nothing about what to do instead. A dead end with no exits named is an invitation to
reach for the one exit the model can remember. Encode the recovery playbook **once, in baton**:

```
baton: hard-block requires --tried.
  Before hard-blocking, exhaust the reversible options:
  · uncommitted work?      commit it, degraded if need be — say how in --notes
  · a step failed?         is there a worse-but-working version? ship it
  · signing/auth down?     take the unsigned path, record the repair
  Hard-block only if every remaining path is irreversible, or needs a
  secret only the human holds.
```

And enforce the field: **refuse a hard block whose `--tried` is empty.** That converts "find another
way" from a virtue the model must remember into a field it cannot leave blank.

### 5. Gate the halting statuses on a clean tree

`blocked`, `waiting` and `checkpoint` should run `git status --porcelain` and refuse — or at minimum
warn loudly — when it is non-empty:

```
baton: 3 uncommitted files. A blocked run can sit for hours.
  Commit or stash before declaring this.
    M CLAUDE.md
    M docs/architecture/08-design-conventions.md
    M frontend/test/design-audit/hover-states.spec.ts
```

This is the single highest-value item here and the cheapest: no model judgment, mechanical, and it
alone would have turned the incident from a near-miss into a footnote. It also enforces the rule
baton already quotes into every brief.

### 6. Record every autonomous decision where a human will see it

Yielding control has a price: **a degradation that used to page the human becomes invisible unless it
is recorded.** Today a stall is loud. If the model instead keeps moving and notes the wart, the note
goes into the next phase's brief and is compacted away. That is strictly worse than stopping.

baton already has the right mechanism — *"every user-visible action baton takes is announced in the
session (`systemMessage`) and logged to `events.jsonl`"*. Auto-proceeds ride that, with two
additions:

- `baton status` and `baton done` surface the run's auto-proceeds, so the human gets the list of
  corners that were cut without having to go looking.
- An escalation's state keeps **history** rather than being overwritten:
  `soft-block → auto-resolved (fallback: unsigned commit; 10m; no response)`. If the model can
  silently clear its own blocked state, `blocked` stops being a trustworthy signal.

### 7. Budget the auto-proceeds

Each auto-proceed being individually reasonable does not make a dozen of them reasonable. Cap them
per phase (3–5), and exceeding the cap forces a real stop regardless of reversibility.

Without a cap, a long unattended run can drift a long way on individually defensible calls and the
human only finds out at the end. This is the general failure the same user records elsewhere as
*phase-by-phase increments are locally coherent and globally wrong*, arriving through a new door.

## What this must not become

Graduated escalation must be **scoped by reversibility**, or it is the worse failure in the opposite
direction: a model told to keep moving will eventually route around a failing test, a confirmation,
or a permission prompt.

Unchanged, and not negotiable:

- **An open permission prompt.** baton's own rule stands — it cannot answer one and must not type
  through it. If a run waits on one, the human is the only way forward.
- **Irreversible acts**: deleting data, pushing, publishing, anything outward-facing.
- **A secret only the human holds.** The incident is the boundary case worth noting: the *signature*
  needed the human, and committing did not. The fallback existed because the two were separable. Where
  they are not, it is a hard block.
- **Anything baton cannot classify.** Default to hard.

## Open questions

**1. Can baton auto-answer its own `AskUserQuestion`, and should it?** This gates the whole design.
`AskUserQuestion` is a **blocking modal**: while it is open the model's turn is frozen, so it can
neither compute nor act, and a timer cannot be served by the model itself. Two candidates:

- **(a) baton answers its own dialog on expiry.** It already types into the TUI, and
  `reliability.md` records that `PostToolUse` for `AskUserQuestion` carries
  `tool_response.answers`, which baton already acts on. One UI, existing machinery, and the fallback
  can be stated in the option label so the record is honest. ⚠ The hazard is obvious and serious:
  **baton must be certain the dialog it is typing into is its own and not a permission prompt.**
  Needs a spike.
- **(b) Never use `AskUserQuestion` for the soft tier.** A soft escalation is a `systemMessage` plus
  a push plus a pending-decision record; the model carries on and honours a late answer when it
  arrives as a normal turn. Non-blocking by construction, and it keeps baton's hands off dialogs
  entirely — at the cost of the human having no in-session control to click.

(b) is safer and probably the right v1; (a) is the better UX if the dialog can be identified
reliably. Worth measuring before choosing.

**2. Does an open dialog suppress the signals the timer needs?** `reliability.md` measured that
`Notification(idle_prompt)` does not fire while `isDialogOnScreen`. A design that waits on an open
dialog therefore cannot lean on idle detection and needs its own clock — an argument for (b).

**3. Who classifies the tier?** The model proposes (it computed the fallback); baton could veto on a
denylist of irreversible verbs. Splitting it this way keeps classification out of pure model
judgment without baton having to understand the work.

**4. Should a per-case timer override the default?** The default is settled — **5m**, in
`~/.baton/config.json` alongside `ntfy_topic`, overridable per project the way every other baton
setting is. What is still open is whether the *model* may propose a longer one for an expensive
reversal and baton clamp it, the way it already clamps `waiting` to two hours. A single default is
simpler and probably enough for v1; the risk it carries is that 5m is generous for a trivially
reversible act and short for a costly one, which is an argument for letting the tier set it rather
than the case.

## What this would have done to the incident

1. The model computes a fallback — commit unsigned, repair with
   `git commit --amend --no-edit -S` — and classifies it **tier 1: fully reversible, default obvious**.
2. It commits, unsigned. baton announces it and logs it to `events.jsonl`.
3. The human's watch buzzes once: *"P4 committed unsigned — gpg-agent lost its passphrase after the
   restart. Re-sign with `git commit --amend --no-edit -S`."*
4. The run continues into P5. `baton done P4` lists the one degradation.

Had the model still reached for a hard block, two independent gates would have caught it: `--tried`
is empty, and the tree is dirty.

Nothing is lost if the human is asleep, and the one-command repair is in their hands when they wake.
