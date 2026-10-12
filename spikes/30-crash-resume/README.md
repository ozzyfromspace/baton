# 30 — a claude that dies mid-turn, and `--resume` (Claude Code 2.1.296, haiku, macOS arm64, 2026-10-12)

Question: when the lead's claude dies (killed, crashed), can baton bring the same conversation back with `--resume`,
and how does a resume that cannot work fail?

Harness: `run.py <scenario>` runs `claude --model haiku --allowedTools 'Bash(sleep:*)' 'Bash(echo:*)'` under a fixed
`--session-id` in a PTY, in a fresh git repo under `work/<scenario>/`, kills it with SIGKILL, then starts
`claude --resume <id>` in a new PTY and asks "Reply with only the word ok." `hook.py` logs every hook event (as in spike 29).
To kill mid-tool-call for certain, its `PreToolUse` holds the call while `work/<scenario>/hold` exists. Each
`result.json` holds the transcript's tail after the kill and after the resume.

| scenario | what | result |
|---|---|---|
| K1 | SIGKILL while a Bash call waits in `PreToolUse` | The tool call never reached the transcript. `--resume` fires `SessionStart(resume)` in ~5 s; Claude Code adds an assistant reply "No response requested." to the interrupted prompt, and the next prompt is answered (`Stop`, no `StopFailure`). |
| K2 | SIGKILL ~2.5 s into a long answer (no tools) | The same: nothing of the answer was kept; the resume works and the next prompt is answered. |
| K3 | a clean turn, `/exit`, then half of a JSON line appended to the transcript (a write cut off) | `--resume` works (`SessionStart(resume)` in ~1 s) and the next prompt is answered. |
| K4 | `--resume` of an id with no transcript | `No conversation found with session ID: …`, exit code 1, ~4 s in (trust prompt included). No `SessionStart`; a `SessionEnd` fires. |

- **A killed claude comes back with `--resume`, mid-tool-call or mid-answer.** The turn in flight is lost, and the model is not told: its next
  prompt has to say what happened and what to continue.
- **A cut-off last line does not stop a resume.** A conversation that cannot be resumed at all is one with no transcript, and claude says so
  and exits within seconds, before any `SessionStart`. That is the case for a fresh session from the brief.
- In K1, an earlier version without the hold let Haiku run `sleep 25` in the background or skip the tool, so the kill landed after the
  turn. The hold makes the timing certain.

Consequences for baton: [`docs/research/recovery.md`](../../docs/research/recovery.md).
