# Usage limits: what baton can read about both windows

**In short:** Claude Code gives baton enough to know which usage window stopped a session and the exact second it
resets, for the five-hour window and the weekly one alike.

- **The status line** carries `rate_limits.seven_day` beside `five_hour` on 2.1.309. Both have the same fields:
  `used_percentage` (0 to 100, with one decimal) and `resets_at` (Unix seconds). It refreshes only with the
  lead's own turns.
- **A usage stop is written to the stopped loop's transcript** before `StopFailure` fires. The entry has
  `quotaLimits.rateLimitType` (`five_hour` or `seven_day`) and `quotaLimits.resetsAt`. All 72 usage stops in a
  private project's transcripts had both, from leads and agents, on Claude Code 2.1.260 to 2.1.304.
- **`StopFailure` itself names no reset:** its `error_details` was empty every time. Its message says the reset
  only as local wall time.

baton reads none of this today. In two real runs, both weekly limits were taken for unknown resets.

- At one, baton retried into the same limit twice, 15 and 30 minutes on, with the reset 4 days away. At the
  other, the human stepped in.
- Four five-hour stops read 99% and were logged with their reset unknown, though it was more than an hour off.
  Only the stops beside them, which read 100, held the run until the reset.
- Every resumption seen was baton's own retry or the human's prompt: no session resumed by itself.

Studied 2026-10-23 on Claude Code 2.1.309 ([spike 42](../../spikes/42-usage-windows/)) and from its binary.
Also from a private project's baton records (runs on 2.1.302 and 2.1.304) and its transcripts (2.1.260 to
2.1.304); only their numbers are given here.

The questions:

- Does the status line carry the weekly window, with which fields, and how fresh is it?
- Does a stop on a usage limit say which window it hit and when that resets?
- What did baton do with the stops of a real run, and what can v0.8.15's hold, resume timer and wind-down rely on?

## The status line

**The weekly window is there.** On 2.1.309, after the first response, the input carries:

```json
"rate_limits": {
  "five_hour": {"used_percentage": 19, "resets_at": 1792742400},
  "seven_day": {"used_percentage": 38, "resets_at": 1793257200}
}
```

- `rate_limits` is `null` before the first response.
- Both resets fell on the hour: the five-hour one 50 minutes ahead, the weekly one 6 days ahead.
- In the real runs, five-hour resets fell on a whole ten minutes (:10, :30, :50) and weekly ones on the hour.
- baton already had `seven_day` in its test fixtures, but reads only `five_hour` (`statusline.Input.FiveHour`).

**What the fields are,** from Claude Code's code:

- **The source:** both windows come from the API's response headers (`anthropic-ratelimit-unified-5h-*` and
  `-7d-*`), so they are the account's readings as of the session's last response.
- **`used_percentage`** is `min(round(utilization × 1000) / 10, 100)`: one decimal, capped at 100. Every reading
  seen was a whole number.
- **A window is left out** once its `resets_at` has passed, until a new response brings a fresh reading.
- **Other windows:** a third key, `spend_limit`, appears only through a gateway. Claude Code knows more kinds
  (`seven_day_opus`, `seven_day_sonnet`, `seven_day_overage_included`, `overage` and others), but gives the
  status line only these.

**How fresh it is:**

- **During a turn,** the input arrived 1.8 to 4 seconds apart, at each change in the session.
- **While idle, nothing came** for 90 seconds; the next input came at a keystroke. No setting refreshes the status
  line on a timer.
- **While only a background agent worked, nothing came either.** The agent made 11 requests over 53 seconds
  with the lead idle, and the lead's status line got no input. The next input came when the agent's report reached
  the lead and started a turn there.

A phase agent has no status line, so baton's only reading is the lead's. It is as old as the lead's last turn,
though every session of the run spends from the same account.

## A stop on a usage limit

**`StopFailure`'s input:** `error: "rate_limit"`, an empty `error_details`, and the message in
`last_assistant_message`. Each loop's state keeps its last stop's input (`Run.LastError`), and 21 of those in
the private project's records are usage stops. None of the 21 has `error_details`. The message is one of two forms, with
local wall time, an IANA zone and no year:

- `You've hit your session limit · resets 5:50am (<zone>)`
- `You've hit your weekly limit · resets Oct 26 at 10am (<zone>)`

**The transcript says it exactly.** Claude Code writes the stop as an assistant entry in the stopped loop's own
transcript: the session's for the lead, `subagents/agent-<id>.jsonl` for an agent.

```json
{"type": "assistant", "isApiErrorMessage": true, "error": "rate_limit", "apiErrorStatus": 429,
 "message": {"model": "<synthetic>", "content": [{"type": "text", "text": "You've hit your session limit · resets …"}]},
 "quotaLimits": {"status": "rejected", "rateLimitType": "five_hour", "resetsAt": 1792489800,
                 "overageStatus": "rejected", "isUsingOverage": false, "…": "…"},
 "apiError": "usage_limit_reached",
 "apiErrorParams": {"rate_limit_info": {"status": "rejected", "rateLimitType": "five_hour", "resetsAt": 1792489800, "…": "…"}}}
```

- **Every usage stop had it.** All 72 such entries in the private project's transcripts carry `quotaLimits` with
  `rateLimitType`, `resetsAt` and `status: "rejected"`. That is 23 of leads' and 49 of agents', on 2.1.260,
  2.1.280, 2.1.289, 2.1.302 and 2.1.304.
