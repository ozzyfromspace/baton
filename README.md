# baton

**Run a long, multi-phase Claude Code plan unattended, in the terminal you're watching. baton compacts the context at every phase boundary, keeps the run moving while gates run, and works on phases that don't need each other at the same time, as Claude Code agents you can see.**

You plan in plan mode, approve, and walk away. From the approval on, baton keeps the session moving: at each phase boundary it compacts the context, gives the model a brief for the next phase, and wakes it. A long test run goes to baton, which runs it on a commit of its own while the model works on. A phase that doesn't need the one in hand starts as a background agent of your session, in a git worktree of its own, and baton lands its work on your branch when it's done. When something needs you, baton asks in the session and pushes a notification.

> Status: **v0.8.3**. macOS and Linux. Windows is planned.

## Why

Long plans run best compacted at every phase boundary: fewer tokens, sharper output, and each phase starts from a clean set of facts. But only a human can type `/compact`, and a model asked to compact itself forgets, mistakes the session's token budget for its context window, says "compacting" without doing it, or sits idle for hours afterwards.

So baton takes those decisions away from the model. The model does the work; deterministic code decides when to compact, when to resume, when to start a phase, and when to call you.

## How a run goes

1. **Plan.** `/baton plan <goal>` plans in plan mode, with a heading per phase (`## P0 — …`). Under each heading, a line says what the phase needs done first: `After: P1, P3`, `After: none` or `After: all`. Without the line, a phase follows the one before it. A few phases are **QA phases** (`Type: qa`): they run the full test suites and lints, once for the phases before them, which run focused checks only, so no small phase spends an hour on a full suite. Before the plan reaches you, the model checks it against your other instructions (CLAUDE.md, memory) and addresses anything that competes with it.
2. **Approve.** baton attaches the plan when you approve it, compacts the planning conversation away, and starts P0 from a brief: the phase's section of the plan, the standing rules, and the notes of earlier phases.
3. **Work.** Your session, the **lead**, works the phase in your checkout and reports with `baton done`. A stop without a status is refused, with instructions.
4. **Phases at once.** Each other phase that can start goes to a **phase agent**: a background agent of your session that baton starts, in a git worktree and on a branch of its own. baton starts as many as the plan and the machine have room for, at least 2 minutes apart, so the machine shows each one's load before the next starts. Each phase agent gets the same guarantees as your session, from baton's hooks rather than a model's memory.
5. **Gates off the critical path.** A gate that takes more than a couple of minutes goes to baton (`baton verify`), which runs it on the committed code outside your checkout while the model works on. A finished phase is reported done while its gate runs: it is done when the gate passes, and reopens if it fails. A phase agent's failed gate goes back to that agent, with the result.
6. **Boundaries.** When a phase is done, baton waits until nothing is in the way (a turn, a dialog) and has its mod compact the session, with your unsent draft left in the input box. Once it has proof that the compaction happened, it briefs the next phase and wakes the model. When baton takes up a phase by itself (a gate's verdict, a landing), a turn under way, yours included, runs to its end first.
7. **Landing.** If your branch moved on while a phase agent worked, baton merges it into the agent's branch (a conflict goes back to the agent, which merges it itself), verifies it again, and fast-forwards your branch between your session's tool calls. Nothing is rebased, so no commit is ever rewritten. Your session is told which commits and files moved.

## What you see

- **Claude Code's agent panel**, under the prompt: a row per phase agent (`P3 — Billing API`), with its time and tokens. ↓ and Enter open its transcript, where you can also type to it.
- **baton's band**, one line above the prompt: `◆ baton · step 2/4 · P3 ⚙ 12m · P4 verifying 3m · P5 waits on P3 · lead: waiting on j4`. A step is a level of the plan's graph, the phases that may run at once. `▶` marks the lead's phase and `⚙` a phase agent at work. A phase may also be `ready`, `verifying`, `landing`, `held` or `needs you`, and `lead:` says what your session is doing.
- **`/baton board`**: every step and phase with how it stands, its agent, job and landing, and what your session is doing. `/baton-board` opens it while Claude works.
- **The status line** starts with your session's segment: `◆ baton · P6 6/23 <title> · ctx 412k/810k`. It also shows a job your session waits on, `? proposal · goes ahead 15:33`, or `⚠ waiting on you`.
- **Every action** baton takes is announced in the session and logged to `.baton/runs/<session>/events.jsonl`.

A phase agent reports to baton, not to your session's model: its transcript line reads *"Prompt dropped by a hook: P3's report went to baton as usual — nothing for Claude to do"*, the first part Claude Code's own. In auto mode Claude Code also hands each agent's report back as a message from another session; baton's mod drops that too, and it never counts as yours.

Phase agents need baton's mod, which ships in its plugin. Where Claude Code doesn't load the mod (an organization that allows only managed mods, or an older Claude Code), or loads one from another baton release, the plan runs one phase at a time in your session, and baton says so. There baton types `/compact` itself (see [Privacy and safety](#privacy-and-safety)).

## When it needs you

A run must not stop for something it can work around, and baton never waits on you for something it can decide. It waits only where you chose that (Pause baton, Wait for me, a cap you set) or where nothing in baton can answer (a permission prompt, the usage limit, a login, a refusal). Every other question has a deadline, and the deadline picks the model's side. When something goes wrong (a commit whose signature times out, a missing tool, a rule of the plan that can't be followed right now), the model reaches you in the lightest way that fits:

| The model runs | When | The run |
|---|---|---|
| `baton note "<what it did>" --undo "<how>"` | It already took a reversible way around. | Goes on; you're told. |
| `baton propose "<what it will do>" --because "<why>" --undo "<how>" --wait 5m` | There's a reversible way forward, but you might choose differently. | Asks you, and goes ahead at the deadline unless you answer: 5 to 20 minutes as the model judges, 10 by default. |
| `baton blocked "<why>" --tried "<what it tried>"` | Every way forward is irreversible, outward-facing, or needs a secret only you hold. | Stops until you answer. |

- **A proposal** reads *"baton: gpg signing timed out. Unless you answer by 15:33, I will commit P4 unsigned."*, and you can answer Wait for me, Pause baton or Go ahead. The model gives the shortest wait that fits: every minute waiting is time the run loses. One whose question you dismiss still goes ahead at its deadline, unless you write first. A proposal that looks outward-facing or irreversible (push, publish, deploy, delete, …) gets no deadline.
- **baton's own questions answer themselves.** Its question when the model has stopped without a status, and its question about a plan you approved mid-run, wait 10 minutes; then the plan goes on (a revision of the running plan is followed; another plan is left for `/baton run`). The context question picks Keep going after 10 minutes.
- **Every decision made without you is kept, with its undo,** for you to read when the plan has run: `baton status` lists them. The model is handed the ones you haven't seen the next time you type, so "undo the unsigned commit" still works hours later. Set `max_auto_decisions` to have baton stop for a review after that many in one phase.
- **A phase agent's question** comes to you in Claude Code's own question dialog, naming its phase, and your answer goes to the agent. Wait for me leaves only that phase waiting, until `/baton run` asks you again.
- **A phase agent you stop** from the agent panel leaves its phase waiting for you. baton pushes that, the board shows it, and `/baton run` starts a fresh agent.
- **A run that stops moving** tells you: when nothing in it has moved for 30 minutes and nothing is at work (no tool call, no gate whose log still grows), baton pushes one notice naming what each phase left waits on.
- **A permission prompt**, your session's or a phase agent's, is never answered or typed through by baton. It shows in your terminal, the band shows `P3 needs you`, and baton pushes if it waits. Only that phase waits.
- **A turn the model's safeguards refuse** is never retried, and baton never switches models. baton tells you which phase stopped, and the two ways on: rewind (Esc Esc) to before the flagged turn and take a different approach, or `/clear` for a fresh conversation from the phase's brief. Phases that don't need it carry on.
- **Whenever the run halts**, baton first saves any uncommitted work into a private ref under `refs/baton/snapshots/`. `baton status` lists them, with the command to restore one.

Questions are asked in the session, and Claude's own notifications take them to every device you're signed in on. baton also pushes to your desktop, and to your phone through [ntfy](https://ntfy.sh) if you set a topic.

baton also handles the stops that aren't the model's to report:
- **A crash.** A claude that exits by itself mid-plan is resumed in place, with its phase agents, at most twice in 30 minutes.
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
| `/baton status` | Where the run stands: phases, jobs, decisions with their undo, snapshots, and the run's earlier plans. |
| `/baton board` | The plan step by step. |
| `/baton pause` | Take the wheel; `/baton run` gives it back. |
| `/baton drop` | Drop the run. Its phase agents end, with their work saved, the plan's record is kept, and baton does nothing until the next plan. |
| `/baton exit` | Leave baton; the conversation goes on as plain `claude`. |
| `/baton drafts` | Drafts baton saved out of the input box to let a compaction through, where its mod doesn't run. |
| `/baton clean` | End what no live session uses: the phase agents and jobs of runs nobody will come back to, lanes, worktrees, leftover files (`--dry-run` only looks). |
| `/baton version`, `setup`, `update` | Which baton runs; check this machine; update. |

`baton --help` lists every command.

You can edit the plan file while it runs: baton follows the new text, and a phase agent at work is told when its phase or the standing rules change. If a phase agent's work reached your branch some other way (merged by hand), `baton done <phase>` in your session records it landed. `/baton pause` hands you the session: a `/compact` of yours while paused counts for the compaction baton owed, so `/baton run` doesn't compact again unless the conversation has grown much since, and a plan attached while paused waits for `/baton run`. A plan that another replaces, or that you drop, is kept in `.baton/runs/<run>/gen-<N>/`.

Each session has a run of its own in `.baton/runs/` (kept out of git), so several terminals can each run a plan in one project. A run follows its conversation through `--resume` and `/clear`. Without git, a plan runs the same way; baton just has nothing to commit or snapshot.

### Long gates

| The model runs | What baton does |
|---|---|
| `baton verify "<command>" --notes "…"` | Runs the gate on `HEAD` in a lane (a worktree outside your checkout), so edits meanwhile can't disturb it. |
| `baton verify --here "<command>" --notes "…"` | Runs it on `HEAD` in the checkout itself, for a gate that needs the checkout's running services. Edits and landings there wait until it ends. In a phase agent's worktree it holds one of the project's lanes while it runs, so here gates and lane gates take turns (one at a time without a `.baton-lane`). |
| `baton await "<command>" --notes "…"` | Waits on something outside the project, such as a CI run. No git needed. |

The result reaches the model at its next tool call or stop, or wakes it. If a phase can't go on without its result and another phase can start, baton sets the phase aside after 5 minutes, and comes back to it when the result is in.

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

## Settings

Settings go in `~/.baton/config.json`. Most have an environment variable too (`BATON_WORKERS`, `BATON_NTFY_TOPIC`, …), which wins over the file.

| Key | Default | Meaning |
|---|---|---|
| `ntfy_topic` | none | Pushes to your phone through ntfy. Treat the topic like a password. |
| `workers` | `auto` | How many phase agents may work at once. `auto` sizes it to the machine (one per 2 CPUs or 4 GiB, whichever allows fewer, less one for your session, at most 8). A number from 1 to 8 sets the most; `0` runs one phase at a time. Phase agents start at least 2 minutes apart. |
| `autocompact` | `810k` | The context cap claude starts with: `auto`, 100k to 1m, or `off`. |
| `escalation_timeout` | `10m` | How long a proposal with no `--wait` of its own waits for you before it goes ahead, and how long baton's own questions wait. |
| `max_auto_decisions` | off | How many decisions one phase may make without you before baton stops for a review. Off unless you set a number. |
| `aggregate_qa` | `true` | A plan's heavy checks (full suites, lints, end-to-end) run in its QA phases only (`Type: qa`); the phases between run focused checks. `false` has every phase check itself in full. |
| `details` | `false` | Put the reason in pushes, which otherwise say only that you're needed. |

The rest are described in [`internal/config/config.go`](internal/config/config.go): `ntfy_server`, `desktop`, `checkpoint_pct` (60), `warn_pct` (90), `auto_restart`, `switch_after` (5m), `verify_timeout` (3h), `foreground_max` (2m) and `worker_usage_max`. `BATON_HOME` moves `~/.baton` elsewhere.

## Privacy and safety

- **Typing.** Where its mod runs, baton types nothing into your session: the mod compacts it and submits baton's short `[baton] …` lines, and your unsent draft stays in the input box. Where the mod doesn't run, baton types `/compact` at boundaries and a `[baton] …` line to wake a stalled session, never while a dialog is open or you've just typed. There it answers its own unanswered question by typing `3`, only while that question is the only dialog open, and saves an unsent draft in the way of a compaction (`baton drafts`) and clears the box, unless one of the model's own background agents may be at work: the Ctrl-C that clears the box would stop every background agent. baton never types into a phase agent.
- **The mod.** baton's plugin carries a mod: JavaScript that Claude Code runs in your session. It starts, messages and stops the phase agents, compacts your session and submits baton's lines, asks baton's questions in Claude Code's dialog, drops the agents' reports before your session's model reads them, and draws the band. It acts only on baton's say-so, and does nothing in a session baton doesn't host.
- **Git.** baton never commits or stages your files. It writes:
  - snapshot refs under `refs/baton/`, and a line in `.git/info/exclude`;
  - worktrees for lanes and phase agents under `~/.baton/`;
  - branches `baton/<run>/<phase>`, which reach your branch only by `git merge --ff-only`.

  It refuses an amend, reset or rebase over landed commits. While another baton session works in the same checkout, it also refuses git commands that take every change.
- **Transcripts** are read, never written: to tell a refused turn apart, to notice a rewind, and to read a phase agent's context.
- **Processes.** baton stops only processes of its own, and with `baton clean`, ones a session that is gone left running under `~/.baton`.
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
