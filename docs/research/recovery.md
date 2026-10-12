# Recovering a run: refusals, rewinds and crashes

**In short:** a refused turn is told apart only by the transcript (Claude Code reports `invalid_request`), stays
refused, and a fresh start from the brief can be refused too, so baton never retries one: the human rewinds or clears.
A rewind fires no hook, but shows in the transcript's parent links by the turn's next hook, and cannot cross a
compaction. A killed claude resumes with `--resume`, unless its transcript is gone. These led to v0.7's recovery.

Measured 2026-10-11 and 2026-10-12 against **Claude Code 2.1.296** (macOS, arm64), with Haiku, and from a real hosted
run: a 21-phase plan hardening a web project's own code, with workers on, where the lead's turn was refused by the
model's safeguards. Two spikes: [`spikes/29-rewind`](../../spikes/29-rewind) and
[`30-crash-resume`](../../spikes/30-crash-resume). Each folder's README has the tables and the harness.

The questions:

- When the lead's turn ends in a way baton cannot retry away (a safety refusal, a crash, a conversation that cannot go
  on), what can baton do by itself, and what must stay the human's?
- The human can rewind a conversation (Esc Esc) without baton knowing. What does that leave, and how does baton keep
  its record and the model's memory in step?

## A refusal, in a real run

The lead's turn ended at a phase that fixed a security hole in the project's own code. Claude Code reported it to
baton as `StopFailure` with `error: invalid_request`, and baton escalated it as an API error it could not retry.

