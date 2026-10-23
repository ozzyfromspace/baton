# Gates: where two runs' hours went

**In short:** in two real runs, checks that could not change what shipped took more of the wall clock than
anything else. baton's own v0.8.6 plan ran 12h19m, and its lead gated for 10.5h of them. About 3 to 4 of those
hours were full end-to-end reruns: after a test-only fix, on long past a first failure, and a minute before the
next full run began. "A red gate stands until the same command passes" (v0.8.6) turned a flaky suite into a
treadmill. A real 57-phase run had spent 43.7h of gate time in 59.5h of wall clock by phase 42, and 58% of its
132 gates were red. QA passes ran past failures they already knew of and re-ran stages that had passed.
Gates in the lead's checkout held landings for 9.4h. Gates went red on their environment before any test ran,
and landings were verified again on a single lane, 17 of 18 green. A human who feels time runs scoped checks
while working and the big check once, at the end. v0.8.7 steers plans, phases and gates toward that. A real run
since showed that asking alone changed nothing: models resubmitted every gate baton questioned, unchanged, within
seconds, and never used `--failed`. v0.8.11 makes the cheap path the easy one ([below](#since-v087-asking-was-not-enough)).
A real run on v0.8.14 then spent its hours in a QA set's long stages: whole reruns of a browser stage baton could
not narrow, nine runs of a final QA phase that each found one red, and failing tests that waited out their
timeouts. v0.8.15 runs every stage in a QA phase and reruns only what failed until all of it passes
([below](#since-v0811-a-qa-sets-hours)).

Studied 2026-10-17 from the runs' records (`state.json`, `events.jsonl`, job logs, read-only), for the two runs
below. The second is a private project: only its numbers are given here.

The questions:

- Where did each run's wall-clock time go, gate by gate?
- Which of those hours could have changed nothing that shipped, and what would a human have done instead?
- What did baton lack to see it: in its records, in what it tells the model, and in what it lets a model do
  unseen?

## baton's v0.8.6 plan (12h19m)

The lead's jobs, from the plan's record (`gen-3`):

| Job | Phase | Command | Minutes | Result |
|---|---|---|---|---|
| j9 | P7 | `go test ./internal/loop ./internal/hooks ./internal/cli` | 18.9 | red |
| j10 | P7 | the same | 27.8 | green |
| j11 | P11 (QA) | the full e2e suite (76 Haiku scenarios) | 92.5 | red |
| j12 | P11 (QA) | the full e2e suite | 88.3 | green |
| j13 | P17 | one scenario, most of it waiting for the lane j12 held | 51.9 | green |
| j14 | P17 (QA) | the full e2e suite | 95.0 | red |
| j15 | P17 | two scenarios | 5.6 | red |
| j16 | P17 | the same two, twice | 1.3 | green |
| j17 | P17 | the full e2e suite | 32.5 | red |
| j18 | P17 | the full e2e suite | 90.4 | red |
| j19 | P17 | the full e2e suite | 91.1 | red |
| j20–j22 | P13 | `gh run watch` (CI) | 35.2 | red, then green |

- **Six full e2e runs** of about 90 minutes each; of the five that ran to the end, one was clean (j12). The
  scenarios drive real Haiku sessions, and a few of 76 flake on any run.
- **j12 re-ran the whole suite after a test-only fix,** and P17's own full run (j14) started a minute after it
  ended.
- **j17 went red at minute 8 and ran on for 24 more minutes.** Nothing told the model of the failure until the
  job ended.
- **j19 re-ran 73 scenarios that had passed on the same shipped code,** after edits to two test files.
- **j13 ended 1.6 minutes after j12:** its 51.9 minutes, and the timeout it ran under, counted the wait for the
  lane j12 held, as every job's did.
- **The rule behind it.** In v0.8.4 a model re-ran only the failure it read in a log's tail, and a regression
  shipped that the full gate had caught; v0.8.6 answered that with "a red gate stands until the same command
  passes". Against a flaky suite, that means a full run after every fix. The human's "ship now" ended it.

About 3 to 4 hours of the 10.5 could not have changed what shipped.

## A real 57-phase run (59.5h at phase 42)

132 gates, 43.7h of gate time, 58% red.

- **QA passes ran past known failures and re-ran what had passed.** One QA phase took four passes, 7h35m.
  Another's gate failed its lint at minute 2 and ran on for 2.5 hours.
- **Gates in the lead's checkout held landings.** `--here` gates held phase agents' landings for 9.4h, with about
  2h more still under way, and then everything landed at once and conflicted.
- **14 or more reds before any test ran.** The lane's stack collided with the developer's: host ports the dev
  stack already held, stale containers and images, a lane database another phase had migrated. At least 8 agents found
  the same fix again (an overlay with no published ports).
- **Landing re-verification:** 18 runs took 5.5h on the project's single lane, and 17 were green.
- **Timings gated on a loaded machine.** One performance phase went red 7 times in a row, on routes it never
  touched.
- **Shared files:** one file every phase edited came back in 11 landing conflicts (see the shape rules' "split by
  files").
- **The lead's own background shell tasks:** long checks run as background shell tasks left the lead idle for
  8.4h on work baton could not see, and the lead polled them in loops.
- **What baton's records lacked:** when a job got its lane (a job's seconds, and its timeout, included the wait),
  and what background tasks ran and for how long.
- **Already fixed by v0.8.6:** checkpoint churn (one phase's agent restarted 23 times; the lead checkpointed 18
  times mid-phase on v0.8.5, and 2 times in 15 hours on v0.8.6), pollers' wording, and lock contention.

## What a human does

A human who feels time tests in scoped, partial runs that take seconds or minutes while working, and runs the big
aggregate check once, at the very end, when presumably done. Not done? Fix it in real time, rerun what failed,
ship. Models run long suites, full test runs and lints far more often, because they don't feel time pass. Every
case above is one a human would have cut short: a gate stopped at its first red, a rerun of the two failed
scenarios, a landing that touches nothing new let through to the QA phase, a port collision fixed once for
everyone.

## What v0.8.7 does with it

| Finding | v0.8.7 |
|---|---|
| A failure known at minute 8 or 2, told at the end | **The gate protocol.** `baton stage <name> -- <command>` marks each stage of a gate script in its log (`::baton stage` lines) and records it in the job's stages file; `::baton fail <id>` lines, or a runner preset (`--runner go`, `--runner pytest`), name what failed. A gate stops at its first failed stage unless that stage says `--keep-going`. The runner reads the markers as they are printed (`stage_finished`, `job_failing`), and the gate's owner is told of the first failure at once, as a look-in: stop it (`baton stop <job>`) and rerun what failed, or let it finish to collect every failure. baton never kills a gate for a failure line. |
| A full rerun after every fix | **`baton verify --failed`** reruns the red gate's failed and skipped stages, each narrowed to its failure ids where known, and carries the stages that passed (naming the commit they passed on and the files changed since). Its pass clears the red gate (`Job.Reruns`), so a flaky suite needs one full run and then minutes. The failure set comes from baton's record, never from a model's reading of a tail. |
| Full runs repeated on nearly the same code; a long gate before the QA phase | **Questioned once as it is queued** (`gate_questioned`): a repeat of a gate that took 10 minutes or more, with the files changed since and the narrower check; a script outside the repository; a long gate not built from stages; a long gate before the plan's QA phase, with the time its last run cost. The same call again queues it. |
| Long checks as background shell tasks | A check started as a background shell task is pointed to `baton verify` once (`background_questioned`), and each background task's command and how long it ran are recorded (`background_told`, `background_ended`). |
| Gates red on their environment | A red gate whose log shows a known environment failure (a port in use, the Docker daemon out of reach, an unhealthy container, a full disk, a registry's rate limit, a host name that does not resolve) and no test failure says so; `baton env "<what it needs>"` records a fact every phase's brief carries. |
| Landings verified again, 17 of 18 green | **Re-verification sized to the change:** a landing that shares no file with what landed since its gate (Markdown aside), with a QA phase not started ahead and `aggregate_qa` on, lands unverified for the QA phase to check (`reverify_skipped`); otherwise it runs the phase's `--land-check`, or its last passing gate. |
| Landings held behind `--here` gates for hours | Look-ins: landings waiting 20 minutes behind a gate in the lead's checkout are told to the lead; `verify --here` names the landings it will hold as it is queued. |
| A gate running far past its history | Look-ins at 1.5 times its command's slowest earlier pass (and 15 minutes more) and at 80% of `verify_timeout`, with how long its log has been still. |
| The lane wait counted as the gate's time | `verify_timeout` counts from when the job gets its lane (`lane_acquired`), and a result says both: "passed after 12m, 31m waiting for a lane". |
| One file every phase edits | A plan may name a record file (`Record: <path>` in its standing rules) that baton alone writes, a phase's record at a time, and commits alone. A file in a second landing conflict is a look-in for the lead. |
| A phase's test stack left running beside the QA gate | A phase agent's `baton done` asks once about what it left running; retiring an agent stops its processes and names its containers. |
| The model judges where full checks go | **The gate rules** at a plan's first `ExitPlanMode` and in short in every brief: QA phases at real seams, scoped checks elsewhere, gate scripts built from stages, stop at the first failure, then `--failed`. |

And two that act on time, not on a single gate:

- **The advisor.** When a look-in fires about a gate, a small model (Haiku by default) reads what baton prepares
  (the look-in's facts, the command, the failure lines and the log's end, earlier runs, the phase's notes) and
  adds a note: the change's failure or the environment's, whether the rest of the run is worth waiting for, the
  smallest next check. It is advice, never an instruction, and the look-in goes without it after 60 seconds
  whatever happens. The call goes through baton's mod ([spike 41](../../spikes/41-model-complete/README.md)), so
  it runs only where the mod runs.
- **The risk ledger.** Scoped testing trades early detection for speed; the ledger says when that trade stops
  paying. Each phase is scored from facts baton has, never from the phase's own rating, and the points add up
  until a full check passes. At each line the total crosses (review, then full), an independent model (Opus by
  default) assesses the exposure: continue, targeted checks, or a full check now. Only its "full now" holds
  anything: new phase starts, until the lead queues the full check or says why not, or for `escalation_timeout`.

## Calibrating the ledger

A replay (`internal/risk/replay_test.go`, under the `replay` build tag, reading run folders named in
`BATON_REPLAY_RUNS`; no data is committed) scored the 61 phases of both runs:

- **Per phase:** 10.6 points on average, at most 27. The 27 phases whose files the next full check had to fix
  averaged 14.2; the other 34 averaged 7.8. The lines changed and the files other phases' sections name told them
  apart best (6 and 4 points on average, against 4 and 2), and nearly every phase touches two or three areas of
  the project, so only areas past three count.
- **Per stretch before a full check:** the three whose check fixed the phases' own files reached 92, 145 and 181
  points; the two whose check did not (a flaky suite's QA) reached 17 and 45.
- **The lines:** review at 56 points crosses each troubled stretch after its 6th of 13, 5th of 7 and 4th of 11
  phases, and neither quiet one; full at 90 after the 9th, 7th and 5th.
- Not replayed: whether a phase ran any check, and its own account of how it departed from the plan; older
  records lack both.

## What stayed as it was

- **`plan.RulesVersion` stays 1.** A bump gives every run carried across the upgrade one structural replan, which
  pauses new agents and can only reorder phases and change `After:` lines: it could not apply the gate rules, and
  would interrupt a live run for nothing. The gate rules go to every new plan at its first `ExitPlanMode`, and to
  every phase's brief.
- **`decide.ShapeRules`** are unchanged.

## Since v0.8.7: asking was not enough

Read 2026-10-18 from a real run's record since its upgrade to v0.8.7 (`events.jsonl`, `state.json`, job logs,
read-only). It is a private project: only its numbers are given here.

- **Every question was resubmitted unchanged.** baton questioned 35 gates as they were queued (`gate_questioned`).
  Every one traced was queued again with the same command, 0 to 12 seconds later. One phase agent got `repeat` and
  `unstaged` seven times running and resubmitted each time, once in the same second.
- **The cheap path went unused.** 0 of 32 gates used `baton verify --failed`. It needed a committed script of
  `baton stage` steps, and the run's gates were scratchpad scripts and make targets.
- **One gate, again and again.** The same agent started its 12-minute gate 11 times in 75 minutes, each time after
  fixing one failure.
- **Three full QA passes.** A QA checkpoint ran its 2h40m set in full three times, about 8 hours, where reruns of
  what failed would have done.

Why: the refusal said "run the same command again", so resubmitting was the cheapest move, and the questions read as
rhetorical. The cheap path was hard to reach: `--failed` worked only on staged gates. A narrower check that passed
cleared nothing, so the model had to run the full gate again anyway. And the cost was abstract. A model does not feel
time pass: a 12-minute gate is one tool call to it, and its result arrives as if no time went by. The human feels
every minute: they wait on the run, their usage drains, and the work they care about next sits behind it.

| Finding | v0.8.11 |
|---|---|
| `--failed` only for staged gates | **Failure ids from any red log** (`lane.FailureIDs`): Go, pytest, vitest, jest and Playwright output and `::baton fail` lines, each with its runner's own rerun (`lane.Reruns`). **`baton verify --failed -- <command>`** reruns only what failed in a gate built from no stages, when the command names each failed test; it stands in for the gate, so its pass clears it. |
| A narrower check that passes clears nothing | It still clears nothing, unless it is that narrowed rerun: baton checks that it names every test the red gate's log says failed, and refuses one that leaves any out, naming them. |
| The cheap path hard to find | A red gate's result lists what failed by id and the command that reruns only that, ready to queue. |
| "Run the same command again" | The question ends: "To run the whole gate anyway, queue it again with `--why "…"`: baton tells the human your reason." `--why` is announced (`gate_repeated`); a long repeat queued again without one still runs, and is told to the human (`gate_unexplained`) and opens its result. |
| The cost abstract | Every gate's result ends with what the phase's gates have cost so far (`decide.GateCost`), and a gate of 10 minutes or more with what it spent of the human's time (`decide.TimeShort`); the question opens with both. `decide.TimeFelt`, the paragraph above in baton's words with this run's numbers, opens the gate rules at plan approval and every phase's brief. |

To measure in the next real run, against these numbers: `gate_repeated` and `gate_unexplained` counts, narrowed
reruns (`job_queued` with `reruns` and `narrows`), and gate time per phase.

## Since v0.8.11: a QA set's hours

Read 2026-10-23 from a real 58-phase run on v0.8.14 (its record, job logs and the project's test output,
read-only). It is a private project: only its numbers are given here.

- **Three whole reruns of one stage.** A staged QA gate's browser stage, 390 tests in 64 files, took 1h18m to 1h50m
  a run. It went red three times, and each `baton verify --failed` reran all 64 files. The queue message said only
  "run whole". The human stepped in: "at some point I'm just clearly waiting too long."
- **The side log.** The project's gate script wrote each stage's output to a log of its own, so the job's log held
  a 40-line tail of it and no test name. Playwright's end-of-run list of failures (`N failed`, then each test) was
  in lines baton's reader skipped.
- **Nine runs of the final QA phase, one red stage each.** A gate stopped at its first failed stage (`lane.Halted`),
  so each run found one red. The phase was the lead's, in the human's checkout, for 3h20m. The human had review
  findings of their own ready and could not start: "the fact that it's running is an active blocker for me."
- **Failing tests that waited out their timeouts.** In the browser stage, 125 failing cases that ran 40 seconds or
  more took 3.75 of the stage's 8 case-hours. In a smaller stage, 12 failing cases took 0.68 of its one. Per-test
  timeouts ran from 30 seconds to 3 minutes, and specs raised them as far as 15 minutes.

| Finding | v0.8.15 |
|---|---|
| The failures in a side log | `baton stage --log <file>` reads a red stage's failures from the file its command writes to (`lane.ReadInto`). Playwright's end-of-run list is read too. |
| "run whole", with no reason | Each stage a rerun runs whole is named with why and its last time (`state.WholeStage`, from `lane.Stage.Unnarrowed`), and a red one of 10 minutes or more is questioned once (`whole_rerun`), offering a rerun by name. |
| One red a run | In a QA phase a gate's stages keep going past a red one (`BATON_QA_PHASE`, `Job.QAPhase`); `--halts` marks a stage the rest need. |
| Full reruns once every red was known | **Slim runs:** in a QA phase, the red gate's own command reruns what failed until all of it passes (`decide.SlimRun`); `--whole --why` runs it whole, told to the human (`gate_whole`). At `baton done`, one full run confirms the reruns only when files beyond Markdown and the failing tests changed since the full run (`state.NeedsConfirm`, `qa_confirm_queued`). |
| The QA phase in the human's checkout | A QA phase goes to a phase agent where the run has them (`State.HasAgents`), and the human is told once when every other phase is done, so a review of theirs reaches the QA phase in time (`review_ready`). |
| Failures that waited out a timeout | A red result says so when 3 or more failures clustered at one long duration took a quarter or more of the run (`lane.TimedOut`), and asks for lower timeouts by runner. The gate rules add rule 11: evidence in notes and commits rather than the permanent suite, browser tests for user journeys, and tests that fail fast. |

To measure in the next real run: whole reruns of a stage (`job_queued` with `reruns`, `gate_questioned` with
`whole`), runs per QA phase, `qa_confirm_queued` against `qa_confirm_skipped`, and `timeout_failures`.
