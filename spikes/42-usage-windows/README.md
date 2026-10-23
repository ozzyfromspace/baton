# 42 — the account's usage windows (Claude Code 2.1.309, Haiku, macOS arm64, 2026-10-23)

Question: v0.8.15 resumes a run as soon as a usage limit resets, for the five-hour window and for the weekly one,
and winds a run down near the end of its weekly allowance. What does Claude Code give baton to do that with?

- Does the status line input carry `rate_limits.seven_day` beside `five_hour`, with which fields, and how often does
  the input refresh?
- Does a `StopFailure` for a usage limit say which window it was and when it resets?

Findings are in [`docs/research/usage.md`](../../docs/research/usage.md); this is the harness and the raw results.

## Harness

- **`capture.py [model] [idle seconds] [agent]`** (defaults `haiku 90`) runs an interactive claude in a PTY
  through `../common/scenario.py`.
  - It uses a scratch project, a scratch `BATON_HOME`, and no `CLAUDE*` or `BATON_*` variables.
  - A status line given with `--settings` runs a logger that appends each input, stamped with when it came, to
    `statusline.jsonl` in a temporary folder outside the repo.
  - After one short turn the session sits idle, then `/exit`.
  - With `agent`, the turn starts a background agent that runs `sleep 8` and `echo tick` six times after the
    lead's turn ends.
  - It prints each distinct `rate_limits`, the input's top-level keys, and the gaps between inputs during the turn
    and while idle.
  - Each capture costs one or two short Haiku turns.
- **`stops.py <transcript folders>`** reads the transcripts under the folders given, the lead's and its agents'
  `subagents/agent-*.jsonl`. For each entry Claude Code wrote for an API error of kind `rate_limit`, it prints only
  the shape: the count per version, window kind, whether a reset is given, and the message with its digits and
  zone blanked.

## Results

**The status line, one run of `capture.py`** (2.1.309; 5 inputs):

| input | at | `rate_limits` |
|---|---|---|
| 1 | start, before any response | `null` |
| 2–3 | 4.0 s and 1.8 s later, in the turn | `{"five_hour": {"used_percentage": 19, "resets_at": 1792742400}, "seven_day": {"used_percentage": 38, "resets_at": 1793257200}}` |
| 4 | 2.3 s later, in the turn | the same, with `five_hour` at 20 |
| 5 | 95.8 s later, at the `/exit` keystroke | the same as 4 |

- Both resets fall on the hour: five-hour 50 minutes ahead, weekly 6 days ahead.
- Nothing came in 90 seconds idle.

**The status line while only a background agent works,** one run of `capture.py haiku 120 agent` (6 inputs):

- The lead's turn started the agent and ended. Its inputs came 3.6, 0.6, 0.8 and 1.3 s apart.
- The agent then ran 11 tool calls over 53 seconds, each a request of its own. The lead's status line got
  nothing in that time.
- The next input came 66 s after the last, when the agent's report reached the lead and started a turn there:
  five-hour 26 → 27.
- Nothing came after that until `/exit`.

**The input's keys:**
`context_window`, `cost`, `cwd`, `effort`, `exceeds_200k_tokens`, `fast_mode`, `model`, `output_style`,
`prompt_cache`, `prompt_id`, `rate_limits`, `scratchpad_dir`, `session_id`, `session_name`, `thinking`,
`transcript_path`, `version` and `workspace`.

**The mod API** (2.1.302's generated types, `plugin/.claude-plugin/types/`) gives the same windows:

- `$.session.usage()` returns `rateLimits`, each with `kind`, `percentUsed` and `resetsAt` (ISO 8601).
- `session.measure` fires "after each main-thread turn, and when a rate-limit window moves a whole point".
- Whether an agent's responses fire it is untested.

**Claude Code 2.1.309's own code** (read from its binary; no strings are kept here):

- The status line builds `rate_limits` from the windows it last read off the API's response headers
  (`anthropic-ratelimit-unified-5h-*`, `-7d-*`). It keeps a window only while its `resets_at` is ahead of now
  and less than a year away.
- `used_percentage` is `min(round(utilization × 1000) / 10, 100)`, so it has one decimal and is capped at 100.
  A third key, `spend_limit`, appears only through a gateway.
- An API error with quota headers is written to the transcript with `error: "rate_limit"` and `quotaLimits`, and,
  for a usage limit, with `apiError: "usage_limit_reached"` and `apiErrorParams.rate_limit_info`.
- A 429 whose quota headers do not parse gets `error: "rate_limit"` with neither of those.
- The window kinds it knows are `five_hour`, `seven_day`, `seven_day_opus`, `seven_day_sonnet`,
  `seven_day_overage_included`, `seven_day_oauth_apps`, `seven_day_cowork` and `overage`.
- No setting refreshes the status line on a timer.

**Transcripts of real runs** (`stops.py` on a private project's transcripts: 72 entries, every one `status:
"rejected"` with `quotaLimits.resetsAt`):

| version | lead | agents | kinds | `apiError`, `rate_limit_info` |
|---|---|---|---|---|
| 2.1.260 | 12 | 7 | five_hour | none |
| 2.1.280 | 1 | 0 | seven_day | none |
| 2.1.289 | 1 | 1 | five_hour | none |
| 2.1.302 | 4 | 6 | five_hour 7, seven_day 3 | `usage_limit_reached`, the same kind and reset |
| 2.1.304 | 5 | 35 | five_hour 29, seven_day 11 | the same |

- The message is `You've hit your session limit · resets 9:10pm (<zone>)` or `You've hit your weekly limit · resets
  Oct 26 at 10am (<zone>)`. It gives local wall time with an IANA zone, the date for the weekly only, and no year.
- baton's records of the same runs logged 42 `stop_failure` events with `error: rate_limit`. Each loop's state
  keeps its last stop's input (`Run.LastError`), and 21 of those are usage stops. None of the 21 has
  `error_details`. 20 have the message as `last_assistant_message`; the one without it is from before baton
  recorded that field.
- For 5 of 5 lead stops whose times match, the transcript entry was written 20 to 30 ms before baton logged
  `stop_failure`.

## Files

- `capture.py`: the status line capture.
- `stops.py`: the shape of usage stops in transcripts.
