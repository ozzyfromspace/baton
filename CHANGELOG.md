# Changelog

All notable changes to baton are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and baton uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
are in [docs/escalation.md](docs/escalation.md).

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

[Unreleased]: https://github.com/ozzyfromspace/baton/compare/v0.3.4...HEAD
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
