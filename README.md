# baton

**Run a long, multi-phase Claude Code plan unattended. baton compacts the context at every phase boundary, never lets the run sit idle on a gate, and works on phases that don't need each other at the same time, from one live terminal session you can watch.**

You plan the work in plan mode, approve it, and walk away. baton picks up the plan the moment you approve it, and keeps the session moving: at each phase boundary it compacts the context, hands the model a brief for the next phase, and wakes it up. A long test run goes to baton, which runs it on a commit of its own while the model works on; if the wait is long and another phase can go ahead, baton switches the session to it and comes back when the result is in. A phase that doesn't need the one in hand goes to a hidden worker session in a git worktree of its own, and baton lands its work on your branch when it's done. If something needs you, it tells you on whatever device you're on.

> Status: **v0.7.0**. macOS and Linux. Windows is planned.

## Why

Long plans run best when the context is compacted at each phase boundary: fewer tokens, sharper output, and every phase starts from a clean set of facts. But in Claude Code only a human can type `/compact`, so someone has to sit by the terminal. Asking the model to compact itself fails in practice:

- it forgets;
- it mistakes the session token budget for its context window;
- it says "compacting" without doing it;
- after a compaction it can sit idle for hours before anyone notices.

baton takes every one of those decisions away from the model. The model does the work; deterministic code decides when to compact, when to resume, and when to call you.

## How it works

