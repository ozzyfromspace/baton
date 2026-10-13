# Changelog

All notable changes to baton are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and baton uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.8.1] - 2026-10-12

A phase agent's work used to land by a rebase, and a conflict went back to the agent with "rebase it
yourself". In a real run, Claude Code's auto mode refused that rebase as destructive. The agent got its
work onto a side branch another way, then asked to move its phase branch and delete the side branch, and
the word "delete" made the question wait for the human, so the phase sat until someone answered. Now
nothing baton runs, or asks an agent to run, rewrites a commit.

### Changed
- **Landing merges instead of rebasing.** When the run's branch has moved on, baton merges it into the
  phase's branch, and the run's branch fast-forwards to the merge. A phase whose branch the run's branch
  never left still lands as a plain fast-forward. The run's history gains a merge commit where phases
  overlapped in time, and keeps every commit as it was made, signatures included.
- **A conflict goes back to the phase agent as a merge:** `git merge <run's branch>`, resolve, `git add`,
  `git commit --no-edit`. A merge adds a commit and rewrites none, so there is nothing for auto mode to
  refuse, and a resolution stays when the run's branch moves on again, where a rebase could drop it without
  a word.
- **baton's merge leaves nothing half done.** It starts only when the phase's worktree has no uncommitted
  change to a tracked file, never stashes, and is aborted if it stops, on a conflict or a merge commit that
  can't be signed, so the branch, its index and its files stay as they were. An untracked file in its way
  makes git refuse before it changes anything.
- **A landing that cannot go through never says to discard work:** the agent is told to commit what is
  uncommitted, never to remove it.

## [0.8.0] - 2026-10-12

v0.7's workers were hidden sessions, each in a pseudo-terminal of baton's own: easy for baton to drive, and
hard to follow. You couldn't tell how many ran, on which phases, or whether the run was moving at all. Now
each phase beside your session runs as a Claude Code background agent of that session, in the terminal
you're already watching: a row per phase agent in the agent panel, its transcript a keystroke away, its
prompts and questions in place, and baton's band above the prompt with the whole run. The experiments
behind it are in [docs/research/visible-workers.md](docs/research/visible-workers.md).

A session hosted by v0.7 stays on its version after you update (before 1.0, a minor release counts as a
major one) and says so once: exit and run `baton --continue` to move it to v0.8.

### Added
- **Phase agents.** Each phase that can go ahead beside your session runs as a background agent of it, in a
  git worktree and on a branch of its own, started, messaged and stopped by baton's mod on the host's
  say-so. Each gets your session's guarantees from baton's hooks, never from a model's memory: a brief as
  it starts, a stop without a status refused, gate results and answers delivered as messages, its context
  read from its own transcript and its checkpoint taken as a fresh agent, a refused, failed or interrupted
  turn handled by the host, and a return after a crash or a `/clear`. Its reports go to baton, not to your
  session's model.
- **baton's mod** (`plugin/hooks/baton.js`): JavaScript that Claude Code runs in your session, a thin
  executor that decides and words nothing. Where Claude Code doesn't load it (an organization that allows
  only managed mods, or an older Claude Code), the plan runs one phase at a time in your session, and baton
  says so.
- **The band**, one line above the prompt with the plan's step and each phase:
  `◆ baton · step 2/4 · P3 ⚙ 12m · P4 verifying 3m · P5 waits on P3 · lead: waiting on j4`.
- **`/baton board`**, `/baton-board` (which opens while Claude works) and `baton board` (`--band`,
  `--width`, `--json`): every step and phase with how it stands, its agent, job and landing, and what your
  session is doing.
- **A phase agent's question comes to you in Claude Code's own dialog**, without stopping your session's
  turn, and your answer goes to the agent. Wait for me leaves only that phase waiting, until `/baton run`
  asks you again.
- **A phase added mid-run is taken up at once** when your session holds no phase, rather than after a
  running job ends.
- `baton verify` and `baton await` also take a gate as its words after `--`
  (`baton verify --notes "…" -- make test`). A phase agent gives its gates that way, and baton rewrites a
  quoted one into it, since Claude Code's worktree guard refuses a quoted command that starts with a shell.

### Changed
- **A compaction counts only on proof.** A phase agent's compaction fires the same hooks as your session's,
  with nothing to tell them apart, so those hooks now change nothing. The proof is the mod's report of your
  session's compaction, or a new boundary in a conversation's own transcript, and the brief after a
  compaction comes with the wake-up, for every kind of compaction.
- **Where baton's mod runs, baton's questions are settled at their deadline with no key typed.** In Claude
  Code 2.1.296 a permission prompt's third option is "Yes, and switch to auto mode", so the `3` baton typed
  could have approved a command that was in front of its question. Without the mod, the typed `3` stays,
  only while baton's question is the only dialog open.
- **A phase agent's permission prompt shows in your terminal,** the band shows `P3 needs you`, and baton
  pushes if it waits.
- **The status line** keeps your session's segment; the phase agents are on the band.
- **baton makes every worktree Claude Code asks for in a hosted session:** phase agents' under
  `~/.baton/worktrees/`, and a model's own isolated subagents' as Claude Code would make them, but without
  `.worktreeinclude`, `worktree.symlinkDirectories` or `worktree.sparsePaths`.
- **A model's own call to baton's agent type is refused,** as its messages to baton's agents and its stops
  of them are: only baton starts, steers and ends phase agents.
- **The docs are much shorter.** The README is about a quarter of its length, and
  [docs/design.md](docs/design.md) replaces docs/plan.md and docs/escalation.md. Each research note opens
  with a summary.

### Removed
- **The hidden worker sessions:** baton no longer launches a claude of its own for a phase, and
  `baton view` and `/baton view` are gone.

## [0.7.0] - 2026-10-12

v0.6 ran a plan unattended until something stopped the lead in a way baton couldn't retry. A turn the
model's safeguards refused looked like any API error, a claude that crashed took the run with it, and a
rewind or a `/clear` left baton and the model remembering different things. Now a refusal says what
happened, what only you decide and what goes on meanwhile; a crashed lead comes back by itself; and
baton notices a rewind and tells the model what still stands. The experiments behind it are in
[docs/research/recovery.md](docs/research/recovery.md).

A session hosted by v0.6 stays on its version after you update (before 1.0, a minor release counts as a
major one) and says so once: exit and run `baton --continue` to move it to v0.7.

