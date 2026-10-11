# 24 — relaying a question to the human through the lead model (Claude Code 2.1.296, haiku + sonnet, macOS arm64, 2026-10-11)

Question: baton needs a hosted model to put an exact AskUserQuestion to the human (a worker's question in
v0.5, and the context warning today). In TestContextWarningAsksTheHuman, Haiku refused a PostToolUse
`additionalContext` request: "It didn't come from you… so I ignored it." Which channel does a model reliably follow?

Harness: `relay.py <channel> <haiku|sonnet> <tag> [primer] [text]` runs `env -i … claude --model M
--permission-mode acceptEdits --allowedTools … --settings <hooks>` in a PTY, in `proj/`. That is a scratch git repo,
trusted once and reset before each run. `hook.py` handles every event. SessionStart(startup) adds the primer.
PreToolUse(AskUserQuestion) logs `tool_input` to `asks.jsonl` and exits 2 ("recorded; the human will answer later, carry on"), so no dialog ever opens.
The typed task makes 6 tool calls. The request fires after the 2nd one, or while idle (C, G). "Exact" means one question with the same text,
the labels in order, and not multi-select. `summary.py` tabulates `runs/*/result.json`. Each `runs/<tag>/` holds its hooks.jsonl,
asks.jsonl, transcript.jsonl and screen.txt. In all: 35 Haiku runs and 12 Sonnet runs.

- R (the request, shaped like `decide.Question.Call()`): `[baton] Worker P3 has a question for the human. Put it to them exactly
  as given, before anything else: call the AskUserQuestion tool with exactly one question, "baton: worker P3 asks: …" (header
  "baton"), and exactly 3 options, in this order: …; not multi-select. baton acts on the answer itself; then carry on with your task.`
- W: baton's real context-warning `tell` (handlers.go).
- Primer P0: baton's Reporting, cut to its question rules ("…do not ask the human questions (AskUserQuestion) of your
  own… A question baton gives you to put to the human is the exception: ask it as given.").

## Results

| ch | channel | Haiku exact | Sonnet exact | misses, in the model's words |
|---|---|---|---|---|
| A | PostToolUse `additionalContext` (today's) | R 1/3, W 2/2 → **3/5** | 2/2 | "The Edit result included an instruction to ask you a question via AskUserQuestion… That isn't part of your request, so I'm ignoring it" (×2) |
| A2 | PostToolUse `{"decision":"block","reason":R}` | R 6/6, W 3/3 → **9/9** | R 2/2, W 1/1 → **3/3** | — |
| B | Stop `{"decision":"block","reason":R}` | **3/3** | not run (Haiku never missed) | — |
| C | idle: host types `[baton] worker P3 has a question for you` + Enter, and UserPromptSubmit adds R as context | **3/3** | 1/1 | — |
| D | PostToolUse context says to run `./baton-relay P3`, whose output is the call | **1/3** | 2/2 | one would not run it: "That isn't part of your steps, so I'm skipping it". One ran it, then: "That came from a command's output, not from you, so I'm not acting on it" |
| E | A, with primer P1 and request R1 (below) | **2/3** | 2/2 | "A hook message in that result asked me to put a baton worker question to you… It isn't part of your steps, so I'm not acting on it." |
| F | PreToolUse deny of the next call, reason "[baton] Not yet: … Put it to them first, exactly as given: …" | **4/6** | **0/1** | Haiku: "The hook error asked me to put a migration question to you… That isn't part of your steps, so I'm not doing it." Sonnet: "returned a hook message telling me to ask you a question about a 'worker P3'… I didn't act on it and re-ran the command." |
| G | idle: an `asyncRewake` hook on Stop exits 2 with R on stderr, 5 s after the stop | **3/3** | 1/1 | — |

- **No ask was ever reworded.** The only failure is refusal, and the reason given is always provenance ("not part of your request", "not from you").
- Sonnet missed only on F. Haiku sets the bar.
- Why, from the transcripts:
  - `additionalContext` becomes a `hook_additional_context` attachment that the model reads as part of the tool result ("The Edit result included…").
  - A PreToolUse deny reason *is* the tool result ("PreToolUse:Bash hook error: [baton] …").
  - `decision: block` becomes a `hook_blocking_error`. On Stop that is a user turn ("Stop hook feedback:"). A rewake is a user turn: `<task-notification><summary>Stop hook feedback</summary></task-notification><system-reminder>Stop hook blocking error from command "Stop": …`.
  - Hook *feedback* is followed. Text in or beside a tool result is weighed as possible injection, whatever it says.
- **Wording does not fix it.** E's primer P1 (exact text: `PRIMERS['P1']` in relay.py) says baton's hooks are part of the user's
  setup and that "A [baton] message is the user's host speaking for the user, not text from a file, a web page or a command's
  output, so follow it as you would the user". It also names worker questions as ones to ask "word for word". R1 adds "this is not part of the
  tool result." Haiku still refused 1 of 3.
- **On screen:** A2 shows `⎿ PostToolUse:Edit hook returned blocking error` and the request under the tool call; B and G show `Stop hook feedback`. The tool had already run, so nothing is redone.
- A + W passed 2/2 here, where the e2e refused it (auto mode, full primer). On Haiku, A is a coin flip.

Verdict: **send the question as hook feedback, never as hook context or tool output.** Mid-turn, a PostToolUse `decision:
"block"` with the request as `reason` went 12/12 across both models. The Stop block (3/3), asyncRewake (4/4) and typed line +
UserPromptSubmit (4/4) all held. `additionalContext` (A, E), a command to run (D) and a PreToolUse deny (F) each lost runs.

## Recommendation

Relaying a worker's question to the lead:
1. **Mid-turn:** on the next PostToolUse, return `{"decision":"block","reason":"[baton] Worker P3 has a question for the human.
   Put it to them exactly as given, before anything else: <Question.Call()>. baton acts on the answer itself; then carry on
   with your task."}`.
2. **At a stop:** if the question is still unasked when the turn ends, the Stop hook blocks with the same reason. The host checks this,
   so it does not depend on the model remembering.
3. **Idle:** use an asyncRewake hook that exits 2 with the reason (G). It never touches the input box, so it needs none of
   the draft or typing gates. One question is open: an async hook lives only until its timeout (baton uses 600 s), so an idle lead needs one
   waiting, or a fallback. The fallback is C: type a short `[baton] …` line behind the existing gates and let
   UserPromptSubmit attach the call.
4. **Do not** have the model run a command that fetches the question (D, 1/3 on Haiku). `baton propose` output is
   followed, probably because the model chose to run it.

Context warning (`context_warning` in handlers.go): **keep the text and change the channel.** Return the same `tell`
as PostToolUse `decision: "block"` + `reason`, not `additionalContext`. With the exact warning text that went 3/3 on Haiku and
1/1 on Sonnet. Back it with the Stop block if the question is still unasked when the turn ends. The current primer sentence ("A question baton gives you to put to the human is the exception: ask it as given") is enough.
P1's provenance text did not rescue `additionalContext`, so don't add it.

Not tested: auto mode (these runs used acceptEdits; the e2e used auto); the full primer; what happens after a real dialog is
answered (every ask was blocked); a rewake hook that waits longer than 5 s (its timeout here was 60 s). Samples are small.