- **The transcript says what it was.** Four refusals in that project's transcripts (one in September, three on
  2026-10-11) each wrote two entries within a millisecond, before the `StopFailure` hook ran: a system entry
  `subtype: model_refusal_no_fallback` with `apiRefusalCategory: "cyber"` and `apiRefusalExplanation` ("This request
  triggered restrictions on violative cyber content…"), and an assistant entry with `isApiErrorMessage: true` and
  `stop_reason: "refusal"`.
- **Claude Code goes on once by itself.** When the safeguards stop a response, its assistant entry carries
  `stop_reason: "refusal"` (with `message.stop_details`: `category`, `explanation`), and Claude Code writes a system
  entry `subtype: informational` ("…safeguards stopped the response above · continuing once with that noted") and a
  meta user entry, and the model goes on. Only when it will not go on does it write the two entries above. So a
  `stop_reason: "refusal"` alone is not the end of a turn: one later stop in the same project went on after that
  single retry. baton reads only the two final entries, and adds no retry of its own.
- **The conversation stays refused.** In one conversation the human's next turn, 12 minutes later, was refused too.
  Compacting would not help: `/compact` sends the refused conversation to the same model to summarize.
- **A fresh start from the brief was refused too.** At the next phase boundary, baton compacted and briefed the phase
  afresh. The refusal came four minutes in, right after the model wrote a note describing the hole. The note was then
  on record in baton's state, and every fresh conversation is given the run's decisions, so any rebuild would carry
  it straight back in. baton cannot know which text tripped the classifier, so "rebuild from clean state" is not a
  thing it can promise.
- **What Claude Code and the API say.** Claude Code can switch models when a request is flagged (`switchModelsOnFlag`;
  Opus 5.5's cyber flags go to Opus 4.8). The user has it off on purpose and will not switch models, so baton never
  does either. The API's guidance: "Re-sending a refused request to the same model usually earns another refusal"
  ([refusals and fallback](https://platform.claude.com/docs/en/build-with-claude/refusals-and-fallback)). Claude Code's
  guidance for a flagged cybersecurity turn ([errors](https://code.claude.com/docs/en/errors.md)): apply for the
  [Cyber Verification Program](https://support.claude.com/en/articles/14604842-cyber-verification-program) if the
  work needs it, or "press Esc twice or run `/rewind` to step back to a checkpoint before the turn that triggered the
  flag, then take a different approach."
- **The rewind worked.** The human rewound to just before the refused reply, asked for the fix itself, and the phase
  went on, on the same model.
- While the lead stood refused, two things in baton went wrong: each worker landing cleared the lead's escalation
  and the watchdog raised it again (a fresh push each time), and a worker's question could not reach the human,
  because the watchdog stopped at the error before it woke the lead to ask. Both are fixed (`651b83b`, `0d1e7d4`), and the
  escalation now says what only the human decides and what goes on meanwhile (`a34f9d7`).

## Rewinds (spike 29)

- **No hook fires during a rewind.** The session id and the transcript file stay the same. baton sees only the next
  prompt, as an ordinary `UserPromptSubmit`.
- **The transcript shows the fork.** The first prompt after a rewind has a `parentUuid` pointing back to where the
  rewind went, not to the entry before it; the old branch stays in the file. Across 1,274 typed prompts in four real
  transcripts, a prompt's parent was the last entry with a uuid written before it every time, except the one known
  rewind. Following the parents from that prompt never reaches the old branch's last entry.
- **It shows one hook late.** At the prompt's own `UserPromptSubmit`, the prompt is not in the transcript yet. It is
  there by the turn's next hook.
- **The turn rewound away may have no prompt entry.** In the real rewind, the human had typed that turn's prompt while
  the model was still working, and Claude Code wrote it as an attachment of type `queued_command`, not as a user entry
  with `promptSource`. So what marks a branch as rewound away is a turn of any kind in it: a prompt, a queued prompt,
  or a reply of the model's.
- **Parents are written first.** In every real transcript checked (over 20,000 entries), an entry's parent comes
  before it in the file. So the walk back from the new prompt and from the old leaf to the entry they share reads only
  as far back as that entry. Replayed prompt by prompt, as if marked at each `UserPromptSubmit`, over 1,400 real
  prompts flagged the one known rewind and nothing else, and spike 29's A, B and C2 one each.
- **Code restore leaves no marker.** "Restore code and conversation" deleted a file the rewound turn had written; the
  transcript only shows it in a later `file-history-snapshot`. Restores cover only files Claude's edit tools changed,
  never git: commits stay, so restored files show as changes against `HEAD`.
- **A rewind cannot cross a compaction.** After `/compact`, the menu lists only the `/compact` entry and later prompts.
  Restoring at the `/compact` entry forks from just after the summary: the model kept the summary, not the
  conversation before it. Every phase boundary is a compaction, so a rewind never takes the model back into an earlier
  phase. But it does drop what came after the summary, which is where baton's brief (the `SessionStart(compact)`
  context) sits.
- "Summarize from here" and "Summarize up to here" are compactions: `PreCompact(manual)`, `SessionStart(compact)`.

## Crashes (spike 30)

- **`--resume` brings a killed claude back,** killed in the middle of a tool call or of an answer. The turn in flight
  is lost: Claude Code answers the interrupted prompt with "No response requested." and the model is not told
  anything else.
- **A transcript whose last line was cut off still resumes.**
- **A conversation with no transcript cannot resume:** `No conversation found with session ID: …`, exit 1, within a
  few seconds and before any `SessionStart`.
- baton today exits with claude, and records "claude exited with code N mid-plan". Its workers end with it, and are
  resumed by the next host.

## Consequences for baton

1. **A refusal is told apart, and never retried by baton.** At `StopFailure`, baton reads the transcript's entries
   since the last prompt for the refusal (the system entry or the assistant entry above) and records its category.
   It escalates it as a refusal: which phase stopped, that baton will not retry it, the two ways on (rewind to before
   the flagged turn and take a different approach, as Claude Code advises; or `/clear`, for a fresh conversation
   from the phase's brief), what goes on meanwhile, and what waits. Nothing is typed into a refused conversation, and
   no fresh one is started by baton: the human decides, every time.
2. **`/clear` mid-phase is a way on.** A conversation cleared while a phase is in hand gets the phase's brief (plan
   section, standing rules, notes, decisions) at `SessionStart(clear)`, not just the primer.
3. **A crashed lead is resumed by the host.** When claude exits mid-plan in a way nobody chose (not a deliberate end,
   not Ctrl-C or SIGTERM, not baton's own restart), the host restarts on the same binary with `--resume`, as an
   upgrade restart does, and tells the model the turn in flight was lost and what to continue. At most twice in 30
   minutes (counted in the run's state, so the count survives the restart); then it stops and says so. If the
   conversation cannot be resumed (claude exits early, before any `SessionStart`), it starts a fresh session bound to
   the same run, from the phase's brief, once.
4. **A rewind is noticed, and the model is told what still stands.** At each prompt, baton notes the last entry in
   the transcript. At the turn's next hook, it checks that the prompt descends from it. If not, it reads the
   abandoned branch once: its time span, the `baton` commands run there, the files Claude's edit tools wrote there,
   and whether baton's brief was in it. Decisions and jobs are found in baton's record by that time span. It then
   tells the model, through hook feedback (`decision: block` on PostToolUse or Stop, the channel spike 24 found
   followed every time), that the human rewound, what still stands (decisions, jobs and their results, a phase done,
   a block), and that the edited files are as they are on disk. If the rewind dropped the brief, it gives the brief
   again. While a compaction is owed, nothing is told: the compaction's brief gives the phase afresh. baton's own
   record is never rolled back by a rewind: undoing a decision stays the human's, through its undo.
5. **Tests use recorded transcripts for refusals.** No test provokes a refusal.
