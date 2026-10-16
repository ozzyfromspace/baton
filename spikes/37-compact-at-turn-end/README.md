# 37 — a compaction called from the lead's turn.complete (Claude Code 2.1.301, Haiku, macOS arm64, 2026-10-16)

Question: in a real run (v0.8.3 and v0.8.4), a compaction went the same way 15 times in 13 hours. The lead's Stop hook
ran, baton queued the compaction and asked the mod for it within 100 ms, Claude Code refused it ("a turn is
running (<turnId>); the conversation compacts between turns, so call it from turn.complete or later"), the host logged
`compact_declined` and `compact_retry`, and asked again about 3 s later, which worked. The host sees the turn over at
the Stop hook, which runs a moment before Claude Code counts it over. Can the mod hold such a compaction and run it from
the lead's `turn.complete` hook instead? Does Claude Code take a compaction called there, from inside the hook, before
or after `next(e)`, or from a timer the hook starts?

Harness: `run.py <mode> <workroot>` runs an interactive claude in a PTY (stdlib only), with this spike's mod
(`mod/hooks/register.js`) loaded by `--plugin-dir` and no user settings, types one prompt, and prints the mod's log
of every lead `turn.start`, the classic `Stop`, the lead's `turn.complete`, its own `session.compact` hook, and each
`$.session.compact()` call: when it was made, how it stood 400 ms later, and how it ended. `SPK_MODE` says where the
mod calls it (the modes are in `run.py`). Times are ms from the turn's start; one run per mode.

| mode | where the call is made | result |
|---|---|---|
| poll | a `$.clock.every` timer set at `session.start` (as baton's poll), while the Stop hooks run (a 1.5 s mod Stop hook stands in for a slow settings one); refused, again from the lead's `turn.complete` after `next(e)` | **The incident, reproduced:** refused at once, `HooksError: spkc: $.session.compact: a turn is running (<turnId>); the conversation compacts between turns, so call it from turn.complete or later`. `turn.complete` came 4 to 6 ms after the classic `Stop` ended. **Called from it, it compacted**: still pending at 400 ms, resolved 3.6 to 4.6 s later with the summary. Twice, the same. |
| before | inside the lead's `turn.complete` hook, before `next(e)` | Taken: pending at 400 ms (the hook waited), compacted 4.6 s later. |
| after | inside the lead's `turn.complete` hook, after `next(e)` | Taken: compacted 3.1 s later. |
| timer | `$.clock.after(0)` started in the lead's `turn.complete` hook | Called 8 ms after the hook returned; taken, compacted 4.1 s later. |
| stoptimer | `$.clock.after(0)` started in the mod's classic `Stop` hook after `next(e)` | It fired 7 ms after `turn.complete` began, before the hook returned; taken, compacted. |
| held | `$.clock.after(0)` started in the mod's classic `Stop` hook before `next(e)` | Refused, in other words: `session.compact: called from a classic.Stop hook, it would compact under the turn this hook is holding; call it from a later event (turn.complete) (host check)`. A timer started in a hook carries that hook's context. Called again from `turn.complete`: taken, compacted. |
| stop | inside the mod's classic `Stop` hook, after `next(e)`, not awaited | Taken, and compacted once the turn had ended (`turn.complete` 4 ms later). In the same run, a second call from `turn.complete` while it ran was refused: `a turn is in flight; the conversation compacts between turns`. |
| `-p` | any call in a headless session | `$.session.compact: not available in a headless (-p / SDK) session yet` |

Also: **the mod's own `session.compact` hook runs for a call made inside one of its hooks** (`trigger: plugin`), unlike
a call from its poll timer (spike 35), so a mod that reports the lead's compactions from that hook must tell its own
call apart.

## What it means for baton (v0.8.5)

- **The mod holds a compaction refused while the lead's turn runs, and runs it at that turn's end.** The lead's turn
  runs by the mod's own account: its `turn.start` heard (no `agentId`), its `turn.complete` not yet. Held, the command
  is not acked; the lead's `turn.complete` calls it, after `next(e)` and inside the hook, so it runs before any prompt
  queued behind the turn, and its ack then says it started. A turn that ends while the call is being refused leaves
  nothing to hold for: it is called again at once. Past 30 s (`HOLD_MS`, within the host's one-minute `modAckWait`) its
  ack carries the refusal and the host asks again after the turn (`loop.redo`), as before: the fallback.
- **The mod's own call is not reported from its `session.compact` hook** while it runs from `turn.complete`: the
  command's ack and `compacted` report say how it went, as for a call from the poll.
- The host stays as it is: it still asks once the Stop hook has run, which the mod now turns into a compaction at the
  turn's end, with no `compact_declined`.
