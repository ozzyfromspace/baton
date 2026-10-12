# 29 — what a rewind leaves behind (Claude Code 2.1.296, haiku, macOS arm64, 2026-10-12)

Question: a rewind (Esc Esc, or `/rewind`) changes what the model remembers, and maybe the files, without baton
knowing. What does it leave that baton could read deterministically, and how far back can it go?

Harness: `run.py <scenario>` runs `claude --model haiku --permission-mode acceptEdits --allowedTools 'Bash(echo:*)'`
in a PTY, in a fresh git repo under `work/<scenario>/`. `hook.py` logs every hook event, with the last entries of the
transcript as it ran. Each step's screen is in `screen-<label>.txt`, and the transcript is copied in.
The three turns are: `echo one`; Write `a.txt` (`alpha`); `echo three`.

| scenario | what | result |
|---|---|---|
| explore | Esc Esc after three turns | The menu, "Rewind — Restore the code and/or conversation to the point before…", lists each prompt with its code changes ("No code changes", "a.txt +1") and `(current)` last, selected. Up moves up. Enter asks "Confirm you want to restore to the point before you sent this message: … The conversation will be forked. The code will be restored -1 in a.txt." with `1. Restore code and conversation 2. Restore conversation 3. Restore code 4. Summarize from here 5. Summarize up to here`, and "⚠ Rewinding does not affect files edited manually or via bash." |
| A | rewind to before the Write turn, `2` (conversation only), then `echo four` | `a.txt` stays. **No hook fires during the rewind.** The session id stays the same, and so does the transcript file. The next prompt is an ordinary `UserPromptSubmit`. |
| B | the same, `1` (code and conversation) | `a.txt` is gone. The transcript has no entry that says code was restored; the next `file-history-snapshot` lists `a.txt` with a new version and no backup. |
| C2 | a codeword, `/compact` (told to leave codewords out), rewind to the `/compact` entry, `1. Restore conversation`, ask for the codeword | **A rewind cannot reach past a compaction.** Only the `/compact` entry and the prompts after it are listed (with "⚠ No code restore"), and its options are `1. Restore conversation 2. Summarize from here 3. Summarize up to here 4. Never mind`. Restoring there forks from just after the compaction summary: the model still answered UNKNOWN. |

What the transcript shows (A, B, C2 alike, and a real rewind in a hosted session on 2026-10-12):

- **The fork.** The first prompt after a rewind has a `parentUuid` that points back to where the rewind went: in A,
  line 61's parent is line 39, the end of the first turn, not line 59, the end of the third. Everything after the fork point on the
  old chain is an abandoned branch, still in the file.
- **When.** At that prompt's `UserPromptSubmit` the prompt is **not in the transcript yet** (the hook saw the old branch's last
  entries). It is there by the turn's next hook (`PreToolUse`, `PostToolUse`, `Stop`).
- A `last-prompt` entry with `explicit: true` and a zero `leafUuid` follows that prompt. It is undocumented; the parent link is the
  signal to rely on.
- **The rule** "the newest typed prompt (`promptSource` set, not meta) does not descend from the last user, assistant or system
  entry before it" flagged exactly one fork in each of A, B and C2, and exactly one, the known rewind, across 1,274 typed prompts in
  four real transcripts of a long hosted project (`compact_boundary` entries link through `logicalParentUuid`).
- After a compaction, the `SessionStart(compact)` hook's `additionalContext` (baton's brief) is an attachment after the summary.
  Rewinding to the `/compact` entry forks from just after the summary, so it drops that attachment: the model keeps the summary but
  loses the brief.
- Summarize from here / up to here run a compaction: `PreCompact(manual)`, `SessionStart(compact)`, `PostCompact` (scenario C,
  which picked option 2 by mistake).

Consequences for baton: [`docs/research/recovery.md`](../../docs/research/recovery.md).
