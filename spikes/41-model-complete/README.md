# 41 — a model call from the mod's poll (Claude Code 2.1.302, Haiku, macOS arm64, 2026-10-17)

Question: v0.8.7's advisor (P14) and risk assessment (P19) want one model call each, with no history, for a short
answer the host appends to a look-in. The mod is where the session's own client and credentials are
(`$.model.complete`), and the host decides and words, so the call would be a bridge command: the host writes the
prompt, the mod runs the call from its poll and acks with the answer. Does that work cleanly? Does it answer while
the lead's turn runs and while it is idle, how fast, and does anything of it reach the session (the transcript, a
turn, the screen)? What does it report as usage, and what do an API error, a time limit and a refused request give?
Does `haiku` (and the `opus` P19 plans) pass the allowlist? How does the test kit stub it?

Harness: `run.py <workroot>` runs an interactive claude (`--model haiku`) in a PTY (stdlib only) in a fresh scratch
project that sets `plansDirectory` and allows one exact Bash command, with no user settings
(`--setting-sources project,local`), no `CLAUDE*` or `BATON*` variables, and only this spike's mod (`mod/`, loaded
with `--plugin-dir`). It writes numbered command files the mod's poll (`$.clock.every(500)`, set at
`session.start`) runs once each, in order, as baton's bridge commands run; each is a `$.model.complete` call the
poll does not wait for. The mod logs each call's result and latency, the lead's `turn.start` and `turn.complete`,
and every row the session appends (`session.append`). Three runs; the numbers are the last run's, the others agree (in the first, Claude Code refused a bare `sleep 25`,
so its turn's calls ran with the lead idle; `run.py` now runs `sh slow.sh`).
`kit/kit.sh` runs `kit/kit.test.ts` against the mod under `claude plugin test`.

| case | request | result |
|---|---|---|
| idle | `haiku`, a short system and prompt, `maxTokens: 400`, `timeoutMs: 60000`; three, 5 s apart, before any prompt | `{isAnswered: true, text: "PONG-1", usage: {input_tokens: 36, output_tokens: 7, cache_read_input_tokens: 0, cache_creation_input_tokens: 0}}`, in 0.52 to 1.6 s over three runs. **No turn started** (the mod's count of lead `turn.start` stayed 0), **no row appended**, nothing on the screen. |
| turn | the same, twice, 6 s into the lead's turn, while it ran a 25 s Bash command | Answered in 0.53 and 0.65 s, the turn running before and after (same `turnId`); the turn went on to its answer untouched, and `turn.complete`'s usage is the turn's alone. |
| opus, sonnet | `model: "opus"` and `"sonnet"`, `maxTokens: 50`, in the turn | Answered in 1.3 and 1.1 s: **both aliases pass the allowlist**, as `haiku` does. |
| advisor size | about 11,500 characters of a made-up gate log, marked as data, to `haiku`, `maxTokens: 400` | 3.5 s, 6,959 input tokens, a 560-character answer. Asked for "at most 600 characters", it answered in Markdown (`**Verdict: …**`): P14's prompt should ask for plain text. |
| risk size | about 30,000 characters to `opus`, `maxTokens: 800` | 2.5 s, 17,810 input tokens, 135 output. |
| empty block | `prompt: [{text: ""}]` | `{isAnswered: false, reason: "api-error", status: 400, error: "unknown", usage: <zeros>}` in 0.22 s. The kind is `unknown`, not `invalid_request`. |
| no such model | `model: "claude-no-such-model-1"` | `{isAnswered: false, reason: "api-error", status: 404, error: "model_not_found", usage: <zeros>}` in 0.2 s: an id that names no model is the API's error, not the engine's refusal. |
| time limit | `timeoutMs: 1` | `{isAnswered: false, reason: "aborted", usage: <zeros>}` in 3 ms. |
| refused | `maxTokens: 200000` | **Rejects** at once: `HooksError: spkm: $.model.complete: maxTokens 200000 is past what claude-haiku-5-5 can produce in one reply (64000)`. Only a request the engine will not send rejects (a blocked model, a bad cap). |
| refusal | not provoked (baton's tests and spikes never provoke one) | The result has no refusal arm (`ModelCompleteResult`): a reply the model refuses comes back as an answer with its text, or `empty-reply` with none. |

Also:

- **Nothing of it reaches the session.** In every run the transcript holds the human's one prompt and the lead's
  turn, and neither a call's prompt nor its answer (`SPK41` and `PONG` occur 0 times in it, its `api-request` rows
  included); the mod's `session.append` hook saw no row while the calls ran; the screen shows none of it. The
  session's `cost-state` row leaves the calls out: its `modelUsage` names only Haiku, with the same counts in a run
  with two more 7k-token Haiku calls, and no Opus or Sonnet.
- A call made from the poll's timer belongs to no hook's dispatch, so no turn's Esc can abort it (the types: a call
  is `aborted` when "the dispatch that made the call was aborted", its own signal fires, its `timeoutMs` elapses, or
  the plugin's environment unloads). A reload of the mod mid-call loses the answer: the mod keeps the calls it runs
  in its store, and acks one it finds there when loaded as interrupted, as it does a command it was running.
- **The test kit** (`kit/kit.test.ts`): `$.model.complete` raises `model.complete` with `e` the request
  (`model`, `system`, `prompt`, `maxTokens`, `timeoutMs`); a test's `on('model.complete', …)` answers
  `{ value: <ModelCompleteResult> }`, any arm (an `api-error` too), or `{ deny }`, which rejects the call
  (`HooksError: <plugin>: $.model.complete: <why>`). With no stub, the call rejects:
  `HooksError: no implementation for model.complete`.

## What it means for baton (v0.8.7)

- **It works cleanly**: P14 and P19 go ahead with model calls.
- **A `complete` bridge command** (`bridge.Complete`: model, system, prompt, max tokens, time limit). The mod starts
  the call from its poll and does not wait for it (a call takes seconds, and the poll runs spawns, messages and
  compactions meanwhile); its ack, when the call resolves, carries the answer, cut at 4,000 characters, with how
  long it took and the tokens it used, or why there is none: `api-error <status> <kind>`, `empty-reply`, `aborted`,
  or the engine's refusal (the call's `.catch`, reason `refused`). The calls in flight are kept in the mod's store;
  one found there when the mod is loaded is acked as interrupted.
- **The host's helper** (`loop.Complete`) issues it and waits for the ack with a deadline, returning the text or why
  not: no mod in the lead's claude, a serial run, the model's error, interrupted, or the deadline (the command is
  then taken back, and an ack after it is dropped). An old mod that knows no `complete` acks "no such command", which
  is a why-not like any other, so `bridge.Protocol` stays 2.
- At these latencies (about 1 s for a short Haiku call, 3.5 s for the advisor's size, 2.5 s for Opus at the risk
  assessment's), P14's 60 s wait and P19's 2 minutes are bounds that should only be reached when something is wrong.