### Added
- **A refused turn is told apart, and never retried.** When the model's safeguards decline a turn and
  Claude Code won't go on with it, baton reads the refusal and its category from the session's
  transcript (read-only) and escalates it as `refused`: which phase stopped, that baton won't retry it,
  the two ways on (rewind to before the flagged turn and take a different approach, or `/clear` for a
  fresh conversation from the phase's brief), what goes on meanwhile and what waits. baton types nothing
  into a refused conversation, starts no fresh one by itself, and never switches models. The status line
  shows `⚠ refused · waiting on you`, and `baton status` names the category.
- **A crashed lead comes back.** When claude exits by itself mid-plan (not with 0, 130 or 143, not on a
  signal sent to baton, a closed terminal, `/exit` or a restart of baton's own), the host resumes the
  same conversation in place, tells the model the turn under way was lost and to check what's done, and
  resumes the workers. At most twice in 30 minutes; a third time it stops and asks you to run
  `baton --resume <id>`. A conversation that can't be resumed gets a fresh session for the same run,
  from the phase's brief, once per phase. Events `lead_exited`, `lead_resumed` and `lead_fresh`. Not on
  Windows.
- **A rewind is noticed, and the model told what still stands.** baton marks the transcript's last entry
  at each prompt and checks at the turn's next hook that the prompt follows on from it. After a rewind
  (Esc Esc), the model is told, once, at its next tool call or stop: the decisions, jobs, phases done and
  block recorded in the part rewound away, which still stand; the files its edit tools wrote there, which
  are as they are on disk; and the brief again if the rewind dropped it. baton's record is never rolled
  back. Event `rewound`.
- **`/clear` mid-phase gets the phase's brief:** its section of the plan, the standing rules, the notes
  and decisions, and a reminder to check what's already done.
- **`baton verify --here "<command>"`** runs a gate on `HEAD` in the session's own working tree, with no
  lane, for a gate that needs the checkout itself (its running services, files not in git). Until it
  ends, baton refuses edits there, starts no other phase there, and lands no worker's work in the
  checkout (`land_deferred`, why "a gate runs in the checkout").

### Changed
- **A gate wait says nothing is needed from you.** The stop line, once the lead's turn ends on a phase
  that waits on its job, says so where Claude Code's idle notice reads otherwise, and the status line
  shows `P3 waits on its gate 12m`.
- **A stop baton can't retry says what only you decide,** and what the rest of the run does meanwhile:
  with workers, every phase that doesn't need the stopped one goes on and the phases that need it wait;
  without them, nothing runs.
- **A `verify` whose command names the checkout is refused,** with `--here` or a `.baton-lane` as the
  way on: run from a lane, it checked the checkout's files as they were, not the commit, while landings
  and edits changed them under it.

### Fixed
- **A worker's phase lands while the lead keeps committing.** A landing was verified again whenever the
  lead's branch had moved since its gate passed, so each lead commit during that run made it stale again,
  and a lead committing more often than the gate takes held it back for good. Another phase's landing
  still means verifying again; commits made in the checkout mean it at most once, and any that come in
  while it runs are landed over and named (`landed` has `unverified`), and the lead is told its next gate
  runs them together. `reverify_queued` says why (`landed` or `lead`).
- **A worker's landing leaves the lead's escalation alone.** Each landing cleared the escalation of a lead
  whose turn had failed, the watchdog raised it again, and you got the same push again.
- **A worker's question never waits on a lead that stopped.** A question the lead had not put to you
  when its turn failed sat in the queue unseen; it is now left in the worker's own session, and you're
  pushed with `baton view P3`.
- **A session baton starts again keeps your permission rules.** A restart on a newer baton started claude
  without your `--allowedTools` and `--disallowedTools`.
- **A worker's brief quotes the notes its own phase left,** so the brief after its own checkpoint no
  longer leaves them out.
- **A `/clear` in a worker's session keeps it in baton,** with its phase's brief. The new conversation's
  hooks used to go dormant, and the worker dropped out of the run.

## [0.6.0] - 2026-10-11

v0.5 ran workers beside the lead, but you picked how many, and what a run left behind waited for that same
run to come back. Now the plan and the machine size the fleet: a worker starts whenever a phase can go
ahead and the machine, the lanes and the landings have room for it, and baton finishes a run sooner by
using the five-hour allowance faster rather than keeping a reserve. Nothing baton creates outlives its
use: a new run ends the workers earlier runs abandoned, a replaced plan ends its own, and `baton clean`
gets back to a known state in one command. The readings and the process lookup behind it are in
[docs/research/workers.md](docs/research/workers.md) ("Machine readings and leftovers").

A session hosted by v0.5 stays on its version after you update (before 1.0, a minor release counts as a
major one) and says so once: exit and run `baton --continue` to move it to v0.6.

### Added
- **`workers: auto`, the default.** The most workers at once is one for each 2 CPUs or 4 GiB of memory,
  whichever allows fewer, less one for the lead, at most 8: 6 on a 14-CPU, 48 GiB Mac, 1 on an 8 GB
  laptop, none under 4 CPUs or 8 GiB, where baton runs without workers and says so. CPUs and memory are
  read without cgo (sysctl on macOS, `/proc` and the cgroup's limits on Linux; a container counts what it
  may use). A number from 1 to 8 sets the most yourself, whatever the machine; `0` still turns workers
  off. `BATON_WORKERS` takes `auto` or the number.
- **What holds a new worker back,** checked by baton at every step and never stopping a worker at work:
  the machine's load at or over its CPUs, memory pressure at critical or swap growing by more than
  256 MiB a minute at warn (macOS), less than 15% of memory available (Linux); a minute since the last
  worker started (with `auto`); a gate that has waited more than a minute for a lane; 2 landings in a row
  handed back for a rebase conflict, while a worker of the plan is left to land; and the five-hour usage,
  if you set `worker_usage_max`. Resuming a worker waits for the machine only. A hold is told once
  (`worker_deferred` with `held`), shown on the status line (`W held: busy`, `lanes` or `conflicts`) and by
  `baton status`, which also says how many workers this machine allows and what it reads now (`workers:
  auto, up to 6 on this machine (14 CPUs, 48 GiB) · load 3.1 · memory pressure normal (45% free)`).
  Attaching a plan names the plan's widest step beside what the machine allows.
- **`baton clean`** (and `/baton clean`) ends what no live session uses in the project: the workers of
  runs nobody will come back to (their work saved, worktrees removed, branches with unlanded commits
  kept), worktrees under `~/.baton/worktrees/` no worker is on, those runs' unfinished jobs, lanes nobody
  holds, processes such sessions left running under `~/.baton` (others are listed with the `kill` to run,
  never stopped), records of runs and sessions that are gone, temporary files from interrupted writes,
  dead `baton view` sockets and old records of sessions handed over. Live runs, and runs quiet for under
  2 minutes or whose baton still runs, are named and left alone. It lists what it keeps with the command
  to remove each, `--dry-run` reports the same actions and changes nothing, and a second run says
  `baton: nothing to clean`. Each run it changed logs `cleaned`.
- **A new run ends the workers earlier runs abandoned.** When a plan is attached, the project's other runs
  that have been quiet for 2 minutes, with no baton of their own running, have their workers ended as
  `/baton drop` would; their phases go back to those runs. The new run logs `workers_retired` and says
  so in one line.

### Changed
- **`worker_usage_max` is off by default** (it was 80%). Set it to keep a reserve of the five-hour
  allowance; the status line's `5h` reading shows only when it is set.
- **A plan replaced over a run ends the old plan's workers**, including suspended ones, which used to be
  orphaned with their worktrees and branches. Plans number their phases alike, so a worker belongs to the
  plan it was started for, and the new plan's worker on a phase of the same id starts on a fresh branch.
- **A branch with commits that never landed** is renamed to `baton/<run>/kept/<phase>-<YYYYMMDD-HHMM>`
  once its plan is replaced or its run dropped, so no later worker carries on from it. While the plan
  stands, it keeps its name, and the phase's next worker carries on from it.
- **A session continued with workers off** (`workers: 0`, or a machine with room for none) ends the run's
  suspended workers, and the lead takes their phases; a phase a suspended worker held used to stay in
  progress forever.
- **Workers can be ended by any baton process, one at a time.** A worker being ended is marked on its
  run, so it is never resumed and ended at once, and nothing ends a worker whose session is live. If its
  uncommitted work can't be saved, its worktree stays and is named. Its gates are stopped only while the
  pid on record is still baton's runner.
- **baton forgets only the worktrees it made.** Where it ran `git worktree prune`, which also forgot a
  worktree of yours on a drive that isn't mounted, it now forgets only missing worktrees under its own
  directories, and never a locked one.

### Fixed
- **`baton view P3`** picks the running worker when a run that ended has a worker on the same phase,
  instead of refusing as ambiguous; it asks for a ref only when several are running.
- **A run's owner records its host's pid**, not that of the hook that first claimed it, which had long
  exited.

## [0.5.0] - 2026-10-11

v0.4 kept a run from waiting on its gates, but still worked on one phase at a time. The replays behind it
put the next step at two sessions working at once: 58 hours to 40 on one long campaign, and 31 to 14 on
another, while a third session added about 1%. baton now works on phases that don't need each other at
the same time. The session in your terminal, the *lead*, works as before; beside it, a hidden *worker*
session takes another phase that can go ahead, in a git worktree of its own, and baton lands its work on
your branch under the lead when it is done. A worker's questions are put to you in your terminal, by the
lead, and `baton view` shows a worker live in another terminal. The experiments behind the design are in
[docs/research/workers.md](docs/research/workers.md).

A session hosted by v0.4 stays on its version after you update (before 1.0, a minor release counts as a
major one) and says so once: exit and run `baton --continue` to move it to v0.5.

### Added
- **Workers.** While a plan runs, baton starts up to `workers` hidden sessions (1 by default, 0 to 3,
  `BATON_WORKERS`), each on the first phase in document order that can start besides the lead's, in a
  worktree under `~/.baton/worktrees/` on a branch `baton/<run>/<phase>` from your branch. A worker
  starts with the lead's flags (model and permission mode included), works from a brief of its phase, the
  standing rules and the notes of the phases it needs, and does everything a phase does: `verify` and
  `await`, `done` while its gate runs, notes, proposals, checkpoints and compactions. Commands that belong
  to the lead, plan mode and edits in your checkout are refused in a worker. A worker whose session exits
  unexpectedly is resumed in its worktree, at most twice. `workers: 0` turns workers off, and baton then
  runs exactly as v0.4; a plan whose phases form a chain never starts one.
- **Landing.** A worker's finished phase joins a merge queue, first finished, first landed. baton rebases
  its branch onto your branch in the worker's worktree, verifies it again in a lane if anything landed
  meanwhile, waits while the branch would touch a file you have uncommitted, staged or untracked (ignored
  files included), and lands it with `git merge --ff-only` in your checkout, only between the lead's tool
  calls. A conflict goes back to the worker, which rebases and resolves it itself; a red re-verification
  reopens its phase. The lead is told what landed and that `HEAD` moved, and baton refuses its
  `git commit --amend`, `reset` and `rebase` over landed commits. The worker then ends, its worktree and
  branch are removed, and its phase is done.
- **Questions relayed through the lead.** A worker's proposals, blocks and reviews are put to you by the
  lead, one at a time and never over a question of its own, naming the phase (`baton: worker P3: …`). A
  timed proposal goes ahead at its deadline as in the lead; anything else that nobody answers within
  `escalation_timeout` is left for you in the worker's own session, so the lead never waits on a worker.
  Your answer goes back to the worker. A worker's block stops only its phase.
- **`baton view [phase]`**, in another terminal, shows a worker's screen live, repainted at your
  terminal's size, and passes your keys to it as yours; `Ctrl-]` leaves. With no phase it lists the
  workers. A permission prompt in a worker is never answered by baton: the lead tells you, baton pushes,
  and you answer it there.
- **Usage limits across sessions.** Every session's status line records the account's five-hour usage
  (`rate_limits`, on Pro and Max plans). No new worker starts above `worker_usage_max` (80%,
  `BATON_WORKER_USAGE_MAX`, 0 turns the check off), and once any session hits a usage limit, no worker
  starts and baton types nothing into any of the run's sessions until it resets.
- **Seeing the workers.** The status line shows each worker after the lead's phase (`W P3 14m`,
  `P3 landing`, `P3 waits to land`, `? P3` while its question waits, `! P3 needs you`), the usage reading
  once it reaches `worker_usage_max` (`5h 85%`), and `usage limit until 15:40`. `baton status` marks a
  worker's phase `⚙` with how it stands, and lists the workers (branch, session, worktree), the merge
  queue with any deferral, the questions from workers, the usage, and the branches of workers that ended
  with commits that never landed. Attaching a plan says how many workers will run, and warns when the
  lead's permission mode would stop them at prompts, or the project has a `.claude/settings.local.json`,
  which worktrees lack. Every step is announced in the lead and logged: `worker_spawned`,
  `worker_started`, `worker_waiting`, `worker_ended`, `worker_resumed`, `land_queued`, `land_deferred`,
  `rebased`, `reverify_queued`, `land_returned`, `landed`, `relay_queued`, `relay_asked`,
  `relay_answered`, `relay_applied`, `usage_limited`. A worker's own events are in
  `.baton/runs/<session>/workers/<phase>/events.jsonl`, and its decisions are numbered with its phase
  (`P3.d1`).

### Changed
- **baton's requests mid-turn are hook feedback.** A checkpoint nudge, the context-warning question, a
  job's result, and now a worker's question or a landing reach the model as PostToolUse
  `{"decision": "block", "reason": …}` instead of `additionalContext`, which Haiku sometimes refused as
  possible injection. Claude Code shows them as "PostToolUse hook returned blocking error" followed by
  baton's text: the tool call ran, and nothing failed. A context-warning question still unasked when the
  turn ends refuses the stop once with the same call.
- **`/baton pause`, `drop` and `exit` reach the workers.** Pause stops baton starting workers and landing
  their work; those already at work carry on. Drop saves each worker's uncommitted work as a snapshot,
  ends it, removes its worktree, and keeps a branch with commits that never landed. Exit, a hang-up or the
  session ending suspends the workers, and the next baton session of the run resumes them.
- **An update restart waits until every worker is idle**, starts no new worker meanwhile, and the
  restarted baton resumes each one in its worktree.

### Fixed
- **An edit in directories that don't exist yet** is matched to the session's files through the deepest
  directory that does, so a checkout reached through a symlink (`/tmp` on macOS) no longer misses it.
- **A prompt after `--` keeps its words.** `baton -- "<prompt>"` put the session id baton picks after the
  `--`, where claude took it for part of the prompt; it now goes before it.

## [0.4.0] - 2026-10-11

Four real runs, replayed second by second, showed where a long plan's time goes: waiting on gates was 65%
and 38% of the two big campaigns, and the model worked while a gate ran for 1.5 hours of 26.8. Most of the
waiting sat inside a phase (run the gates, read the failures, fix, run them again), and most phases of
those plans did not need each other. baton now keeps the wait off the critical path: the model hands a
long gate to baton and works on, and when the wait is long and another phase can go ahead, the session
switches to it and comes back when the result is in. Phases still run one at a time, in one session. The
evidence is in [docs/research/parallel.md](docs/research/parallel.md).

A session hosted by v0.3 stays on its version after you update (before 1.0, a minor release counts as a
major one) and says so once: exit and run `baton --continue` to move it to v0.4.

### Added
- **Phases name what they wait for.** The first line under a phase's heading may say `After: P1, P3`,
  `After: none` or `After: all` (every phase before it); without it, a phase follows the one before, so
  plans written before v0.4 run as they did. Only earlier phases may be named. baton reads the lines
  from the plan file at every stop, so they can change mid-run. Attaching or approving a plan prints each
  phase's dependencies and the longest chain, and so does `baton status`. `/baton plan` writes the lines.
- **`baton verify "<command>" --notes "…"`** hands a gate to baton, which runs it on `HEAD` in a lane
  while the model keeps working. It needs git and the phase's work committed. **`baton await`** does the
  same for something outside the project (a CI run, a deploy), from the working tree, with no git
  needed. A job runs detached from the session, survives baton restarting itself, and is stopped after
  `verify_timeout` (3h). Its result reaches the model with its next tool call, at its next stop, or with
  a short line baton types to wake an idle session, and names the other phases that are red.
- **Lanes.** A lane is a worktree of the project under `~/.baton/lanes/`, outside the checkout, reused
  from job to job so ignored caches stay. A project may commit an executable `.baton-lane` (`up`, `run`,
  `down`, `slots`) to install dependencies, start services and run up to 4 lanes at once; without one, a
  gate runs with `sh -c` in the bare worktree, one at a time. A lane that can't run the gate (`up` failing
  twice, exit 125 to 127) is reported as a lane error, never as a failed gate. Lanes come down when the
  session ends, the run is dropped, or the plan is complete, and lanes left behind are swept.
