# baton

**Run a long, multi-phase Claude Code plan unattended, in the terminal you're watching. baton compacts the context at every phase boundary, keeps the run moving while gates run, and works on phases that don't need each other at the same time, as Claude Code agents you can see.**

You plan in plan mode, approve, and walk away. From the approval on, baton keeps the session moving: at each phase boundary it compacts the context, gives the model a brief for the next phase, and wakes it. A long test run goes to baton, which runs it on a commit of its own while the model works on. A phase that doesn't need the one in hand starts as a background agent of your session, in a git worktree of its own, and baton lands its work on your branch when it's done. When something needs you, baton asks in the session and pushes a notification.

> Status: **v0.8.9**. macOS and Linux. Windows is planned.

## Why

Long plans run best compacted at every phase boundary: fewer tokens, sharper output, and each phase starts from a clean set of facts. But only a human can type `/compact`, and a model asked to compact itself forgets, mistakes the session's token budget for its context window, says "compacting" without doing it, or sits idle for hours afterwards.

So baton takes those decisions away from the model. The model does the work; deterministic code decides when to compact, when to resume, when to start a phase, and when to call you.

## How a run goes

1. **Plan.** `/baton plan <goal>` plans in plan mode, with a heading per phase (`## P0 — …`); a small task can be a plan of one phase. A phase's id also names its agent, so it is letters, digits, `_` or `-` (`P8b`, not `P8.5`). Under each heading, a line says what the phase needs done first: `After: P1, P3`, `After: none` or `After: all`. Without the line, a phase follows the one before it. A plan's longest chain of phases waiting one for another sets how long it takes, so the model builds a graph, not a list: a phase waits for another only when it needs something that phase makes; the small part others need (an interface, a schema, a fixture) goes first in a phase of its own; work is split by the files it touches; and waits on CI or a deploy stay off the main chain. A few phases are **QA phases** (`Type: qa`), at real seams (the end, and in a long plan the end of a group of phases later work builds on): they run the full test suites and lints, once for the phases before them, which run scoped checks that finish in seconds or minutes, so no small phase spends an hour on a full suite. A spike whose findings should reshape the phases after it is marked `Replan: after`. Before the plan reaches you, the model checks it against your other instructions (CLAUDE.md, memory) and addresses anything that competes with it, checks its gates against baton's gate rules ([Checks in proportion](#checks-in-proportion)), and, when most of a long plan is one chain, checks each link of that chain once.
2. **Approve.** As the plan comes to you, baton shows its shape in one line: `baton: 12 phases · longest chain 5 (P1 → P7 → P8 → P9 → P11) · up to 7 at once`. baton attaches the plan when you approve it, compacts the planning conversation away, and starts P0 from a brief: the phase's section of the plan, the standing rules, and the notes of earlier phases. Claude Code would add the whole plan file to the conversation again after every compaction, on top of that brief, so baton renames it in its folder to a name from the plan's title, and tells you the new path.
3. **Work.** Your session, the **lead**, works the phase in your checkout and reports with `baton done`. A stop without a status is refused, with instructions.
4. **Phases at once.** Each other phase that can start goes to a **phase agent**: a background agent of your session that baton starts, in a git worktree and on a branch of its own. baton starts as many as the plan and the machine have room for, at least 2 minutes apart, so the machine shows each one's load before the next starts. Each phase agent gets the same guarantees as your session, from baton's hooks rather than a model's memory.
5. **Gates off the critical path.** A gate that takes more than a couple of minutes goes to baton (`baton verify`), which runs it on the committed code outside your checkout while the model works on. A finished phase is reported done while its gate runs: it is done when the gate passes, and reopens if it fails. A phase agent's failed gate goes back to that agent, with the result. A gate built from `baton stage` steps reports as it runs: its first failure reaches the model at once, which can stop it and rerun only what failed.
6. **Boundaries.** When a phase is done, baton waits until nothing is in the way (a turn, a dialog) and has its mod compact the session, with your unsent draft left in the input box. Once it has proof that the compaction happened, it briefs the next phase and wakes the model. When baton takes up a phase by itself (a gate's verdict, a landing), a turn under way, yours included, runs to its end first.
7. **Landing.** If your branch moved on while a phase agent worked, baton merges it into the agent's branch (a conflict goes back to the agent, which merges it itself), verifies it again, and fast-forwards your branch between your session's tool calls. When nothing that came since shares a file with the phase and a QA phase ahead will check them together, the phase lands without being verified again. Nothing is rebased, so no commit is ever rewritten. Your session is told which commits and files moved. A landing that would touch files your session has uncommitted waits, and after 10 minutes your session's model is asked to commit those files.

## What you see

- **Claude Code's agent panel**, under the prompt: a row per phase agent (`P3 — Billing API`), with its time and tokens. ↓ and Enter open its transcript.
- **Writing to a phase agent.** Each phase agent is named for its phase: `@P3 <your words>` at your prompt sends your words to P3's agent, which gets them at its next tool call (an idle one wakes for them and goes on with its phase). No turn starts in your session, its model never sees them, and baton empties the input box a second later, unless you've typed since. For a phase no agent works on (not started, done, your session's own, or one you stopped), nothing is woken: your words go to your session's model, which is told where that phase stands, and you're told why. The board shows each working agent's handle, and an agent's question to you names it. Typing in an agent's own transcript view wakes it whatever it is, but an agent baton let go of is refused every tool call, so it changes nothing.
- **baton's band**, one line above the prompt: `◆ baton · step 2/4 · P3 ⚙ 12m · P4 verifying 3m · P5 waits on P3 · lead: waiting on j4`. A step is a level of the plan's graph, the phases that may run at once. `▶` marks the lead's phase and `⚙` a phase agent at work. A phase may also be `ready`, `verifying`, `landing`, `held` or `needs you`, and `lead:` says what your session is doing.
- **`/baton board`**: every step and phase with how it stands, its agent, job and landing, and what your session is doing. `/baton-board` opens it while Claude works.
- **The status line** starts with your session's segment: `◆ baton · P6 6/23 <title> · ctx 412k/810k`. It also shows a job your session waits on, `? proposal · goes ahead 15:33`, or `⚠ waiting on you`.
- **`baton watch`**, in a second terminal: a live view of the project's runs. Each phase as the board shows it; each running gate's stage, its time against earlier runs of its command, how long its log has been still, its first failure and its wait for a lane; the landings waiting and what holds them; and the latest events, in words. It only reads, and `--once` prints it once.
- **Every action** baton takes is announced in the session and logged to `.baton/runs/<session>/events.jsonl`.

A phase agent reports to baton, not to your session's model: its transcript line reads *"Prompt dropped by a hook: P3's report went to baton as usual — nothing for Claude to do"*, the first part Claude Code's own. In auto mode Claude Code also hands each agent's report back as a message from another session; baton's mod drops that too, and it never counts as yours.

Phase agents need baton's mod, which ships in its plugin. Where Claude Code doesn't load the mod (an organization that allows only managed mods, or an older Claude Code), or loads one from another baton release, the plan runs one phase at a time in your session, and baton says so. There baton types `/compact` itself (see [Privacy and safety](#privacy-and-safety)).

## When it needs you

A run must not stop for something it can work around, and baton never waits on you for something it can decide. It waits with no deadline only where you chose that (Pause baton, a cap you set) or where nothing in baton can answer (a permission prompt, the usage limit, a login, a refusal). Every other wait has a deadline, and the deadline picks the model's side. None is longer than 20 minutes, except a block's, which is 30. When something goes wrong (a commit whose signature times out, a missing tool, a rule of the plan that can't be followed right now), the model reaches you in the lightest way that fits:

| The model runs | When | The run |
|---|---|---|
| `baton note "<what it did>" --undo "<how>"` | It already took a reversible way around. | Goes on; you're told. |
| `baton propose "<what it will do>" --because "<why>" --undo "<how>" --wait 5m` | There's a reversible way forward, but you might choose differently. | Asks you, and goes ahead at the deadline unless you answer: 5 to 20 minutes as the model judges, 10 by default. |
| `baton blocked "<why>" --tried "<what it tried>"` | Every way forward is irreversible, outward-facing, or needs a secret only you hold. | Stops until you answer, or for 30 minutes: then the model looks again for a safe way on, and proposes it. |

- **A proposal** reads *"baton: gpg signing timed out. Unless you answer by 15:33, I will commit P4 unsigned."*, and you can answer Wait for me, Pause baton or Go ahead. The model gives the shortest wait that fits: every minute waiting is time the run loses. One whose question you dismiss still goes ahead at its deadline, unless you write first. Wait for me holds it until you write, or for 20 minutes with no word from you; then it goes ahead.
- **An outward proposal is answered Not now.** A proposal that looks outward-facing or irreversible (push, publish, deploy, delete, …) has a deadline too, and a fourth answer, Not now, which baton gives for you at the deadline. The model then does it only if it is safely reversible and stays in your project (pushing its own branch, a draft pull request), and otherwise leaves it undone and carries on with everything that doesn't need it. `baton status` and the run's summary list what was left for you.
- **A block looks again.** A block nobody answers for 30 minutes is lifted, and the model looks again for a safe way on that doesn't need you (the parts that don't depend on the answer first, or a reversible way around) and proposes it.
- **baton's own questions answer themselves.** Its question when the model has stopped without a status, and its question about a plan you approved mid-run, wait 10 minutes; then the plan goes on (a revision of the running plan is followed; another plan is left for `/baton run`). Their Wait for me holds the run until you write, for 20 minutes at most; on the plan question it comes first, so a stray Enter holds the run rather than starting the new plan from P0. The context question picks Keep going after 10 minutes.
- **The model's own questions**, which it may ask in a turn you started, wait 10 minutes too where baton's mod runs; then baton picks the option the model recommended, or its first, and tells the model it chose for you.
- **Every decision made without you is kept, with its undo,** for you to read when the plan has run: `baton status` lists them. The model is handed the ones you haven't seen the next time you type, so "undo the unsigned commit" still works hours later. Set `max_auto_decisions` to have baton stop for a review after that many in one phase.
- **A phase agent's question** comes to you in Claude Code's own question dialog, naming its phase, and your answer goes to the agent. Wait for me leaves only that phase waiting, until `/baton run` asks you again, or for 20 minutes with no word from you.
- **A phase agent you stop** from the agent panel is asked about: Restart it, Leave it stopped, or Pause baton. With no answer in 10 minutes, baton lets it go if its work is done, and otherwise restarts it and tells you. Stop it a second time and nobody answers, and baton takes it down and asks you what to do with its phase, since you're the one in its way: only that phase and those that need it wait, and `baton status` shows it. Once nothing else in the run can move, baton waits 10 minutes more, then takes the phase back, and your session does it, from where the agent left it; `/baton pause` stops that, and an answer from you before then wins. After Leave it stopped, `/baton run` starts a fresh agent.
- **A run that stops moving** tells you: when nothing in it has moved for 20 minutes and nothing is at work (no tool call, no gate whose log grows or whose processes use the CPU, no wait on something outside), baton pushes one notice naming what each phase left waits on.
- **A permission prompt**, your session's or a phase agent's, is never answered or typed through by baton. It shows in your terminal, the band shows `P3 needs you`, and baton pushes if it waits. Only that phase waits: under a phase agent's prompt, baton still compacts your session and gives it its results.
- **A turn the model's safeguards refuse** is never retried, and baton never switches models. baton tells you which phase stopped, and the two ways on: rewind (Esc Esc) to before the flagged turn and take a different approach, or `/clear` for a fresh conversation from the phase's brief. Phases that don't need it carry on.
- **Whenever the run halts**, baton first saves any uncommitted work into a private ref under `refs/baton/snapshots/`. `baton status` lists them, with the command to restore one.

Questions are asked in the session, and Claude's own notifications take them to every device you're signed in on. baton also pushes to your desktop, and to your phone through [ntfy](https://ntfy.sh) if you set a topic.

baton also handles the stops that aren't the model's to report:
- **A crash.** A claude that exits by itself mid-plan is resumed in place, with its phase agents. Past two crashes in 30 minutes, baton keeps trying, 5 minutes later and then every 15.
- **An API error a fresh conversation avoids** (a prompt too long, a request too large) starts the phase afresh from its brief, your session's or a phase agent's. An expired login, billing, an unknown model and a refusal stay yours.
- **A rewind** (Esc Esc) is noticed, and the model is told what of baton's record still stands.
- **A `/clear` mid-phase** gets the phase's brief, and baton starts each unfinished phase's agent again in its worktree.

## Install

You need Claude Code 2.1.289 or newer, on macOS or Linux. Phase agents were verified on 2.1.296 and 2.1.300.

```sh
claude plugin marketplace add ozzyfromspace/baton
claude plugin install baton@baton
```

Then run `/baton setup` in any Claude Code session. On first use, the plugin downloads the `baton` binary for your machine from this repository's GitHub release into `~/.baton/bin`, and verifies its sha256. `/baton setup` says what's left to do. Usually that's one line in `~/.zshrc` (or `~/.bashrc`):

```sh
eval "$(~/.baton/bin/baton init zsh)"
```

That line puts `baton` on your PATH, and lets a plain `claude` session hand itself to baton.

`baton update` updates the plugin and the binary. Sessions baton hosts move to the new version by themselves, in the same conversation: once idle, or at the next phase boundary. After a major update (before 1.0, a minor one), a session stays on its version until you exit and run `baton --continue`.

## Use

Start sessions with `baton` instead of `claude`; it takes the same arguments. In a plain `claude` session, `/baton start` hands the session to baton, keeping the same conversation (`/baton plan` and `/baton run` do it when they need to).

| In a session | What it does |
|---|---|
| `/baton plan <goal>` | Plan in plan mode, in a form baton can run. baton runs it once you approve. |
| `/baton run [plan.md]` | Run a plan. This session's own plan goes on where it stopped (a pause, a block, phases you added to it); any other plan is attached and started. |
| `/baton replan [what to consider]` | Restructure the phases not started so more of them can run at once, weighing what you say. baton checks the result, keeps the phases that started as they are, and records the change with its undo. |
| `/baton status` | Where the run stands: phases, jobs, decisions with their undo, snapshots, replans, and the run's earlier plans. Once the plan is complete, what phase agents saved: "phase agents saved about 3h20m: 8h29m of phase work in 5h09m". |
| `/baton board` | The plan step by step. |
| `/baton pause` | Take the wheel; `/baton run` gives it back. |
| `/baton drop` | Drop the run. Its phase agents end, with their work saved, the plan's record is kept, and baton does nothing until the next plan. |
| `/baton exit` | Leave baton; the conversation goes on as plain `claude`. |
| `/baton drafts` | Drafts baton saved out of the input box to let a compaction through, where its mod doesn't run. |
| `/baton clean` | End what no live session uses: the phase agents and jobs of runs nobody will come back to, lanes, worktrees, leftover files (`--dry-run` only looks). |
| `/baton version`, `setup`, `update` | Which baton runs; check this machine; update. |

`baton --help` lists every command, and `baton watch` in another terminal shows the run live.

You can edit the plan file while it runs: baton follows the new text, and a phase agent at work is told when its phase or the standing rules change. baton also replans by itself, at a phase boundary, in your session: once after an upgrade whose planning rules changed, at a QA phase when the phases left are mostly one chain, and once a phase marked `Replan: after` is done. The first two change only which phases wait for which, and stand only if more of the plan can run at once; the third may also rework the steps of the phases left with what that phase found, never dropping work. While a replan is open, no new phase agent starts, for 20 minutes at most. If a phase agent's work reached your branch some other way (merged by hand), `baton done <phase>` in your session records it landed. A plan may name a record file in its standing rules (`Record: docs/progress.md`): as each phase is done or lands, baton appends its record (its notes and its commits) and commits that file alone, and no model may edit it, so no phase's landing conflicts over a shared progress file. `/baton pause` hands you the session: a `/compact` of yours while paused counts for the compaction baton owed, so `/baton run` doesn't compact again unless the conversation has grown much since, and a plan attached while paused waits for `/baton run`. A plan that another replaces, or that you drop, is kept in `.baton/runs/<run>/gen-<N>/`.

Each session has a run of its own in `.baton/runs/` (kept out of git), so several terminals can each run a plan in one project. A run follows its conversation through `--resume` and `/clear`. A conversation Claude Code moves to its background service is no longer hosted, though it inherited baton's settings, and baton says so once: `baton --resume <session id>` in a terminal gives it back, and `/baton run <plan>` there carries the run on where it stood. Without git, a plan runs the same way; baton just has nothing to commit or snapshot.

### Long gates

| The model runs | What baton does |
|---|---|
| `baton verify --notes "…" -- <command>` | Runs the gate on `HEAD` in a lane (a worktree outside your checkout), so edits meanwhile can't disturb it. |
| `baton verify --here --notes "…" -- <command>` | Runs it on `HEAD` in the checkout itself, for a gate that needs the checkout's running services. Edits and landings there wait until it ends, and baton names the landings it holds. In a phase agent's worktree it holds one of the project's lanes while it runs, so here gates and lane gates take turns (one at a time without a `.baton-lane`). |
| `baton await --notes "…" -- <command>` | Waits on something outside the project, such as a CI run. No git needed. |
| `baton stop <job>` | Stops a job, as at a gate's first failure. It checked nothing, so a red gate stands. |
| `baton verify --failed --notes "…"` | Reruns only what failed in a gate built from stages: the stages that passed are carried, and each failed one runs narrowed to its failed tests where it knows them. Its pass clears the red gate. |
| `baton env "<what the gates' environment needs>"` | Records a fact about the environment (a port that must stay free, a daemon to start) that every later phase's brief carries. |

A phase runs one gate and one `await` at a time, beside each other: neither stops the other, and the phase is done once both pass. The same command again replaces the one of its kind that runs (the gate on a newer commit); another command is refused unless the model adds `--replace`. A red gate stands until the same command, or its `--failed` rerun, passes: a narrower check that passes (one test of a red suite) doesn't clear it, unless the model makes it the phase's gate with `--replace`, which you're told of. What `--failed` reruns comes from baton's record of the gate, never from the model's reading of a log. A failed job's result lists the failure lines from its whole log, not only its end, and its log is kept. A red gate whose log shows its environment failed it (a port in use, the Docker daemon down, a full disk, a registry's rate limit) and no test failure says so, so the model fixes the environment rather than hunting for a bug.

The result reaches the model at its next tool call or stop, or wakes it. If a phase can't go on without its result and another phase can start, baton sets the phase aside after 5 minutes, and comes back to it when the result is in. `verify_timeout` counts from when a gate gets its lane, and a result says both ("passed after 12m, 31m waiting for a lane"). A `verify` gate that hangs in silence, its log unchanged and its processes using no CPU for 20 minutes, is stopped and counts as unverified, with the reason; where baton can't read the CPU, it never stops a gate on log silence alone.

While a gate runs, baton tells its model what it notices, as information that waits on nothing: the gate's first failure, a gate well past the slowest earlier run of its command or near its timeout, landings waiting 20 minutes behind a gate in your checkout, a file that keeps conflicting as phases land. Where its mod runs, a small model (Haiku, `advisor_model`) adds a short note to each look-in at a gate (the change's failure or the environment's, whether the rest of the run is worth the wait, the smallest next check); it is a suggestion, and the look-in goes without it after a minute whatever happens. `advisor: false` turns it off.

A phase agent whose gate is long (a measurement, an end-to-end suite) can give a focused check for its landing: `baton done <phase> --land-check "<command>"`.

A project whose gates need dependencies or services commits an executable `.baton-lane` at its root:

```sh
#!/bin/sh
set -e
case $1 in
up)    cd "$2" && npm ci ;;             # once per lane
run)   cd "$2" && exec sh -c "$3" ;;    # each gate; its exit code is the gate's
down)  ;;                               # stop what up started
slots) echo 2 ;;                        # lanes at once (1 to 4)
esac
```

It runs with `BATON_LANE` (the lane's number, for ports and names), `BATON_LANE_DIR`, `BATON_ROOT` (your checkout) and `BATON_COMMIT` set. A phase agent's `--here` gate runs its command directly in the agent's worktree, with the same variables for the lane it holds (`BATON_LANE_DIR` is the worktree), so a test database or Compose project named by lane stays its own.

### Writing a gate with `baton stage`

A long gate is a script committed in the repository, each step a stage:

```sh
#!/bin/sh
# scripts/check.sh, run as: baton verify --notes "…" -- sh scripts/check.sh
trap 'docker compose -p "check$BATON_LANE" down -v' EXIT    # tear down what the gate starts
baton stage up -- docker compose -p "check$BATON_LANE" up -d --wait
baton stage lint -- make lint
baton stage unit --runner go -- go test ./...
baton stage e2e --runner pytest -- pytest tests/e2e
```

Each stage prints when it starts and how it ended (`▶ unit 14:02:11`, `■ unit failed (exit 1) in 2m14s`), marks both in the gate's log for baton, and exits with its command's code. The gate stops at its first failure: when `lint` fails, the stages after it are skipped, and the model hears of it at once. A stage that should collect every failure on purpose says `--keep-going`. A stage knows which tests failed from its runner (`--runner go` reads `--- FAIL: TestX`, `--runner pytest` reads `FAILED path::name`) or from `::baton fail <id>` lines any command prints, so `baton verify --failed` reruns exactly those: the Go and pytest presets narrow the run themselves, and a custom command reads the ids from `$BATON_FAILED` (a file) or `$BATON_FAILED_RE` (a regular expression). Run by hand, outside baton, the script prints its stages the same way and records nothing. A process a stage leaves running must write its output elsewhere: the stage stops reading 2 seconds after its command ends.

### Checks in proportion

baton biases a plan toward how a person who feels time tests: scoped checks that finish in seconds or minutes while working (the tests of what changed, by name), and the full suites, end-to-end runs and lints once, in QA phases at real seams (the end of the plan, and in a long plan the end of a group of phases later work builds on). In two real runs, most of the hours went to checks that could not change what shipped ([`docs/research/gates.md`](docs/research/gates.md)).

- **The gate rules** go to the model as it plans (with its other instructions, at the first `ExitPlanMode`, each QA phase listed with the phases it covers) and, in short, in every phase's brief.
- **A gate out of proportion is questioned once,** as it is queued: a repeat of a gate that took 10 minutes or more on nearly the same code (with the files changed since, and the narrower check), a gate script outside the repository, a long gate not built from stages, a long gate before the plan's QA phase (with what its last run cost you). A long check started as a background shell task is pointed to `baton verify`. The same command again runs: the model decides.
- **What a phase starts, it stops.** A phase agent's `baton done` is asked once about processes and Compose containers it left running in its worktree, and when baton lets an agent go it stops the processes left there, and names any container to your session with the command to remove it.
- **The risk ledger.** Each finished phase is scored from what baton knows of it (how much its commits changed, files other phases name, manifests and migrations, code changed without tests, a finish over a red gate, its decisions and conflicts, whether it ran any check, how it says it departed from the plan), never from its own rating, and the points add up until a full check passes. At 56 points (`risk_review`), and again at 90 (`risk_full`), a fresh model (Opus, `risk_model`) with no stake in the work reads what baton prepares and answers continue, targeted checks, or a full check now. Only "full now" holds anything: no new phase agent starts until your session queues the full check (`baton verify --qa`) or says why not, or for 10 minutes (`escalation_timeout`). Without baton's mod, your session is told the score alone.

## Settings

Settings go in `~/.baton/config.json`. Most have an environment variable too (`BATON_WORKERS`, `BATON_NTFY_TOPIC`, …), which wins over the file.

| Key | Default | Meaning |
|---|---|---|
| `ntfy_topic` | none | Pushes to your phone through ntfy. Treat the topic like a password. |
| `workers` | `auto` | How many phase agents may work at once. `auto` sizes it to the machine (one per 2 CPUs or 4 GiB, whichever allows fewer, less one for your session, at most 8). A number from 1 to 8 sets the most; `0` runs one phase at a time. Phase agents start at least 2 minutes apart. |
| `autocompact` | `810k` | The context cap claude starts with: `auto`, 100k to 1m, or `off`. |
| `notice_pct` | `60` | At this share of the context limit (the model's window, or `autocompact` if smaller), the model is told where its context stands, and asked nothing. `0` turns it off. |
| `checkpoint_pct` | `80` | At this share, the model is asked to checkpoint at its next safe point (a compaction, or for a phase agent a fresh agent), and again at every further tenth. At `warn_pct` (90, never under 200k tokens) you're asked whether to checkpoint now. Claude Code's own compaction, 33k below the limit, is the backstop. |
| `escalation_timeout` | `10m` | How long a proposal with no `--wait` of its own waits for you before it goes ahead, and how long baton's questions, and the model's own, wait: `1m` to `20m`. |
| `max_auto_decisions` | off | How many decisions one phase may make without you before baton stops for a review. Off unless you set a number. |
| `aggregate_qa` | `true` | A plan's heavy checks (full suites, lints, end-to-end) run in its QA phases only (`Type: qa`); the phases between run scoped checks, and a landing that shares no file with what landed since its gate is left to the QA phase ahead. `false` has every phase check itself in full. |
| `auto_replan` | `true` | baton replans the phases not started by itself, at a phase boundary: after an upgrade whose planning rules changed, at a QA phase when the phases left are mostly one chain, and once a phase marked `Replan: after` is done. `false` turns that off; `/baton replan` works either way. |
| `advisor` | `true` | Where baton's mod runs, a small model adds a short note to each look-in at a gate. Advice only; the look-in never waits on it for more than a minute. |
| `advisor_model` | `haiku` | The advisor's model: an alias Claude Code knows (`haiku`, `sonnet`, `opus`) or a full model id. |
| `risk` | `true` | The risk ledger: each finished phase is scored, and the points add up until a full check passes. `false` turns it off. |
| `risk_review`, `risk_full` | `56`, `90` | The ledger's lines, in points since the last full check. Crossing either asks for a risk assessment. |
| `risk_model` | `opus` | The model that assesses the risk at a line. `off` tells your session the score alone. |
| `risk_wait` | `5m` | How long the assessment is waited for: `30s` to `20m`. |
| `details` | `false` | Put the reason in pushes, which otherwise say only that you're needed. |

The rest are described in [`internal/config/config.go`](internal/config/config.go): `ntfy_server`, `desktop`, `warn_pct` (90), `auto_restart`, `switch_after` (5m), `verify_timeout` (3h), `foreground_max` (2m) and `worker_usage_max`. `BATON_HOME` moves `~/.baton` elsewhere.

## Privacy and safety

- **Typing.** Where its mod runs, baton types nothing into your session: the mod compacts it and submits baton's short `[baton] …` lines, and your unsent draft stays in the input box. Where the mod doesn't run, baton types `/compact` at boundaries and a `[baton] …` line to wake a stalled session, never while a dialog is open or you've just typed. There it answers its own unanswered question only while that question is the only dialog open, by dismissing it with Esc (which picks no option in any dialog) and recording the question's default itself; and it saves an unsent draft in the way of a compaction (`baton drafts`) and clears the box, unless one of the model's own background agents may be at work: the Ctrl-C that clears the box would stop every background agent. baton never types into a phase agent.
- **The mod.** baton's plugin carries a mod: JavaScript that Claude Code runs in your session. It starts, messages and stops the phase agents, compacts your session and submits baton's lines, asks baton's questions in Claude Code's dialog, drops the agents' reports before your session's model reads them, and draws the band. For the advisor and the risk assessment, it makes a model call with your session's own Claude Code client (a prompt baton wrote, with no history), which never enters your conversation. It acts only on baton's say-so, and does nothing in a session baton doesn't host.
- **Git.** baton never commits or stages your files. It writes:
  - snapshot refs under `refs/baton/`, and a line in `.git/info/exclude`;
  - worktrees for lanes and phase agents under `~/.baton/`;
  - branches `baton/<run>/<phase>`, which reach your branch only by `git merge --ff-only`;
  - only when a plan names a record file (`Record: <path>`), that file, committed alone (`git commit --only`) as each phase is done or lands, never while it has changes of yours.

  It refuses an amend, reset or rebase over landed commits. While another baton session works in the same checkout, it also refuses git commands that take every change.
- **Transcripts** are read, never written: to tell a refused turn apart, to notice a rewind, and to read a phase agent's context.
- **Processes.** baton stops only processes of its own, ones a phase agent it lets go of left running in its worktree, and with `baton clean`, ones a session that is gone left running under `~/.baton`. Those such a session left in your project it lists, with the command to stop each, and one working anywhere else (a gpg-agent or ssh-agent your shell started) it leaves alone. It removes no container: one a phase left running is named to your session, with the command to remove it.
- **Settings.** baton never edits your Claude Code settings. Its hooks, its status line and an allow rule for its own CLI reach each session through `claude --settings`.
- **Downloads** are verified against checksums committed to this repository. See [SECURITY.md](SECURITY.md).

## Uninstall

In each project you ran baton in, run `baton clean` once no baton session runs there. Then:

```sh
claude plugin uninstall baton@baton
claude plugin marketplace remove baton
rm -rf ~/.baton
```

Remove the `baton init` line from your shell config, and `.baton/` from each project. In a git project, `git for-each-ref --format='%(refname)' refs/baton/ | xargs -n1 git update-ref -d` removes baton's snapshots, and `git worktree prune` forgets the worktrees you just deleted. `git branch --list 'baton/*'` lists any phase branches kept because they hold commits that never landed.

## Develop

```sh
make build     # ./dist/baton
make test      # unit and integration tests
make e2e       # real claude sessions (Haiku): a few cents per run
BATON_BIN=$PWD/dist/baton claude --plugin-dir ./plugin   # try the plugin from source
```

[`docs/design.md`](docs/design.md) is the design, and [`docs/research/`](docs/research) the experiments behind it. [CLAUDE.md](CLAUDE.md) has the conventions and the non-negotiables.

## License

[MIT](LICENSE)
