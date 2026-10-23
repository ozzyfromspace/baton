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
  the log. An update that changes nothing writes nothing, and on macOS a write is synced with a plain `fsync`,
  not the `F_FULLFSYNC` that stalls for seconds on a busy disk; a temporary file an interrupted write left is
  swept as the host starts. A run follows its conversation through `--resume` and `/clear`.
- **Handing a session over.** `/baton start` records the session. Once its turn ends, the plugin's Stop hook ends
  claude, and the `baton init` shell hook relaunches the same conversation under baton. A host that finds a newer
  compatible baton installed restarts its session on it in place, resuming the same conversation.
- **The skill** is a stub that loads its instructions from the binary (`baton skill`), so they always match the baton
  that carries them out.
- **`baton watch`** is a live view for a second terminal: every live run of the project, its phases as the board
  shows them, each running job's stage, its time against earlier runs of its command, how long its log has been
  still, its first failure and its wait for a lane, the merge queue and what holds it, and the latest events in
  words. It reads the records only, takes no lock, and redraws every 2 seconds (`--once` prints once, and so does
  output that is not a terminal).

## Plans

A plan is a Markdown file with a heading per phase (`## P0 — …`). The line under each heading says what the phase
needs done first (`After: P1, P3`, `After: none` or `After: all`); a phase without it follows the one before it. Beside
it, `Type: qa` marks a QA phase (below), and `Replan: after` a phase whose findings should reshape the phases after it.
One phase is enough: a small task can be a plan of one phase. A plan with no phase headings is ordinary plan-mode
work, and baton leaves it alone. Each anchor is the phase's heading, or more of its lines where the plan quotes its
own headings, and fenced code is passed over, so the anchor occurs once.

