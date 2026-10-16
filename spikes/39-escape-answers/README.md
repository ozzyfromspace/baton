# 39 — what Esc does to baton's question and to a permission prompt, and what a digit does (Claude Code 2.1.301, Haiku 5.5, macOS arm64, 2026-10-16)

Question: where baton's mod does not run (serial mode), the host answered its own question by typing `3`, where the
default sits, on the premise that `3` is "No" in a permission prompt should one be on screen instead (spike 16,
Claude Code 2.1.289). On Claude Code 2.1.30x it is not. And the question about a plan approved mid-run put "Start
the new plan from P0", which sets the run's progress aside, first, where Enter lands. What does Esc do to a question
of baton's (what does the call's result say, which hooks fire), and to a permission prompt? What does each digit
mean in today's permission prompt? What else could an Esc typed by the host touch: a draft, the lead's turn, a
background agent?

Harness: `run.py <scenario> <workroot>` runs an interactive claude in a PTY in a fresh git repo, with no mod (this is
serial mode), a settings hook on every event (`hook.py`, logging each hook's input to `hooks.jsonl`) and the raw
screen kept (`screen.raw`, rendered with `pyte`, so the spike runs under a venv that has it). The scratch project's
`.claude/settings.json` sets `plansDirectory` inside the scratch tree, and `BATON_HOME` points there too. `probe`
dumps the screen before and after a key, the hooks that fired after it, and what ran (`ran.txt`); each run's
`summary.json` also lists every tool call of the transcripts with its result. `digest.py <work>` prints a run's
hooks in time order with the driver's steps. The question is baton's about a plan approved mid-run, in v0.8.6's
order (Wait for me, Start the new plan from P0, Keep the current run), asked by the model as baton tells it to.

| scenario | what | result |
|---|---|---|
| Q (q1) | default mode: baton's question on screen; Esc | It shows `1. Wait for me`, `2. Start the new plan from P0`, `3. Keep the current run`, `4. Type something.`, `5. Chat about this`, and `Enter to select · ↑/↓ to navigate · Esc to cancel`. **Esc dismissed it:** the screen says `User declined to answer questions`; the call's result (`is_error`) is "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed.", then `[Request interrupted by user for tool use]`. **The turn ended, and no hook fired**: no `PostToolUse`, `PostToolUseFailure`, `PermissionDenied` or `Stop`. At its next prompt the model quoted that result word for word. |
| Q (q2) | asked again, the selection on its first option; Enter | Answered `Wait for me` (`PostToolUse` with the answer), and the model went on with it. |
| T (t1) | the free-text option focused (↓ three times) and `partial answer` typed into it; Esc | **One Esc dismissed the whole question**, as in q1, words and all. |
| T (t2) | the digit 4 | Moved the selection to `4. Type something.` and answered nothing; Esc then dismissed the question. |
| I | after one Esc on the question, the session left alone; then a turn that ends normally, left alone | Claude Code's `idle_prompt` notification came 61 s after the dismissal, and 60 s after the normal turn. In T, where a second Esc was pressed at the idle prompt 8 s after the dismissal, none came in the 115 s that followed: a key seems to put it off. |
| P (p1) | default mode: the lead's Bash prompt; Esc | It reads `1. Yes`, `2. Yes, and don't ask again for: sh ask.sh`, **`3. Yes, and switch to auto mode · auto mode handles these prompts for you`**, `4. No`, with `Esc to cancel · Tab to amend`. **Esc rejected it:** `Interrupted · What should Claude do instead?`, the command did not run, the turn ended, no hook fired. |
| P (p2) | the lead's Write prompt; Esc | It reads `1. Yes`, `2. Yes, and switch to accept edits (…) for this session (shift+tab)`, `3. No`. Esc rejected it (`User rejected write to notes.txt`, nothing written), no hook. |
| P (p3) | the lead's Bash prompt; 3 | **The command ran and the session switched to auto mode**: the `PostToolUse` and every hook after it carry `permission_mode: "auto"`, and the footer reads `auto mode on`. |
| A | acceptEdits mode: the lead's Bash prompt; Esc, then another answered 3 | The same four options; Esc rejected the call (no hook); 3 ran the next one and switched to auto mode. |
| K | a terminal that speaks the kitty keyboard protocol (it answers claude's `CSI ? u` query with the flags claude pushed, `CSI > 5 u`, so claude takes keys to come encoded, Esc as `CSI 27 u`); a bare Esc byte on the question, then on a Bash prompt | **A bare Esc byte still works:** it dismissed the question and rejected the prompt, as in Q and P. |
| G (g1) | default mode: a background agent running a 60-second command (allowed), the lead idle, the box empty; Esc | Nothing changed on screen. **The agent ran on**: its command ended, it ran it again, and reported FINISHED. |
| G (g2) | a draft typed in the box, the agent still at work; Esc, and Esc again | **The draft stayed**, both times; the agent ran on. |
| G (g3) | the lead's turn running a 25-second command (allowed), the agent still at work; Esc | The lead's turn ended (its model later: "you interrupted that tool use"), with no `Stop`; **the agent ran on.** |
| G (g4) | a background agent's permission prompt on the lead's screen; Esc | **Rejected the agent's call alone:** its result is "Permission for this tool use was denied. The tool use was rejected (…). Try a different approach or report the limitation to complete your task." The agent went on, reported the denial and ended normally; nothing of the idle lead's changed. |

Each scenario ran once. The rendered screens show a stray `u` after some keys: `pyte` draws the kitty keyboard
sequences claude writes (`CSI < u`, `CSI > 5 u`, `CSI ? u`) as a letter. Nothing was typed into the box (the next
prompt sent was exactly what the driver typed).

## What it means for baton (v0.8.6)

- **A digit is no longer safe to type into a dialog.** In a Bash permission prompt, the lead's or an agent's, `3` now
  approves the call and switches the session to auto mode, and `2` approves it and adds an allow rule. Serial mode
  stops typing digits.
- **Esc is.** It picks no option in any dialog: it dismisses a question, rejects a permission prompt (an agent's
  too, and the agent goes on without the call), leaves a draft in the box alone, stops no background agent, and at
  most ends the lead's turn, which baton's next line starts again. So the host answers its own question by Esc, still
  only when the question is the only dialog open and was put exactly as issued, and once per question: one Esc. (Not
  tested here: the human in an agent's transcript view. Spike 36 found the first Esc there only moves the focus, and
  a second stops the agent.)
- **The dismissal is the default, and the host records it.** No hook reports a dismissed question, so the host
  records its default itself as it presses Esc (`hooks.Lapse`, as the mod's answer is recorded), and closes the lead's
  turn on record: no `Stop` hook comes, and the idle notification comes a minute later at best. The model's call
  came back telling it to stop and wait for the user, so baton's mail line (the answer, as baton's message) is what
  moves it on.
- **Enter picks the first option.** The question about a plan approved mid-run puts Wait for me first, Start the new
  plan from P0 second, and the default third: a stray Enter holds the approved plan for the human, which lapses into
  the default, and no single key sets a run's progress aside.