- **Done while the gate runs.** `baton done` on a phase whose gate still runs makes it verifying, and the
  run moves on: the phase is done when the gate passes, and reopens with the result when it fails, ahead
  of any phase not started. Phases that need it wait until it passes. `done` is refused over a red
  verification unless `--despite-red "<why>"`, which counts as a decision made without you.
- **Switching.** A phase that can't go on without its gate's result idles in place. Once the gate has run
  for `switch_after` (5m, or at once for a command whose earlier runs took that long) and another phase
  can start, the host sets the phase aside with its notes, compacts, and starts the other phase; the
  phase set aside resumes, with its result, at the next boundary or `baton checkpoint`. A switch whose
  gate ends before its compaction is typed is called off, and never happens with the phase's work
  uncommitted.
- **Long commands move to the background.** baton starts claude with
  `CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS` set from `foreground_max` (2m, `off` to leave it to Claude
  Code), unless you set it yourself, so a long command keeps running in the background and wakes the
  session when it ends.
- **Seeing it.** The status line shows the oldest job the run waits on (`P3 verifying 12m +1`).
  `baton status` says why each phase not done is verifying or set aside, with its notes, and lists the
  jobs with their logs and the lanes, and how to add a `.baton-lane`. A session that starts afresh is
  told which phases are verifying or set aside. Every step is announced in the session and logged:
  `job_queued`, `job_started`, `job_finished`, `phase_verifying`, `phase_parked`, `phase_switched`,
  `phase_resumed`, `switch_cancelled`, `switch_held`, `results_delivered`, `lanes_down`. A phase done by
  its verification says so in later briefs.