A phase's id is also the name of its agent and the human's handle on it ([Phases at once](#phases-at-once)), so it is
a name Claude Code takes for an agent: 1 to 32 letters, digits, `_` or `-`, starting with a letter or digit (`P8b`,
not `P8.5`), none of the names Claude Code keeps for itself (`main`, `team-lead`, `user`, `system`) nor of an agent
id's shape, and no two ids of a plan equal ignoring case, as Claude Code matches a name ignoring case. A plan with
another id is not attached, and baton says why; so is a replan's draft, or a phase added to the plan file. The ids a
running plan already has stand as they are.

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
  - a plan with phase headings that baton could not attach as written (an id that cannot name an agent, a duplicate
    id, an `After:` naming an unknown or later phase) is turned back with what is wrong;
  - the plan's first `ExitPlanMode` is turned back with baton's request to check it against CLAUDE.md, memory, skills
    and settings, and address in it anything that competes (a rule to run the full suite before every commit, say);
  - in the same turn-back, every plan is asked to check its gates against baton's gate rules ([Gates](#gates)):
    each QA phase is listed with how many phases it covers since the one before, and the model confirms that each
    sits at a real seam (the end, or the end of a group of related phases later work builds on), moving or merging
    any that does not. Where the seams are is the model's judgment, not a number baton counts;
  - in the same turn-back, a plan that is mostly one chain (6 phases or more, the longest chain over 60% of them) is
    asked to check each link of that chain against the shape rules. Where every link holds, the model puts the plan
    to the human unchanged, saying in it why the chain is real. On those ten plans, the threshold flags the eight
    and passes the two at 43% and 58%.

  Every phase's brief carries the gate rules in short. `plan.RulesVersion` stayed 1 for them: a bump replans every
  run carried across the upgrade, which can only reorder phases, so it could not apply them.

  A stop in plan mode before the plan reaches the human is turned back once too.
- **The shape line.** As the plan goes to the human, they see its shape in one line: `baton: 12 phases · longest
  chain 5 (P1 → P7 → P8 → P9 → P11) · up to 7 at once`.
- **A record file baton writes.** A plan may name one in its standing rules (`Record: <path>`, inside the project,
  not a directory). No model of the run may edit it. As each phase is done (a lead phase) or lands (an agent's),
  baton appends the phase's record (its heading, when, its `baton done` notes, its commits) in the lead's checkout,
  and commits that file alone (`git commit --only`): a new commit of the one path, so the lead's own work, staged or
  not, stays as it was. It holds the landing lock and waits for the lead's tool calls as a landing does, and a
  record file with uncommitted changes, or a checkout on a branch the run cannot follow, holds it until that clears. It is the one
  commit baton makes into a project, and only for a plan that names the file: one progress file every phase edits
  makes their landings conflict, and this gives a plan one that none of them does.
- **Out of Claude Code's way.** After every compaction, Claude Code adds the session's plan-mode file
  (`<plansDirectory>/<slug>.md`) to the conversation in full, for as long as that file exists, on top of baton's
  brief, which already quotes the phase, the standing rules and the notes. In a real run that was about 32k tokens
  of every phase's starting context, and no setting turns it off. So when baton attaches a plan from that file, it
  renames it in its folder to a name from the plan's title, follows it there, and says so; renamed, it is attached
  no more ([spike 38](../spikes/38-plan-file-reference/README.md)). Claude Code writes the session's next plan at
  the old path, so an approval there is a revision of the run's plan, moved aside in its turn. A run attached
  before baton did this has its plan moved before the compaction that starts the lead's next phase.

### Replans

What a run learns (a phase that turned out to wait on CI, phases that turned out independent, a newer baton's
planning rules) used to stop at the phases already done. A replan carries it to the phases not started: the lead's
model restructures them, in the session where the human sees it, and baton checks the result. **The model decides
how; the host decides when, and whether the result stands.**

- **Only on a cue,** never on a timer or the model's own initiative:
  - the human's `/baton replan "<what to consider>"`, done at once in that turn;
  - a run carried across an upgrade whose planning rules changed (`plan.RulesVersion`), once;
  - a QA phase under way while the phases not started are mostly one chain, once per QA phase;
  - a phase marked `Replan: after` (a spike, an exploration), once it is done;
  - a file that conflicted in a second landing of the run while phases not started name it (v0.8.15), once per
    file ([Look-ins](#look-ins)).

  baton's own replans open at the lead's phase boundary, in its fresh context: the replan's brief comes before the
  next phase's, and the lead replans, runs `baton replanned`, then begins the phase. `auto_replan: false` turns them
  off.
- **A draft, checked.** `baton replan` copies the plan file to a draft and prints the brief: the phases not started
  and those that started, the shape now, the shape rules, the human's words, and what this replan may change. It
  also names, as information, the phases not started that could run at once and whose sections name the same
  file: they collide when they land. The model edits only the draft, and ends with `baton replanned --notes "<what
  changed and why>"`, or `--none "<why>"`. baton checks the draft before the plan file is touched: one that fails is
  refused with each thing it may not change, and the model fixes it.
- **What each kind may change.** Phases that started or are done never change, and the order and `After:` lines of
  the rest always may.
  - After an upgrade or at a QA phase, that is all: every other byte of each phase stays as approved, so however
    many such replans run, each phase's goal, steps and "done when" are what the human approved. One stands only if
    the phases not started got a shorter longest chain or more of them can run at once; otherwise the plan stays as
    it was, and the model is told why.
  - After a marked phase, the steps of the phases not started may also take in what it found, and a phase may be
    split, but no work is dropped or merged. A conflict's replan may change as much, to give each phase that names
    the file a file of its own.
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
   on what does not depend on that, starts no loop or other background task only to wait for it, and with nothing
   left, ends its turn. Its stop over that work is then allowed; the refusal that says the same is left for work
   whose start baton did not see.
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

**Mid-phase valves.** The status line gives the exact context size. Three lines are percentages of the limit (the
model's window, or the `--autocompact` cap if smaller):
- at 60% (`notice_pct`), a heads-up: the model is told where its context stands, that nothing is asked of it now,
  and to make the room last (a line range or a grep rather than a whole file, short command output);
- at 80% (`checkpoint_pct`), the model is asked to checkpoint at its next safe point, which goes through the same
  loop, and asked again at every further tenth;
- at 90% (`warn_pct`), but never under 200k tokens, the human is asked whether to checkpoint now; unanswered at
  `escalation_timeout`, baton keeps going.

Phase agents get the first two (for them a checkpoint is a fresh agent from their notes). With an 810k cap the lines
are 486k, 648k and 729k, and Claude Code's own compaction, 33k tokens below the limit (777k), is the backstop; where a
small window would put a line at or after that, every line is its percentage of where Claude Code compacts instead.
The checkpoint came at 60% until v0.8.6. Every compaction restarts the lead at about 180k tokens (about 32k of it was
the plan file, before baton moved it out of the way), so a stretch had about 300k tokens of room, and in a real run
the lead checkpointed 9 times mid-phase against 7 phase boundaries. At 80% a stretch has about 470k; the 129k left
before Claude Code compacts is about 14 minutes at that run's pace, where models acted within 1 to 10 minutes of the
ask, and Claude Code itself starts precomputing its summary near 80% of its window.

**The watchdog** catches a run that has gone quiet: idle turns, stale gates, unanswered prompts, usage limits and API
errors. Everything baton waits on either clears by itself or expires into action, except an open permission prompt.
Each loop's watchdog stays quiet while it waits on something that ends by itself, so **the stall alarm** looks at the
run as a whole, about once a minute: with the plan running and phases left, nothing busy, and nothing in the lead's
run or any phase agent's sub-run moved for 20 minutes, it pushes the human one notice naming what each phase waits
on. Busy is a tool call, the model's own background work, a wait on something outside (`await`) or a gate waiting
for a lane while its runner beats, and a gate whose log grows or, with its log still, whose processes used CPU since
the alarm's last look, as the check that stops a hung gate reads it: in a real run, a QA gate whose tests printed
nothing for an hour while they worked raised a false alarm. Where the CPU can't be read, the log decides. The alarm
holds nothing, and the run moving arms it again. A usage limit, an open dialog and a machine just woken are never a
stall, nor is the weekly wind-down's wait once nothing is under way; while work is under way, it is watched as
ever, so a stuck agent is named whatever the wind-down.

## Gates

A long gate goes to baton:
- `baton verify` runs it on `HEAD` in a lane: a detached worktree under `~/.baton/lanes/`, set up by the project's
  `.baton-lane`.
- `verify --here` runs it in the checkout, and holds edits and landings there until it ends; as it is queued, it
  names the landings it will hold. In a phase agent's worktree it also holds one of the project's lanes while it
  runs, with `BATON_LANE` set to it, so here and lane gates together never run more at once than the lanes allow,
  and per-lane resources (a test database, a Compose project) stay apart. With no `.baton-lane` there is one lane,
  so agents' here gates take turns. A gate waiting for a lane is announced with how many are ahead of it.
- `baton await` waits on something outside the project.

A phase runs one gate and one wait on something outside at a time, side by side: a local gate and an outside wait
never check the same thing, so a `verify` never stops an `await`, nor the reverse. In a real run, a QA phase's wait on
CI replaced its running suite, and queuing the suite again replaced the CI watch: of that run's 16 replacements, 12
swapped one check for another, and phases took to gluing gates into one command. Within a kind, the same command
queued again replaces the one that runs (the gate on a newer commit, or the wait again). Another command is refused,
naming the job that runs: the model stops it with `--replace`, folds both into one command, or queues its own once
the result comes. The phase stands on its latest job of each kind: it is done once both passed, and reopens on the
first red.

The job runs detached (`baton lane-run`), so it outlives claude. Its result reaches the model at its next tool call
or its stop, or wakes it. A finished phase is "verifying" while its jobs run: it is done when they pass, and
reopens when one fails. A phase that can't go on without its result is set aside after `switch_after` for another phase
that can start, and later resumes from its notes. `verify_timeout` counts from when the job gets its lane
(`lane_acquired`), so a wait for one never times a gate out, and a result says both: "passed after 12m, 31m waiting
for a lane". In v0.8.6's own plan, a one-scenario gate took 52 minutes, nearly all of it behind the full suite in
the project's one lane.

**Two real runs spent most of their hours in checks** ([`research/gates.md`](research/gates.md)). baton's own v0.8.6
plan gated for 10.5 of its 12.3 hours, about 3 to 4 of them in full end-to-end reruns that could not change what
shipped: after a test-only fix, on long past a failure at minute 8. A real 57-phase run spent 43.7 hours of its 59.5
in gates, 58% of them red: QA passes ran past failures they already knew of, gates went red on their environment
before any test ran, and landings were verified again on one lane, 17 of 18 green. A human who feels time runs
scoped checks while working and the big one once, at the end. The rest of this section is how baton steers toward
that, and tells the model what a gate does while it still runs.

**Asking alone did not change what models ran** (v0.8.11). In a real run after v0.8.7, baton questioned 35 gates,
and every one traced was queued again unchanged, 0 to 12 seconds later; none of its 32 gates used `--failed`; one
agent ran its 12-minute gate 11 times in 75 minutes, each time after fixing one failure. The refusal had said "run
the same command again", so that was the cheapest answer; the cheap path, a rerun of what failed, needed a gate
built from stages; a narrower check that passed cleared nothing; and the cost was abstract. A model does not feel
time pass: a 12-minute gate is one tool call to it, and its result arrives as if no time went by. So the cheap path
is now the easy one (a narrowed rerun of any red gate, offered in its result), a repeat states its reason, every
result carries what the phase's gates cost, and wherever a gate is decided baton says plainly what the human's time
is (`decide.TimeFelt`, and `decide.TimeShort` beside a long gate: "This gate spends 12m of the human's time; to you
it is one tool call.").

**A QA set's hours** (v0.8.15). A real run on v0.8.14 spent its waiting in a QA gate's long stages. A browser stage
of 64 files, 1h18m to 1h50m a run, went red three times, and each `--failed` rerun ran it whole: its script wrote
the stage's output to a side log, so baton had no failure ids, and the queue message said only "run whole". The
final QA phase ran 9 times in 3h20m in the human's checkout, one red stage a run, since a gate stopped at its first
failed stage, while the human waited to review. And failing tests waited out their timeouts: 3.75 of the browser
stage's 8 case-hours. So in a QA phase every stage runs, and then only what failed reruns until all of it passes; a
stage reads failures from its side log; a whole rerun says why and what it costs; a QA phase runs beside the lead;
and a red result says when its failures waited out a timeout.

**The gate rules** go to the model where plans are made and where phases start ([Plans](#plans)), after
`TimeFelt`: QA phases at real
seams (the end, and in a long plan the end of a group of related phases later work builds on), never per phase;
every other phase runs scoped checks that finish in seconds or minutes, by name; gate scripts are committed, built
from `baton stage`, and tear down what they start; a gate stops at its first failure, and then only what failed runs
again; a setup phase proves the lane recipe first; timings on a shared machine are warnings; no file many phases
edit; long checks go through `verify` or `await`, never a background shell; CI early in the QA phase; a phase
stops what it started before `baton done`; and (v0.8.15) a phase's evidence goes in its notes and commit, not the
permanent suite: a browser test is for a user journey, a design rule is a lint or a component test, and a test
fails fast, with short timeouts, no retries in a gate and no fixed sleeps.

### The gate protocol

A gate reports as it runs (package `lane`'s protocol). Each step of a gate script is a stage:
`baton stage <name> [--keep-going | --halts] [--log <file>] [--runner go|pytest|vitest|jest|playwright] -- <command>` prints `▶ <name> HH:MM:SS` and a start marker
(`::baton stage <name> start`), passes its command's output through line by line, prints how it ended and an end
marker (`::baton stage <name> passed|failed|skipped|carried exit=<n> seconds=<n>`), appends the stage to the job's
stages file (`jobs/<id>.stages`, beside its log) and exits with the command's code. What failed is named by
`::baton fail <id>` lines, which any command may print, and by a runner preset: Go's top-level `--- FAIL: TestX`
names, pytest's `FAILED path::name`, vitest's and jest's `file > suite > test`, Playwright's `file:line` (from its
`✘` lines and, since v0.8.15, its end-of-run list). A stage whose command writes its output to a file of its own
names it with `--log <file>`, and a red stage reads its failures from that file too (`lane.ReadInto`); a file that
is not there is said, and changes nothing else. A package
or file that failed with no test named (a build failure, a timeout, a file that did not load) is named whole, and a
stage with one records no ids, so its rerun runs whole. A stage given no `--runner` reads every preset, a
command's fail lines first, and records the runner whose lines named all its ids (`Stage.Runner`, v0.8.13), and
its rerun narrows by that runner: the Go and pytest presets through their variables, and every preset through
`BATON_FAILED_ARGS`, the runner's own arguments for only those tests (`Preset.Args`). Vitest, jest and Playwright
read no variable that selects tests, so a stage of theirs runs fewer only when its command passes those on
(`playwright test $BATON_FAILED_ARGS` when it is set); a red result of one names the stage, how long it ran and
the arguments, and says that (`decide.passOn`): a real run's 33-minute Playwright stage reran whole for 8
failures. A custom command reads `BATON_FAILED` and `BATON_FAILED_RE`.

- **A gate stops at its first failure.** A stage reads the stages file, and when an earlier stage failed without
  `--keep-going`, it prints why and is skipped (exit 1), however the script goes on. A stage that collects every
  failure on purpose says `--keep-going`.
- **Except in a QA phase** (v0.8.15), whose job is to find every red at once. A gate queued in a `Type: qa` phase
  (`Job.QAPhase`) runs with `BATON_QA_PHASE=1`, and there each stage keeps going by default; `--halts` marks a stage
  the later ones need (a setup, a build), which stops them when it fails (`decide.StageHaltedQA`).
- **Why a red stage runs whole** is recorded with it (`lane.Stage.Unnarrowed`): no failure ids, more than 100
  failures, a file or package that failed with no test named, or failures from more than one runner.
- **It acts on no run.** `baton stage` needs no host: outside a job it prints and runs its command and records
  nothing, so a script run by hand still shows its stages.
- **The runner reads the log as it is written.** A gate's command gets `BATON_JOB`, `BATON_STAGES` (its stages
  file) and, on a rerun, `BATON_RERUN`, and the directory of the baton that runs it first on its `PATH`; the
  runner's own lines carry the time (`[baton 14:02:11] …`). A watcher on the job's output updates the job's stages
  as each ends (`stage_finished`), and its first failure (a failure line, a fail marker or a failed stage) is
  recorded on the job (`job_failing`). It writes the run's state only then, never for each line.
- A stage stops reading its command's output 2 seconds after the command ends, so a process it leaves running
  must write elsewhere; a gate script tears down what it starts (`trap … EXIT`).

### After a red gate

- **The first failure is a look-in.** The gate's owner, the lead or the phase agent that queued it, is told at
  once (below): the line, its stage, how long in, that the gate runs on, and, for a gate in the checkout, the
  landings it holds. The model decides: stop it with `baton stop <job>` and rerun what failed, or let it finish to
  collect every failure. baton never kills a gate for a failure line. A failure in a stage run with
  `--keep-going` waits for a later stage that failed without it, or for the result.
- **`baton stop <job> [--why]`** stops a job of the caller's own, queued or running. It ends interrupted, so it
  checked nothing, and a red gate stands as it was (`job_stopped`).
- **`baton verify --failed`** reruns the phase's red gate (`Job.Reruns`), or, with none red, its latest gate
  stopped after a failure (the common case: `baton stop` at the first failure). The stages that passed are
  carried, each failed stage runs narrowed to its failure ids where it recorded them (`GOFLAGS=-run=^(A|B)$`,
  `PYTEST_ADDOPTS=-k`, `BATON_FAILED_ARGS` for a command that passes them to its runner, or `BATON_FAILED` and
  `BATON_FAILED_RE` for a custom command), and the rest run whole;
  `--stages a,b` brings carried stages back. What it reruns comes from baton's record (the red gate, with what a
  stopped run learned since, written beside the rerun's log as it is queued), never from a model's reading of a
  tail. Its result names each carried stage's job and commit and the files changed since, and each narrowed
  stage's ids. It is refused, saying why, with nothing to rerun. As it queues, each stage it runs whole is named
  with why and how long it took last time (`state.WholeStage`: "run whole: `browser` (1h18m last time; baton read
  no failure ids from it)"), and one that was red and took 10 minutes or more is questioned once (`whole_rerun`,
  v0.8.15): its own log may name the failures, to rerun by name with `--replace`, or `--why` runs it whole, told
  to the human; queued again without one, it runs, and the human is told (`gate_unexplained`).
- **Slim runs in a QA phase** (v0.8.15, the human's rule): once the full run has found every red, reruns are
  narrowed until all of it passes. In a `Type: qa` phase, `baton verify` of the red gate's own command, for a gate
  built from stages, is queued as its `--failed` rerun, saying so (`decide.SlimRun`). `--whole --why "…"` runs it
  whole, and the human is told the reason (`gate_whole`); `--whole` without `--why` is refused. A gate built from
  no stages keeps the repeat question, which offers its narrowed rerun.
- **One full run confirms them, only when needed.** At `baton done` of a QA phase whose red gate reruns cleared,
  baton takes the files changed since that whole run (`state.NeedsConfirm`): Markdown and the failing tests' own
  files need no confirmation, and any other file does. Then one full run of the gate's command is queued at
  `HEAD`, as a landing's re-verification is (no questions, every stage run), and the phase is verifying until it
  passes (`decide.Confirming`, `qa_confirm_queued`); a red one is the phase's red gate, and slim runs begin again.
  With none needed, `done` goes on, and says why (`qa_confirm_skipped`).
- **`baton verify --failed -- <command>`** reruns only what failed in a red gate that recorded no stages (a make
  target, a test command, a script): baton reads the failure ids of every run the rerun draws on from their logs,
  for Go, pytest, vitest, jest and Playwright and `::baton fail` lines (`lane.FailureIDs`), and runs the model's
  command when it names each of them as a whole word: its id, its test name, or its `file:line` (`lane.Unnamed`;
  `TestParse` inside `TestParseAll` names nothing). It stands in for the gate (`Job.Reruns`, `Job.Narrows`), so its
  pass clears it, and it replaces a run of the gate still under way. As it queues, the model is told what it does
  not run: what a gate stopped before its end never reached, and, for a gate that runs its tests through make or a
  script, what came after its first failing command; the QA phase runs the whole set. It is refused, saying why,
  for a command that leaves a failure out (naming those, and offering the runners' own rerun of them,
  `lane.Reruns`, where every id has a runner), no command, the gate's own command, a gate that ran out of time
  (what it never reached is unchecked), more failures than baton reads by name (100), or a log with no id baton
  reads, which is rerun by name with `--replace` instead.
- **A red gate stands until a gate that takes over from it passes:** the same command, in a lane or `--here`, its
  `--failed` rerun, staged or narrowed, or another command the model makes the phase's gate with `baton verify --replace`, recorded as a
  decision the human is told of. v0.8.4 shipped a regression its own full end-to-end gate had caught: the result
  showed the log's tail, which held only the last failure, the model gated again with a one-test `-run`, baton let
  that narrower gate stand in for the red one, and the log was later pruned. So any other narrower command's pass
  leaves the phase red, and the model is told so as it queues it and when it passes; a narrowed `--failed` rerun
  clears it only because baton checked that it names every test the red gate's log says failed. `baton done
  --despite-red` stays for what is not the phase's to fix. With `--failed`, a flaky suite needs one full run and
  then minutes, never another full run: v0.8.6's "the same command" alone had made one a treadmill.
- A result lists the lines that report a failure anywhere in the log (`--- FAIL`, `FAILED`, `panic:`, `✗` and the
  like, at most 20) before its tail, and the log and stages of every red job are kept, and go with the plan's record
  when another plan replaces it.
- **A result points at the cheap path.** A red gate's result names what failed by id ("Failed (n): …", at most 20,
  then how many more) and, when a narrowed rerun could stand in for it (it failed rather than ran out of time,
  recorded no stages, did not fail on its environment, and every id was read), the command that reruns only them,
  to run as `baton verify --failed -- <that command>`. Every gate's result ends with what its phase's gates have
  cost so far (`state.GateCost`: the phase's gate runs and their clock time, overlaps counted once; from the second
  run on), and a gate that ran 10 minutes or more with `TimeShort`.
- **A red gate says when its failures waited out a timeout** (v0.8.15). When 3 or more failures ran about as long
  as each other (within 10% of their median), 30 seconds or more each, and together a quarter or more of the
  gate's run (`lane.TimedOut`, from each runner's own times), the result says what they cost and asks for tests
  that fail in seconds, naming the timeouts of the runners present; `job_finished` logs `timeout_failures` and
  `timeout_seconds`.
- **A red gate its environment failed says so.** A log with no test failure and a known environment line (a port
  in use, the Docker daemon out of reach, an unhealthy container, a full disk, a registry's rate limit, a host name
  that does not resolve) reads as the environment's failure, not the change's. The result quotes the line, lists
  the run's environment facts, and asks the model to record the fix with `baton env "<what the environment
  needs>"`, which every later brief of the run, the lead's and the agents', carries, and a phase agent at work is
  told of once (`env_fact`), and to make it permanent in `.baton-lane`. In a real run, at least 8 agents found the
  same fix for a port collision again.

A gate that hangs in silence is stopped: a `verify` job whose log has not grown and whose processes used no CPU for 20
minutes ends unverified, with the reason, and the model is told. Log silence alone never stops one, so where the CPU
can't be read (Windows), a gate runs to `verify_timeout`.

### Look-ins

What baton notices about a gate or a landing while it runs is told to the session it concerns, as information:
nothing is stopped for it, and nothing waits on it. Besides the first failure, the coordinator looks about once a
minute, and each goes once per job and kind:
- a gate past 1.5 times the slowest earlier passing run of its command, and 15 minutes more, or at 80% of
  `verify_timeout`: to its owner, with its elapsed time, the earlier runs and how long its log has been still;
- landings that have waited 20 minutes behind a gate in the lead's checkout: to the lead, which and since when;
- a file in a second landing conflict of the run, while phases not started name it (`replan.Naming`): to the
  lead, naming them, and saying that one file every phase edits serializes them. Since v0.8.15 baton opens a
  replan of those phases itself at the lead's next boundary, once per file ([Replans](#replans)), and the look-in
  no longer asks for the human: a real run's lead, told to "ask the human to run `/baton replan`" with one phase
  left, asked the human to carry a merge note to an agent that had already merged. With no such phase, the lead can do
  nothing about it, and is told nothing;
- what finished phases left running, while the machine has no room and a gate runs past its history: to the gate's
  owner (below).

A look-in reaches its session by whichever hook comes first, as a result does (a tool call, a stop, a hand-back, a
prompt, a brief), whatever phase the run is on, and an idle session is woken for it. It never goes through the
inbox, which drops what concerns a phase not in hand: after `baton done`, a gate's phase is no longer in hand, and
its owner still needs to hear what the gate does. Look-ins run with `workers: 0` too.

**The advisor.** When a look-in fires about a gate (its first failure, past its history or near its timeout,
landings held behind it, what finished phases left running), or a red gate's result says its environment failed it,
and baton's mod runs, the host asks a small model,
Haiku unless `advisor_model` says otherwise, for a short note, and the look-in goes with it: "Advisor (Haiku): …".
baton writes every word it reads, at most 12,000 characters with logs and notes marked as data: the look-in's facts,
the command, the failure lines and the log's end, earlier runs, the phase's notes. It asks whether the failure is
the change's or the environment's, whether the rest of the run is worth waiting for, and what the smallest next
check is; the answer, at most 600 characters, is a suggestion, never an instruction. It routes none of the
session's work, so baton still never switches models. The call goes through the mod ([The bridge](#phases-at-once)),
so it is never made in serial mode. The handoff cannot get stuck: the look-in is held while the host asks, and the
host's own tick releases it when the answer comes or after 60 seconds, whatever the mod or the model does; an
answer after that is dropped, and a hold no call in the host's memory stands for (a restart) is released at once.
`advisor: false` turns it off.

### Proportion

A gate is weighed against what it could prove as it is queued (`cli/proportion.go`), from the run's record and git,
and questioned once, with the narrower check named; the same call again queues it, so the model decides, and
nothing waits:
- a repeat: the command's latest run took 10 minutes or more. On the same code, it already passed, or, red, use
  `--failed`; on other code, the files changed since are listed, with whether a narrower check proves them; when
  only tests and Markdown changed since a full green run, shipping on that evidence is named; a red run's failure
  ids are listed, with the runners' own rerun of them as `baton verify --failed -- <command>` where every id has a
  runner;
- a script outside the repository (a scratchpad, `/tmp`): commit it first;
- a long gate whose log held no stage marker: build it from `baton stage`;
- a long gate before the plan's QA phase, folded into the QA question, with the time its last run cost the human.

**A repeat states its reason** (v0.8.11). The question opens with what the gate would spend of the human's time
(`TimeShort`, from its last run) and what the phase's gates have cost so far, and ends: "To run the whole gate
anyway, queue it again with `--why "…"`: baton tells the human your reason." `--why` (on `verify` and `await`,
`Job.Why`) answers every kind of question, and the human is told the reason: for a repeat, "P53 reruns `make
test-degraded` (12m, run 8): <why>" (`gate_repeated`). A long repeat queued again without it still runs, since
nothing is refused twice, but the human is told ("P53 reran `make test-degraded` (12m, run 8, 1h20m so far) without
saying why", `gate_unexplained`), and the job's result opens with that line for the model. A phase agent's line
goes to the lead's session as well, where the human is. A silent resubmit was the cheapest answer to the question;
now the reason costs a sentence, and its absence is seen.

A check the model starts as a background shell task (`run_in_background`) is refused once and pointed to `baton
verify`, where it is recorded, holds a lane, and its failures reach the model as they happen. In a real run the
lead's own background checks left it idle for 8.4 hours on work baton could not see. baton now records each
background task's command and description as it starts, and how long it ran once a stop no longer lists it.

### Landings verified again, sized to the change

When the run's branch moved on since a phase agent's gate passed, the merge queue first compares what came on it
since with what the phase changes. When they share no file (Markdown aside), a QA phase not started yet follows the
phase, and `aggregate_qa` is on, the phase lands without being verified again, and the QA phase checks those commits
together (`reverify_skipped`). Otherwise it is verified again, with the phase's land check if it gave one
(`baton done --land-check "<command>"`: a focused command, for a phase whose gate is long) and its last passing gate
if not. A file in common is no proof that two changes meet, nor none that they don't; the QA phase ahead is what
checks them together, and without one every landing is verified again. In a real run, 18 such verifications took
5.5 hours on the project's one lane, and 17 were green.

## The risk ledger

Scoped testing trades early detection for speed. The ledger says when that trade stops paying (package `risk`).

- **Scored from facts, never from the phase's own rating,** since a model finishing a phase wants to be done. A
  lead phase is scored as it is done, an agent's as it lands: the lines its commits changed, the areas of the
  project they touched past three, files other phases' sections name, manifests, lockfiles and migrations, code
  changed with no test changed, tests deleted; and from the record, finishing `--despite-red`, its decisions, its
  landings that came back for conflicts, whether it ran any check at all, and how it departed from the plan in its
  own words (`baton done --deviation "<…, or none>"`; unknown when it did not say, and baton asks nothing).
- **The points add up until a full check passes:** a QA phase done (not over red), or a `--qa` gate of the lead's
  that passed. `baton status`, the board and `baton watch` show the total.
- **At each line the total crosses** (`risk_review`, 56 points, once; `risk_full`, 90, again whatever the last
  review said), an independent model, Opus unless `risk_model` says otherwise, with no stake in the work, assesses
  the exposure of building on what no full check has passed: it reads what baton prepares (the plan's goal, the
  phases since the last full check with their notes, deviations, decisions and factors, the red verifications
  finished over, and the diff since, its largest hunks clipped), at most 30,000 characters marked as data, and its
  first word is its verdict. Continue, or targeted checks (named), go to the lead as information.
- **Only a "full now" verdict holds anything,** so a miscalibrated number never starts a long run by itself: no new
  phase agent starts, and the lead is told why as soon as it holds a phase: queue the full check with `baton verify
  --qa`, in a lane rather than `--here` where the project allows (the landings a check in the checkout would hold
  are named), or `baton note` why not. Either lifts the hold, and work goes on beside the check; so do a full check
  that passes and `escalation_timeout` from the verdict.
- **With no assessment** (no mod, serial mode, `risk_model: off`, a call that fails or outlasts `risk_wait`), the
  lead is told the score and its factors, and nothing is held.
- **Calibrated** by a replay of both studied runs' records: the phases whose files the next full check had to fix
  averaged 14.2 points, the rest 7.8, and the lines cross each troubled stretch early and neither quiet one
  ([`research/gates.md`](research/gates.md#calibrating-the-ledger)).

## Phases at once

The plan's `After:` lines make a graph. The lead, the session in the human's terminal, works one phase at a time.
Each other phase that can start runs as a **phase agent**: a background agent of the lead's session.

- **The mod is a thin executor.** `plugin/hooks/baton.js` is JavaScript that Claude Code runs in the lead's claude.
  It spawns `baton:phase` agents, each named for its phase, and messages them, asks the human in Claude Code's own
  dialog, compacts the lead and submits baton's lines to it, empties the lead's input box of a prompt baton sent on
  to an agent (below), drops the agents' reports before the lead's model reads them (a task notification, or in auto
  mode a peer hand-back), draws the band, makes the host's model calls (below), and reports what it sees. It decides
  nothing and words nothing: the host does both. A mod of another baton release speaks another protocol, and the
  run is serial.
- **The bridge** is files and the CLI:
  - host to mod: numbered command files under `.baton/runs/<run>/mod/`, each addressed to one host instance,
    idempotent, and retried until acked;
  - mod to host: `baton mod report`, with each report recorded once, under the run's lock. A report of the lead's
    compaction is counted in the run's state before the bridge records it, so a report process killed between the
    two (the mod gives it 15 seconds, above two lock waits) loses nothing: the mod sends it again, and it counts
    once;
  - a model call: the advisor and the risk assessment each make one, with no history, for a short answer. The
    host writes the prompt into a `complete` command, the mod runs it with the session's own client and
    credentials (`$.model.complete`) from its poll without waiting on it, and its ack carries the answer, cut at
    4,000 characters, or why there is none. The host waits for it with a deadline and takes the command back past
    it. Nothing of a call reaches the transcript, the screen or a turn, and it answers while the lead's turn runs
    ([spike 41](../spikes/41-model-complete/README.md)). It is advice the host words into its own messages, never a
    route for the session's work.
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
    and tells the human, and a second takes it down and asks the human what to do with the phase, with no deadline
    while the rest of the run can go on, since they are the one in the way. That question steps aside after
    `escalation_timeout` and comes back when they next write; only that phase and those that need it wait. Once
    nothing else can move (no phase can start, no other agent is at work, and the lead is idle), baton waits
    `escalation_timeout` more, then takes the phase back from the agent, and the lead does it in its session,
    merging the agent's branch if it builds on it, never rebasing. That is a decision without the human, with
    `/baton pause` as its undo; before it, the lead's stop line says when it comes, and an answer from the human
    wins. Leave it stopped, and `/baton run` starts a fresh agent.
  - An agent's permission prompt holds that agent alone where the mod runs: the lead's compaction, its deliveries and
    its switching go on under it, and the prompt survives them ([spike 36](../spikes/36-agent-dialog/)). In serial
    mode any dialog holds the lead, since a typed key would answer the agent's prompt.
  - A model's own call to the `baton:phase` type is refused, and so is its own Agent call named for one of the
    plan's phases (below).
- **Writing to a phase agent.** An agent's name is the human's handle on it: `@P23 <words>` at the lead's prompt
  writes to P23's agent. Claude Code sends `@name` at the main prompt only to agent-team teammates; for a subagent
  the whole line is an ordinary prompt of the lead's ([spike 40](../spikes/40-agent-handles/README.md)). So the
  lead's `UserPromptSubmit` hook routes it, by its shape alone, since the hook cannot tell who typed it: a prompt
  from the human (never one of baton's lines or an agent's report) that starts with `@` and one of the run's phase
  ids, ignoring case, then whitespace.
  - When an agent of baton's is at work on the phase, the hook queues the words in the run's state and blocks the
    prompt: no turn starts, and nothing of it reaches the lead's model, whether the lead is idle or mid-turn. Claude
    Code shows the human only the block's reason, under its own frame, so the reason says what baton did: "sent to
    P23". The host sends the words through the mod, by the agent's id, framed as the human's own: Claude Code gives
    them to the agent at its next tool round, and they wake an idle one, which goes on with its phase. Claude Code
    keeps a blocked prompt in the input box, where a second Enter would send it again, so a second later the mod
    empties the box, only while it still holds exactly that prompt. Words the agent cannot take after all (it ended
    or was stopped first, or Claude Code refused the message) go to the lead's model, and the human is told.
  - For a phase no agent of baton's works on (not started, done, the lead's own, an agent the human stopped, one
    starting or landing), nothing is woken: the prompt goes on to the lead's model, told where that phase stands, and
    the human is told why.

  A name in Claude Code points to the latest agent given it (the typeahead, a message by name), so a model's own
  Agent call named for one of the plan's phases is refused with why. The board shows each working agent's handle,
  and an agent's question to the human names it. The human can also type in an agent's own view in Claude Code's
  agent panel, which wakes it whatever it is, and which baton does not see; an agent whose unchanged worktree Claude
  Code removed is resumed in the lead's own checkout. So baton records every agent it lets go of for good (retired,
  or replaced by a fresh one), and refuses every tool call of one that wakes, letting its stop through: it ends its
  turn having changed nothing, and nothing waits on it.
- **Room.** `workers: auto` sizes the fleet to the machine. New agents start at least 2 minutes apart, since the
  machine's readings lag the load an agent brings. A new agent also waits while the machine is busy, the lanes fall
  behind, landings keep conflicting, a usage limit holds or the run winds down near the end of the weekly allowance
  ([Usage limits](#usage-limits)), a replan is open or owed ([Replans](#replans)), or a risk assessment said "full
  now" ([The risk ledger](#the-risk-ledger)). Every hold is host code. An agent whose work waits to land takes no
  slot, since the merge queue and the lead hold it, not the agent; one handed back counts again. With every slot
  at work, a ready phase is held at the cap (`state.HoldCap`), which the board and `baton watch` show ("ready: all
  6 agent slots at work") and nothing announces: the run is going as it should.
- **QA phases run beside the lead** (v0.8.15). Where the run has phase agents (`State.HasAgents`: the host runs a
  fleet, `Runtime.Fleet`, the run is not serial, and it has a branch), a ready QA phase goes to an agent, even as
  the plan's last phase, and the lead, with no phase in hand, idles as it does while agents hold every phase. In a
  real run, the final QA phase held the human's checkout for 3h20m while they waited to review. Its agent's
  narrowed plan keeps the phases it checks (`Plan.QASince`). It is the lead's again once an agent of this plan
  failed on it, or when it is parked.
- **Ready for review.** Once every phase but the QA phases is done and the QA phase's session can take the human's
  words (the lead holds it, or its agent is at work), the human is told once a plan, announced and pushed, that a
  review of theirs now reaches the QA phase in time, through `@<its id>` or phases they add, and that nothing waits
  on them (`state.ReviewReady`, `decide.ReviewReady`, `review_ready`, `Runtime.Review`).
- **Landing.** A finished phase joins the merge queue. If the run's branch has moved on, it is merged into the phase's
  branch in its worktree, and a conflict goes back to its agent, which merges and resolves it itself. Under each
  conflicting path, the agent is told the commits that changed it on the run's branch since the merge base, with
  the phase that landed each (at most 5 a path; `gitx.LogPaths`, read before the lock), so it knows whose changes
  it merges. The lead's standing words say never to ask the human to carry words between sessions
  (`decide.NoCourier`): an agent gets baton's messages and does its own merges. Nothing baton
  runs, or asks an agent to run, rewrites a commit: a rebase can drop a resolved conflict without a word, and Claude
  Code's auto mode may refuse one as destructive. A merge starts only on a worktree with nothing uncommitted, and one
  that stops is aborted, leaving everything as it was. The phase is verified again if the run's branch moved (at most
  once for the checkout's own commits), unless nothing that came since shares a file with it and a QA phase ahead
  will check them together ([Gates](#landings-verified-again-sized-to-the-change)). Then the run's branch is fast-forwarded to it with `git merge --ff-only`
  between the lead's tool calls, never over a file the lead has uncommitted. The lead is told what moved, and never
  to rewrite it. A landing held for 10 minutes by the lead's uncommitted changes to the same files tells the lead's
  model to commit those files by path. When the lead's checkout moved to another branch that holds the run's (a
  session branch made at approval), the run's branch follows it, and the lead's model is told; one held because the
  checkout is on a branch that lacks the run's commits, or on a detached HEAD, is pushed to the human once, and the
  lead's model is told once a branch to merge the run's branch in, never rebase, or switch back.
- **Landed some other way.** When every commit of a phase's branch is on the run's branch already (merged by hand),
  the lead's `baton done <phase>` records it landed, as a decision without the human with its undo, and the host
  retires the agent, saving what it had not committed as a snapshot first. Otherwise the refusal says how to finish
  the phase: its agent's own `done`, or a merge into the run's branch first.
- **What a phase starts, it stops.** In a real run, a phase agent kept its own test stack up after its work, beside
  the main one, and the QA gate competed with it for the machine: the rule lived only in model judgment. Package
  `leftover` finds what a phase left running: processes whose working directory is inside its worktree or a lane
  it used and that carry a baton variable (or are hidden in the process group of one), and, where the `docker` CLI
  is on the PATH, containers whose Compose project's working directory is there. A phase agent's `baton done` is
  refused once over them, listing each with the command to stop it; the same call again goes through. Retiring an
  agent stops its processes before its worktree goes, and names its containers to the lead with the command to
  remove them: baton removes no container itself. While the machine has no room and a gate runs past its history,
  the gate's owner is told what finished phases left running. `baton clean` reads through the same package.
- **What they saved.** As the plan completes, baton says what phase agents saved: the phases' working time (a phase
  the lead did, from its start to its done; an agent's, from its agent's start to its finish, not to its landing,
  which waits on the merge queue and the lead) against the wall clock from the first phase's start, as in "phase
  agents saved about 3h20m: 8h29m of phase work in 5h09m". The wall clock runs through every wait, so the figure is
  never more than the work done side by side, and never below zero. `baton status` shows it for a complete plan.
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
- **Nothing waits on the human for what baton can decide** (v0.8.3, v0.8.4, v0.8.6). With no deadline, baton waits
  on them only where they chose it (Pause baton, `/baton pause`, a `max_auto_decisions` they set, a phase agent they
  stopped twice whose work is not done, while the rest of the run can go on) or where nothing in baton can answer
  (a permission prompt, the usage limit until its reset, a login or billing error, a refusal, which is never
  retried). A run that was working on its own goes on at the usage limit's reset, and the weekly wind-down ends at
  the weekly reset; the human cancels either by pausing or writing ([Usage limits](#usage-limits)). Every other
  wait has a deadline, and the deadline picks the model's side: the human said they would rather correct an
  assumption later than find a run idle for hours, and that nothing should wait more than a few minutes, 20 at most.
  Only a block waits longer, 30 minutes, as below. baton's own holds have one too: a risk assessment's hold on new
  phases lifts at `escalation_timeout`, and a look-in held for the advisor goes without it after 60 seconds. Every
  question baton puts to a model about its gates (a gate out of proportion, a background check, what a phase left
  running) is asked once, and the same call again goes through.
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
  (`hooks.Lapse`); in serial mode the host dismisses it with Esc, which picks no option in any dialog, and records
  the default itself (`hooks.Lapse`): no digit is typed, since a Bash prompt's `3` now approves the call and switches
  to auto mode ([spike 39](../spikes/39-escape-answers/README.md)). A review the human opted into is never answered
  for them.
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
  prompt's third option is "Yes, and switch to auto mode". Where the mod doesn't run (serial mode), the host
  dismisses the question with Esc, which picks nothing in any dialog (it rejects a permission prompt, leaves a
  draft and background agents alone, and at most ends the lead's turn), and records the default itself, since no
  hook reports a dismissal; only while baton's question, in exactly the shape baton issued, is the only dialog open.
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
  burn hours and hold everything behind them, so a phase before a QA phase is briefed to run checks that finish in
  seconds or minutes (the tests of what it changed, by name) and may end in a work-in-progress state; the QA
  phase runs the full suites and lints once, reruns only what failed after a red, and fixes what the phases before
  it left incoherent. QA phases sit at real seams, never per phase ([Gates](#gates)). baton cannot tell a focused
  check from a full one, so it asks the model once of each `verify` or `await` queued before a QA phase (a wait on
  CI is often a full check in disguise), and the job runs either way; a gate it knows is long (an earlier run took
  10 minutes or more, or a whole suite) is questioned with the time it costs ([Proportion](#proportion)), and
  `--qa "<why now>"` flags a full check run early on purpose. A red result there says that other phases' work in progress may be why: the
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

## Usage limits

Every session of a run spends from one account, which has two windows: five hours and a week. In real runs, two
five-hour stops cost 3h20m, and both weekly limits were taken for unknown resets: baton retried into one at 15 and
30 minutes with its reset 4 days away. Nothing resumes a stopped session by itself: every resumption was baton's
retry or the human's prompt ([`research/usage.md`](research/usage.md)). The human chose time to completion over
pacing: no budget, a run as far as the account goes, and baton's waits no longer than the limit's own.

- **Which window, and when it resets.** The stopped loop's own transcript says both before the stop is reported
  (`quotaLimits.rateLimitType`, `resetsAt`); the stop's hook reads them before the lock. Failing that, a window the
  lead's status line reads at 99% or more names the limit, the weekly window first. Otherwise the window is unknown:
  a 429 not known to be a usage limit, retried at 15, then 30 minutes. The status line's weekly reading
  (`rate_limits.seven_day`) is stored beside the five-hour one.
- **The hold runs in every run,** with phase agents or none: until the reset plus 2 minutes (`state.ResumeGrace`,
  for the account's clock and baton's), no new agent starts and no session is typed into. A reply to any of the
  run's sessions after the hit lifts it (the human switched accounts, say).
- **The resume timer.** A limit hit while the run worked on its own (running, not paused, and the stopped turn not
  the human's) arms a timer at the stored reset plus 2 minutes, never a fresh reading, since the status line drops
  a window once its reset passes. It lives in the run's state, so a restart keeps it and a machine asleep through
  the reset resumes on waking. Firing is each stopped session's held retry, typed once, with no second wake path; a
  session limited again within 10 minutes tries again 2, 5, 10, then every 15 minutes. The band, the board, the
  status line and `baton status` say when the run resumes.
- **The human's act cancels it:** a pause, any prompt of theirs at the lead while it waits (an `@<phase>` too),
  which also pauses the run, and leaving baton. A limit hit by the human's own turn cancels it at once. Then the run
  waits for `/baton run`, which arms it again. A `/baton` command that only looks (status, board) cancels nothing.
- **The weekly wind-down.** At `weekly_reserve_pct` (2 by default) of the weekly allowance left, nothing new
  starts: the lead takes no new phase, no new agent starts, and the phases under way, their gates and landings
  finish. The run goes on 2 minutes after the weekly reset it read, or at once on a reading below the line. The
  line reads the lead's status line, the only reading there is, which moves only with the lead's own turns, so it
  can come late; a weekly limit hit first is the timer's. Its cancel is the pause itself, as the timer's is, and
  `/baton run` arms it again.

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
