# baton

**Run a long, multi-phase Claude Code plan unattended. baton compacts the context at every phase boundary, in one live terminal session you can watch.**

You plan the work in plan mode, approve it, and walk away. baton picks up the plan the moment you approve it, and keeps the session moving: at each phase boundary it compacts the context, hands the model a brief for the next phase, and wakes it up. If something needs you, it tells you on whatever device you're on.

> Status: **v0.3.4**. macOS and Linux. Windows is planned.

## Why

Long plans run best when the context is compacted at each phase boundary: fewer tokens, sharper output, and every phase starts from a clean set of facts. But in Claude Code only a human can type `/compact`, so someone has to sit by the terminal. Asking the model to compact itself fails in practice:

- it forgets;
- it mistakes the session token budget for its context window;
- it says "compacting" without doing it;
- after a compaction it can sit idle for hours before anyone notices.

baton takes every one of those decisions away from the model. The model does the work; deterministic code decides when to compact, when to resume, and when to call you.

## How it works

- **Hosting.** `baton` starts `claude` inside a pseudo-terminal it owns and passes every byte through. You see and use the normal Claude Code interface.
- **One run per session.** Each Claude Code session has a run of its own in `.baton/runs/` at the root of your project, which is kept out of git. So every `baton` terminal can run a plan, in the same project or not ([Several sessions](#several-sessions-in-one-project)). A run stays with its conversation through `--resume`, and through a `/clear` in the same terminal. The model reports progress with `baton done`, `waiting` or `checkpoint`, and reaches you with `note`, `propose` or `blocked`.
- **Plan approval.** When you approve a plan with phase headings (`## P0 — …`), baton attaches it, compacts the planning conversation away, and starts P0 from a brief. If a run is already under way, it asks you whether to continue with the revised plan or start the new one. baton reads the plan from the file Claude Code wrote, and checks it at every stop: an edit that keeps the phases still to run is followed, and one that loses any pauses the run.
- **Phase boundaries.** When a phase is done, baton's hooks (not the model) decide to compact. baton waits until no turn, dialog, subagent or human draft is in the way, types `/compact` itself, and confirms it ran. Then it injects a brief for the next phase, quoted from your plan, and wakes the model.
- **Silent stops.** A stop without a status is refused with instructions. The model also can't start the next phase until the compaction has happened.
- **Context valves.** baton reads the exact context size from Claude Code's status line. When a long phase reaches 60% of the limit, it asks the model to checkpoint at its next safe point. At 90% it asks you, with a question in the session, whether to checkpoint now or keep going. If nobody answers within 20 minutes, it keeps going. Claude Code's own auto-compaction remains the backstop.
- **No questions mid-run.** While a plan runs, the model may not stop to ask you questions of its own, because nobody may be there. It takes a reversible way forward and tells you, proposes one with a deadline, or reports itself blocked ([When something goes wrong](#when-something-goes-wrong)). Questions in turns you started yourself still get through.
- **Watchdog.** It catches a session that has gone quiet: idle turns, expired waits, unanswered prompts, usage limits, API errors. **Every gate either clears by itself or expires into action**, so nothing baton waits on can hold a run — a half-typed message is saved and taken out of the box, a hung background subagent is overridden, an open turn and a never-settling screen stop being trusted after their ceilings. Whatever still needs you is pushed again until it's resolved.
- **One exception, and it is deliberate: an open permission prompt.** Keystrokes landing in a prompt *select* an option, so a baton that typed through one could approve a tool call you never approved. That wait is escalated and pushed; it is never overridden.
- **Drafts.** If an unsent draft is in the way of a compaction, baton saves it and clears the box rather than waiting. `baton drafts` hands it back. Each one is a file holding exactly the text, so `cat .baton/runs/*/drafts/*.txt` works even if baton will not start.
- **Escalation.** When baton needs you, it asks in the session (a question that reaches every device signed in to Claude) and sends its own push notification. Whenever the run halts, baton first saves any uncommitted work.
- **Status line.** A permanent `◆ baton · P6 6/23 <title> · ctx 412k/810k` segment sits in front of your own status line. It shows the context size against the limit, and `? proposal · goes ahead 15:33` while a proposal waits on you.

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
```

Inside the session:

| Command | What it does |
|---|---|
| `/baton plan <goal>` | Plan the work in plan mode, in a format baton can run. When you approve the plan, baton runs it. |
| `/baton run [plan.md]` | Run a plan that already exists. Defaults to the plan you just approved. |
| `/baton start` | Hand this session to baton, same conversation (see below). `/baton plan` and `/baton run` do it when they need to. |
| `/baton status` | Show where the run stands. |
| `/baton pause` / `/baton resume` | Take the wheel and give it back. While paused, baton observes but never acts. |
| `/baton drop` | Drop the run. Its plan is set aside, and baton does nothing in this session until the next plan. |
| `/baton exit` | Leave baton: this conversation goes on as plain `claude` in the same terminal. |
| `/baton drafts` | List drafts baton saved out of the input box; `--last` prints the newest. |
| `/baton version` | Which baton this session runs, the one installed, and the latest release. |
| `/baton setup` | Check this machine and list what's missing. |
| `/baton update` | Update baton to the latest release (also `baton update` in a shell; `--check` only looks). |

You don't need a command to run a plan you approve in plan mode: baton attaches any approved plan whose phases are headings, such as `## P0 — Title`, `### P23: Title` or `## Phase C: Title`, when the session has no plan under way. `/baton plan` writes plans in that format. `/baton run` also takes phases written as bold bullets or table rows.

The `/baton` skill is a stub: its instructions come from the `baton` binary each time you use it, so they always match the baton that carries them out, and a session never needs a restart just to pick up new instructions.

### Starting baton in a running session

Started a session with plain `claude` and want baton to drive it? Run `/baton start`, or just `/baton plan …` or `/baton run …`, there. baton records the session, stops `claude` cleanly when the turn ends, and your shell relaunches **the same conversation** under baton. `/baton plan` does this before planning, so baton is watching when you approve the plan. `/baton exit` does the reverse. Both need the `baton init` line above. Without it, baton tells you what to run by hand: `baton --resume <session id>` to hand a session to baton, `claude --resume <session id>` to leave it.

### Several sessions in one project

Each `baton` terminal runs its own session's plan, so you can run two plans in one project at once, or keep one terminal for a plan and another for everything else. `baton status` in a session shows its own run and names the other sessions working in the checkout. From a plain shell it lists every run.

Two sessions in one checkout share its files and its git index, so while another baton session is live there:

- baton refuses git commands that take every change (`git add -A` or `.`, `commit -a`, `stash`, `reset --hard`, `checkout .` or `restore .`, `clean -f`), and the model stages its own files by path;
- `baton done` only holds a phase to the files this session's edit tools wrote, so the model is never asked to commit the other session's work.

For full isolation (separate builds, test runs and branches), give each plan its own git worktree, which gets its own `.baton/`.

### Without git

baton doesn't need a git repository. In a plain folder, or on a machine without git, a plan runs the same way; baton just has nothing to commit or snapshot. Its wording says "complete" and "saved" instead of "committed", and it never asks the model to commit or to create a repository. If you start a repository mid-run, baton uses it from then on.

## When something goes wrong

A run must not stop for something it can work around. When something goes wrong mid-run (a commit whose signature times out, a missing tool, a rule of the plan that can't be followed right now), the model reaches you in one of three ways, the lightest that fits:

| The model runs | When | The run | What reaches you |
|---|---|---|---|
| `baton note "<what it did>" --undo "<how to reverse it>"` | It already took a reversible way around the problem. | Keeps going. | A line in the session and a low-priority push. |
| `baton propose "<what it will do>" --because "<what went wrong>" --undo "<how>"` | There's a reversible way forward, but you might choose differently. | Goes ahead at the deadline (5 minutes by default) unless you answer. | A question in the session, with the deadline, and a push. |
| `baton blocked "<why>" --tried "<what it tried>"` | Every way forward is irreversible or outward-facing, or needs a secret only you hold. | Stops until you answer. | A question in the session, and a push, sent again if it goes unanswered. |

**Proposals.** A proposal reaches you as a question such as *"baton: gpg signing timed out. Unless you answer by 15:33, I will commit P4 unsigned."* You have three answers:

- **Wait for me** holds the run until you say what to do.
- **Pause baton** hands the session to you.
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

With the defaults on a 1M-token model, baton asks for a checkpoint at 486k tokens and asks you at 729k, and Claude Code compacts on its own at about 777k. `/baton setup` prints the numbers for your settings.

`BATON_HOME` moves `~/.baton` elsewhere.

## Privacy and safety

- **Typing.** baton types into the terminal it hosts: `/compact` at boundaries, and short `[baton] …` reminders when a session stalls. It never types while a dialog is open or you've typed in the last few seconds, with one exception: it answers its own question when nobody has, by typing `3`, and only while that question is the only dialog open (in a permission prompt, `3` means No). An unsent **draft** no longer holds it either: after a grace period baton saves the text among the run's drafts and clears the box (`baton drafts` gives it back), because a message nobody sent used to be able to park a whole run. It will not do that twice inside two minutes, however the flag reads. Every action is announced in the session and logged to the run's `events.jsonl`.
- **Git.** baton never commits, stages or changes your files. All it writes to a repository is its snapshot refs under `refs/baton/`, and a line in `.git/info/exclude` that keeps `.baton/` out of git. The only git commands it ever refuses are the ones above, and only while another baton session works in the same checkout.
- **Settings.** baton never edits your Claude Code settings. Its hooks, status line and an allow rule for its own CLI are passed to each hosted session with `claude --settings`.
- **Notifications.** Pushes go to the ntfy topic you configure and say only that you're needed, unless you turn on `details`.
- **Downloads.** The binary download is verified against checksums committed to this repository. Set `BATON_BIN` to use a binary you built yourself.

See [SECURITY.md](SECURITY.md).

## Uninstall

```sh
claude plugin uninstall baton@baton
claude plugin marketplace remove baton
rm -rf ~/.baton          # binaries, config, and records of sessions handed over
```

Then remove the `baton init` line from your shell config, and the `.baton/` directory from any project you ran baton in. In a git project, this removes baton's snapshots:

```sh
git for-each-ref --format='%(refname)' refs/baton/ | xargs -n1 git update-ref -d
```

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
