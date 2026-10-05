# Changelog

All notable changes to baton are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and baton uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

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

[Unreleased]: https://github.com/ozzyfromspace/baton/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ozzyfromspace/baton/compare/v0.1.0-rc.2...v0.1.0
[0.1.0-rc.2]: https://github.com/ozzyfromspace/baton/compare/v0.1.0-rc.1...v0.1.0-rc.2
[0.1.0-rc.1]: https://github.com/ozzyfromspace/baton/releases/tag/v0.1.0-rc.1