### Changed
- **A stop on the model's own background work** (a command Claude Code moved to the background, or a
  background subagent) is refused once, to remind it to keep working on what does not depend on it and to
  send a long or stateful gate to `baton verify`; after that the session may idle until the work wakes it.
  The watchdog now holds until that work has run for an hour (it was 30 minutes from the last stop).
- **A stop without a status** offers `baton await` where it offered a declared wait.
- **The model is told that a question baton gives it to ask is not one of its own**, which it must not
  ask while a plan runs: it asks it as given.

### Removed
- **`baton waiting` (breaking).** Its Stop row, the watchdog's hold (and `BATON_WAIT_GRACE`), and its
  place in the status line and `baton status` are gone too. A long command now runs in the background by
  itself; a wait on something outside the project goes to `baton await`.

### Fixed
- **A Monitor no longer counts as background work.** Claude Code reports one at Stop as a shell, so baton
  took a session watching a Monitor for a busy one. baton now tells them apart by task id. A Monitor
  still holds an update restart, since it ends with claude.
- **Claude Code's idle compaction is off in hosted sessions** (`"idleCompaction": false` in the settings
  baton passes to each session). Claude Code compacts a conversation of 200k tokens or more on its own
  once it has been idle for most of an hour; baton decides when a hosted session compacts, and a session
  may now idle on purpose while its gate runs.
- **Phases stay in document order when the plan file changes.** baton put the finished phases first
  when it re-read a plan, and ended a phase's section at the next phase in that list rather than the next
  heading in the file, so a phase added above a finished one was misplaced.

## [0.3.7] - 2026-10-08

