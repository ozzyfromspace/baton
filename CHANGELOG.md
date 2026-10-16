# Changelog

All notable changes to baton are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and baton uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.8.6] - 2026-10-16

A read-only review of a real run's last 15½ hours (a 57-phase plan, hosted by v0.8.4 and then v0.8.5) found
nothing that threatened to derail it, but a list of lost time and a few latent bugs, and v0.8.5's own release
and its records showed more. The lead checkpointed 9 times mid-phase against 7 phase boundaries: baton asked for
a checkpoint at 60% of the limit (486k of 810k), and every compaction restarted the lead at about 180k, about
32k of it the plan file, which Claude Code attaches in full again after every compaction. A false stall alarm
was pushed for a QA gate whose tests printed nothing for an hour while they worked. The run had 6 lock timeouts
and a failed host heartbeat, and left 7 stray temporary state files, and in v0.8.5 the loop tests got slower on
every system (Linux 18 s to 20 s, macOS 81 s to 116 s, Windows about 8 to 11 minutes). v0.8.4 shipped a regression
its own full end-to-end gate had caught, because a narrower gate that passed stood in for the red one. A stray
key could misanswer two of baton's questions, and a phase agent you stopped twice could stall a run for good.
So the model now gets a heads-up at 60% and the checkpoint ask at 80%, baton moves the plan file out of Claude
Code's way, and each of the rest is fixed. And you can now write to a phase agent from your own prompt,
`@P3 <your words>`, so each phase's id must be a name Claude Code takes for an agent.

A session on v0.8.x moves to v0.8.6 by itself at its next restart point; `baton update` updates baton's host
and its mod together. A run attached before v0.8.6 from the session's plan-mode file has that file moved before
the compaction that starts the lead's next phase. A `checkpoint_pct` you set keeps its value. The planning rules
did not change (`plan.RulesVersion` stays 1), so no run is replanned for this upgrade. A running plan keeps the
phase ids it has; a new plan, a replan's draft and a phase added to the plan file need ids an agent can take.

### Added
- **A heads-up at 60% of the context.** At `notice_pct` (60 by default, `0` off; `BATON_NOTICE_PCT`) the model
  is told where its context stands, that nothing is asked of it now, and to make the room last (a line range or
  a grep rather than a whole file, short command output), beside a tool result and never as a block, once a
  stretch (`context_notice`). The lead and phase agents get it, and `baton setup` and `baton status` show its
  line.
- **The plan moved out of Claude Code's way.** After every compaction Claude Code attaches the session's
  plan-mode file (`<plansDirectory>/<slug>.md`) to the conversation in full while it exists
  (`plan_file_reference`), on top of baton's brief; no setting turns it off. Spike 38 found that renaming the
  file stops it, and that a later planning simply writes a fresh one. So when baton attaches a plan from that
  file, and no other live run uses it, it renames it in its folder to a name from the plan's title (never over
  another file), follows it there, and tells you the new path (`plan_moved`, or `plan_not_moved` with why;
  `plan.json` keeps the old path as `plan_origin`). An approval at the old path is a revision of the run's plan,
  as one of its own file was, and is moved aside in its turn.
- **What phase agents saved.** When a plan completes, baton says it: "phase agents saved about 3h20m: 8h29m of
  phase work in 5h09m". The work counts each agent's phase from its start to its finish (`Worker.Finished`), not
  to its landing, and the wall clock runs from the first phase's start. `plan_complete` logs `agent_phases`,
  `work_seconds`, `wall_seconds` and `saved_seconds`, and `baton status` shows the line for a complete plan.
- **A replan's brief names phases that share files.** It lists the phases not started that could run at once and
  whose sections name the same file, as information: they collide when they land (`replan_opened` logs
  `shared`). What a replan may change is the same.
