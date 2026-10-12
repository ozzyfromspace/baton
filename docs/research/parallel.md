# Running phases concurrently, and never waiting on a gate

**In short:** in four real runs, most of the wall clock went on waiting for gates (65% and 38% of the two long
campaigns), with the model idle. Replayed against their real dependency graphs, two sessions that never wait, with
two verification lanes, would have finished 1.44× and up to 2.24× faster; a third session adds about 1%, a chain
gains about 10%, and re-verifying every merge can cancel the gain. This is why v0.4 hands long gates to baton (lanes,
`verify`, `await`, switching between phases), and why v0.5 works on phases at once.

Measured 2026-10-10 from four real baton runs, with three spikes against **Claude Code 2.1.296** (macOS, arm64):
[`spikes/19-background-work`](../../spikes/19-background-work), [`20-tool-steering`](../../spikes/20-tool-steering),
[`21-attach`](../../spikes/21-attach) and [`22-run-timelines`](../../spikes/22-run-timelines). Two of the runs are
the 17-phase campaigns in a separate product repo (the quiet campaign of 2026-10-05, the craft campaign of
2026-10-08); the per-run evidence (dependency graphs, timelines, gate inventory) describes that codebase and is
kept outside this repo.

Two ideas were on the table:

- **Run phases concurrently** where they do not depend on each other, each in its own worktree, with baton still
  compacting, escalating and keeping the run moving.
- **Never sit waiting on a script.** When a test suite or a sweep takes an hour, keep working on something that
  does not depend on its result, and deal with what it finds afterwards.

The questions: where does a run's time actually go, how much faster would these runs have finished (with
hindsight about what really depended on what), and what does Claude Code allow?

## Where the time went

`timeline.py` gives every second of a run one label, from `events.jsonl` and the session transcript (turns, tool
calls with their durations, background tasks from launch to their completion notice, compactions, questions).

| Run | Wall | Model working | Waiting on verification | Compaction | Human, pauses | Other idle |
|---|---|---|---|---|---|---|
| Craft campaign (17 phases) | 58.3 h | 18.6 h (32%) | **37.7 h (65%)** | 0.8 h | 0.5 h | 0.6 h |
| Quiet campaign (17 phases) | 33.6 h | 16.2 h (48%) | **12.7 h (38%)** | 0.9 h | 0.7 h | 3.1 h |
| attaform (6 phases) | 2.7 h | 1.3 h | 0.5 h | 0.1 h | 0.8 h | — |
| baton v0.2 (8 phases) | 3.0 h | 2.2 h | 0.5 h | 0.1 h | — | 0.2 h |

"Waiting on verification" is a gate (test, lint, build, browser sweep) running in the foreground, a poll loop
in the foreground, or the session idle while its own background job ran. In the craft campaign that was 14.6 h,
10.8 h and 12.3 h. Attaform's human time is five timed proposals.

- **The model almost never worked while a gate ran.** The craft campaign's stateful gates ran for 26.8 h in
  all; for 1.5 h of that the model was doing anything else (quiet: 0.8 h of 10.5 h).
- **It waited actively.** 87 foreground poll loops of two minutes or more (`until grep … ; do sleep 15; done`,
  each cut at the 10-minute Bash limit) took 10.6 h of the craft campaign. Otherwise it declared a
  `baton waiting` and idled: P1 was committed at 18:51, then waited for a 52-minute backend suite until 19:44.
- **Backgrounding was not the gap.** Claude Code moved a long foreground command to the background at its
  timeout 34 times in the craft campaign and 42 times in the quiet one. With nothing else it was allowed to do,
  the model then polled or ended its turn.
- **Not the gap either:** compaction (1–3% of wall clock) and usage limits (no rate limit during either
  campaign). The craft campaign made 6,773 Opus calls, about 300k tokens of context each: 5.4M output and 2.0B
  cache-read tokens, 0.76M and 291M in its busiest five hours.
- **Most waiting sits inside a phase, not after it.** Waiting after a phase's last code commit was 6.6 h of
  the craft campaign and 2.0 h of the quiet one. The rest is the inner loop: run the gates, read the failures,
  fix, commit, run them again.

## What the phases really depended on

For each run, every pair of phases was checked: `git blame` over the lines each phase changed (does a later
phase edit lines an earlier one wrote?), then the plan text and the handoff notes (does it use something the
earlier phase produced, or apply its decision?). The quiet campaign's plan file had been overwritten by the
next campaign's; its full text was recovered from the session transcript, and its sha256 matches the one
baton recorded at attach. Edges are `hard` (needs the earlier phase's output), `gate` (only its verification
needs the earlier phase's instruments), `merge` (same files, independent changes), `resource` (contention for
the shared dev stack) or `ordering-only`.

- **Small feature plans are chains.** In attaform every phase built on the API P0 introduced, and P3's fixes
  rewrote lines from P0 and P1. In baton v0.2, P3 rewrote 118 lines P1 had written; only P0 (docs and spikes)
  could have run alongside anything.
