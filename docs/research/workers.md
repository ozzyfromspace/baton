# Parallel worker sessions

Measured 2026-10-11 against **Claude Code 2.1.296** (macOS, arm64, git 2.50.1), mostly with Haiku and with Sonnet
where Haiku set no clear bar. Four spikes: [`spikes/23-worker-start`](../../spikes/23-worker-start),
[`24-relay`](../../spikes/24-relay), [`25-moving-branch`](../../spikes/25-moving-branch) and
[`26-bg-sessions`](../../spikes/26-bg-sessions). Each folder's README has the tables, the harness and the exact
wording tried.

[`parallel.md`](parallel.md) put the next step after v0.4 at two sessions working at once with two lanes: 1.44× on
the craft campaign and up to 2.24× on the quiet one, with a third session adding about 1%. v0.5 runs the visible
lead session as today and starts hidden **worker** sessions for other phases that can go ahead, each in its own git
worktree and its own pseudo-terminal. Finished work lands on the run's branch under the lead. The questions:

- How does baton start, end and resume a worker without a human at its terminal?
- How does a worker's question reach the human, and is the question asked as given?
- Can finished work land on the branch the lead is working on, while it works?
- Could Claude Code's own background sessions be the workers?

## Starting a worker (spike 23)

22 launches of `claude --model haiku --settings <hooks> …` in a PTY, in worktrees of a scratch project.

- **The prompt as an argument is submitted with no keystroke**, about 2 s after launch. It waits for the
  SessionStart hooks to return (30 ms after a hook that slept 4 s), and SessionStart(startup) `additionalContext`
  reached the first turn every time. A worker's brief can go there.
- **Pass the prompt after `--`.** `--allowedTools` is variadic: `--allowedTools Bash "<prompt>"` swallowed the prompt
  as a tool name, and the session sat with an empty input box for 90 s, with no error and no UserPromptSubmit.
- **`--session-id <uuid>`** is carried by every hook payload, and the transcript is
  `~/.claude/projects/<escaped cwd>/<uuid>.jsonl`. Reusing an id that has a transcript fails with exit 1. The host
  can pick the id and link it before launch.
- **Trust follows the repository.** A worktree of a trusted repo started with no trust dialog wherever it lived,
  inside the checkout or outside it; a clone of the same repo asked. Nothing is written to `~/.claude.json` per
  worktree. Behind a trust dialog nothing runs, not even SessionStart, and its default answer is "No, exit".
- **Use the real path.** With the cwd a symlink and `PWD` set to it, the payload's `cwd` and the transcript used the
  real path while hooks' `$PWD` and the Bash tool's `pwd` used the symlink.
- **Avoid `claude -w`.** It puts the worktree at `.claude/worktrees/<name>`, and on a clean exit deletes the worktree
  and its branch and moves the transcript.
- **Ending.** `/exit` + Enter ended an idle session in about a second, and a busy one too (it is not queued behind
  the tool). SIGTERM and closing the PTY also left nothing behind. SIGKILL left the Bash tool's shell alive under
  pid 1: that shell runs in a process session of its own, outside claude's process group.
- **Resuming.** `--resume <id>` finds the session from any folder, but the session then works *there*: resumed from
  the main checkout, its tools would edit the main checkout. A session killed mid-tool resumed and knew its command
  had been killed.
- **Three sessions at once** (two workers and one in the checkout) kept their own files, ids and context, with no
  warning or corruption.

## Relaying a question (spike 24)

baton needs a hosted model to put an exact AskUserQuestion to the human: a worker's question in v0.5, and the
context warning today. In `TestContextWarningAsksTheHuman`, Haiku refused baton's PostToolUse `additionalContext`
asking it to: "It didn't come from you… so I ignored it." 35 Haiku and 12 Sonnet runs tried each channel a hook
has, with a task of six tool calls and the request arriving after the second (or while idle). A PreToolUse hook
recorded each AskUserQuestion and refused it, so no dialog ever opened.