- **Hosting.** `baton` starts `claude` inside a pseudo-terminal it owns and passes every byte through. You see and use the normal Claude Code interface.
- **One run per session.** Each Claude Code session has a run of its own in `.baton/runs/` at the root of your project, which is kept out of git. So every `baton` terminal can run a plan, in the same project or not ([Several sessions](#several-sessions-in-one-project)). A run stays with its conversation through `--resume`, and through a `/clear` in the same terminal. The model reports progress with `baton done` or `checkpoint`, hands long gates to baton with `verify` or `await`, and reaches you with `note`, `propose` or `blocked`.
- **Plan approval.** When you approve a plan with phase headings (`## P0 — …`), baton attaches it, compacts the planning conversation away, and starts P0 from a brief. `/baton run` does the same whenever the conversation had turns before it, so a plan never starts in the middle of whatever came before. If a run is already under way, it asks you whether to continue with the revised plan or start the new one. baton reads the plan from the file Claude Code wrote, and checks it at every stop: an edit that keeps the phases still to run is followed, a phase you add (a heading like the plan's own, `## P3 — …`) joins the run, and an edit that loses a phase still to run pauses the run. `/baton run` picks up phases added to a plan that has finished.
- **Phase boundaries.** When a phase is done, baton's hooks (not the model) decide to compact. baton waits until no turn, dialog, subagent or human draft is in the way, types `/compact` itself, and confirms it ran. Then it injects a brief for the next phase, quoted from your plan, and wakes the model.
- **Which phase needs which.** A plan can say what each phase waits for (`After: P1, P3`). Without it, each phase follows the one before, as in any plan. Your session works on one phase at a time, and picks the next one that can start ([Never waiting on a gate](#never-waiting-on-a-gate)).
- **Workers.** Another phase that can start goes to a hidden worker session, in a git worktree of its own outside your checkout. baton starts as many as the plan has phases ready for, the machine has room for, and the lanes keep up with; you don't pick a number. When it's done, baton lands its work on your branch with a fast-forward merge, between your session's tool calls, and tells your session what moved. A worker's questions are put to you in your terminal, by your session; `baton view P3`, in another terminal, shows a worker live ([Working on phases at once](#working-on-phases-at-once)).
- **Gates off the critical path.** The model hands a gate that takes more than a couple of minutes to baton (`baton verify`), which runs it on the committed code in a lane outside your checkout while the model keeps working. A phase that is finished is reported done while its gate runs, and is marked done when the gate passes, or reopened if it fails. A phase that can't go on without the result is set aside for another phase that can start, and comes back when the result is in.
- **Long commands.** A command that runs for more than 2 minutes moves to the background by itself and wakes the session when it ends.
- **Silent stops.** A stop without a status is refused with instructions. The model also can't start the next phase until the compaction has happened.
- **Requests mid-turn.** When baton needs something of the model in the middle of a turn (a checkpoint, a job's result, a question to put to you, news of a landing), it answers the model's next tool call with hook feedback, which the model acts on as an instruction. Claude Code shows it as "PostToolUse hook returned blocking error" followed by baton's text: nothing failed, and the tool call ran.
- **Context valves.** baton reads the exact context size from Claude Code's status line. When a long phase reaches 60% of the limit, it asks the model to checkpoint at its next safe point. At 90% it asks you, with a question in the session, whether to checkpoint now or keep going. If nobody answers within 20 minutes, it keeps going. Claude Code's own auto-compaction remains the backstop.
- **No questions mid-run.** While a plan runs, the model may not stop to ask you questions of its own, because nobody may be there. It takes a reversible way forward and tells you, proposes one with a deadline, or reports itself blocked ([When something goes wrong](#when-something-goes-wrong)). Questions in turns you started yourself still get through.
- **Watchdog.** It catches a session that has gone quiet: idle turns, background work that has run for an hour, unanswered prompts, usage limits, API errors. **Everything baton waits for before it acts either clears by itself or expires into action**, so nothing can hold a run — a half-typed message is saved and taken out of the box, a hung background subagent is overridden, an open turn and a never-settling screen stop being trusted after their ceilings. Whatever still needs you is pushed again until it's resolved.
- **One exception, and it is deliberate: an open permission prompt.** Keystrokes landing in a prompt *select* an option, so a baton that typed through one could approve a tool call you never approved. That wait is escalated and pushed; it is never overridden.
- **Drafts.** If an unsent draft is in the way of a compaction, baton saves it and clears the box rather than waiting. `baton drafts` hands it back. Each one is a file holding exactly the text, so `cat .baton/runs/*/drafts/*.txt` works even if baton will not start.
- **Escalation.** When baton needs you, it asks in the session (a question that reaches every device signed in to Claude) and sends its own push notification. Whenever the run halts, baton first saves any uncommitted work.
- **Recovery.** A turn the model's safeguards refuse is never retried: baton tells you which phase stopped, how you can go on, and what goes on meanwhile. A claude that exits by itself mid-plan is resumed by baton, in the same conversation. A rewind (Esc Esc) or a `/clear` mid-phase never leaves baton and the model remembering different things ([When the session itself stops](#when-the-session-itself-stops)).
- **Status line.** A permanent `◆ baton · P6 6/23 <title> · W P3 14m · P4 verifying 12m · ctx 412k/810k` segment sits in front of your own status line. It shows each worker, the oldest job the run waits on (`+2` when there are more), the context size against the limit, and `? proposal · goes ahead 15:33` while a proposal waits on you.

The design and the experiments behind it are in [`docs/plan.md`](docs/plan.md) and [`docs/research/`](docs/research).

## Install

You need Claude Code 2.1.289 or newer, on macOS or Linux.

```sh
claude plugin marketplace add ozzyfromspace/baton
claude plugin install baton@baton
```

Then, in any Claude Code session, run `/baton setup`. On first use the plugin downloads the `baton` binary for your machine from this repository's GitHub release into `~/.baton/bin`, and verifies its sha256 before running it. `/baton setup` then shows what's left to do. For most people that's one line in `~/.zshrc` (or `~/.bashrc`):

```sh
eval "$(~/.baton/bin/baton init zsh)"
```

That line puts `baton` on your PATH. It also lets a plain `claude` session hand itself to baton (see [Starting baton in a running session](#starting-baton-in-a-running-session)).

To update later, run `baton update` (or `/baton update` in a session). It updates the plugin through Claude Code, then downloads and verifies the matching binary. Sessions baton hosts then move to the new version by themselves: each one restarts on it in place, with the same conversation, once it has been idle for two minutes, or at its next phase boundary while a plan runs. Nothing that would end with claude may be running at that moment (background work, a monitor, a scheduled wakeup), and a session never restarts with a draft in the input box. This applies to patch and minor releases (before 1.0, patch releases only). After a major update, a session tells you once and stays on its version until you restart it: exit and run `baton --continue`. Until a hosted session restarts, every `baton` command in it, and its `/baton`, run the version that hosts it. A plain `claude` session uses the new version the next time you run `/baton` there (with the `baton init` line in your shell; without it, once the session restarts).

## Use

```sh
baton              # instead of `claude`; it takes the same arguments
baton --version    # or -v: which baton this is, which one hosts the session, which is installed (claude --version for Claude Code's)
baton --help       # or -h: every command
baton view P3      # in another terminal: watch P3's worker and type into it (Ctrl-] leaves)
baton clean        # end what no live session uses: workers, jobs, lanes, worktrees, leftover files
```

Inside the session:

| Command | What it does |
|---|---|
| `/baton plan <goal>` | Plan the work in plan mode, in a format baton can run. When you approve the plan, baton runs it. |
| `/baton run [plan.md]` | Run a plan. This session's own plan goes on where it stopped: after a pause or a block, or with phases you added to its file. Any other plan is attached and started (defaults to the plan you just approved), but never over a run underway unless you agree, and a finished plan is not run again. If the conversation had turns before it, baton compacts them away before P0. |
| `/baton start` | Hand this session to baton, same conversation (see below). `/baton plan` and `/baton run` do it when they need to. |
| `/baton status` | Show where the run stands. |
| `/baton pause` | Take the wheel; `/baton run` gives it back. While paused, baton observes but never acts. If the conversation grew by more than 20k tokens meanwhile, the model records where the phase stands and baton compacts before it goes on. |
| `/baton drop` | Drop the run. Its plan is set aside, and baton does nothing in this session until the next plan. |
| `/baton view` | List the run's [workers](#working-on-phases-at-once); watching one takes another terminal (`baton view P3`). |
| `/baton clean` | End what no live session uses: the workers and jobs of runs nobody will come back to, worktrees and lanes nobody holds, leftover files ([Cleaning up](#cleaning-up-baton-clean)). `--dry-run` only says what it would end. |
| `/baton exit` | Leave baton: this conversation goes on as plain `claude` in the same terminal. |
| `/baton drafts` | List drafts baton saved out of the input box; `--last` prints the newest. |
| `/baton version` | Which baton this session runs, the one installed, and the latest release. |
| `/baton setup` | Check this machine and list what's missing. |
| `/baton update` | Update baton to the latest release (also `baton update` in a shell; `--check` only looks). |

You don't need a command to run a plan you approve in plan mode: baton attaches any approved plan whose phases are headings, such as `## P0 — Title`, `### P23: Title` or `## Phase C: Title`, when the session has no plan under way. `/baton plan` writes plans in that format. `/baton run` also takes phases written as bold bullets or table rows.

The `/baton` skill is a stub: its instructions come from the `baton` binary each time you use it, so they always match the baton that carries them out, and a session never needs a restart just to pick up new instructions. A copy loaded earlier stays in the conversation, though; so when a session restarts on a newer baton while idle, and the conversation still holds an older copy, the next prompt hands Claude the current instructions and says the earlier copy is out of date.

### Starting baton in a running session

Started a session with plain `claude` and want baton to drive it? Run `/baton start`, or just `/baton plan …` or `/baton run …`, there. baton records the session, stops `claude` cleanly when the turn ends, and your shell relaunches **the same conversation** under baton. `/baton plan` does this before planning, so baton is watching when you approve the plan. `/baton exit` does the reverse. Both need the `baton init` line above. Without it, baton tells you what to run by hand: `baton --resume <session id>` to hand a session to baton, `claude --resume <session id>` to leave it.

### Several sessions in one project

Each `baton` terminal runs its own session's plan, so you can run two plans in one project at once, or keep one terminal for a plan and another for everything else. `baton status` in a session shows its own run and names the other sessions working in the checkout. From a plain shell it lists every run. A run's [workers](#working-on-phases-at-once) are part of its run, not sessions of their own here: each works in its own worktree.

Two sessions in one checkout share its files and its git index, so while another baton session is live there:

- baton refuses git commands that take every change (`git add -A` or `.`, `commit -a`, `stash`, `reset --hard`, `checkout .` or `restore .`, `clean -f`), and the model stages its own files by path;
- `baton done` only holds a phase to the files this session's edit tools wrote, so the model is never asked to commit the other session's work.

For full isolation (separate builds, test runs and branches), give each plan its own git worktree, which gets its own `.baton/`.

### Without git

baton doesn't need a git repository. In a plain folder, or on a machine without git, a plan runs the same way; baton just has nothing to commit or snapshot. Its wording says "complete" and "saved" instead of "committed", and it never asks the model to commit or to create a repository. If you start a repository mid-run, baton uses it from then on.

## Never waiting on a gate

In long runs, most of the time went on waiting: for test suites, builds and CI runs, then reading the failures, fixing, and running them again. The model sat idle while they ran, even when another phase could have gone ahead. baton now keeps that wait off the critical path. The measurements behind it are in [`docs/research/parallel.md`](docs/research/parallel.md).

### Which phases wait for which

The first line under a phase's heading may say what the phase needs done first:

```markdown
## P3 — Billing API

After: P1

…
```

- `After: P1, P3` names earlier phases. Only earlier phases may be named, so a plan can never wait on itself.
- `After: none` means the phase needs nothing before it.
- `After: all` means every phase before it, for integration, verify-everything and release phases.
- **Without the line, a phase follows the one before it.** A plan with no `After:` lines runs exactly as it always did.

Mark a phase independent only when it needs nothing another phase produces: no code, schema, file, result or decision of it. `/baton plan` writes the lines for you. When baton attaches a plan, it prints each phase's dependencies and the longest chain of phases that must run one after another. The lines are read from the plan file at every stop, so you can change them mid-run; a changed line only decides when a phase may start.

Your session works on one phase at a time. The graph tells it which phase can go ahead while another waits on a gate: a phase set aside whose result is in comes first, then the first phase in document order whose dependencies are done. The graph also tells baton which phases a [worker](#working-on-phases-at-once) can take beside it. A phase never starts while one it needs is still verifying, has failed its verification, or is a worker's that hasn't landed yet.

### Long gates go to baton

A gate that takes more than about 2 minutes, or needs state of its own (a database, a server, ports), goes to baton. Quick checks still run in place.

| The model runs | What baton does |
|---|---|
| `baton verify "<command>" --notes "<where the phase stands>"` | Runs the command on `HEAD` in a lane: a worktree of its own outside your checkout, so nothing the model edits meanwhile can disturb it. Needs git, and the phase's work committed. |
| `baton verify --here "<command>" --notes "…"` | Runs the command on `HEAD` in the session's own working tree, with no lane, for a gate that needs your checkout itself: its running services, or files that aren't in git. Until it ends, baton refuses Claude's edits there, starts no other phase there, and lands no worker's work in your checkout, since the gate reads those files as it runs. |
| `baton await "<command>" --notes "…"` | Waits on something outside the project the same way (a CI run: `gh run watch --exit-status`), from the working tree. No lane, and no git needed. |

A gate in a lane never reaches back into your checkout: baton refuses a `verify` whose command names the checkout's path (a `cd` into it, say), since it would check your files as they are rather than the commit, while landings and edits change them under it. The refusal offers `--here`, or a [`.baton-lane`](#lanes) for gates that need services of their own.

The command runs detached from the session, so it survives baton restarting itself. The notes are what the phase resumes from if baton sets it aside. A phase has one job at a time: a new `verify` replaces one still running. The result reaches the model with its next tool call, or at its next stop; if the session is idle, baton wakes it with a short `[baton] job j4 (P3) finished` line. A result quotes the end of the log, and names any other phase whose verification is red, so the model leaves failures in that phase's area alone.

**Done while the gate runs.** When the phase is finished and committed, the model runs `baton done` without waiting for its gate. The phase is then *verifying* and the run moves on:

- the gate passes: baton marks the phase done, and the phases that need it can start;
- the gate fails, or times out: the phase reopens with the result, ahead of any phase not started, and resumes from its notes;
- `baton done` is refused while the phase's latest verification is red. If the failure isn't the phase's to fix, `baton done <phase> --despite-red "<why>"` finishes it anyway, as a decision made without you ([When something goes wrong](#when-something-goes-wrong)).

**Switching.** Sometimes the phase can't go on without the result: the model has committed, handed the gate over and ended its turn. Once the gate has run for `switch_after` (5 minutes; at once for a command whose earlier runs took that long), and another phase can start, baton sets the phase aside with its notes, compacts, and starts that phase from a fresh brief. When the gate ends, the phase set aside comes back at the next phase boundary or `baton checkpoint`, with the result in its brief. If the gate ends before the switch's compaction is typed, the switch is called off and the result is given in place. baton never sets a phase aside with its work uncommitted. In practice, a phase that is finished is reported done while its gate runs, so a switch happens only when the phase truly waits on the result.

**When nothing can go ahead,** the session idles until a result is in, or until a worker's phase it needs has landed. Claude Code's own idle notice then reads as if you were needed, so baton's line at that stop says you aren't (*"baton: P3 waits on job j3 (`make e2e`) — nothing is needed from you: Claude is given its result when it ends"*), and the status line shows `P3 waits on its gate 12m`. A block in your session still stops the whole run, since its question is on screen; jobs and the workers already at work keep going meanwhile (though no worker starts and nothing lands), and the run picks up once you answer. A worker's block stops only its phase.

A job that runs past `verify_timeout` (3 hours) is stopped and counts as failed. `baton status` lists the jobs with their logs (`.baton/runs/<session>/jobs/`), and the lanes. `/baton drop` stops the run's jobs and removes their logs; `/baton exit` and the session ending stop them too.

### Lanes

A lane is a detached git worktree of your project under `~/.baton/lanes/`, outside your checkout, so file watchers, hot reload and `git clean` never reach it. Each job checks out its commit in a free lane. A lane is reused from one job to the next, so ignored caches such as `node_modules` stay.

Without a lane script, a gate runs with `sh -c` in the bare worktree, one at a time; a gate that needs your checkout's running services then runs there, with `verify --here`. Most projects need dependencies installed or services started first, so they commit an executable `.baton-lane` at the root:

```sh
#!/bin/sh
# .baton-lane: how baton runs this project's gates in a lane.
set -e
case $1 in
up)    cd "$2" && npm ci ;;                # once per lane: dependencies, services
run)   cd "$2" && exec sh -c "$3" ;;       # each gate; its exit code is the gate's
down)  ;;                                  # stop whatever up started
slots) echo 2 ;;                           # how many lanes may run at once (1 to 4)
esac
```

- **`up <dir>`** runs once per lane before its first gate, and **`down <dir>`** when the session ends, the run is dropped, or the plan is complete.
- **`run <dir> <command>`** runs each gate, from the job's commit. An `up` that fails twice, or a `run` that exits 125, 126 or 127, is a *lane error*: the gate never ran on its own terms, so it is not a test result. The model then runs the gate in place and tells you with `baton note`.
- **The environment** has `BATON_LANE` (the lane's number, for per-lane ports and names), `BATON_LANE_DIR`, `BATON_ROOT` (your checkout) and `BATON_COMMIT`. Every other `GIT_*`, `BATON_*` and `CLAUDE_*` variable is removed.
- **Several runs in one checkout** share its lanes.

baton never writes a `.baton-lane` into your project.

### Long commands

baton starts Claude Code with `CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS` set to `foreground_max` (2 minutes), unless you set it yourself. A command that runs longer moves to the background, keeps running, and wakes the session when it ends. The first time the model stops on such work, baton refuses the stop once, to remind it to keep working on what doesn't depend on it, and to send a long or stateful gate to `baton verify`, since its own edits would invalidate a gate running in the checkout. After that, the session may idle until the work wakes it. If the work runs for an hour, the watchdog looks in. Monitors are not counted as work.

## Working on phases at once

The session in your terminal, the **lead**, works on the plan in your checkout as before. Beside it, baton starts a hidden **worker** session for another phase that can go ahead, in a git worktree of its own. When the worker's phase is done, baton lands its work on your branch, underneath the lead, and the phases that need it can start. You keep watching the lead; a worker's questions come to you there, and `baton view` shows a worker live in another terminal. In the measurements behind this, a second session working at once took one long campaign from 58 hours to 40, and a third added about 1% ([`docs/research/parallel.md`](docs/research/parallel.md)). The experiments behind the design are in [`docs/research/workers.md`](docs/research/workers.md).

### When a worker starts

You don't size the fleet. The plan says how wide it can go, baton reads how much room the machine has, and a worker starts whenever a phase can go ahead and nothing below holds it back. A worker takes the first phase in document order that can start, other than the one the lead has or takes next, when:

- the run is running: not paused, blocked, or waiting on a review of its decisions;
- **a slot is free.** With `workers: auto`, the default, the most workers at once is one for each 2 CPUs or 4 GiB of memory, whichever allows fewer, less one for the lead, and never more than 8: 6 on a 14-CPU Mac with 48 GiB, 1 on an 8-core laptop with 8 GB, none on a machine with fewer than 4 CPUs or 8 GiB (baton then runs without workers, and says so when a plan is attached). In a container, the CPUs and memory it may use count. A number from 1 to 8 sets the most yourself, whatever the machine; `0` turns workers off, and baton then runs exactly as v0.4 did;
- **the machine has room:** its load is under its number of CPUs; on macOS, memory pressure is below critical, and while it is at warn, swap grew by no more than 256 MiB in the last minute; on Linux, at least 15% of memory is available (within a container's limit);
- **a minute has passed** since the last worker started (with `auto`), so each one's load shows before the next starts;
- **the lanes keep up:** no gate of the run or its workers has waited more than a minute for a free [lane](#lanes), since another worker's gates would only queue behind it;
- **landings aren't colliding:** fewer than 2 of the workers' landings in a row went back for a rebase conflict. A landing resets the count, and once no worker of the plan is left at work to land, a new one starts anyway;
- no session waits on a usage limit, and, if you set `worker_usage_max`, the account's five-hour usage is under it ([Usage limits](#usage-limits));
- no newer baton is waiting to restart the session.

These checks are made by baton, not the model, and only hold back new workers: a worker at work is never stopped. The machine's room also holds back resuming a worker. When a phase that could go ahead is held back, baton tells the lead once (*"baton: no worker on P3 for now: the machine is busy (load 11.2 on 8 CPUs). One starts once there is room; the workers at work carry on"*), and the status line and `baton status` say why until a worker starts.

The model's part is the plan. A plan with no `After:` lines is a chain, so no worker ever starts on it; `/baton plan` marks a phase `After: none` when it needs nothing from the others, and splits the work so that phases running at once don't touch the same files. When a plan is attached, baton says how many workers will work beside the lead: *"up to 3 workers work beside this session on the phases that can go ahead, … (the plan's widest step is 4 phases; this machine allows 6)"*.

A worker starts with the flags you gave `baton` (the same model and permission mode), baton's hooks, and a prompt to begin its phase. Its brief holds the phase's section of the plan, the standing rules, and the notes of the phases it depends on. Inside its phase it does everything the lead does: `verify` and `await`, `done` while a gate runs, notes and proposals, checkpoints and compactions. It works only in its worktree: an edit in your checkout is refused, and so are the commands that belong to the lead (`attach`, `run`, `pause`, `drop`, `exit`, and plan mode).

If a worker's session exits unexpectedly, baton resumes it in its worktree, at most twice; then its phase goes back to the queue, and its commits stay on its branch. If a worker can't start (a trust dialog, or two failed starts in a row), baton turns workers off for this session and tells you.

### Landing

When a worker's phase is done and its last verification passed, the phase joins the merge queue: first finished, first landed. The worker waits, idle, while baton:

1. **rebases** the worker's branch onto your branch, in the worker's worktree. On a conflict, the phase goes back to the worker, which rebases and resolves it itself, then reports done again;
2. **verifies it again** in a lane, with the phase's last verification command, if your branch has commits the commit it verified lacks. Red reopens the phase in the worker. Another phase's landing in between always means verifying again, but commits made in your checkout (the lead's, or yours) mean it at most once: if more come in while that second gate runs, baton lands over them, names them, and tells the lead its next gate runs them together. So a lead that commits more often than the gate takes never holds a landing back;
3. **waits for your changes:** if the branch changes a file that is uncommitted, staged or untracked in your checkout (ignored files included), landing waits until the lead has committed it. It also waits while your checkout is on another branch than the one the plan was attached on, and while a gate of the lead's runs in your checkout (`verify --here`);
4. **lands it** with `git merge --ff-only` in your checkout, while no tool call of the lead's is running (the lead's next call waits for it, 10 seconds at most). A fast-forward changes only the files the branch changed, and leaves the lead's uncommitted work alone, or does nothing at all;
5. **tells the lead** what landed (the phase, its commits and the files they changed), that `HEAD` moved, to read those files again before editing them, and never to amend, reset, rebase or revert commits it didn't make. baton refuses the lead's `git commit --amend`, `reset` and `rebase` over landed commits;
6. **ends the worker**, removes its worktree and branch, and marks the phase done.

baton never moves your branch any other way: no `update-ref`, no `branch -f`, no `--autostash`.

### Questions from workers

Nobody watches a worker, so its questions come to you through the lead. A worker's `propose`, `blocked`, or a review of its decisions that is due goes into a queue, and the worker is told that baton is asking you: it keeps working on what doesn't depend on the answer, or ends its turn. The lead puts the questions to you one at a time, and only while no question of its own is open: *"baton: worker P3: gpg signing timed out. Unless you answer by 15:33, I will commit P3 unsigned."* baton gives your answer to the worker.

| The worker | Your answers | If nobody answers in time |
|---|---|---|
| proposes something, with a deadline | Wait for me, Pause baton, Go ahead | Go ahead, at the time the question names. |
| proposes something outward-facing or irreversible | Go ahead, Pause baton, Wait for me | Wait for me, after `escalation_timeout`. |
| is blocked, or is due a review of its decisions | Continue, Pause baton, Wait for me | Wait for me, after `escalation_timeout`. |

- **Go ahead** and **Continue** let the worker go on. An answer in your own words reaches it as your instruction.
- **Wait for me** takes the question off the lead's screen and leaves it in the worker's own session: open it with `baton view P3`, and what you type there answers it. Only that phase waits; the lead never waits on a worker's question.
- **Pause baton** pauses the whole run.

The lead is given the question with its next tool call, when it stops, or, if it is idle, with a short `[baton] worker P3 has a question for you` line that baton types. A worker's block stops only its phase; the lead and any other worker carry on.

A **permission prompt**, or any other dialog, in a worker's session is never answered by baton and never relayed. The lead tells you about it, baton pushes, the status line shows `! P3 needs you`, and you answer it with `baton view P3`. Only that phase waits.

### Watching a worker: `baton view`

```sh
baton view        # list the workers
baton view P3     # show P3's worker and type into it; Ctrl-] leaves
```

Run it in another terminal, in the project; inside a session it only lists them, since it would take over the terminal. The viewer repaints the worker's whole screen at your terminal's size, and your keys go to the worker as if you typed in its session: baton counts them as yours, so it never types over you. `Ctrl-]` leaves; the worker carries on, at its own size again. One viewer at a time per worker. The lead's terminal never changes.

### Permission modes

Workers start in the permission mode the lead started in. In `auto` or `bypassPermissions` they run like the lead. In `default`, `acceptEdits` or `plan`, a permission prompt stops a worker until you answer it with `baton view`, and baton warns you about that when you attach a plan. To keep workers going, start baton with `--permission-mode auto`, or allow what the plan needs in `.claude/settings.json`. Workers don't see `.claude/settings.local.json`: a worktree holds only what is committed.

### Usage limits

Every session's status line reports the account's five-hour usage to baton (Claude Code reports it on Pro and Max plans). baton holds nothing back for it by default: a run that finishes sooner uses the five-hour allowance faster, rather than leave part of it unused when the window resets. To keep a reserve, set `worker_usage_max` (say `80`): no new worker starts while the usage is at or above it. When a session's turn ends on a usage limit, no worker starts and baton types nothing into any of the run's sessions until the limit resets (15 minutes when baton can't tell when); Claude Code resumes each session by itself.

### Pausing, dropping, leaving

- **`/baton pause`** stops baton starting workers and landing their work. Workers already at work carry on, and their work waits to land until `/baton run`.
- **`/baton drop`** saves each worker's uncommitted work into a snapshot ref, ends it, and removes its worktree. A worker's branch is kept if it holds commits that never landed, renamed to `baton/<run>/kept/<phase>-<YYYYMMDD-HHMM>` so that no later worker on a phase of the same name starts from it, and `baton status` names it.
- **A new plan over the run** (approving another plan and starting it, or `/baton run` with another plan) ends the workers of the plan it replaces the same way, kept branches under `kept/` included. Plans number their phases alike, so the new plan's P3 worker starts on a fresh branch.
- **`/baton exit`**, closing the terminal, or the lead session ending suspends the workers. The next baton session of the run (`baton --continue`) resumes each one in its worktree.
- **A run nobody comes back to** doesn't hold its workers forever. Once it has been quiet for 2 minutes with no baton of its own running, the next session that starts a plan in the project ends them, as `/baton drop` would, and says so: *"baton: ended 2 workers left by an earlier run (P3, P4); its branch baton/<run>/P3 keeps 1 commit that never landed"*. Their phases go back to that run, for whenever it is continued. `baton clean` does the same at once ([Cleaning up](#cleaning-up-baton-clean)).
- **Continuing a run with workers off** (`workers: 0`, or a machine with room for none) ends the run's suspended workers, and the lead takes their phases.
- **An update restart** waits until every worker is idle (its turn over, nothing of its own running, no dialog open), and the restarted baton resumes them.

### Seeing the workers

- **The status line** shows each worker after the lead's phase: `W P3 14m` while it works, `P3 landing` (`P3 waits to land` while landing waits for your changes), `? P3` while the lead has its question for you, and `! P3 needs you` while something in its session waits on you. After them, `W held: busy` (or `lanes`, `conflicts`) while a phase that could go ahead is held back. In front of them, `usage limit until 15:40` while a limit holds the run, and, if you set `worker_usage_max`, `5h 85%` once the usage reading reaches it.
- **`baton status`** says how many workers may run and what the machine reads now (`workers: auto, up to 6 on this machine (14 CPUs, 48 GiB) · load 3.1 · memory pressure normal (45% free)`), and what holds a phase back, since when. It marks a worker's phase `⚙` with how it stands, and lists the workers (branch, session, worktree, and `baton view P3` when one needs you), the merge queue with any deferral and why, the questions from workers, and the usage reading.
- **Every step** is announced in the lead and logged to the run's `events.jsonl`; each worker also has its own log, in `.baton/runs/<session>/workers/<phase>/`.

### What workers write

- **Worktrees** under `~/.baton/worktrees/`, outside your checkout, one per worker (`git worktree list` shows them), removed once its work lands or the worker ends. If its uncommitted work can't be saved first, the worktree stays, and baton names it.
- **Branches** named `baton/<run>/<phase>` in your repository, from your branch, removed once landed. A branch with commits that never landed is kept: under its own name while its plan still stands, so the phase's next worker carries on from it, and as `baton/<run>/kept/<phase>-<YYYYMMDD-HHMM>` once the plan is replaced or the run dropped. `git branch --list 'baton/*'` lists them; `baton clean` names each with the command to delete it.
- **Your branch** moves forward by fast-forward merges in your checkout, as each worker's work lands. Nothing else of yours is changed.

## Cleaning up: `baton clean`

baton tidies up after itself as it goes: a worker's worktree goes when its work lands, lanes come down when the session ends, and a new run ends the workers earlier runs left. When something has gone wrong anyway (a machine that crashed, a host killed with `kill -9`), `baton clean`, in the project, gets back to a known state in one command:

```sh
baton clean --dry-run   # say what it would do, and change nothing
baton clean
```

It ends what no live session uses in the project of the working directory:

1. the **workers** of runs nobody will come back to, as `/baton drop` would: their uncommitted work saved into a snapshot ref, their worktrees removed, their branches kept when they hold commits that never landed, and their phases given back to their runs;
2. **worktrees** under `~/.baton/worktrees/` that such runs left with no worker on them, or that no run names, with their work saved first while git still knows them;
3. the unfinished **jobs** of those runs, and their gates, while the process on record is still baton's;
4. **lanes** nobody holds;
5. **processes** those sessions left running: a process of yours that a session of this project started, from a session no live run owns, is stopped if it runs under `~/.baton` (a worker's or a lane's), and otherwise only listed, with the `kill` to run (a dev server the lead started in your checkout is yours to stop). A process baton can't identify is never stopped. Windows has no process table baton reads, so there it says it can't look;
6. **leftover files:** records of sessions and runs that are gone, temporary files from writes that were interrupted, `baton view` sockets that nothing answers, and old records of sessions handed to baton.

A run is left alone, and named, while it is live, or while it has been quiet for less than 2 minutes, or while a baton of its own is still running (a machine that slept): an update restart releases a run for a few seconds, and a session may just be starting. In a session, `/baton clean` leaves the session's own run alone; `/baton drop` first to clean that one.

`baton clean` never deletes your work. It lists what it keeps, with the command to remove each: branches with commits that never landed (`kept/` ones included), worktrees kept because their work couldn't be saved, snapshot refs, drafts, and the runs' histories. It ends with `baton: clean`, or `baton: nothing to clean`, which is what a second run right after the first says.

## When something goes wrong

A run must not stop for something it can work around. When something goes wrong mid-run (a commit whose signature times out, a missing tool, a rule of the plan that can't be followed right now), the model reaches you in one of three ways, the lightest that fits:

| The model runs | When | The run | What reaches you |
|---|---|---|---|
| `baton note "<what it did>" --undo "<how to reverse it>"` | It already took a reversible way around the problem. | Keeps going. | A line in the session and a low-priority push. |
| `baton propose "<what it will do>" --because "<what went wrong>" --undo "<how>"` | There's a reversible way forward, but you might choose differently. | Goes ahead at the deadline (5 minutes by default) unless you answer. | A question in the session, with the deadline, and a push. |
| `baton blocked "<why>" --tried "<what it tried>"` | Every way forward is irreversible or outward-facing, or needs a secret only you hold. | Stops until you answer. | A question in the session, and a push, sent again if it goes unanswered. |

**Proposals.** A proposal reaches you as a question such as *"baton: gpg signing timed out. Unless you answer by 15:33, I will commit P4 unsigned."* You have three answers:

- **Wait for me** holds the run until you say what to do.
- **Pause baton** hands the session to you; `/baton run` hands it back.
- **Go ahead** lets the model do it now.

You can also type an answer of your own. If nobody answers by the deadline, baton picks Go ahead itself. It only does that while its question is the only dialog on screen, and a key press restarts the clock, so it never runs out under your fingers. A proposal that looks outward-facing or irreversible (push, publish, deploy, delete, force, …) gets no deadline: it waits for you like a block.

**Answering late.** Every decision made without you is kept, with its undo. The next time you type in the session, the model is handed the ones you haven't seen yet. So "undo the unsigned commit" still works hours later, after compactions or a `/clear`. After `max_auto_decisions` decisions made without you in one phase (5 by default), baton stops the run so you can review them.

**Saved work.** Whenever the run halts, baton saves any uncommitted work into a private ref, `refs/baton/snapshots/<phase>-<time>`. A halt is a block, a question or permission prompt left waiting on you, a usage-limit wait, a pause, or the session ending.

- **What's saved.** Staged and unstaged changes, and new files. Ignored files are left out.
- **What's left alone.** Your working tree, index and `HEAD`. gpg is never called.
- **Getting it back.** `baton status` lists the refs. `git show --stat <ref>` shows what one holds, and `git restore --source=<ref> --worktree -- .` puts it back in the working tree, overwriting those files. baton keeps the newest 20.

baton also asks the model to commit before it halts: `blocked` and `done` refuse to leave new work uncommitted unless the model says why.

**Where to look.** `/baton status` shows:

- a proposal waiting on you, with its deadline;
- a decision held for you, or a review that's due;
- every decision the run made without you, with its undo;
- the snapshots, with the command to restore one.

`baton done` lists each phase's decisions, and the plan-complete notice gives the count. Everything is also logged to the run's `events.jsonl` (`.baton/runs/<session>/events.jsonl`).

**How it reaches you.**

- **Questions in the session.** Proposals, blocks and reviews are asked as questions. Claude's own question notification takes them to every device signed in to Claude, your phone included, with nothing to set up.
- **baton's own pushes.** These go to your desktop, and to [ntfy](https://ntfy.sh) if you set a topic (see [Configure](#configure)). **ntfy is the only way baton's own pushes reach a phone.** Without it, you hear about a note, or a proposal that went ahead, only on this machine and in the session. Pushes are content-free unless you turn on `details`.

**What never changes.**

- baton never answers a permission prompt or types through one.
- Nothing irreversible or outward-facing goes ahead on a timer.
- Anything that needs a secret only you hold stops the run.

The design and the experiments behind it are in [`docs/escalation.md`](docs/escalation.md).

## When the session itself stops

Some stops aren't the model's to report: the model's safeguards refuse a turn, claude exits, or you rewind the conversation or clear it. baton tells each apart, from Claude Code's hooks and the session's transcript, which it reads and never writes. The experiments behind this are in [`docs/research/recovery.md`](docs/research/recovery.md).

### A refused turn

When the model's safeguards decline a turn, Claude Code goes on once by itself. If it then won't go on, the turn ends, and baton reads why from the transcript (Claude Code reports only an invalid request). Then:

- **baton never retries it.** It types nothing into that conversation, never starts a fresh one by itself, and never switches models. Sending the same conversation to the same model again usually earns another refusal, and a fresh conversation from the brief can be refused too: baton can't know which text tripped the safeguards.
- **It asks you how to go on,** in the session and with a push: *"P3 stopped: the model's safeguards declined the last turn (cyber), and this conversation will not go on as it is. baton does not retry it. To go on, rewind (Esc Esc) to before the flagged turn and take a different approach, as Claude Code advises, or /clear for a fresh conversation from P3's brief. Meanwhile workers carry on with every phase that does not need P3, and P5 waits for it."* If your work needs it, Claude Code's guidance for flagged security work also offers the [Cyber Verification Program](https://support.claude.com/en/articles/14604842-cyber-verification-program).
- **The rest of the run goes on where it can.** Workers carry on with every phase that doesn't need the stopped one. A worker's question can't wait for the lead to put it to you, so it's left in the worker's own session, and you're pushed with `baton view P3`. Without workers, nothing else runs.
- The status line shows `⚠ refused · waiting on you`, `baton status` names the category, and `events.jsonl` logs `escalated` with type `refused`. The escalation clears once a turn ends well.

A worker's refused turn stops only its phase: rewind or `/clear` in its session, with `baton view P3`.

The same holds for any other error baton can't retry away: it says which phase stopped, that only you decide how it goes on, and what goes on meanwhile.

### A rewind

Rewinding (Esc Esc, or `/rewind`) fires no hook, and the model forgets everything after the point it goes back to, while baton still holds what it recorded there. So baton watches for one: at each prompt it notes the transcript's last entry, and at the turn's next hook it checks that the prompt follows on from it. When it doesn't, you rewound.

**baton's record wins.** Nothing it recorded is rolled back: undoing a decision is still yours to ask for, through its undo. At the model's next tool call or stop, baton tells it that you rewound, and what still stands of what came after that point:

- the decisions made there, each with its undo; the jobs handed over there, and their results; a phase done; a block;
- the files Claude's edit tools wrote there, which are as they are on disk now, whether you restored the code with the rewind or not;
- the phase's brief again, if the rewind dropped it (a rewind to the `/compact` entry does).

The session says *"baton: noticed a rewind to before 14:02 — telling Claude what still stands of baton's record"*, and `events.jsonl` logs `rewound`. A rewind can't cross a compaction, so it never takes the model back into an earlier phase. While a compaction is owed, baton says nothing to the model: the brief after it gives the phase afresh.

### `/clear` mid-phase

A conversation you clear while a phase is in hand gets the phase's brief, as after a compaction (its section of the plan, the standing rules, the notes and decisions), and is told to check what's already done before redoing anything. Nothing wakes it: your next prompt does. `/clear` in a worker's session (through `baton view`) keeps the worker on its phase.

### A crash

When claude exits by itself mid-plan, baton brings it back in place, on the same binary, as an update restart does: it resumes the same conversation and tells the model that the turn under way was lost, and to check what's done before going on. The workers are suspended and resumed, and jobs go on. The session says *"baton: claude exited with code 137 mid-plan → resumed this conversation"*.

- It doesn't for an exit anyone chose: code 0, Ctrl-C (130), a termination (143), a signal sent to baton itself, a closed terminal, `/exit` or `/baton exit`, a restart of baton's own; nor while the run is paused or complete.
- **At most twice in 30 minutes.** A third time, baton stops and asks you to run `baton --resume <id>`.
- **If the conversation can't be resumed** (claude exits before its session starts, as it does when the transcript is gone), baton starts a fresh session for the same run, from the phase's brief, once per phase.
- `events.jsonl` logs `lead_exited`, then `lead_resumed` or `lead_fresh`. On Windows, where baton can't restart in place, the session ends as before, and you continue it with `baton --continue`.

A worker that exits unexpectedly is resumed in its worktree, at most twice, as before.

## Configure

Optional settings live in `~/.baton/config.json`. Environment variables override the file.

| Key | Env | Default | Meaning |
|---|---|---|---|
| `ntfy_topic` | `BATON_NTFY_TOPIC` | none | Push notifications through [ntfy](https://ntfy.sh): the only way baton's own pushes reach your phone. Treat the topic like a password. |
| `ntfy_server` | `BATON_NTFY_SERVER` | `https://ntfy.sh` | Your own ntfy server. |
| `desktop` | `BATON_NOTIFY_DESKTOP` | `true` | Desktop notifications (macOS, and Linux with `notify-send`). |
| `details` | — | `false` | Include the reason text in pushes. Pushes are content-free by default. |
| `autocompact` | `BATON_AUTOCOMPACT` | `810k` | Launch-time cap on the context (`claude --autocompact`): `auto`, or 100k to 1m. Claude Code compacts on its own about 33k tokens below the limit, which is this cap or the model's window, whichever is smaller. `off` leaves the limit to Claude Code's own settings. An `--autocompact` you pass to `baton` yourself wins. |
| `checkpoint_pct` | `BATON_CHECKPOINT_PCT` | `60` | Context fill (% of the limit) at which baton asks the model to checkpoint at its next safe point. `0` turns it off. |
| `warn_pct` | `BATON_WARN_PCT` | `90` | Context fill (% of the limit, but never under 200k tokens) at which baton asks you whether to checkpoint now or keep going. If nobody answers within 20 minutes, baton answers "Keep going" itself, and Claude Code compacts on its own when the context is full. If Claude Code would compact first (small windows), the question moves to before that point. `0` turns it off. |
| `escalation_timeout` | `BATON_ESCALATION_TIMEOUT` | `5m` | How long a proposal waits for you before baton goes ahead with it: 1m to 2h. It never goes ahead within a minute of the question appearing, and a key press restarts the clock. |
| `max_auto_decisions` | `BATON_MAX_AUTO_DECISIONS` | `5` | How many decisions one phase may make without you (notes, and proposals that went ahead because nobody answered) before baton stops for you to review them. `0` means no limit. |
| `auto_restart` | `BATON_AUTO_RESTART` | `true` | Restart hosted sessions on a newer compatible baton once it is installed (see [Install](#install)). `false` keeps each session on its version until you restart it. |
| `switch_after` | `BATON_SWITCH_AFTER` | `5m` | How long a phase that can't go on waits on its gate before baton switches the session to another phase that can start: 1m to 2h. A command whose earlier runs took at least that long switches at once. See [Switching](#long-gates-go-to-baton). |
| `verify_timeout` | `BATON_VERIFY_TIMEOUT` | `3h` | How long a job handed to baton (`verify` or `await`) may run before baton stops it and counts it as failed: 1m to 24h. |
| `foreground_max` | `BATON_FOREGROUND_MAX` | `2m` | How long a command runs in the foreground before Claude Code moves it to the background: 10s to 10m, or `off` to leave it to Claude Code. baton passes it as `CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS`, unless that is already set. |
| `workers` | `BATON_WORKERS` | `auto` | How many hidden worker sessions may work on other phases beside your session. `auto` sizes it to the machine (one per 2 CPUs or 4 GiB, whichever allows fewer, less one for the lead, at most 8), and starts them as the plan, the machine and the lanes have room. A number from 1 to 8 is the most at once, whatever the machine; `0` turns workers off. See [When a worker starts](#when-a-worker-starts). |
| `worker_usage_max` | `BATON_WORKER_USAGE_MAX` | `0` | The share of the account's five-hour usage (%) at or above which no new worker starts, to keep a reserve. `0`, the default, holds nothing back. |

With the defaults on a 1M-token model, baton asks for a checkpoint at 486k tokens and asks you at 729k, and Claude Code compacts on its own at about 777k. `/baton setup` prints the numbers for your settings.

`BATON_HOME` moves `~/.baton` elsewhere.

## Privacy and safety

- **Typing.** baton types into the terminals it hosts, yours and its workers': `/compact` at boundaries, short `[baton] …` reminders when a session stalls, and `/exit` to end a worker. It never types while a dialog is open or you've typed in the last few seconds, with one exception: it answers its own question when nobody has, by typing `3`, and only while that question is the only dialog open (in a permission prompt, `3` means No). An unsent **draft** no longer holds it either: after a grace period baton saves the text among the run's drafts and clears the box (`baton drafts` gives it back), because a message nobody sent used to be able to park a whole run. It will not do that twice inside two minutes, however the flag reads. Every action is announced in the session and logged to the run's `events.jsonl`.
- **Git.** baton never commits or stages your files. All it writes to a repository is its snapshot refs under `refs/baton/`, a line in `.git/info/exclude` that keeps `.baton/` out of git, and, once the model runs `baton verify`, the worktrees of its lanes under `~/.baton/lanes/` (`git worktree list` shows them). With workers, it also makes their worktrees under `~/.baton/worktrees/` and their branches (`baton/<run>/<phase>`), rebases those branches in their worktrees, and lands them on your branch with `git merge --ff-only`, which changes only the files the worker changed and only when none of them holds changes of yours. When it tidies worktrees away, it makes git forget only the ones it made under `~/.baton` whose directory is gone; a worktree of yours on a drive that isn't mounted is never forgotten. The only git commands it ever refuses are the ones above, while another baton session works in the same checkout, and an amend, reset or rebase over commits that landed from a worker.
- **Running your code.** A gate handed to baton runs the command the model gave it, in a lane, through your `.baton-lane` if you committed one, or, with `verify --here`, in the session's own working tree; baton itself runs only that script's `up`, `down` and `slots`.
- **Transcripts.** baton reads the session's transcript, Claude Code's own record of the conversation, to tell a refused turn apart and to notice a rewind. It never writes to it.
- **Processes.** baton stops a process only when it is its own (a gate's runner, a worker's session), or when `baton clean` finds it left by a session of the project that is gone and running under `~/.baton`. It signals each process on its own (never a process group read from a file), and checks first that a pid on record still belongs to baton. Anything else it finds is listed, never stopped.
- **Settings.** baton never edits your Claude Code settings. Its hooks, status line, an allow rule for its own CLI, and `"idleCompaction": false` (baton decides when a hosted session compacts) are passed to each hosted session, workers included, with `claude --settings`. claude also gets `CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS` (see `foreground_max`), unless you set it yourself.
- **Notifications.** Pushes go to the ntfy topic you configure and say only that you're needed, unless you turn on `details`.
- **Downloads.** The binary download is verified against checksums committed to this repository. Set `BATON_BIN` to use a binary you built yourself.

See [SECURITY.md](SECURITY.md).

## Uninstall

In each project you ran baton in, once no baton session runs there, `baton clean` ends whatever baton left behind. Then:

```sh
claude plugin uninstall baton@baton
claude plugin marketplace remove baton
rm -rf ~/.baton          # binaries, config, lanes, workers' worktrees, and records of sessions handed over
```

Then remove the `baton init` line from your shell config, and the `.baton/` directory from any project you ran baton in. In a git project, this removes baton's snapshots, and makes git forget the lanes and worktrees you just deleted:

```sh
git for-each-ref --format='%(refname)' refs/baton/ | xargs -n1 git update-ref -d
git worktree prune
```

`git branch --list 'baton/*'` lists the branches of workers that ended with commits that never landed. Merge what you want from them, then delete them with `git branch -D`.

## Develop

```sh
make build     # ./dist/baton
make test      # unit and integration tests
make e2e       # real claude sessions (Haiku): a few cents per run
claude --plugin-dir ./plugin   # with BATON_BIN=$PWD/dist/baton, to try the plugin from source
```

[CLAUDE.md](CLAUDE.md) has the conventions and the non-negotiables.

## License

[MIT](LICENSE)