- **The kinds:** 57 were `five_hour` and 15 `seven_day`.
- **Since 2.1.302,** `apiError: "usage_limit_reached"` and `apiErrorParams.rate_limit_info` say the same again.
- **It is there before the hook runs.** For the 5 lead stops whose times can be matched, the entry was written 20
  to 30 ms before baton logged `stop_failure`.
- **A 429 that is not a usage limit** is written with `error: "rate_limit"` and no `quotaLimits`: in 2.1.309's
  code, that is a 429 whose quota headers do not parse. So a `rate_limit` stop with no `quotaLimits` is not known
  to be a usage window.

## What two real runs did

Two runs of the private project, on Claude Code 2.1.302 and 2.1.304: baton logged 27 `usage_limited` events in
five stops of the whole run.

1. **A weekly limit, taken for unknown.**
   - The status line read 27% for the five-hour window.
   - baton logged the limit with `reset_known: false` and retried 15 minutes later, then 15 minutes after that.
     Each retry met the same limit within two seconds. The lead's transcript holds one weekly-limit error for the
     stop and one for each retry.
   - baton's own plan, running on the same account at the time, met the limit 13 times in that hour.
   - The weekly reset was 4 days 8 hours away.
2. **A five-hour limit, read as 99% for its first two stops.**
   - Six sessions stopped within 81 seconds. The first two logged `used_pct: 99` and `reset_known: false`, which
     holds 15 minutes. The other four logged 100 with the reset known, an hour and 28 minutes ahead, and held the
     run until then.
   - At the reset plus baton's minute of grace (`limitGrace`), the lead and six agents resumed in the same
     second. Six of them got baton's retry (`error_retry`); the seventh got a result baton had held for it. None
     met the limit again.
3. **The same again:** two stops read 99% with the reset unknown, then six at 100% known.
   - The six agents resumed through baton's retry at the reset plus one minute.
   - The human's own prompt had resumed the lead 20 seconds before that.
4. **A weekly limit at 77% five-hour use,** again taken for unknown. The weekly reset was 5 days 21 hours away.
   The human stepped in three minutes later.
5. **A five-hour limit** at 100%, with the reset known.

**Two corrections to earlier notes:**

- [`workers.md`](workers.md) says interactive sessions wait out a usage limit by themselves and resume when it
  resets, and `loop/usage.go` says the same. In these runs nothing started a turn before baton's retry or the
  human's prompt: a `StopFailure` ends the turn, and only a line, a message or a prompt starts the next one.
- A limit's stop can read 99%, not 100. Four of the 14 `usage_limited` events in stops 2 and 3 did.

## What v0.8.15 can rely on

- **The weekly reading:** `rate_limits.seven_day` has the same shape as `five_hour`, so it is parsed the same way.
  It is absent before the first response and after its reset. A reading may have a decimal, so compare it as a
  number, never as an integer percentage.
- **Which window a stop hit, and when it resets: the stopped loop's transcript.**
  - Take its last entry with `isApiErrorMessage` and `error: "rate_limit"`, and read `quotaLimits.rateLimitType`
    and `quotaLimits.resetsAt`.
  - Read it in the `StopFailure` hook, before the lock, as the hook already reads the transcript for a refusal:
    `transcript_path` for the lead, `agentTranscript(input)` for an agent.
  - Keep the window and the reset on the stop's record (`state.StopError`), so the coordinator's `usage` takes
    them from the lead's or an agent's state as it takes the hit today.
  - Any `seven_day…` kind is the weekly window. Any other kind still gives its reset.
- **No reset from `error_details`:** it is empty. The message's local time could be parsed as a last resort, but
  every usage stop seen had `quotaLimits`, so nothing needs it.
- **The status line comes second.** When the transcript gives nothing, a window at 99% or more names the limit,
  the weekly window first. That is the case P9 planned for.
- **Unknown stays unknown.** A `rate_limit` stop with no `quotaLimits` and no window at 99% is a 429 that is not
  known to be a usage limit, and today's retry (15 minutes, then 30) fits it. On Claude Code 2.1.260 and later, a
  usage stop with an unknown reset should not happen.
- **The resume is baton's.** Nothing resumes a stopped session by itself.
  - The reset plus one minute was enough for the 13 sessions baton resumed over two resets. Two minutes, as P10
    planned, adds margin.
  - The status line drops a window once its reset passes, so the timer goes by the reset it stored, never by a
    fresh reading.
- **The wind-down reads the lead's weekly reading,** the only one there is.
  - With `weekly_reserve_pct` at 2, the line is `used_percentage >= 98`.
  - The reading moves only with the lead's turns. While agents work and the lead sits idle, the account's use
    goes on and the reading stands still. In a run the lead is woken for each landing and result, so the
    reading is at most that old. A weekly limit hit before the line shows is P10's case, and the timer covers
    it.
  - The mod API gives the same windows: `$.session.usage()` returns `rateLimits` (`kind`, `percentUsed`,
    `resetsAt`). Its `session.measure` event fires "after each main-thread turn, and when a rate-limit window moves
    a whole point" (2.1.302's types). If agents' responses move those windows, the mod could report a fresher
    reading than the status line. That is untested, and this release leaves the mod untouched.
  - The weekly reset can differ between accounts and move when the human switches accounts (`/login`). That is a
    prompt of the human's, which cancels the resume timer anyway.