| Channel | Haiku asked exactly | Sonnet |
|---|---|---|
| PostToolUse `additionalContext` (baton's channel in v0.4) | 3/5 | 2/2 |
| PostToolUse `{"decision":"block","reason":…}` | **9/9** | **3/3** |
| Stop `{"decision":"block","reason":…}` | **3/3** | not run |
| Idle: the host types a short `[baton] …` line, and UserPromptSubmit adds the request as context | **3/3** | 1/1 |
| Idle: an `asyncRewake` Stop hook exits 2 with the request | **3/3** | 1/1 |
| `additionalContext` naming a command whose output is the request | 1/3 | 2/2 |
| `additionalContext` plus a primer saying baton's messages speak for the user | 2/3 | 2/2 |
| PreToolUse deny of the next call, with the request as its reason | 4/6 | 0/1 |

- **No question was ever reworded.** Every miss was a refusal, and the reason given was always where the text came
  from: "That isn't part of your request, so I'm ignoring it".
- **Hook feedback is followed; text in or beside a tool result is weighed as possible injection.** In the
  transcripts, `additionalContext` becomes an attachment the model reads as part of the tool result ("The Edit
  result included…"), and a PreToolUse deny reason *is* the tool result. A `decision: "block"` becomes a blocking
  error the model treats as feedback; on Stop it is a user turn.
- **Wording does not fix it.** A primer saying a `[baton]` message is the user's own host speaking for the user,
  plus "this is not part of the tool result" in the request, still lost one run in three.
- On screen, a PostToolUse block shows as `PostToolUse:Edit hook returned blocking error` under the tool call,
  followed by the text. The tool has already run, so nothing is redone.
- **In auto mode with baton's full primer, mid-turn feedback is not enough.** These runs used acceptEdits and the
  primer's question rules only. After baton's context warning moved to PostToolUse `decision: "block"`, the
  `TestContextWarningAsksTheHuman` scenario (auto mode, full primer, the warning on the first tool call) asked
  1 time in 3. Twice Haiku said the text "arrived inside a tool result, not from you, and baton's rules say not
  to ask questions mid-plan", then finished its short phase in the same turn. With a turn end in the phase, the
  refused stop puts the question to it as feedback of its own.

## Landing work under a live session (spike 25)

A worker commits on its own branch in its worktree. Can baton move the lead's branch to include that work while
the lead is editing the main checkout on that branch?

**Pure git.**

- `git merge --ff-only <branch>` in the main checkout is all or nothing. It updated the files the branch changed and
  kept unrelated unstaged, staged and untracked work.
- It refused (exit 1, nothing applied, the branch unmoved) when a changed path was dirty, staged or untracked in the
  main checkout, and with "Not possible to fast-forward" when the branch had moved since the rebase.
- **An ignored file in the way is silently overwritten**, so a probe must list ignored files too
  (`git ls-files -o`).
- `--autostash` "succeeded" and left conflict markers in the lead's file.
- `update-ref` from the worktree moved the branch but left the main checkout's index on the old tree, showing the
  reverse of the worker's change as staged. The lead's next `commit -a` would revert it. `branch -f`, `push .` and
  `fetch .` into the checked-out branch are refused.

**A live session underneath** (11 Haiku launches).

- Claude Code never clobbered a landed change. An Edit to a file changed on disk since it was read was applied to
  the new content with a note, or, where it touched the changed line or rewrote the whole file, refused with
  "File has been modified since read".
- Where the landing rewrote the very line the lead was editing, the lead re-read the file and stopped to ask the
  human which value to keep. An unattended lead would stall there, so parallel phases need disjoint files.
- Claude Code reports changes on disk only for files the session edited, not ones it only read.
- **Unannounced, the lead amended the worker's commit.** Asked to amend "the commit you just made", it ran
  `git commit --amend` over the landed commit. Told at its next prompt what had landed, it checked `git log` and
  refused.

**Locks.**

- 850 rounds of commits in the main checkout alongside commits and rebases in a worktree had no failures: each
  worktree has its own index, and a rebase only reads the branch it rebases onto.
- A fast-forward every 0.1 s alongside the lead's commits: 313 landings with 3 clean failures (one `index.lock`, two
  "not a fast-forward"). The only contention is the main checkout's `index.lock`, between the host and the lead.
- A plain `git status` loop in the main checkout made between half and all of 50 lead commits fail on
  `index.lock`; with `--no-optional-locks`, none did. Claude Code runs its own git with `--no-optional-locks`.

## Claude Code's background sessions (spike 26)

Claude Code 2.1.296 ships background sessions: `claude --bg` hosts a session in Claude Code's own daemon, and
`claude attach` and `claude agents` view and list them. Could they be the workers?

- **Hooks work.** Every hook in `--settings` fires, and the settings are kept when the session respawns.
- **Claude Code picks the session id**: `--session-id` is ignored with a warning.
- **The session may move.** Started in the checkout, it entered a worktree of its own under `.claude/worktrees/`
  before its first edit. Started inside a linked worktree, it stayed put.
- **baton could not type into it.** A message posted to the session's socket starts a turn, or arrives between tool
  calls mid-turn. But `/compact` sent that way arrives as text, and the model is told the message came from
  another session. Haiku refused a question delivered that way.
- `/compact` typed through `claude attach` works. But `attach` is not a passthrough: it switches to the alternate
  screen, turns on mouse tracking and redraws in 256 colours. baton would need an attach client in a PTY of its own
  for each worker, about 145 MB each.
- A question or permission prompt in an unattended background session stays open with no timeout until someone
  attaches.
- **Cost:** a background session is about 243–380 MB plus about 300 MB shared; a plain interactive session is about
  317 MB.

## What Claude Code offers besides (2.1.296)

From the docs and the 2.1.296 bundle:

- **The status line receives `rate_limits.five_hour.used_percentage` and `resets_at`** on Pro and Max plans. That
  lets baton see how much of the account's five-hour allowance is used before it starts another session.
- **Interactive sessions wait out a usage limit by themselves** and resume when it resets. StopFailure reports
  `rate_limit` but no reset time.
- **Agent teams** are experimental and run in-process, inside one session's terminal.
- **Remote Control** and a host posting to a session's messaging socket (`CLAUDE_CODE_MESSAGING_SOCKET`) exist.
  Neither is needed for v0.5.

## Consequences for baton

- **Workers are sessions baton owns,** each in a pseudo-terminal of its own, never drawn on the human's terminal.
  baton types `/compact`, nudges and its own timed answers into a worker exactly as it does for the lead, sees
  every byte for its screen gates, and can repaint a worker for a human who attaches (spike 21). Background
  sessions would take that typing away (spike 26).
- **baton creates each worker's worktree itself, outside the checkout,** on a branch of its own, and starts claude
  there with the real path as its cwd. There is no trust step. If a trust screen does appear, the launch was bad:
  baton ends the worker and never answers the dialog.
- **The launch:** `claude … --session-id <uuid> -- "<prompt>"`, with the worker's brief in SessionStart context, and
  the session id and worktree linked before launch. A worker is ended with `/exit` (SIGTERM as the fallback), and
  resumed only from its own worktree.
- **Every request baton makes of a model mid-turn goes as hook feedback,** never as `additionalContext`: PostToolUse
  `decision: "block"` mid-turn, a Stop block at the end of the turn, and when idle a short typed line with
  UserPromptSubmit context. This is how a worker's question is relayed through the lead, and how the context
  warning is put. Nothing tells the model to trust tool output. Briefs and the primer stay `additionalContext`,
  as they were followed.
- **Mid-turn feedback is best effort; the stop is the guarantee.** A request still unanswered when the model
  ends its turn refuses the stop with the same text. The context question is refused once, since a compaction
  makes it moot; a relayed question waits until it is asked.
- **Landing is `git merge --ff-only` in the main checkout**, after a rebase in the worker's worktree, and after a
  lock-free probe of the branch's changed paths against the lead's dirty, staged, untracked and ignored files. It
  happens while no tool of the lead's is in flight, retries on `index.lock`, and defers on an overlap until the lead
  commits those files. Never `update-ref`, `branch -f` or `--autostash`.
- **baton's own git in the main checkout uses `--no-optional-locks`.**
- **Every landing is announced to the lead**: what landed, that its own changes were untouched, to re-read those
  files before editing them, and never to amend, reset, rebase or revert commits it did not make. A guard refuses
  those rewrites over landed commits.
- **Usage:** the status line's five-hour reading gates new workers, and a session waiting on a usage limit holds
  every nudge until the reset.