### Changed
- **`/baton run` picks a paused run back up, and `/baton resume` is gone.** "Run" now means "make the
  plan go", whatever state it is in, and baton decides what that means rather than Claude. With no file,
  or this session's own plan file, a paused or blocked run is resumed (with v0.3.6's compaction if the
  conversation grew while paused), and a running one is left alone. Another plan is attached and started
  as before; over a run that is underway, only once you agree to discard its progress. A finished plan
  is not run again (only phases added to it run): `/baton drop` it first if you mean to. A paused run whose plan file changed so that
  baton can no longer follow it stays paused until the file is fixed. `baton resume` is removed too.
- Inside a hosted session, `baton <not a command>` says so, as it already did in a plain session.

### Fixed
- **Phases you add to the plan file run.** baton re-reads the plan file whenever it changes (by its hash, at
  every stop, and now also as each phase ends), but it only checked the phases it already had, so a phase
  added while the plan ran was never run: the run finished without it, and the phase before it was
  briefed with its text. A new heading like the plan's own (`## P3 — …`, at the same level, with a new id)
  now joins the run in document order, and baton says so. On a plan that has finished, `/baton run`
  starts the first new phase after a compaction; with nothing new, it says the plan is complete.

## [0.3.6] - 2026-10-08

### Changed
- **`/baton run` compacts before the first phase**, as approving a plan in plan mode does, whenever the
  conversation had turns before it (or was resumed): P0 starts from a brief, not in the middle of
  whatever came before. That includes a new plan after `/baton drop`. A conversation whose first turn it
  is starts at once, as before. In a plain session, the compaction happens once baton hosts it.
- **`/baton resume` compacts first if the conversation grew while paused.** `baton pause` notes the
  context's size; if it grew by more than 20k tokens by the time you resume, the model must first record
  where the phase stands (`baton checkpoint --notes`, including anything from the paused conversation
  the phase needs), and baton compacts before the phase goes on. The hooks hold the model to it: other
  tools are refused until it does, and if it stops without the notes three times, baton compacts anyway.

## [0.3.5] - 2026-10-08

### Fixed
- **A conversation stops following old /baton instructions after an update.** Each `/baton` puts its
  instructions into the conversation, and an update cannot change what is already there: a long-lived
  session went on reading the copy it loaded under an older baton until a compaction or the next
  `/baton`. When a session restarts on a newer baton while idle and the conversation still holds such a
  copy (baton reads the session's transcript once to tell), the first prompt after the restart hands
  Claude the current instructions, says the earlier copy is out of date, and tells you it did. A restart
  at a phase boundary needs none: the compaction it takes the place of clears the old copy.

## [0.3.4] - 2026-10-07

### Fixed
- **`baton update` in a session that has not restarted yet** no longer downloads again and says
  "updated" every time. It runs on the session's own, older baton, and now counts the installed one: it
  says baton is up to date, and when the session moves to the new version.

## [0.3.3] - 2026-10-07

### Fixed
- **A hosted session stays on one version until it restarts.** After an update, the newest `baton` comes
  first on the PATH, so the model's `baton` commands and `/baton`'s instructions ran the new version while
  the session's host and hooks still ran the old one. A `baton` run inside a hosted session now hands
  itself to the host's binary. This is what keeps a session on its version after a major update.
- **`baton update` says what happens to each kind of session**: hosted sessions restart on the new
  version (or, after a major update, stay put), and plain sessions use it the next time they run
  `/baton`. It used to say every running session kept the old version until restarted.

## [0.3.2] - 2026-10-07

### Added
- **`/baton update` updates `/baton` itself.** The skill's instructions now come from the `baton`
  binary each time you use `/baton`, so every session, plain or hosted, follows the new version as soon
  as it is installed, without a restart. The plugin's skill is a stub that loads them.
- **`/baton version`** shows which baton this session runs, which one is installed, and the latest
  release. `baton version` in a shell prints the first two.

### Changed
- **`/baton elevate` is now `/baton start`**, and **`/baton stop` is now `/baton drop`**. The old names
  are gone. In a shell, `baton start` starts a session, like plain `baton`.
- The unlisted `baton run` (the same as plain `baton`) is gone.

### Fixed
- **No claude inside claude.** Inside a Claude Code session, `baton` with an unknown command (a typo, or
  a command from an older skill) used to be taken for a prompt and start another claude in the Bash
  tool. It now says so and points to `/baton start`.

## [0.3.1] - 2026-10-07

### Added
- **An update reaches the sessions already running.** After `baton update`, every session baton hosts
  moves to the new version by itself: it restarts in place (the same terminal, the same process, the same
  conversation) once it has been idle for two minutes, or, with a plan running, at its next phase
  boundary, where the restarted session takes the compaction. It never restarts with a turn, a dialog or
  a draft open, or while anything that would end with claude runs (background work, a monitor, a
  scheduled wakeup). Only compatible releases are taken this way: patch and minor releases, or before
  1.0, patch releases. After a major update, a session says so once and stays on its version until you
  restart it. `"auto_restart": false` in `~/.baton/config.json` turns it off. Sessions started on
  v0.3.0 or earlier need one last restart by hand.

## [0.3.0] - 2026-10-07

A campaign finished, and the next one was planned and approved in the same session. baton still held the
finished run, and the model, not baton, said what to do next: compact, attach the new plan, begin P0. And a
second `baton` terminal in the same project could not run a plan at all; it ran as plain claude, unless you
knew to give it a git worktree. baton now attaches the plan you approve, and every session runs its own.
The evidence is in [docs/research/sessions.md](docs/research/sessions.md).

### Added
- **Approving a plan runs it.** When you approve a plan whose phases are headings (`## P0 — …`), baton
  attaches it, compacts the planning conversation away, and starts P0 from a brief: the phase's
  instructions, the standing rules and nothing else. Approved while a run is under way, the plan is put to
  you: **Continue with the revised plan** (phases already done stay done) or **Start the new plan from
  P0**, or for a plan made after a `/clear`, **Keep the current run**. A plan without phase headings is
  left alone. A plan approved with Claude Code's "clear context" option starts at once.
- **One run per session.** Every `baton` terminal runs its own session's plan, in the same project or not.
  A run stays with its conversation through `--resume`, and through `/clear` in the same terminal.
  `baton status` in a session names the other sessions working in the checkout; from a shell it lists
  every run. Worktrees are no longer needed to run two plans at once.
- **A shared checkout is guarded.** While another baton session is live in the same checkout, baton
  refuses git commands that take every change (`git add -A` or `.`, `commit -a`, `stash`, `reset --hard`,
  `checkout .` or `restore .`, `clean -f`), and `baton done` holds a phase only to the files this session's
  edit tools wrote.
- **The plan file is watched.** baton reads the plan from the file Claude Code wrote, and Claude Code
  writes every plan a session makes to the same file. At every stop baton compares it with the text it
  attached: an edit that keeps the phases still to run is followed; one that loses any pauses the run and
  says why.
- **`/baton stop`** ends the run; baton does nothing in that session until the next plan.
- **`/baton exit`** leaves baton: elevation in reverse. A running plan is paused, baton stops claude once
  the turn ends, and the shell resumes the same conversation as plain Claude Code.

### Changed
- **`/baton run`** is the new name of `/baton attach` (which still works). `/baton plan` hands a plain
  session to baton before planning, so baton is watching when you approve. `baton run` in a shell, the
  same as plain `baton`, is no longer listed in the help.
- **Where state lives.** `.baton/` holds a run per session: `runs/<session id>/` (plan, state, events,
  notes, drafts), with `sessions/` and `hosts/` saying which session and which terminal each belongs to. A
  `.baton/` from an earlier baton is moved into `runs/` the first time v0.3 starts there, once no older
  baton is driving it. A plan attached from a shell is taken by the next `baton` session started there.

### Fixed
- **A plan approval's dialog stayed on record until the turn ended.** Its result arrives with an empty
  input, so it never matched the request. baton thought a dialog was open for the rest of that turn.
- **A moved `BATON_HOME` now reaches hosted sessions.** baton drops its variables from the session it
  hosts, so baton's commands inside it read the default `~/.baton` (config and records) instead.

## [0.2.2] - 2026-10-05

### Fixed
- **A session ends when its terminal does.** Closing the tab (or killing the shell) that a hosted
  session ran in could leave baton and claude running with nobody able to see or reach them, still
  holding the project, so the next `baton` there ran as plain claude. baton stopped reading claude's
  output once the screen was gone, and claude then blocked on that output (found stuck in a
  `tcsetattr` for hours), so it never got to the SIGHUP baton forwarded. baton now keeps draining
  the output and ends claude itself: SIGHUP, then SIGTERM and SIGKILL 5 seconds apart. The event
  `terminal_gone` records why.

## [0.2.1] - 2026-10-05

### Changed
- **`baton --version` and `baton --help` say who made baton.** `--version` (`-v`) adds the copyright,
  license and homepage under its first line, which stays `baton <version>` on its own. `--help` (`-h`)
  lists both flags, says they shadow claude's own, and lines up long command names.

## [0.2.0] - 2026-10-05

A 17-phase run hard-blocked on a GPG signing timeout, with a verified phase's work staged but not
committed. `baton blocked` was the model's only way to tell the human anything, and it halts the run, so
every "you should know" became a stop. Somebody happened to be watching. Unwatched, the run would have sat
there, its work uncommitted, until somebody looked. Replayed against v0.1.1 with nobody at the terminal,
Sonnet hard-blocked 4 times out of 4.

A run no longer stops for something it can work around. When something goes wrong, the model notes what
it did, proposes a way forward that goes ahead unless you object in time, or blocks, saying what it
tried. Whenever the run does halt, baton saves any uncommitted work, and every decision made without you
is recorded with its undo. The same replay now finishes the plan. The design and the experiments behind it
are in [docs/design.md](docs/design.md).

### Security
- **baton could type into a dialog that wasn't its own.** v0.1.1 answered its context question (the 90%
  warning) by typing `2` into the last dialog it had recorded. Claude Code shows dialogs oldest first, and
  records them when they are requested, not when they are shown. So a background subagent's permission
  prompt could be on screen in front of that question, and in a Bash permission prompt `2` is "Yes, and
  always allow". An unattended run could have approved a command, and added an always-allow rule, that
  nobody chose. baton now tracks open dialogs as a queue. It types only while its own question is the only
  dialog open, and it types `3`, which in a permission prompt is "No". The context question's options are
  now `Checkpoint now · Pause baton · Keep going`. **Upgrade if you run v0.1.x unattended.**
