# 34 — a deadline on baton's question, with no key typed (Claude Code 2.1.296, haiku, macOS arm64, 2026-10-12)

Question: baton puts questions to the human with a deadline: a phase agent's proposal goes ahead when nobody
answers it, and so does the lead's own. v0.7's host answered at the deadline by typing 3 into the dialog, and
spike 32 (S7) found that a 3 typed with a permission prompt in front approves the command and turns auto mode on.
`$.ui.ask` has no timeout and no way to close it. Can baton's mod settle a question at its deadline without a key?

Harness: `run.py <scenario> <workroot>`, spike 32's (claude in a PTY in a fresh git repo, a settings hook on every
event, the PTY's bytes kept so a screen renders as drawn), with this spike's mod (`mod/hooks/register.js`).
`/spk-ask <timer|render> <deadline ms> [delay ms]` asks a question of the mod's own after the delay: from a timer
(as a command poll would), or queued for the band's render hook, which calls `$.ui.ask` itself. The mod's
`tool.call` hook for `AskUserQuestion` races what runs beneath it (`next(e)`: the dialog) against the question's
deadline, and at the deadline answers the call itself: a hook that returns while its `next` is pending aborts what
runs beneath. `/spk-deadline <question> <ms>` puts a deadline on a question the model asks.

| scenario | what | result |
|---|---|---|
| A | asked from a timer, a 6 s deadline | **The mod's own `tool.call` hook does not see it** (re-entry: from a timer the whole module is skipped, spike 32 S0), so nothing bounds it: the dialog was still up after the deadline, and Enter answered it (Go ahead, 13 s). |
| B | asked from inside the band's render hook, a 6 s deadline | **Seen, and closed at the deadline.** The hook saw the call (origin the mod's own plugin), returned at 6 s, and the dialog left the screen; the call beneath resolved as rejected. Answered with the tool's own output shape (`{ questions, answers: { [question]: label } }`), `$.ui.ask` resolved to that label (`Let baton decide`, 6016 ms); a wrong shape made it reject instead ("does not match its output shape"), the dialog closed all the same. The settings hooks saw `PreToolUse` and `PermissionRequest`, and **no `PostToolUse`**: the hook answered, so core never finished the call. A first try failed only because the mod awaited a log line before calling `$.ui.ask`, so the render hook had returned: the call must be made while the hook runs. |
| C | from the render hook, answered in time (Enter at 3 s, a 20 s deadline) | The answer stands (`Go ahead`); the race ended with `next`'s result, and `PostToolUse` fired as usual. |
| D | the model's own `AskUserQuestion` (as the lead asks baton's questions), an 8 s deadline | Seen (origin the engine), closed at 8 s: the transcript reads "User answered Claude's questions: spk: proceed? → Yes", the model got `"spk: proceed?"="Yes"` and replied Yes. Again **no `PostToolUse`**. |
| E | from the render hook, queued while a permission prompt is open | **Not asked until the prompt was answered**: the band is not drawn while a dialog is open (spike 32 S0b), so the render that asks came only after Enter approved the command; then the deadline closed it. A question asked this way never waits behind a permission prompt. |

Consequences for baton (v0.8, P4):

- The mod asks baton's questions from its band's render hook, calling `$.ui.ask` before it awaits anything, never
  from its poll timer.
- Its `tool.call` hook bounds every one of baton's questions, the lead's own included, by the deadline and the
  answer Go gives it; nothing types into a dialog any more.
- A question settled at its deadline fires no `PostToolUse`: baton records the answer from the mod's report, and
  closes the dialog in its record of what is on the lead's screen.
