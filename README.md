# baton

**Run a long, multi-phase Claude Code plan unattended. baton compacts the context at every phase boundary, in one live terminal session you can watch.**

You plan the work in plan mode, approve it, and walk away. baton keeps the session moving: at each phase boundary it compacts the context, hands the model a brief for the next phase, and wakes it up. If something needs you, it tells you on whatever device you're on.

> Status: **release candidate** (v0.1.0-rc.1). macOS and Linux. Windows is planned.

## Why

Long plans run best when the context is compacted at each phase boundary: fewer tokens, sharper output, and every phase starts from a clean set of facts. But in Claude Code only a human can type `/compact`, so someone has to sit by the terminal. Asking the model to compact itself fails in practice:

- it forgets;
- it mistakes the session token budget for its context window;
- it says "compacting" without doing it;
- after a compaction it can sit idle for hours before anyone notices.

baton takes every one of those decisions away from the model. The model does the work; deterministic code decides when to compact, when to resume, and when to call you.

## How it works

- **Hosting.** `baton` starts `claude` inside a pseudo-terminal it owns and passes every byte through. You see and use the normal Claude Code interface.
- **State.** The plan's phases live in `.baton/` in your project, which is kept out of git. The model reports progress with `baton done`, `blocked`, `waiting` or `checkpoint`.
- **Phase boundaries.** When a phase is done, baton's hooks (not the model) decide to compact. baton waits until no turn, dialog, subagent or human draft is in the way, types `/compact` itself, and confirms it ran. Then it injects a brief for the next phase, quoted from your plan, and wakes the model.
- **Silent stops.** A stop without a status is refused with instructions. The model also can't start the next phase until the compaction has happened.
- **Watchdog.** It catches a session that has gone quiet: idle turns, expired waits, unanswered prompts, usage limits.
- **Escalation.** When baton needs you, it asks in the session (a question that reaches every device signed in to Claude) and sends its own push notification.
- **Status line.** A permanent `◆ baton · P6 6/23 <title> · ctx 41%` segment sits in front of your own status line.

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

That line puts `baton` on your PATH. It also lets a plain `claude` session hand itself to baton automatically (see [Elevation](#elevation)).

## Use

```sh
baton              # instead of `claude`; it takes the same arguments
```

Inside the session:

| Command | What it does |
|---|---|
| `/baton plan <goal>` | Plan the work in plan mode, in a format baton can run, then attach the approved plan and start it. |
| `/baton attach [plan.md]` | Run a plan that already exists. Defaults to the plan you just approved. |
| `/baton status` | Show where the run stands. |
| `/baton pause` / `/baton resume` | Take the wheel and give it back. While paused, baton observes but never acts. |
| `/baton elevate` | Hand a plain `claude` session to baton (see below). |
| `/baton setup` | Check this machine and list what's missing. |

baton finds phases by short ids at the start of headings, such as `## P0 — Title`, `### P23: Title` or `## Phase C: Title`. They can also be bold bullets or table rows. `/baton plan` writes plans in that format.

### Elevation

Started a session with plain `claude` and want baton to drive it? Run `/baton plan …` or `/baton attach …` there. baton records the session, stops `claude` cleanly when the turn ends, and your shell relaunches **the same conversation** under baton. This needs the `baton init` line above. Without it, baton tells you to exit and run `baton --resume <session id>`.

## Configure

Optional settings live in `~/.baton/config.json`. Environment variables override the file.

| Key | Env | Default | Meaning |
|---|---|---|---|
| `ntfy_topic` | `BATON_NTFY_TOPIC` | none | Push notifications through [ntfy](https://ntfy.sh). Treat the topic like a password. |
| `ntfy_server` | `BATON_NTFY_SERVER` | `https://ntfy.sh` | Your own ntfy server. |
| `desktop` | `BATON_NOTIFY_DESKTOP` | `true` | Desktop notifications (macOS, and Linux with `notify-send`). |
| `details` | — | `false` | Include the reason text in pushes. Pushes are content-free by default. |
| `autocompact` | `BATON_AUTOCOMPACT` | `400k` | Launch-time cap on the context (`claude --autocompact`). `off` disables it. |
| `checkpoint_pct` | `BATON_CHECKPOINT_PCT` | `60` | Context fill (%) at which baton asks the model to checkpoint at its next safe point. |

`BATON_HOME` moves `~/.baton` elsewhere.

## Privacy and safety

- **Typing.** baton types into the terminal it hosts: `/compact` at boundaries, and short `[baton] …` reminders when a session stalls. It never types while you have a draft in the input box, a dialog is open, or you've typed in the last few seconds. Every action is announced in the session and logged to `.baton/events.jsonl`.
- **Settings.** baton never edits your Claude Code settings. Its hooks, status line and an allow rule for its own CLI are passed to each hosted session with `claude --settings`.
- **Notifications.** Pushes go to the ntfy topic you configure and say only that you're needed, unless you turn on `details`.
- **Downloads.** The binary download is verified against checksums committed to this repository. Set `BATON_BIN` to use a binary you built yourself.

See [SECURITY.md](SECURITY.md).

## Uninstall

```sh
claude plugin uninstall baton@baton
claude plugin marketplace remove baton
rm -rf ~/.baton          # binaries, config and elevation records
```

Then remove the `baton init` line from your shell config, and the `.baton/` directory from any project you ran baton in.

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