- **Only the questions baton issued pass as baton's.** Mid-run, any question starting with `baton:` was let
  through and its answer acted on. Now a question counts as baton's only if it matches one baton issued,
  word for word: the same labels in the same order, single-select. Anything else is refused, and the model
  is shown the exact call.

### Added
- **Three ways for the model to reach you,** the lightest that fits ([README](README.md#when-something-goes-wrong)):
  - **`baton note "<what it did>" --undo "<how>"`.** It already took a reversible way around a problem.
    The run keeps going, and you're told.
  - **`baton propose "<action>" --because "<what went wrong>" --undo "<how>"`.** A question with a deadline:
    `Wait for me` · `Pause baton` · `Go ahead`. If nobody answers, baton picks Go ahead and the model does
    it. A key press restarts the clock. An outward-facing or irreversible action gets no deadline.
  - **`baton blocked "<why>" --tried "<what it tried>"`.** `--tried` is now required.

  Every refusal names the way forward, from one playbook. In particular: when a rule of the plan can't be
  followed right now, propose the closest reversible way around it.
- **Snapshots of uncommitted work.** Whenever the run halts with uncommitted work, the host saves it into
  `refs/baton/snapshots/<phase>-<time>`. A halt is a block, a dialog waiting on you, a usage-limit wait,
  a pause, or the session ending. Your working tree, index, `HEAD` and gpg are left alone. `baton status`
  lists the snapshots with the command to restore one.
- **A record of every decision made without you,** each with its undo. It is shown in:
  - `baton status`;
  - `baton done`, for that phase;
  - the plan-complete notice, as a count;
  - the brief after each compaction.

  The next time you type, the model is handed the ones you haven't seen, so a late "undo that" works, even
  after `/clear`.
- **A review stop.** After `max_auto_decisions` decisions made without you in one phase (default 5), the run
  stops for you to review them.
- **Config:**
  - `escalation_timeout`: how long a proposal waits for you, `5m` by default;
  - `max_auto_decisions`: the review cap, `5` by default.
- **Status line:**
  - `? proposal · goes ahead 15:33`;
  - `? proposal · waiting on you`;
  - `⚠ review due`.
- **Notices:** `note`, `proposal`, `proceeded`, `decision` and `review`. On ntfy, a note is low priority
  and the rest are high.
- **Events:**
  - `note`, `proposed`, `proposal_asked`, `proposal_resolved`;
  - `review_due`, `review_resolved`, `decisions_told`;
  - `snapshot`, `snapshot_failed`, `refused`;
  - `auto_answered`, which replaces `warning_timed_out`;
  - `attached` and `host_started` now record `git`.

### Changed
- **`blocked` and `done` refuse to leave new work uncommitted.** Files that are new or changed since the
  phase started must be committed, or kept with `--keep-dirty "<why>"`. Committing in a degraded way (such
  as unsigned) is fine, if the model says how to repair it. A gate the model can satisfy itself is a
  prompt, not a guarantee, so the snapshot exists too.
  - Work already there when the phase started only gets a warning.
  - `checkpoint` only warns.
  - `waiting` and `propose` are not gated.
- **The model can't clear its own block.** While a block, a held proposal or a review waits for you,
  `resume`, `done`, `checkpoint`, `waiting` and `blocked` are refused until you have taken part.
- **Projects without git work the same.** One check decides everything git-related: a `.git` at the
  project root, and `git` on `PATH`. Without git:
  - there is no commit gate, no snapshot, and no "no commit since the phase started" warning;
  - baton's wording says "complete" and "saved", and never asks the model to commit or create a repository.
- **The skill and baton's instructions to the model teach the three tiers.**
- **The context question's timer restarts on a key press.** A draft in the input box no longer holds the
  answer, since keys go to the dialog.

### Fixed
- **baton's own settings reach the session again.** v0.1.1 stripped every `BATON_*` variable from the
  hosted session, so that a nested session wouldn't inherit another plan's state. baton's own settings
  went with them, which broke two things:
  - the context valves measured against the model's whole window instead of the `--autocompact` cap, so
    the 90% question came after Claude Code had already compacted;
  - an elevated session never received its pending command.
- **`baton attach --suggest` offered table rows as phases.** In a plan with `## P0 — …` headings, a
  context table whose rows start with an id (`| S1 | … |`) was offered as phases S1–S4, ahead of P0.
  Only the strongest notation present counts now: headings, then bold bullets, then table rows.
- **A reworded context question went unanswered.** A model that asked it with a sentence missing got
  through in a turn the human had started, and the host never answered it. While one of baton's
  questions is waiting, a question starting with `baton:` must now be that question exactly.

### Unchanged, deliberately
- **An open permission prompt is never answered or typed through.**
- **Nothing outward-facing or irreversible goes ahead on a timer.**
- **A secret only you hold means a hard block.** So does anything baton can't classify.

### Upgrading
A running session keeps its version until you restart it: `/baton update`, then `baton --continue`. A
plan attached under v0.1.x carries on. For a phase that started before the upgrade, baton has no record
of the uncommitted work at its start, so the commit gate only warns there.

## [0.1.1] - 2026-10-05

A run could be parked indefinitely by a half-typed message. Found live: a plan sat with a compaction
queued for **35 minutes** while `compacting…` showed on the status line, and only a human noticing could
have cleared it. An unattended harness that has to be watched is not one, so every gate that could hold
forever now expires into action.

### Fixed
- **A draft no longer holds a run.** When an unsent draft is in the way of something baton must type, it
  is saved to `.baton/drafts/` and the input box is cleared. Two conditions keep it polite: nobody has
  touched a key for `HandsOff`, and the draft has been in the way for `DraftGrace` (20s) — so an ordinary
  message being composed and sent is never touched. Not more than once per `RescueBackoff` (2m), so a
  flag that stays up however often it is cleared cannot make baton the thing destroying your typing.
- **A lost bracketed-paste end marker latched the draft flag forever.** `ESC[200~` put the key tracker in
  paste mode and only a cleanly parsed `ESC[201~` took it out — and in paste mode Enter adds a line
  instead of clearing the box, so the flag could never go back down. This was the root cause of the
  incident above. Paste mode is now bounded by `PasteMax` (2s).
- **An X10 mouse report no longer swallows the sequence after it.** Its three payload bytes were skipped
  blind; ESC is never one of them, so skipping it ate the next escape's prefix — one way the paste end
  marker went missing.
- **A hung background subagent no longer blocks a compaction for good.** The gate escalated once and then
  waited on a human; it now carries on after `BackgroundMax`. A compaction does not cancel background
  work — it reports when it finishes.
- **The status line no longer claims to be compacting while it is held.** It reads
  `compact held: <reason>`, because "compacting…" for 35 minutes is indistinguishable from a hang.
- **A nested session no longer inherits `BATON_*`.** A second `claude` started inside a hosted session
  picked up `BATON_DIR` and `BATON_INSTANCE`, so its hooks would write another plan's state. They are
  stripped unless baton is wiring that session itself. (It also made `go test` fail inside a baton
  session, which is how it was found.)
- **The PreToolUse block lets the model read while an escalation is live.** It refused every tool, so in
  the one incident where a human needed help, the agent could not read `.baton/events.jsonl` to diagnose
  it. `Read`, `Grep`, `Glob` and `NotebookRead` are allowed; Bash is deliberately not.

### Added
- **`baton drafts`** (and `/baton drafts`): list what baton took out of the input box, newest first.
  `--last` or `N` prints one raw, `--path` prints where it lives. Each draft is a file holding exactly
  the text and nothing else, so `cat` recovers it even if baton will not start.
- `Run.Gate` in `.baton/state.json`: why baton may not type right now, in its own words.

### Unchanged, deliberately
- **An open permission prompt still waits, with no ceiling.** Keystrokes landing in a prompt select an
  option, so overriding that gate could approve a tool call nobody approved. Every other gate expires
  into action; this one is the human's consent and may not. It is escalated and pushed instead.

## [0.1.0] - 2026-10-05

The first release: everything in the two release candidates below, plus:

### Added
- **`baton update`** (and `/baton update`). It checks for a newer release, updates the plugin through Claude Code, then downloads and verifies the matching binary, so the `baton` in your shell never lags behind the plugin. `--check` only reports.

### Changed
- **`/baton setup` runs the check itself.** It runs `baton setup`, which downloads the binary on first use, and explains each item left to do. Before, it only described the steps.

## [0.1.0-rc.2] - 2026-10-05

The second release candidate: the context measured in tokens (with an 810k cap and a context warning), separate state for each worktree, and a reliability pass so that nothing baton waits on can hold an unattended run forever.

### Added
- **Context warning.** At 90% of the context limit (never under 200k tokens), baton has the model ask you, with a fixed `AskUserQuestion`, whether to checkpoint now or keep going. If the question goes unanswered, the watchdog sends a push, and after 20 minutes baton answers "Keep going" itself. Set `warn_pct` to `0` to turn it off.

### Changed
- **The context question times out.** If nobody answers the 90% question within 20 minutes, baton answers "Keep going" itself, so an unattended run never stops on a warning. Claude Code still compacts on its own when the context is full.
- **No questions mid-run.** While a plan runs, the model's own `AskUserQuestion` calls are refused, because nobody may be there to answer and the run would wait. The model decides and notes its assumption, or runs `baton blocked`, which notifies you. baton's own questions, and turns the human started, are not affected.
- **Answers act directly.** The human's answer to baton's own questions resumes, pauses or checkpoints the run (and "Checkpoint now" makes the next stop a checkpoint), without depending on the model to run a command.
- **A stop needs a status even while background work runs.** A dev server or a log watcher runs forever, so waiting on one takes `baton waiting … --until`. Waits are capped at 2 hours, and reminders that get answered without the plan moving escalate after two.
- **baton's own CLI is allowed by its `PreToolUse` hook**, so no permission prompt or classifier decision can park a run on it. A command whose notes the shell would rewrite (backticks or `$` inside double quotes) is refused, with the fix.
- **The checkpoint nudge repeats** for every further tenth of the limit.
- **A hook that fails open is recorded** in `events.jsonl` (`hook_failed`).
- **`autocompact` defaults to 810k** (was 400k). With the defaults on a 1M-token model, baton asks for a checkpoint at 486k, asks you at 729k, and Claude Code compacts on its own at about 777k.
- **The context is measured in tokens.** baton records the exact size from the status line's `current_usage` and works out the limit itself (`min(model window, cap)`), because Claude Code reports the model's window, not the cap. The status segment shows `ctx 412k/810k`; `baton status` and `/baton setup` show where each threshold falls.
- **An `--autocompact` passed to `baton` wins** over the config, and baton validates its own setting before launching `claude`, which would otherwise refuse to start.

### Fixed
- **Nothing waits forever.** A review of every place baton waits found several silent stalls ([docs/research/reliability.md](docs/research/reliability.md)):
  - **Auto-compaction's background precompute** fires `PreCompact(auto)` long before any compaction. While a phase boundary was owed, baton mistook it for the compaction and waited 10 minutes for one that never came. Only a real compaction (`SessionStart(compact)`) counts now.
  - **A gate that never opens is handled.**
    - A status line refreshing every second kept the screen from ever settling, so baton could never type. The quiet gate now expires after 30s.
    - An interrupted turn (no `Stop`), a subagent that died in an API error (no `SubagentStop`), or a hook that failed could leave a flag stuck for good. Claude Code's `idle_prompt` notification and each `Stop` now correct them, and an open turn with no sign of life for 15 minutes stops gating.
    - Gates that depend on someone else (a draft, background subagents) are reported with their reason.
  - **Escalations clear themselves** when their cause is gone (before, a stale one silenced the watchdog for the rest of the run). Unresolved ones are pushed again after 30 minutes, 2 hours and 6 hours. A failed push is retried.
  - **A failed compaction is retried** with backoff, and a late finish is accepted.
  - **API errors are classified.**
    - Login, billing and similar errors are reported at once instead of being retried every 16 minutes all night.
    - Usage limits are retried every 15-30 minutes, so the run resumes soon after the reset (the backoff used to reach 4 hours).
    - Persistent overloads are reported.
  - **Laptop sleep.**
    - Sleep is now detected (Go's monotonic clock stops during sleep on macOS).
    - Deadlines restart on wake.
    - The owner's hooks no longer go dormant before the first heartbeat after waking.
  - **The heartbeat has its own goroutine**, so a slow notification can't let ownership lapse. A panic in the controller is logged instead of ending the session.
  - **Draft tracking follows the input box.** Ctrl-C, backspace and single-line Ctrl-U clear it. Mouse reports, arrows and mode keys no longer create one.
- **The checkpoint nudge never fired on 1M-token models.** It compared `checkpoint_pct` with Claude Code's percentage of the model's window (60% of 1M is 600k), but the old 400k cap made Claude Code compact at about 367k. The thresholds are now fractions of the limit.
- **Worktrees get their own state.** The search for `.baton/` stops at the first repository or worktree root, so a worktree nested inside its main checkout (such as `.claude/worktrees/<name>`) no longer picks up the main checkout's plan. baton's own `~/.baton` is never used as a project's state.

## [0.1.0-rc.1] - 2026-10-04

The first release candidate: everything in the v0.1 plan except Windows.

### Added
- **Host.** `baton` hosts `claude` in a pseudo-terminal it owns: a byte-for-byte passthrough, so the live Claude Code interface stays exactly as it is.
- **Phase boundaries without a human.** At each boundary baton queues a compaction, types `/compact` once it is safe (no open turn, dialog or subagent; no human draft; quiet screen), and confirms it started. It then injects a brief for the next phase, quoted from the plan, and wakes the model.
- **A Stop decision table.** A stop without a status is refused with instructions. Blocked runs and repeated silent stops escalate. While a compaction is owed, the model is held from starting more work.
- **Escalation in two layers.** A fixed question in the session (AskUserQuestion, which reaches every device), plus content-free desktop and ntfy notifications sent by the host itself.
- **Watchdog.** Covers idle sessions, expired waits, long background work, unanswered dialogs, usage limits and overloads (with backoff), sleep and wake, and sessions that end mid-plan.
- **Context valve.** The status line records the measured context fill. Past a threshold, the model is asked to checkpoint at its next safe point, and a launch-time `--autocompact` cap is the hard backstop.
- **Status line.** A permanent `◆ baton` segment in front of the user's own status line.
- **The `/baton` skill.** `plan` (plan mode, then attach and start), `attach`, `status`, `pause`, `resume`, `elevate` and `setup`.
- **Elevation.** A plain `claude` session hands itself to baton (same conversation) through an opt-in shell init line.
- **Distribution.** The repo is its own plugin marketplace. A launcher downloads the release binary on first use and verifies its sha256. Releases are reproducible: CI rebuilds every binary and refuses to publish on any mismatch.
- **Research.** Spikes and verification runs against Claude Code 2.1.289 (`docs/research/`). An end-to-end suite runs real `claude` sessions (`make e2e`).

[Unreleased]: https://github.com/ozzyfromspace/baton/compare/v0.8.1...HEAD
[0.8.1]: https://github.com/ozzyfromspace/baton/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/ozzyfromspace/baton/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/ozzyfromspace/baton/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/ozzyfromspace/baton/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/ozzyfromspace/baton/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/ozzyfromspace/baton/compare/v0.3.7...v0.4.0
[0.3.7]: https://github.com/ozzyfromspace/baton/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/ozzyfromspace/baton/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/ozzyfromspace/baton/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/ozzyfromspace/baton/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/ozzyfromspace/baton/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/ozzyfromspace/baton/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/ozzyfromspace/baton/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/ozzyfromspace/baton/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/ozzyfromspace/baton/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/ozzyfromspace/baton/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/ozzyfromspace/baton/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/ozzyfromspace/baton/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/ozzyfromspace/baton/compare/v0.1.0-rc.2...v0.1.0
[0.1.0-rc.2]: https://github.com/ozzyfromspace/baton/compare/v0.1.0-rc.1...v0.1.0-rc.2
[0.1.0-rc.1]: https://github.com/ozzyfromspace/baton/releases/tag/v0.1.0-rc.1
