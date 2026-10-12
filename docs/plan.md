# baton v0.1 — implementation plan

> Approved 2026-10-04. Phases are committed in order; `git log` tells the story. Research behind it: [`research/spikes.md`](research/spikes.md).
>
> **v0.7.0 recovers a run.** A turn the model's safeguards refuse is told apart from the session's transcript (`internal/transcript`, read-only and best effort) and escalated as `refused`: baton never retries it, never starts a fresh conversation by itself, and never switches models; the human rewinds or `/clear`s. A conversation cleared mid-phase gets the phase's brief (`brief.Fresh`). A rewind is noticed from the transcript's parent links (marked at each `UserPromptSubmit`, checked at the turn's next hook), and the model is told what of baton's record still stands; the record is never rolled back. A lead whose claude exits by itself mid-plan is brought back by its host (`host.Exit.By`, `internal/cli/recover.go`): resumed, at most twice in 30 minutes, or started fresh from the brief once per phase when it cannot be resumed. `baton verify --here` runs a gate in the session's own working tree and holds edits, switches and landings there (`state.HereGate`); a gate wait says nothing is needed from the human; and the lead's commits verify a landing again at most once. The evidence is in [`research/recovery.md`](research/recovery.md).
>
> **v0.6.0 sizes the workers itself, and ends what nobody uses.** `workers` is `auto` by default: the most workers at once is one per 2 CPUs or 4 GiB, whichever allows fewer, less one for the lead, at most 8 (`internal/machine` reads the machine without cgo), and a number from 0 to 8 still sets it. A new worker also waits while the machine is busy (load, memory pressure and swap on macOS, available memory on Linux), for a minute after the last one started, while a gate has waited over a minute for a lane, and after 2 landings in a row conflicted; `worker_usage_max` is off by default. A run counts its plans (`State.Generation`), so a replaced plan's workers are retired and their unlanded branches kept under `baton/<run>/kept/`; `loop.RetireWorker` ends a worker from any baton process, guarded by a mark on the roster. When a plan is attached, the workers of the project's abandoned runs (`state.Abandoned`: no live host, no baton of theirs running, quiet for 2 minutes) are retired, and a host with workers off retires its own run's. `baton clean` ends what no live session uses: those workers, their worktrees and jobs, idle lanes, processes they left under `~/.baton` (found without `ps` or `lsof`, `internal/procs`), and leftover files. Where the v0.5 note below says 1 by default, 0 to 3, read `auto`, 0 to 8. The evidence is in [`research/workers.md`](research/workers.md), "Machine readings and leftovers".
>
> **v0.5.0 works on phases at once.** The session in the human's terminal is the *lead*: it works as before, one phase at a time in the checkout. Beside it, the host starts up to `workers` hidden *worker* sessions (1 by default, 0 to 3; 0 is v0.4's behaviour), each in a PTY of its own, on a phase that can start, in a git worktree under `~/.baton/worktrees/` on a branch `baton/<run>/<phase>`. A worker is a sub-run of the run (`.baton/runs/<run>/workers/<phase>/`), so everything a phase does runs unchanged inside it. One `host.Supervisor` owns the terminal and the sessions, and a `loop.Coordinator` ticks one `Loop` per session and does what belongs to the run: starting and resuming workers, the merge queue that lands a finished worker's branch on the run's branch with `git merge --ff-only` under the lead, the relay that puts a worker's questions to the human through the lead, usage limits, and the upgrade restart. A permission prompt in a worker waits for the human, who opens the worker with `baton view <phase>` in another terminal. Where this plan says one session, read one per lead or worker. A request PostToolUse adds mid-turn (the checkpoint nudge, a job's result, a question to ask) now goes as hook feedback (`decision: block`) rather than `additionalContext`, which Haiku sometimes refused as possible injection. The evidence is in [`research/workers.md`](research/workers.md).
>
> **v0.4.0 keeps a run from waiting on its gates.** A plan may name what each phase waits for (`After: P1, P3`, `none`, `all`; without the line, the phase before), and the run takes up the next phase that can start. The model hands a long gate to baton: `baton verify` runs it on `HEAD` in a lane (a worktree outside the checkout, set up by the project's own `.baton-lane`), and `baton await` waits on something outside the project. A finished phase is reported done while its gate runs, and is done when the gate passes or reopens when it fails; a phase that can't go on without its result is set aside, and the host switches the session to another phase. Phases still run one at a time, in one session. `baton waiting` and the declared wait are gone: where this plan has them (the Stop table, P3's CLI, P9's watchdog), read `verify` and `await`, and a long command that moves to the background by itself (`foreground_max`). The design's evidence is in [`research/parallel.md`](research/parallel.md).
>
> **v0.3.7 folds resume into run.** `/baton run` resumes this session's paused or blocked plan, and attaches any other; `/baton resume` and `baton resume` are gone. Where this plan says resume, read run.
>
> **v0.3.2 renames two commands and moves the skill into the binary.** Elevation is now `/baton start` (in a shell, `baton start` starts a session) and `/baton stop` is `/baton drop`. The plugin's `SKILL.md` is a stub that loads its instructions from the binary (`baton skill`), so an update reaches every session's `/baton` at once. Where this plan says elevate, read start.
>
> **v0.3.1 moves running sessions onto an update.** A host that finds a newer compatible baton installed (`internal/upgrade`) restarts its session on it in place, by exec, resuming the same conversation: once idle, or at the next phase boundary while a plan runs, where the restarted session takes the compaction. Across a major version the session stays put and says so once.
>
> **v0.3.0 keeps a run per session and attaches the plan the human approves.** Each Claude Code session has its own run under `.baton/runs/`, so a second terminal in the same project runs a plan of its own instead of running as plain claude; approving a plan with phase headings attaches it and starts P0 after a compaction. Where this plan speaks of one plan per project, read one per session. The evidence is in [`research/sessions.md`](research/sessions.md).
>
> **v0.2.0 changed how a run reaches the human.** The model has three ways to do it (`note`, a timed `propose`, and `blocked --tried`). baton snapshots uncommitted work whenever the run halts, and runs a plan the same way in a folder without git. That design, and the spikes behind it, are in [`escalation.md`](escalation.md). Where this plan says a blocked run escalates, read that.

## Context

The workflow: plan in plan mode, then run a multi-phase plan across many compactions, compacting often to cut tokens, keep quality high, and give each phase a clean set of facts. Today that needs a human at each boundary. A previous crude fix, a `self-compact.sh` script, had the model background a script that types `/compact` into iTerm2 via AppleScript. It fails because:
- the model forgets to run it, or misreads the `<total_tokens>` session budget as its context window;
- exit codes get swallowed;
- long focus text gets pasted rather than typed;
- it only works on macOS/iTerm2;
- after compaction the session can sit idle for hours with nobody noticing.

**Outcome.** **baton** is a self-contained, distributable Claude Code plugin plus one compiled Go binary. It runs an approved plan phase by phase in **one live, visible, interactive session**. Every control decision (compact, resume, escalate, notify) is made by deterministic host code, never by the model's memory or judgment.

## Agreed decisions

- **One conversation, compacted** (not a fresh session per phase).
- **Live terminal always visible.** No headless driver.
- **No external deps.** No tmux, no AppleScript, no `claude` PATH shim.
- **baton hosts `claude` in its own PTY:** a byte-for-byte passthrough that types `/compact` itself at boundaries.
- **Go.** The plugin launcher downloads a sha256-pinned GitHub release on first run. The manifest's `binaries` field is gated to allowlisted marketplaces, so it can't be used.
- **Elevation.** A plain `claude` session elevates automatically through an **opt-in** `eval "$(baton init zsh)"` line. Without it, you type `baton` once.
- **Permanent baton segment in the status line.**
- **Public repo `github.com/ozzyfromspace/baton`.** Conventional commits per coherent unit, pushed at each phase end, so the history tells the story.
- **Auto mode assumed.**

## Proven by spikes (Claude Code 2.1.289, 2026-10-04)

| Mechanism | Result |
|---|---|
| Stop hook `{"decision":"block","reason":…}` | ✅ model keeps working |
| `autoCompactWindow` changed mid-session | ❌ only read at launch (usable as a launch-time cap) |
| Scheduled `/compact` (CronCreate) | ❌ arrives as plain text |
| `/compact` typed through our own PTY | ✅ PreCompact `trigger: manual` |
| `SessionStart` (matcher `compact`) `additionalContext` | ✅ brief reaches the model |
| `PostCompact` + `asyncRewake: true`, exit 2 | ✅ wakes the idle model, which continues |
| Passthrough wrapper (spike 7) | ✅ Stop → typed `/compact` → brief → rewake → next phase, ~25s |
| Elevation (spike 8) | ✅ SIGTERM clean in <1s; prompt hook relaunches `claude --resume <id>` (same id and context); `SessionStart` asyncRewake hands over the pending command; ~9s |
| `claude --settings '{"statusLine":…}'` | ✅ overrides the status line for that session only |
| Haiku | refuses opaque "run this script" prompts, so the skill must explain commands honestly |

## Architecture

```
terminal ──bytes──▶ baton host (PTY owner) ──bytes──▶ claude (interactive TUI)
                    │ injector · watchdog · notifier     │ hooks (passed via --settings) ──▶ `baton hook <event>`
                    │                                     │ model ──Bash──▶ `baton done|blocked|checkpoint|verify|await`
                    └──────── .baton/ (locked files, one schema, events.jsonl) ◀──────┘
```

**Where hooks live.** The host generates the hot-path hook config and passes it to claude in the same `--settings` JSON as the statusLine and the `--autocompact` cap. That means:
- zero overhead in plain sessions;
- hooks always match the running binary.

The plugin itself ships only the `/baton` skill, `bin/` launchers, and one **Stop hook for elevation**. That hook is gated in the launcher's first line: a shell test for a pending elevation marker, so no Go binary starts.

**Exit-code policy.** For hooks, exit 2 means "block", and Go panics and flag errors also exit with 2.
- `baton hook` wraps everything in `recover`, uses `ContinueOnError`, and **fails open with exit 0** (the watchdog is the backstop).
- Exit 2 is emitted only by the explicit rewake path.
- The launcher never exits 2.
- Each event gets a test that a panic exits 0.

**State.** `.baton/`, excluded through `.git/info/exclude` so your repo isn't touched:
- `plan.json`: phases with **verbatim anchors** that the model writes and the binary validates (each anchor appears exactly once in the plan doc). Heading styles vary across your plans (bold bullets, tables, `## Phase C:`), so there's no heading parser.
- `run/<instance>/state.json`: runtime state machine, owned by one host instance (pid + heartbeat). A second host in the same project refuses to run the plan; a stale owner can be taken over.
- `events.jsonl` (one line per decision), `handoff.md` (model notes), `baton.log`.

All writes are locked and atomic.

**Env contract.** The host sets `BATON_HOST=1`, `BATON_DIR`, `BATON_INSTANCE` and `BATON_BIN` (its own binary, which the generated hooks call directly).

**Turn and dialog awareness.** Hooks record:
- who started each turn (`UserPromptSubmit` = human, versus a rewake or nudge);
- open dialogs (`AskUserQuestion` / `ExitPlanMode` Pre without Post, `Notification` permission_prompt / elicitation);
- subagents (`SubagentStart`/`SubagentStop`).

**The injector types only when all of these hold:**
- the last turn event was `Stop`;
- no dialog is open;
- no subagents or background agents are running;
- the screen has been quiet for ≥1.5s and the human hasn't typed for ≥3s;
- the input box is known to be clean (method chosen in P1).

Escape, Ctrl-C or a human prompt pauses autonomy until the next `Stop`.

**Phase-boundary loop:**
1. The model finishes a phase and runs `baton done P6`. A missing commit is a warning only. It then ends its turn.
2. The **Stop** decision table:

| State | Action |
|---|---|
| Human started this turn | allow the stop |
| Phase done | allow; queue compaction; `systemMessage` "baton: P6 done → compacting" |
| Plan complete | allow; notify; **no compaction after the final phase** |
| Blocked | escalate |
| Declared wait within its window | allow; the watchdog owns the deadline |
| No status | block with a fixed reason; after N consecutive blocks (default 3), escalate |

v0.2.0 adds three rows: a review due after too many decisions without the human (checked before a phase boundary), a proposal not yet put to the human (refused with the exact question), and a proposal the human held. See [`escalation.md`](escalation.md).

3. The **injector** types `/compact` (8 chars, then a lone CR).
4. **PreCompact** records `{epoch, trigger, requested_by_baton}` and acks. If there's no ack in 15s: retry once, then escalate.
5. **SessionStart(compact)** builds the brief from that record:
   - after a boundary: the next phase's anchored section, the handoff notes and the standing rules;
   - after an auto-compaction mid-phase: "continue P6".
6. **PostCompact** (matcher `manual`, asyncRewake) exits 2 **only for a baton-requested epoch** with "baton: compacted; begin P7". Any compaction after `done`, including an auto one, counts as the boundary, so there's never a double compaction. No PostCompact within 180s of the ack → escalate.

**Mid-phase valves:**
- The status line feeds the exact context size (`context_window.current_usage`) and the model's window into state ([measurements](research/context-window.md)). The limit is `min(window, cap)`.
- Over a soft threshold (60% of the limit), PostToolUse (main agent only, never inside subagents) adds "at your next safe point run `baton checkpoint` and end your turn" → same loop.
- Over the warning line (90% of the limit, never under 200k, and always before Claude Code's own compaction), the model is told to ask the human with `AskUserQuestion`, using fixed text and options: "Checkpoint now", "Pause baton" or "Keep going". Unanswered, the host picks "Keep going" by typing `3`, and only while that question is the only dialog open ([why](research/escalation.md)).
- Hard backstop: launch with `--autocompact <cap>` (default 810k). Claude Code compacts on its own 33k tokens below the limit.

**Escalation (two layers):**
- **In session:** a Stop block with a fixed reason makes the model call `AskUserQuestion` with fixed text and options, which reaches all devices.
- **Out of band:** the host sends its own push (ntfy over `net/http` + native desktop notification). Content-free by default.

**Watchdog:**
- Liveness = hook heartbeats, never running processes.
- Timers pause while a human or dialog is active, and reset after sleep or wake (clock jumps).
- Per-state deadlines: ack 15s · compaction 180s · rewake → first activity 60s · idle 10 min · background work 60 min.
- **StopFailure:** `rate_limit` → one push, then resume when the limit resets; `overloaded` → back off and nudge.
- **SessionEnd** mid-plan → push, with the reason (crash vs deliberate).
- Nudges go through the injector, so they're dialog-safe.

**Visibility:**
- Every host action appears inline as a `systemMessage` and as a line in `events.jsonl`.
- Status segment: `◆ baton` · `◆ baton · P6/23 <title> · ctx 41%` · `· compacting…` · `· ⚠ waiting on you`.
- `baton statusline` runs **your own** status line command with the same stdin and prepends the segment. It never calls itself.

**Elevation (v0.1: macOS/Linux, zsh/bash):**
1. The `/baton` skill notices there's no `BATON_HOST`. With you present, the model confirms it has no running background tasks or subagents.
2. It runs `baton elevate "<original command>"`, which records the session id, cwd, tty, the original argv (from `ps`) and the permission mode in `~/.baton/elevate/<tty>.json` (used once, ignored after 60s).
3. The plugin's Stop hook confirms the turn has ended, then SIGTERM, then the marker is cleaned up.
4. The `baton init` prompt hook runs `baton --resume <id>` with the original flags.
5. `SessionStart(resume)` asyncRewake delivers the pending command.

**Launcher and install:**
- `bin/baton` (sh) and `bin/baton.ps1` / `.cmd`:
  - first line: a fast `BATON_HOST` / marker check;
  - curl or wget for the download; shasum, sha256sum or Get-FileHash to verify `checksums.txt`;
  - installs to `~/.baton/bin/<ver>/`; `BATON_BIN` override for dev and offline use; quoted paths.
- The plugin's `bin/` is on the Bash tool's PATH. `/baton setup` does the first download and prints the init line and the `Bash(baton:*)` allow rule for auto mode.
- **Release order:** reproducible cross-build (`CGO_ENABLED=0 -trimpath -buildvcs=false`, pinned Go) → commit `checksums.txt` → tag → CI rebuilds and verifies the hashes match → upload the release assets.

## Phases

Each phase ends with its verification, commits, and a push.

- **P0 Bootstrap:**
  - `brew install go`, `git init`, `gh repo create ozzyfromspace/baton --public`.
  - README, MIT LICENSE, SECURITY.md, CHANGELOG, `.gitignore`, repo `CLAUDE.md`.
  - `docs/research/spikes.md` plus `spikes/` archived **scrubbed** of absolute paths and session ids. `cc-strings.txt` is never committed.
- **P1 Verification round 2** (throwaway scratch projects; results appended to `docs/research/`), checking:
  - Do `--settings` hooks (including asyncRewake) merge with user and plugin hooks, and do they survive `--resume`?
  - Is the brief (SessionStart compact) in place before the PostCompact rewake fires?
  - What happens to `/compact` typed while the model is busy, or next to an autocomplete clash?
  - Input-box clean method: a clear-line key versus keystroke tracking.
  - Dialog signals: AskUserQuestion, permission prompt, ExitPlanMode.
  - `StopFailure` / `SessionEnd` input shapes.
  - Is the plugin `bin/` on the Bash PATH?
  - p95 hook latency.
  - Does the auto-mode classifier allow `baton` commands (Sonnet smoke run)?
  - `CLAUDE_PID` / `CLAUDE_CODE_SESSION_ID` availability.
  - Linux and Windows items get flagged for CI or later.
- **P2 Go skeleton:**
  - `cmd/baton` router; packages behind a `pty` interface (unix: creack/pty; windows stub compiles).
  - Exit-code policy and panic tests.
  - Makefile; CI running `go test` on macOS, Linux and Windows, plus a `GOOS=windows` build.
- **P3 Plan and state:**
  - Anchor-based `plan.json` + validation; per-instance state with owner lock; `events.jsonl`.
  - CLI: `attach`, `status`, `done`, `blocked`, `waiting`, `checkpoint`, `pause`, `resume`.
  - Fixtures copied from real plan documents.
- **P4 PTY host + E2E harness:**
  - Passthrough: raw mode, resize, signals, exit codes; terminal restored even on panic.
  - Launch args: generated `--settings` hooks + statusLine, `--autocompact` cap.
  - Keystroke tracking; injector state machine.
  - Go PTY test harness (a stub child for integration tests, and real `claude` for E2E).
- **P5 Hooks and plugin skeleton:**
  - Dispatcher for SessionStart (startup/resume/compact), Stop, StopFailure, Subagent*, UserPromptSubmit, Pre/PostToolUse, PreCompact, PostCompact, Notification and SessionEnd. Fail-open.
  - Plugin manifest, `marketplace.json`, gated elevation Stop hook.
  - Dev launcher (`BATON_BIN` → local build); `claude --plugin-dir`.
- **P6 Boundary loop + basic escalation:**
  - Stop table, compaction epochs, injector → ack → brief → rewake.
  - Retry once → escalate; per-state deadlines; desktop + ntfy notifier; narration.
  - **E2E (Haiku):** a two-phase plan with zero keystrokes; a refused stop; blocked → question + push.
- **P7 Status line + context valve:**
  - `baton statusline`; context % into state; soft checkpoint nudge; cap config.
  - E2E: mid-phase checkpoint.
- **P8 Skill `/baton`:**
  - `SKILL.md` covering help, plan, attach, status, pause, resume, elevate, setup.
  - Plan flow: EnterPlanMode → approval → the model writes anchors → `baton attach` validates.
  - Honest wording; verify the invocation name (`/baton` vs `/baton:baton`) and arguments; skill-creator test prompts.
- **P9 Watchdog:**
  - Heartbeats, declared waits, human/dialog pause, clock jumps, StopFailure and SessionEnd handling, dialog-safe nudges.
  - Fake-clock tests; E2E: a stalled model gets nudged.
- **P10 Elevation:**
  - `elevate`, `init zsh|bash`, used-once markers + cleanup, Stop-confirmed SIGTERM, resume with the original flags, pending rewake.
  - E2E in a zsh with an isolated config folder (`ZDOTDIR`).
- **P11 Distribution:**
  - Launchers, checksums, release workflow, minimal `baton doctor` (Claude Code minimum version, install sanity).
  - README install and uninstall, privacy note.
  - Clean install on macOS: `claude plugin marketplace add ozzyfromspace/baton` → install → `/baton setup`.
  - Tag **v0.1.0**.
- **P12 Windows (may slip to v0.2):** ConPTY host, PowerShell launcher, toast notifications, CI. Manual check flagged for you or a VM.
- **P13 Acceptance:**
  - Full E2E suite + Sonnet auto-mode smoke run.
  - **The owner's manual run on a real plan with Opus and auto mode**; quirk fixes; final docs.

**Deferred past v0.1:** tab-title prefix (rewriting OSC sequences in the byte path is risky), fish/pwsh init, webhook notifier, Windows elevation, transcript-based context meter.

## Verification

- `make test`: unit tests on all 3 OSes in CI, covering:
  - the Stop table and compaction epochs
  - the injector gates
  - anchors
  - the brief
  - the statusline wrapper
  - the watchdog (fake clock)
  - the exit-code policy
- `make e2e` (`BATON_E2E=1`): real `claude --model haiku` through the PTY harness with inherited `CLAUDE*` env scrubbed, plus one Sonnet auto-mode smoke run.
  - Scenarios: boundary, refused stop, checkpoint, escalation, nudge, StopFailure simulation where possible, elevation.
  - Assertions read `events.jsonl` and hook logs, never the screen.
  - About $0.10–0.50 per run.
- `claude plugin validate ./`, then a clean install from the public marketplace.
- **Final acceptance (owner):** a real plan, Opus, auto mode, a real terminal.

## Risks

- **Human typing at the boundary:** turn, dialog, quiet and clean-box gates; ack, retry once, escalate; every injection announced first.
- **Claude Code updates change hook behavior:** `baton doctor` checks the version; the E2E suite re-runs on every Claude Code update.
- **The auto-mode classifier blocks `baton`:** the documented allow rule. Plugins can't grant permissions.
- **Windows and Linux can't be verified on the development Mac:** CI plus flagged manual checks. Linux E2E needs an API key in CI, which is optional.
