# Changelog

All notable changes to baton are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and baton uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **Context warning.** At 90% of the context limit (never under 200k tokens), baton has the model ask you, with a fixed `AskUserQuestion`, whether to checkpoint now or keep going. If the question goes unanswered, the watchdog sends a push. Set `warn_pct` to `0` to turn it off.

### Changed
- **`autocompact` defaults to 810k** (was 400k). With the defaults on a 1M-token model, baton asks for a checkpoint at 486k, asks you at 729k, and Claude Code compacts on its own at about 777k.
- **The context is measured in tokens.** baton records the exact size from the status line's `current_usage` and works out the limit itself (`min(model window, cap)`), because Claude Code reports the model's window, not the cap. The status segment shows `ctx 412k/810k`; `baton status` and `/baton setup` show where each threshold falls.
- **An `--autocompact` passed to `baton` wins** over the config, and baton validates its own setting before launching `claude`, which would otherwise refuse to start.

### Fixed
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

[Unreleased]: https://github.com/ozzyfromspace/baton/compare/v0.1.0-rc.1...HEAD
[0.1.0-rc.1]: https://github.com/ozzyfromspace/baton/releases/tag/v0.1.0-rc.1