- **The craft campaign was mostly independent in the middle.** Of 189 relations between phases, 21 are hard
  dependencies. Given P0's instruments, P1, P2, P3, P5, P6, P7, P8 and P13 could all have started at once.
  One chain runs through it: P2 → P10 → P11 → P12 → P14 → P15. P0 (instruments, 7.1 h) gates every later
  phase's verification, and P16's finish (11.5 h) depends on everything: 14 of its 16 commits repaired fallout
  from nine earlier phases that only the full sweep of the merged result found, even though the run was sequential.
- **The quiet campaign was wide.** P4 is the hub; P5, P9 and P14 were nearly roots.
- **The plan's own ordering was wrong in both directions.** Nine dependencies the craft plan implied have no
  data behind them. One real dependency ran backwards: scope moved mid-run, so P14 needed P12's vocabulary,
  through no shared file, which no diff analysis can see. Quiet phases refuted many of their own premises
  (P10 four items of five, P12 five of nine), which moved the effective dependencies away from the plan.
- **Semantic merge hazards are real without any file overlap.** In the craft run, P7's loader delay broke a
  helper P8's tests used. In the quiet run, a prop rename in P5 silently disabled three instruments; one was
  still measuring the old name at the end.

## How much faster, in hindsight

`sim.py` replays each phase as its observed sequence of work and verification segments, under the reconstructed
dependency graph, with N sessions working at once and L verification lanes (isolated environments that can run
the project's stateful gates). Static gates (lint, types) need no lane. Replaying each run as a chain reproduces
its actual duration exactly. Quiet ranges run from all hard edges to dropping the textual-only ones a rebase
resolves.

| Scenario | Craft (58.0 h) | Quiet (31.1 h) | attaform / baton v0.2 |
|---|---|---|---|
| Today's sequential plan; the next phase starts during the closing gates | 53.9 h (1.08×) | 29.3 h (1.06×) | 1.0× / 1.1× |
| One agent that never waits + dependency graph + one separate lane | 47.4 h (1.22×) | 21.5–23.1 h (1.35–1.45×) | 1.0× |
| Parallel sessions that wait on their own gates, one lane (today's stack) | 44.7 h (1.30×) | 14.5–17.4 h (1.8–2.2×) | 1.1× |
| **2 sessions that never wait, 2 lanes** | **40.3 h (1.44×)** | **13.9–16.9 h (1.85–2.24×)** | 1.1× |
| 3 sessions, 2 lanes | 39.8 h (1.46×) | 11.9–16.1 h (1.9–2.6×) | 1.1× |
| Unlimited sessions and lanes | 38.2 h (1.52×) | 10.8–16.0 h (1.9–2.9×) | 1.1–1.24× |

- **The plan's shape decides the gain.** Chains gain about 10%. A plan shaped as instruments, a wide middle,
  then integration gains the most; in the craft campaign the two ends alone were a third of the run.
- **Lanes, not sessions, are the scarce resource.** A third session adds almost nothing; a second lane adds a
  lot. One agent that never waits needs a lane separate from its own checkout (next section).
- **Re-verifying each merge can cancel the gain.** Added per merge as a stateful check:

  | Per merge | Craft: 1 session, 1 lane | Craft: 2 + 2 | Quiet: 1 + 1 | Quiet: 2 + 2 |
  |---|---|---|---|---|
  | none | 47.4 h | 40.3 h | 21.5 h | 13.9 h |
  | 20 min | 52.3 h | 43.2 h | 25.5 h | 15.3 h |
  | 45 min | 59.0 h | 47.4 h | 30.6 h | 19.5 h |
  | 90 min | 70.5 h | 54.4 h | 43.4 h | 25.6 h |

  On one lane, 45 minutes per merge leaves the craft campaign slower than it actually ran. Merges need cheap,
  targeted checks, with the full sweep batched (as P16 effectively was).
- **Starting dependents before their predecessor's closing gates** (two-stage done) adds about an hour.

These are hindsight bounds: a planner at the start would not have known every edge (though the plans also
overstated them). Model work time is held fixed, contention for the machine itself is ignored, and the per-merge
cost is a flat number. Lane occupancy is if anything overstated: the simulator charges the craft campaign 36 h of
stateful verification against the 26.8 h its gates were measured to run.

## Verification is the bottleneck

The product repo verifies against one docker-compose dev stack, and today it cannot verify two things at once:

- The stack mounts the one checkout and hot-reloads it, and gates run inside its containers. A worktree cannot
  verify its own code there, and **any edit invalidates a run in flight**: running targeted tests and editing a
  file while the full unit sweep ran produced 86 false failures, all green in isolation. So even a single agent
  cannot keep working while its gates run, unless the gates run somewhere else.
- Browser gates re-seed fixture tenants, rotate their login tokens and clear the cache; a guard refuses a
  second backend test run anywhere on the machine.
- The runs serialized themselves: at least 12 explicit "nothing else running" orders, and six false-red or
  dead-stack incidents.

A second lane is feasible without changing the product: a checkout at the commit under test (0.5 s and 65 MB as
a worktree), its own compose project (network, database, cache, fixtures, no fixed host ports), shared images,
one seeding pass, and one tracked line scoping the test guard to its project. Each lane costs about 2.5–3.5 GB of
volumes and minutes to boot. The machine fits the main stack plus one browser lane plus one backend-only lane.
How to build a lane is the project's knowledge, not baton's.

## What Claude Code does (2.1.296)

| Mechanism | Result |
|---|---|
| Long foreground Bash | Moved to the background at its timeout (default 2 min, max 10 min), not killed; except a command starting with `sleep` (killed), and a standalone `sleep` ≥ 25 s (refused) |
| `CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS` in claude's environment | ✅ caps the foreground timeout for the main agent: every long command, poll loops included, backgrounds itself. No hook needed |
| PreToolUse `updatedInput` | ✅ rewrites the command or forces `run_in_background`, in every permission mode including auto. The model's transcript keeps its original call; `additionalContext` tells it |
| PreToolUse deny with an instruction | ✅ Haiku ran it as told on its next call, 3/3 |
| Background Bash across `/compact` | ✅ survives; a `task_status` record (with the model's description) is re-attached after the boundary; the completion notice wakes an idle model |
| A hook for "background task finished" | ❌ none. Signals: `UserPromptSubmit` with a `<task-notification>` prompt; the task leaving `Stop.background_tasks`; `PostToolUse(Bash).tool_response.backgroundTaskId` at start |
| Background job when claude is ended (SIGTERM) and resumed | ❌ killed with claude's process group; the resumed session gets a `stopped` notice that does not wake it |
| Monitor and one-shot cron across `/compact` | ✅ both survive. A Monitor is reported in `background_tasks` as `type: "shell"` |
| Telling a Monitor apart | `PostToolUse(Monitor).tool_response` is `{taskId, timeoutMs, persistent}`, and `taskId` is the task's `id` in `background_tasks`. A Monitor whose command ends leaves the list at the next Stop, and its end wakes the model |
| Subagents | Auto-compact on their own; `isolation: "worktree"` creates a worktree and never merges back; at most 20 concurrent by default |
| Workflow tool | Gated by policy, settings and a feature flag; callable only on the user's explicit opt-in, which a skill the user invokes can carry and a hook's `additionalContext` cannot |
| Worker in a PTY nobody watches | ✅ a size change repaints the full visible screen in about 6 ms (a same-size SIGWINCH is ignored; Ctrl-L also works but is a keystroke); resizing mid-turn is harmless; 30 s unread did not stall it; about 271 MB and 0.3% CPU idle |
| Worktree under the trusted project folder | Starts with no folder-trust prompt |

## Consequences for baton

- **The two ideas are one feature.** Never waiting pays off only when there is something else allowed to run,
  which takes a dependency graph. On today's sequential plans it is worth 6–8%.
- **The enabling primitive is a verification lane:** gates run on a frozen commit in an isolated environment,
  so work can continue in the checkout. Lanes are a contract with the project (how to bring one up, run gates in
  it, tear it down), not something baton can infer.
- **Target 2 working sessions and 2 lanes.** That is 1.44× on the craft campaign and up to 2.24× on the quiet
  one, about 17–18 hours saved on each; beyond it the curve flattens.
- **Merge verification must be cheap per merge,** with the full sweep batched and run on the merged result.
- **A plan's dependency graph is a hypothesis.** Edges appear and invert mid-run; the scheduler must accept new
  ones, and the integration sweep stays.
- **Say what a plan can gain before it runs:** a chain is about 1.1×.
- **Backgrounding needs no new machinery:** baton launches claude, so it can set
  `CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS`; `updatedInput` is there for anything finer. A job that must outlive
  baton's own restart or elevation has to be owned by baton.
- **Hidden workers with an attachable viewer are viable** (resize to the viewer to repaint, keep draining,
  restore on detach); [`21-attach`](../../spikes/21-attach/README.md) has the protocol.
- **Unmeasured:** the usage limit at two to three times today's rate.

Found on the way:

- **The monitor check never matches.** A Monitor is reported as `type: "shell"`, so `restartable`'s check for
  `"monitor"` (`internal/loop/loop.go`) never matches, and `BusyBackground` counts monitors as waitable work.
  Fixed in v0.4: baton records each Monitor's `taskId` from PostToolUse and tells them apart by id.
- **Haiku now has auto mode.** Haiku 5.5 runs in auto mode, contrary to the note in `CLAUDE.md`.
- **Idle compaction compacts a session unasked.** From the 2.1.296 bundle: an interactive session whose prompt
  cache has a one-hour TTL is compacted when it has been idle for 90% of that hour (about 54 minutes), provided
  its context holds at least 200k tokens (`CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS`, at least 100k), auto-compact is
  on, no turn is running and the usage limit is not near. A server flag gates it; on 2026-10-10 it was on (`compact`)
  on the machine these runs came from. It needs no prompt; the transcript gets "Compacted while idle, before the prompt
  cache expired". A session that idles while its gate runs elsewhere would lose the phase's context without
  baton deciding it, so baton's session settings set `"idleCompaction": false` (the setting can only turn it
  off). A separate "sleep" compactor exists only for headless hosts started with `CLAUDE_CODE_SLEEP_COMPACT`.