- **Write to a phase agent from your prompt.** `@P3 <your words>` at your session's prompt sends the words to
  P3's agent, framed as yours: it gets them at its next tool call, and an idle one wakes for them and goes on with
  its phase. No turn starts in your session and its model never sees them; Claude Code shows you baton's "sent to
  P3", and baton empties the input box a second later, unless you've typed since. For a phase no agent of baton's
  works on (not started, done, your session's own, one you stopped), nothing is woken: the words go to your
  session's model, which is told where that phase stands, and you're told why; so do words an agent could not take
  after all. Each step is logged `human_to_agent`. The board shows each working agent's handle, an agent's
  question to you names it, and `/baton help` says how. Spike 40 found that Claude Code sends `@name` at the main
  prompt only to agent-team teammates, and that a hook's block keeps a prompt from the model at no cost: no turn,
  nothing in any request.
- **An agent baton let go of changes nothing if it wakes.** Typing in an agent's own view in Claude Code's agent
  panel wakes it whatever it is, and one whose unchanged worktree Claude Code removed wakes in your checkout.
  baton now records every agent it lets go of (retired, or replaced by a fresh one) and refuses its every tool call
  with why, so it ends its turn having changed nothing, and nothing waits on it (`retired_agent_woke`, once).

### Changed
- **The checkpoint is asked at 80% of the context,** not 60% (`checkpoint_pct`). With an 810k cap: the
  heads-up at 486k, the checkpoint at 648k, your question at 729k (`warn_pct`, unchanged), Claude Code's own
  compaction at 777k. A stretch from a 180k floor has about 470k tokens of room instead of about 300k, so about a
  third fewer mid-phase checkpoints; the 129k left before Claude Code compacts was about 14 minutes at the real
  run's pace, where models acted within 1 to 10 minutes of the ask. Where a small window would put any line at or
  after Claude Code's own compaction, every line is its percentage of where Claude Code compacts.
- **A red gate stands until the same check passes.** After a phase's `verify` goes red, a passing `verify` of
  another command (one test of a red suite) no longer clears it: the model is told so as it queues it
  (`red_gate` on `job_queued`) and when it passes, and `baton done` names the command that must pass. `baton
  verify --replace` makes another command the phase's gate, recorded as a decision you're told of
  (`replaces_gate`); `--despite-red` is unchanged.
- **A result lists failures from the whole log.** Before the log's end, a job's result lists the lines that
  report a failure anywhere in it (`--- FAIL`, `FAIL`, `FAILED`, `panic:`, `✗`; at most 20, each clipped), so a
  failure early in a long log is no longer lost behind its tail. The log of every red job is kept, and goes with
  the plan's record into `gen-<N>/jobs/` when another plan replaces it.
- **Wait for me comes first on the mid-run plan question.** Its options were Start the new plan from P0, Wait
  for me, then the default, so a second Enter set the run's progress aside. Now they are Wait for me, Start the
  new plan from P0, then the default (Continue with the revised plan, or Keep the current run): a stray Enter
  holds the run, and lapses into the default.
- **Serial mode answers its own question by Esc, never a digit.** Where baton's mod doesn't run, the host typed
  `3` into its own unanswered question, on the premise that `3` is "No" in a permission prompt should one be in
  front of it. On Claude Code 2.1.30x it is "Yes, and switch to auto mode" (spike 39). The host now dismisses the
  question with Esc, which picks no option in any dialog (it rejects a permission prompt, and leaves a draft and
  background agents alone), and records the question's default itself, since no hook reports a dismissal
  (`auto_answered` with `key: esc`). It still acts only while its question is the only dialog open.
- **Unchanged state is not rewritten, and macOS syncs with a plain `fsync`.** Every update rewrote and synced
  `state.json` even when nothing changed, and on macOS Go's sync is `F_FULLFSYNC`, which stalls for seconds on a
  busy disk (per 6 KB write under load: 21 to 40 ms, against 13 to 15 for `fsync`). An update that changes
  nothing now writes nothing, a bridge's too (1,899 of 4,435 bridge writes in the loop tests changed nothing),
  and the write's rename stays atomic.
- **Background work is never polled.** baton's reminders of the model's background work, and the skill, now say
  never to start a loop or another background task only to wait for one: Claude Code wakes the model when it
  ends. The real run's lead had started such loops.
- **Phase ids are agent names.** Each phase agent is named for its phase, and Claude Code takes for a name only
  letters, digits, `_` and `-`, and none of `main`, `team-lead`, `user`, `system` or an agent id's shape. A plan's
  ids are now held to that, within baton's 32 characters (so no dot: `P8b`, not `P8.5`), and no two may be equal
  ignoring case, as Claude Code matches a name ignoring case. A plan with another id is turned back at
  `ExitPlanMode` with why, and not attached; so is a replan's draft, or a phase added to the plan file.
- **A model's agent can't take a phase's name.** Claude Code gives a name to the latest agent that took it, so a
  model's own Agent call named for one of the plan's phases, ignoring case, is refused with why
  (`agent_name_refused`). baton's own messages go to an agent by its id.
- `compact_reported`, `compact_declined` and `compact_started` are now logged before the `mod_*` event of the
  report that carries them, and `baton clean` takes a stray temporary file once it is a minute old, not ten.

### Fixed
- **The loop tests are fast again.** Lock holds and `state.json` writes per test were the same at v0.8.4 and
  v0.8.5 (75,316 against 75,443 holds; 12,193 against 12,226 writes). The slowdown was v0.8.5's record of a
  lock's holder: two writes of the lock file on every hold, reads included, 150,886 more writes in the loop
  tests, and on a loaded Mac a lock and unlock went from 0.09 ms to 3.8 ms. A holder now records itself only
  once it has held the lock for 100 ms: 22 such writes. The loop tests' per-test minimum times add up to 122.7 s,
  against 263.1 s at v0.8.5 and 229.6 s at v0.8.4.
- **No false stall alarm for a gate that works in silence.** The stall alarm called a gate whose log had not
  grown for 20 minutes hung, though the check that stops hung gates rightly also wants its processes idle. A
  quiet gate now counts as busy while its process tree uses CPU (where it can't be read, the log decides), and a
  wait on something outside (`await`), or a gate waiting for a lane, while its runner beats.
- **No compaction report is lost to a killed report process.** The mod gave `baton mod report` 5 seconds, while
  one report may wait on two locks of up to 5 seconds each, and a report killed after the bridge recorded it but
  before the run's state did was lost for good (its resend was a duplicate). A report of the lead's compaction is
  now counted in the run's state first, once, so its resend is counted, and the mod gives a report, and `baton
  board`, 15 seconds.
- **Stray temporary files are swept.** Temporary files that interrupted writes left under `.baton/` are removed
  once a minute old, as the host starts and by `baton clean`.
- **A phase agent you stopped twice no longer stalls the run for good.** Taken down with its work not done, its
  phase waited on your answer with no deadline, and when every phase left needed it, the run stopped, while the
  lead's stop line said baton would take the next phase up as soon as one could. While the rest of the run can
  go on, that is unchanged; once nothing else can move, baton waits `escalation_timeout` more, then takes the
  phase back from the agent, and the lead does it in its session, its brief saying why and where the agent's
  work is (merge its branch, never rebase). It is a decision with its undo (`/baton pause`), announced, pushed
  and logged (`stopped_phase_taken`); until then the lead's stop line names the phase and when
  (`stopped_phase_waits`), and your answer wins.
- **A phase agent's loop no longer logs "typing anyway".** It types nothing (its lines go through the mod), so
  it no longer waits for a quiet screen.
- **"No room on the machine" no longer flaps.** The log line followed every load reading, and could change every
  second; it now changes only when the kind of room the machine lacks (busy, swapping, memory) changes, and has
  held for 30 seconds.
- **A stop is no longer refused over another loop's background work.** Claude Code lists every background task
  of the session at each stop, whoever started it, so the lead's stop was refused over its phase agents'
  commands, and an agent's over the lead's or another agent's. Each loop now waits on, and is told of, its own
  background work only.

## [0.8.5] - 2026-10-16

A read-only look at a long real run found baton itself healthy: no questions, blocks or deadline answers in 13
hours, and a 3-second move to v0.8.4. The time went to the plan's shape. Its longest chain held 42 of its 57
phases, so of the 12 phases it ran in those 13 hours, 2 ran beside the lead. One phase on that chain verified
itself by waiting on CI, which went red 5 times over 3 hours (runs of 14 to 48 minutes), and the phase after it
waited all that time. Of ten plans saved on one machine, eight had a longest chain over 60% of their phases, and
six were one chain. So `/baton plan` now builds a graph by six shape rules, baton shows a plan's shape as it
comes to you and asks the model once about a long chain, and a running plan is replanned as the run learns: on
your word, after an upgrade with newer planning rules, at a QA phase, and after a phase marked to reshape what
follows. The model decides how; baton decides when, checks every replan against what its kind may change, and
records it with its undo. This release also fixes four small things that run turned up, a wait on CI that
stopped a running gate, and two kinds of plan that would not attach.

A session on v0.8.x moves to v0.8.5 by itself at its next restart point; `baton update` updates baton's host
and its mod together. At its next phase boundary, a run whose plan was attached before v0.8.5 has the model
replan its phases not started once by the new planning rules: only which of them wait for which may change, and
the change stands only if more of them can then run at once (`auto_replan: false` turns this off).

### Added
- **A plan's shape, shown and checked at approval.** As a plan comes to you, baton shows its shape in one line
  (`plan_shape`): `baton: 12 phases · longest chain 5 (P1 → P7 → P8 → P9 → P11) · up to 7 at once`. A plan of
  one phase shows none. When a plan of 6 phases or more is mostly one chain (its longest chain over 60% of its
  phases; on the ten saved plans this flags the eight and passes the two at 43% and 58%), baton's one turn-back
  at the model's first `ExitPlanMode`, which already checks competing instructions, also asks it to check each
  link of that chain against the shape rules. Where every link holds, the model puts the plan to you unchanged
  and says in it why the chain is real. Still one turn-back per plan; `directives_asked` gains `phases`,
  `longest_chain`, `width` and `shape_flagged`.
- **`/baton replan [what to consider]`.** The lead's model restructures the phases not started so more of them
  can run at once, weighing your words, in your session: `baton replan` copies the plan file to a draft and
  prints the brief, the model edits the draft, and `baton replanned --notes "<what changed and why>"` (or `--none
  "<why>"`) hands it back. baton checks the draft before the plan file is touched: phases that started or are
  done never change, the order and `After:` lines of the others may, and so may adding, splitting and rewording
  phases; dropping or merging phases, or changing the plan's goal sections, only as far as your words ask, with
  each dropped phase named in the notes. A draft that fails is refused with each breach (`replan_refused`), for
  the model to fix. One that passes is written over the plan file and followed (`plan_replanned`), the phase
  agents at work hear of their part, and it is recorded as a decision with its undo: copy the text it found,
  `.baton/runs/<run>/replan/<n>.md`, back over the plan file (`replan_undone`). The approved text is kept
  (`replan/0.md`, `plan_kept`), and `baton status` lists each replan with what it changed against it. While a
  replan is open, no new phase agent starts; one not finished in 20 minutes lapses (`replan_lapsed`), and the
  plan goes on as it was. Also `replan_opened`, `replan_taken_over` and `replan_declined`.
- **baton replans by itself, now and then.** At the lead's phase boundary, in its fresh context, a replan's brief
  comes before the next phase's: once for a run whose plan was made under older planning rules
  (`plan.RulesVersion`, 1 in this release; a release that changes the planning rules bumps it); at most once per
  QA phase, when the phases not started are mostly one chain; and once a phase marked `Replan: after` is done,
  a new line under a heading, beside `After:` and `Type:`, for a spike or an exploration whose findings should
  reshape what follows. The first two change only the `After:` lines and the order of the phases not started,
  every other byte of each phase as you approved it, and stand only if the longest chain got shorter or more
  phases can run at once; otherwise the plan file is left as it was (`replan_unchanged`). One after a marked
  phase may also rework the steps of the phases not started with what it found, and split them, but never drops
  or merges work. A cue with nothing to change is logged (`replan_skipped`). New phase agents wait while a
  boundary owes one, for 20 minutes at most. The setting `auto_replan` (on by default) turns these off; `/baton
  replan` works either way.
- **A plan that would not attach is turned back before you approve it.** A plan with phase headings that baton
  could not attach as written (a duplicate id, an `After:` naming an unknown or later phase, an anchor that
  occurs twice) is turned back at the model's `ExitPlanMode` with what is wrong (`plan_unbuilt`), once per plan
  text: the same text again goes on to you, and your approval says it was not attached, as before.
- **Locks name their holder.** A hold over a second logs `lock_slow` with where the lock was taken, and a waiter
  that gives up after 5 seconds logs `lock_timeout` with the holder's pid, where it took the lock, and how long
  it had held it.

### Changed
- **`/baton plan` builds a graph, not a list.** The skill's planning guidance is now six shape rules: a
  dependency is a need (being checked by another phase's gate is not one); split enabling work; contract first;
  split by files; outside waits off the main chain; a small task can be one phase. Phases that would edit the
  same files go one after the other only as the last resort of the fourth. It also teaches `Replan: after`.
- **The reminder of the model's background work comes with the call that starts it.** A command run in or moved
  to the background, a Monitor or a background agent gets baton's reminder with its result (it wakes the model
  when it ends; keep working on what does not depend on it; with nothing left, end the turn), logged
  `background_told` with `via: start`, and the stop over it is no longer refused. In the real run, the lead's
  stop was refused 17 times to say this, each an extra turn on a context of 200k to 480k tokens. The refusal
  stays for work whose start baton did not see.
- **A gate and a wait on something outside run side by side.** In the real run, a QA phase's `baton await` on CI
  replaced its running `baton verify --here` suite, and queuing the suite again replaced the CI watch: of the
  run's 16 replacements, 4 ran the same check again and 12 swapped one check for another, and phases took to
  gluing gates into one command. A phase now runs one `verify` and one `await` at a time, and neither stops the
  other. `baton done` waits for both and is refused while either is red; the phase is done once both passed, and
  reopens on the first red. The same command again replaces the one of its kind that runs, as before; another is
  refused, naming the job that runs (`refused`, why "another check runs"), unless the model adds `--replace`.
  `job_queued` gains `beside` and `replace`, `phase_verifying` and `idle_on_job` gain `beside`, and the board,
  `baton status` and the briefs show both jobs.
- **Nothing slow runs under a lock.** A hook timed out on the run's lock 4 times since 2026-10-10, and failed
  open. Every update under a store lock was audited: the transcript reads, git commands, machine and process
  reads, and copies of a plan's record found there now run before the lock or after it, with the state change
  alone under it. A plan's record that cannot be kept is logged (`plan_not_archived`).
- **The question about a plan approved mid-run is the plan question.** "Replan" now names the new feature, so
  that question's events are renamed outright: `replan_asked`, `replan_held`, `replan_resolved` and
  `replan_waits` are now `plan_question_asked`, `plan_question_held`, `plan_question_resolved` and
  `plan_question_waits`. A v0.8.4 state file with that question pending loses it, and the question lapses as an
  unanswered one does.

### Fixed
- **A plan of one phase attaches.** An approved plan with a single `## P0 — …` heading was not attached, with a
  message saying it had no phase headings. It now attaches and runs, and the competing-instructions check covers
  it.
- **A plan that quotes its own headings attaches.** The plan for this release failed to attach at approval: its
  decisions quoted the standing-rules heading, so that anchor occurred twice. Every anchor (each phase's, the
  standing rules', the end section's) is now made to occur once: the heading line, else the heading with its line
  end, else with the lines after it. Headings in fenced code are passed over.
- **The compaction is asked for between turns.** At every boundary, baton's mod asked Claude Code to compact while
  the lead's turn was still ending, was declined ("call it from turn.complete or later"), and asked again a few
  seconds later: 15 times in the 13 hours of the real run. The mod now holds such a compaction and runs it from
  the lead's `turn.complete`, where Claude Code takes it (spike 37), so the boundary logs no `compact_declined`.
  Past 30 seconds, the host's retry after the turn stays the fallback.
- **`baton clean` leaves your own daemons alone.** It listed a gpg-agent that a hosted shell had started, and that
  so carried baton's variables, as "left by a session that is gone: kill". A leftover process is now stopped only
  under baton's home, listed only when it works in the project, a worktree on record or a lane, and left alone
  anywhere else.

## [0.8.4] - 2026-10-15

v0.8.3 made baton wait on you only where you chose it or where nothing in baton can answer. Going through
every wait in the code, and a real run's transcript, still turned up edge cases that left a run stuck or
spinning: a hold nobody lifted, a block nobody answered, an outward proposal with no deadline, an agent you
stopped, a crash loop that ended the host, a gate that hung in silence, a landing deferred forever, and a
session that only looked hosted. Each now ends by itself, in the safest way baton can take without you.
Nothing waits on you for longer than 20 minutes, except a block, which waits 30 and then looks again. Each
case has a test that drives it to its end with nobody there, and the new ones also run end to end on real
Claude Code (`test/e2e/stuck_test.go`).

A session on v0.8.x moves to v0.8.4 by itself at its next restart point; `baton update` updates baton's
host and its mod together. A session that only inherited the settings of a v0.8.3 host still runs that
host's baton, and so still looks hosted, until it is started again with v0.8.4.

### Changed
- **Wait for me lapses.** A hold you chose (Wait for me, on a proposal, on baton's stall question, on a plan
  you approved mid-run, or on a phase agent's question) lasts until you write, or for 20 minutes with no
  word from you. Then it lapses into the question's default, and the model is told (`hold_lapsed`). The
  options' descriptions say so.
- **An outward proposal is answered Not now at its deadline.** A proposal that names an outward or
  irreversible act (push, publish, deploy, delete, pay, send, force …) waited for you with no deadline. It
  now has one like any other proposal (the model's `--wait`, 5 to 20 minutes, or `escalation_timeout`), and
  a fourth answer, Not now, which baton gives for you then (`proposal_resolved`, answer Not now, by
  timeout). The model does the act only if it is safely reversible and stays inside the project (pushing
  its own branch, a draft pull request, deleting files it generated), and `baton note`s it; otherwise it
  leaves it undone and carries on with everything that does not need it. `baton status` lists what was left
  for you, and so does the run's summary when the plan completes.
- **A block looks again after 30 minutes.** A `baton blocked` nobody answers for 30 minutes (from when it
  began, or when you last wrote) is lifted, and the model is told to look again for a safe, independent
  way on that drops no work, and to propose it, which goes ahead at its deadline (`block_lapsed`). A phase
  agent's block the same: its question's default is now Look again. Your answer before then works as
  before; Look again is also yours to give (`look_again`).
- **The model's own questions have a deadline.** In a turn you started while a plan runs, a question the
  lead's model asks you (AskUserQuestion) is answered at `escalation_timeout` where baton's mod runs: the
  option the model marked Recommended, else its first, one per question; the model is told that baton chose
  for you (`question_answered`). Serial mode leaves them to you, as no typed key can pick an option safely.
- **baton's questions keep their default third,** so serial mode answers them at their deadline too. The
  stall question is now Pause baton · Wait for me · Continue; the question about a plan approved mid-run is
  Start the new plan from P0 · Wait for me · Continue with the revised plan (or Keep the current run). Wait
  for me on a plan holds it until you write (`replan_held`). Starting the new plan from P0 still keeps the
  old run's record in `.baton/runs/<run>/gen-<N>/`.
- **A phase agent you stop is asked about, and decided at a deadline.** Its phase waited for `/baton run` for
  good. Now baton asks you on the lead's screen: Restart it · Leave it stopped · Pause baton
  (`agent_restart_asked`). With no answer at `escalation_timeout`, baton decides from its record: an agent
  whose work is done (its phase done and not red, or landed) is let go and its work lands
  (`worker_retired`); otherwise baton restarts it from its brief and tells you, with a question of its own
  (`agent_restarted`). Stopped a second time with no answer, the agent is taken down (`agent_taken_down`),
  and if its work is not done baton asks what to do with the phase with no deadline, since you are the one
  in its way: only that phase and those that need it wait, and the question steps aside after
  `escalation_timeout` and comes back when you next write. Restart it then starts the count afresh.
- **`baton await` asks the QA question too,** as `baton verify` does (`--qa "<why now>"` flags an early full
  check): before a QA phase, a wait on CI is often a full check in disguise. A red result before a QA phase
  says that other phases' work in progress may be why: the model fixes what is its phase's, and finishes
  with `baton done --despite-red "<why>"` if the rest is not.
- **`escalation_timeout` is 1 to 20 minutes** (it was up to 2 hours); 10 by default.
- **Every timing was audited.** What waits on you takes at most 20 minutes (most take 10); the rest is sized
  to what it waits for:

  | Timing | Before | After | It waits for |
  |---|---|---|---|
  | `escalation_timeout` | 10m (1m to 2h) | 10m (1m to 20m) | you, on a question or a proposal |
  | Wait for me (`HoldLapse`) | until you wrote | 20m | you, on a hold you chose |
  | A block (`BlockLook`) | until you answered | 30m, then the model looks again | you, on a block |
  | An outward proposal | until you answered | its `--wait` or `escalation_timeout`, then Not now | you |
  | The model's own question | until you answered | `escalation_timeout` (where the mod runs) | you |
  | A stopped phase agent | until `/baton run` | `escalation_timeout`, then from the record | you |
  | Pushes of an open escalation | 30m, 2h, 6h | 10m, 20m | you; then the deadline settles it |
  | Notices of an open dialog | 3m, 33m, 2h3m, 6h3m | 3m, 13m, 23m | you, on a dialog |
  | The stall alarm (`StallAfter`) | 30m | 20m | anything in the run to move |
  | Reminders of a stalled session (`StalledNudge`) | 30m | 15m | the model |
  | The model's background agents holding a compaction (`BackgroundMax`) | 30m | 20m | its agents |
  | The model's background work keeping reminders away (`BackgroundIdle`) | 1h | 20m | its work |
  | After a resume nudge's escalation | never reminded again | every 15m | the model |
  | Crash recovery past two in 30 minutes | the host exited | 5m, then every 15m | nothing: it retries |
  | A silent gate (`HungAfter`) | `verify_timeout` (3h) | 20m of no log and no CPU | the gate |
  | A landing held by the lead's changes (`LandNudge`) | for good | the lead is told after 10m | the lead |

  Unchanged, as none waits on you: the context question (10m), `switch_after` (5m), `verify_timeout` (3h),
  `foreground_max` (2m), the resume nudge (1m) and its escalation (3m), a phase agent's start (5m), a stale
  turn (15m), an idle nudge (10m), a compaction's timeout (10m) and its retries (5m to 15m), the usage
  limit's retry (15m) and an overload's (1m, backing off to 16m), and phase agents starting 2 minutes apart.
  With no deadline, by your choice: Pause baton, `/baton pause`, the review you opt into with
  `max_auto_decisions`, and the question about an agent stopped twice whose work is not done.

### Fixed
- **A phase agent's permission prompt holds only that agent.** It was recorded on the lead's screen, so it
  held the lead's compaction, deliveries and phase switches, and an Agent-tool prompt held every new agent.
  Where baton's mod runs, the lead now goes on under it, and the prompt survives (spike 36); the board and
  the stall alarm name the prompt by its phase. In serial mode any dialog still holds the lead, since a
  typed key would answer the agent's prompt. While a phase agent's own Agent call waits on
  its prompt, new phase agents still wait: a worktree request names nobody, so one could go to the wrong
  agent.
- **A crash loop no longer ends the host.** Past two crashes in 30 minutes, the host stayed down until you
  ran `baton --resume`. It now waits 5 minutes, then 15 between tries, for as long as the plan runs
  (`recovery_backoff`, pushed); a Ctrl-C in that wait stops it. A session whose last turn was refused is
  resumed quietly, never started afresh.
- **An API error a fresh context avoids starts the phase afresh.** A prompt too long, a request too large, a
  broken tool history, a rejected image or PDF stopped the session for good. The lead (`lead_afresh`) or the
  phase agent (`agent_afresh`) is now started afresh from its brief, with the same backoff, once nothing
  would be lost. A refusal, an expired login, billing and an unknown model stay yours.
- **A gate that hangs in silence is stopped.** A `verify` job whose log has not grown and whose processes
  used no CPU for 20 minutes ends unverified, with the reason, and the model is told (`job_hung`). Log
  silence alone never stops one: where baton can't read the CPU (Windows), a gate runs to `verify_timeout`.
  A gate whose work runs outside its own processes (`docker exec`, `ssh`) and prints nothing for 20 minutes
  is taken for hung.
- **A landing is not deferred forever.** One held for 10 minutes by the lead's uncommitted changes to the same
  files tells the lead's model to commit those files by path, never to stash or discard them
  (`land_nudged`); one held because the checkout is off the run's branch is pushed to you once
  (`land_off_branch`).
- **A session that only looks hosted is dormant.** Claude Code can move a conversation into its background
  service, under a new session id, and the new process inherits every `BATON_*` variable of the hosted
  claude that started the service. It looked hosted, yet no baton host drove it: nothing compacted, no agent
  started, and its plan's run stayed with the old session. Hosted now means a live baton host above the
  session (the host records its pid); a session that only inherited the variables changes nothing, says
  once how to give it back (`baton --resume <session id>` in a terminal; `inherited_notice`), and `baton
  version`, `baton status` and its status line say "not hosted (inherited)". Its `WorktreeCreate` still gets
  a plain worktree, since baton's hook replaces Claude Code's own. `/baton run <plan>` in it, or in the
  resumed session, carries on the old session's run of that plan where it stood, when no live host owns it
  (`run_carried`), and baton's mod follows its host to the carried run, so its phase agents start again.
  The check reads the asking process's ancestors, one at a time, about 7µs each, and keeps no cache on
  disk.
- **A run is carried on whatever path names its plan file,** through a symlink too.
- **A stalled session's resume is nudged again.** After the escalation of a resume nudge nobody answered,
  the model was never reminded again; it now is, every 15 minutes (`resume_nudge`).
- **The words say what baton does.** The model's primer and playbook said a block stops the run until you
  answer, and that no answer to any proposal means the model does it; each Wait for me said it waits for
  your word. Each now says what happens at the deadline.

## [0.8.3] - 2026-10-15

baton exists so a run never waits on you for something it can decide, and v0.8.2 still did. A run left
overnight sat idle for two hours because its phase had made five decisions without the human, and baton
stopped it until someone said "Let the run go on". Every wait like that is gone. baton now waits on you
only where you chose it (Pause baton, Wait for me, a cap you set) or where nothing in baton can answer (a
permission prompt, the usage limit, a login, a refusal). Every other question has a deadline, and the
deadline picks the model's side. Every decision is still recorded with its undo, for you to read when the
plan has run. And a plan no longer runs its full test suite after every small phase: QA phases do the
heavy checks once for many.

A session on v0.8.x moves to v0.8.3 by itself at its next restart point; `baton update` updates baton's
host and its mod together.

### Changed
- **No cap on decisions without you, unless you set one.** `max_auto_decisions` is off by default; set it
  to a number to have baton stop for a review after that many in one phase.
- **The model sizes each proposal's wait.** `baton propose … --wait 5m` takes 5 to 20 minutes, and the
  model is told to give the shortest wait that fits, since every minute waiting is time the run loses.
  With none, or one out of range, a proposal waits `escalation_timeout`, now 10 minutes by default.
- **QA phases carry the heavy checks.** A plan phase with `Type: qa` under its heading runs the full test
  suites, lints and end-to-end runs, and fixes what the phases since the last one left incoherent. The
  phases between run focused checks only and may end in a work-in-progress state, as their briefs say.
  `/baton plan` places a few, and baton asks once about each gate queued before one (`baton verify --qa
  "<why now>"` flags a full check run early on purpose). `aggregate_qa: false` (or
  `BATON_AGGREGATE_QA=0`) has every phase check itself in full, as before.
- **A plan is checked against your other instructions before you see it.** At the model's first
  `ExitPlanMode` for a plan, baton asks it once to flag anything in CLAUDE.md, memory, skills or settings
  that competes with the plan (a rule to run the full suite before every commit, say) and to address it
  in the plan. A stop before the plan reaches you is turned back once.

### Fixed
- **A proposal nobody answered goes ahead.** One whose question went away unanswered (Esc, a restart)
  was held for you like Wait for me, its deadline thrown away; now it goes ahead at its deadline unless
  you write first. A phase agent's proposal counts its wait from when the agent made it, and goes ahead at
  its deadline even when you never saw it because your session's own question went first.
- **baton's own questions answer themselves.** Its stall question (Continue / Pause baton) and its
  question about a plan you approved mid-run waited on you for good. Each now waits 10 minutes, then the
  plan goes on: a revision of the running plan is followed, and another plan is left for `/baton run`.
  The context question waits 10 minutes, not 20. A block stays the model's own choice to wait.
- **Nothing gives up on the run.** A failed compaction is retried every 15 minutes for as long as it
  fails. A job's result reaches a session the watchdog had down as stalled, and the stalled session is
  still reminded every 30 minutes. A plan file baton can't follow no longer pauses the run: it goes on
  with the plan as attached, and you're told. A lead's `baton blocked` holds the lead alone, and phase
  agents keep starting and landing. A phase agent that stops without a status is nudged instead of being
  told to ask a question nobody would answer.
- **CI runs on Node 24.** `actions/checkout` and `actions/setup-go` v7, and Ubuntu 26.04 for the Linux
  jobs, so no job warns about Node 20 or the `ubuntu-latest` move.

## [0.8.2] - 2026-10-15

A real v0.8.1 run, with four phase agents beside the lead, stopped for over three hours with nobody told.
A phase agent's gate had failed after its `baton done`, and its phase waited for a compaction nothing
would ever run. The same run showed seven more faults: baton's Ctrl-C ended an agent and threw away your
draft, agents' reports counted as your turns, a `/compact` while paused didn't count, agents started
faster than the machine showed their load, your edit to the plan never reached them, work merged by hand
couldn't be recorded, and a turn you asked for was cut short. Each is fixed, and each is replayed by an
end-to-end scenario on real Claude Code. The evidence is in
[docs/research/visible-workers.md](docs/research/visible-workers.md).

A session on v0.8.x moves to v0.8.2 by itself at its next restart point. baton's host and its mod must
now be of the same release: with a mod of another, the plan runs one phase at a time, and baton says so.
`baton update` updates both.

### Fixed
- **A phase agent's failed gate goes back to its agent.** When its gate fails after its `baton done`, or a
  landing's verification fails, the phase is taken up again in place, and the agent is given the result
  at its next tool call or stop; an agent that ended is started afresh, with the result in its brief. A
  phase agent never owes a compaction, and a sub-run already stuck that way heals by itself.
- **baton types nothing where its mod runs.** The mod compacts your session and submits baton's lines,
  and your unsent draft stays in the input box, through the compaction and the turn after it. Where the
  mod doesn't run, baton types as before, but never sends the Ctrl-C that clears a draft while an agent
  may be at work: one Ctrl-C at an idle prompt stops every background agent for good, the model's own
  too. The draft holds baton then, and you're told why.
- **A compaction while paused counts.** Your `/compact` while paused at a boundary, or baton's queued
  before the pause, covers the one the run owed, and `/baton run` starts the next phase from its brief
  with no second compaction, unless the conversation has grown much since.
- **An agent's report handed back is never yours.** In auto mode Claude Code hands each agent's report
  back to your session as a message from another session. baton's mod now drops it, and baton never takes
  it for your turn: it no longer releases holds or hands your session the run's decisions.
- **A turn runs to its end.** When baton takes up a phase by itself (a gate's verdict, a landing, a
  switch), a turn under way, or one you started, is no longer cut short: the model is told once that the
  phase comes after this turn, and baton compacts at its end. A held boundary says what comes next (the
  phase begins, resumes, or reopens after its verification failed), not that a failed phase is done.
- **Attaching while paused stays paused.** A plan attached over a paused run waits for `/baton run`, and
  `baton attach` says so.
- **No sentence leaves a blank where a phase goes.** With no phase in hand, baton's sentences name the
  plan, and `baton note` records the note on the run, counting toward no phase's review.

### Added
- **A run that stops moving says so.** With the plan running, nothing at work, and nothing moved for 30
  minutes, baton pushes you one notice naming what each phase left waits on (`run_stalled`). It holds
  nothing, and the run moving arms it again. A gate whose log has stopped growing is named a hung gate.
- **A phase held for you is pushed and on the board.** A phase agent you stopped from the agent panel
  leaves its phase waiting for you; baton now pushes that, and the board shows it.
- **Plan changes reach the agents at work.** Edit the plan file while it runs, and each phase agent whose
  section or standing rules changed is given the new text, once.
- **The lead's `baton done` of a worker's phase** records it landed when every commit of its branch is on
  the run's branch already (merged by hand), as a decision without you, with its undo. baton retires the
  agent, saving what it hadn't committed as a snapshot first. Otherwise the refusal says how to finish it.
- **A replaced plan keeps its record.** Before another plan replaces it, or `baton drop` sets it aside,
  its state, plan and handoff are copied into `.baton/runs/<run>/gen-<N>/`, and `baton status` lists the
  run's earlier plans, with how many phases each finished.
- End-to-end scenarios for each of the run's stalls (`test/e2e/nostall_test.go`): an agent's red gate
  after `done`, a landing's red re-verification, a draft through a compaction with an agent mid-command,
  your `/compact` while paused, attaching while paused, an agent's hand-back in auto mode, a gate result
  in your turn, a plan change reaching an agent, and the stall notice.

### Changed
- **Phase agents start at least 2 minutes apart,** whatever `workers` says: the machine's readings lag
  the load an agent brings. A resumed or restarted agent doesn't wait.
- **A phase agent's `verify --here` gate takes a lane.** It holds one of the project's lanes while it runs
  in its worktree, with a lane gate's variables set (`BATON_LANE` the lane's number, `BATON_LANE_DIR` the
  worktree), so here gates and lane gates
  together never run more at once than the lanes allow, and a test database named by lane stays its own.
  Without a `.baton-lane` there is one lane, so agents' here gates take turns. Your session's here gate is
  unchanged.
- **A gate waiting for a lane** is announced with how many gates are ahead of it.
- **The bridge protocol is 2,** for the mod's two new commands, compact and submit.

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

[Unreleased]: https://github.com/ozzyfromspace/baton/compare/v0.8.6...HEAD
[0.8.6]: https://github.com/ozzyfromspace/baton/compare/v0.8.5...v0.8.6
[0.8.5]: https://github.com/ozzyfromspace/baton/compare/v0.8.4...v0.8.5
[0.8.4]: https://github.com/ozzyfromspace/baton/compare/v0.8.3...v0.8.4
[0.8.3]: https://github.com/ozzyfromspace/baton/compare/v0.8.2...v0.8.3
[0.8.2]: https://github.com/ozzyfromspace/baton/compare/v0.8.1...v0.8.2
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
