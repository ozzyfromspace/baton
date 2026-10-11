# 21 — hidden PTY workers with an attachable viewer (Claude Code 2.1.296, haiku, macOS arm64, 2026-10-10)

([write-up](../../docs/research/parallel.md))

Question: can baton run worker `claude` sessions in PTYs nobody watches, and let a human attach a real
terminal to one on demand (`baton view P3`), byte for byte, with no tmux?

Harness: `drv.py` (raw-recording PTY driver: timestamped chunks, pausable reads, TIOCSWINSZ + SIGWINCH),
`vt.py` (minimal VT emulator used to render "only the bytes after the trigger" onto a blank screen, i.e.
what a freshly attached terminal would show). Scripts (run from this directory; each creates its own `proj*/` scratch folder): q1.py (repaint triggers), q2.py/q2b.py (long
content, resizes mid-turn), q3.py (undrained PTY), q4.py (cost of 3 workers). Raw recordings (*.json) and
rendered screens (B-*.txt for the viewer, A-*.txt for a full replay) are written beside them.

## Q1 — what makes Claude Code repaint the whole screen?

| trigger | bytes in next 3s | first byte | fresh terminal shows the right screen? |
|---|---|---|---|
| (b) TIOCSWINSZ to the SAME size + SIGWINCH | 0 | — | no repaint |
| (e) bare SIGWINCH | 0 | — | no repaint |
| (a) size change 160x50 → 120x40 | 2286 | 6 ms | yes: header, prompt, answer, draft, input box, footer |
| (d) 150x45 then back to 160x50 (150 ms apart) | 5220 (two repaints) | 5 ms | yes |
| (c) Ctrl-L | 2693 | 0 ms | yes — byte-identical to the full-replay ground truth; the unsent draft is kept |

- A repaint is: `ESC[?2026h` (synchronized output), `ESC[H`, `ESC[2K` + cursor-down for every row, then
  the full frame. It never uses `ESC[2J`/`ESC[3J` and Claude Code never entered the alternate screen
  (`?1049h` never seen), so nothing clears the viewer's scrollback.
- It re-sends the input modes (bracketed paste `?2004h`, kitty keyboard `>5u`, modifyOtherKeys `>4;2m`),
  but not focus reporting (`?1004h`) or `?2031h`, which are sent only at startup.
- Claude Code ignores a SIGWINCH whose size did not change, so "same size" is not a trigger.
- With history longer than the screen (1..400, 1..1500), the repaint is the visible window only (~2.3 KB:
  the last ~40 lines + input box + footer). Older history is not re-sent.

Verdict: **viable. A real size change (or Ctrl-L) repaints the full visible screen in <10 ms; a fresh
terminal receiving only those bytes shows exactly the hidden worker's screen.**

## Q2 — size mismatch and resizing mid-turn

- 27 resizes (cycling 120x40/160x50/100x30/140x45 every 0.7 s) during a 6 s Bash call plus a 1,500-line
  streamed answer: turn time 18.9 s, identical to the no-resize control (18.9 s); output contiguous to
  1500; hooks in the same order; final frame clean.
- Policy: resize the worker PTY to the attaching viewer's size (that is also the repaint trigger). Restore
  the default size on detach (another repaint, harmless).

Verdict: **resize freely, including mid-turn; size the PTY to the active viewer.**

## Q3 — nobody drains the PTY

- Reads paused 2 s → 32 s after the prompt. The whole turn (3 Bash calls, ~900 streamed lines) ran to
  `Stop` at 12.1 s *during* the pause; hooks fired on time; `ps` showed `Ss+` at 0.4–8 % CPU (not blocked).
- On resume, ~28 KB of buffered output arrived at once (1 KB PTY reads within 40 ms) and the final screen
  was correct.
- Not tested: hours of no draining (claude buffers in-process, so memory would grow).

Verdict: **claude does not stall on an undrained PTY for 30 s, but workers should still be drained
continuously** (the host needs the bytes anyway for its quiet-screen gate, and in-process buffering is
unbounded).

## Q4 — cost of N workers

- 3 idle interactive workers: **~271 MB RSS each** (single `claude` process, no children), **~0.3 % CPU**
  idle (mean of 15 samples).
- Launch → trust prompt 0.8 s; trust answered → idle 0.6 s.
- Trust: a new folder needs the trust dialog (default highlighted option is "No, exit"), but a folder
  **under an already-trusted project** (`<proj>/.baton/worktrees/P3`) started with no trust prompt.

Verdict: **cheap: N=4 workers ≈ 1.1 GB RAM, ~0 idle CPU; place worktrees under the project root so they
inherit its trust.**

## Recommended attach protocol (`baton view <worker>`)

1. The host keeps every worker's PTY drained into a ring buffer (and its gates), whether or not anyone views it.
2. On attach: the viewer's terminal goes raw; the host sends it `ESC[?1004h`/other startup-only modes it
   recorded from the worker's first output, then `ESC[H ESC[2J`.
3. Repaint without a keystroke: set the worker PTY to the viewer's size; if that equals the current size,
   nudge (cols−1, then back, ~150 ms apart). Prefer this over Ctrl-L: it carries no input semantics, so it
   is safe while a dialog is open (Ctrl-L worked and kept the draft, but it is a key press, and baton must
   never type into a dialog).
4. Then forward bytes both ways; viewer keystrokes count as the human's (baton's existing human-typing,
   draft and dialog gates apply unchanged).
5. On detach: restore the default size (another repaint, harmless) and keep draining.
6. History above the visible window is not reproduced by a repaint; if wanted, show a transcript excerpt
   separately rather than replaying raw bytes across resizes.
