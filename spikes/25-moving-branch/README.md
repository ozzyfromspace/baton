# 25 — moving the run branch under a live session (Claude Code 2.1.296, haiku, git 2.50.1, macOS, 2026-10-11)

Question: can the merge queue fast-forward `run` while the lead edits the main checkout on `run`? How?

Harness: `q1.sh`, `q1d.sh` (pure git); `q3.sh` (concurrent loops); `live.py <scn>` (Haiku in a PTY via `scenario.py`,
`--settings` hooks → `hooklog.py`, `shim/git` logs Claude Code's git; per scenario `results/<scn>/`: `host.log`,
`tools.txt` with Edit `tool_response`, `transcript.jsonl`; `tx.py` prints what the model saw). Worktree at
`.baton/worktrees/w`; landing = commit there, `git rebase run` there, `git merge --ff-only w` in main. 11 launches.

## Q1 — pure git: `git merge --ff-only w` in main (`q1.out`, `q1d.out`)

- (a) worker touches A,B; main has unstaged C, untracked U → exit 0, A,B updated, C and U kept. (c2) staged C kept too.
- (b) worker touches dirty C, (c1) staged A, (c3) dirty A already equal to the worker's → exit 1 `Your local changes
  to the following files would be overwritten by merge … Aborting`. Nothing half-applied, `run` unmoved.
- (e1) worker adds N, main has untracked N → exit 1. **(e2) N ignored → exit 0, silently overwritten.**
- (f) `run` moved since the rebase → exit 128 `Not possible to fast-forward`. index.lock held → exit 1, nothing applied.
- **(h) `--autostash` → exit 0 but `UU C`, conflict markers in the lead's file.**
- (g) from the worktree, `push . w:run`, `fetch . w:run`, `branch -f` are refused; **`update-ref` succeeds and leaves
  main's index on the old tree (`M  A`, the reverse of the worker's change), so a lead `commit -a` reverts it.**
- (d) `read-tree -m -u -n HEAD w` takes index.lock, false-alarms on stale stat info, passes (f). Use the path probe
  below; the ff itself is the real check.

Verdict: **ff-only in the main checkout is all-or-nothing and never touches unrelated local changes. No autostash; never
move the ref without the tree.**

## Q2 — a live session underneath

- **2a** (read last turn, idle landing on other lines, "Edit without re-reading") and **2b** (landing during `sleep
  20`): Edit applied to the new content. `tool_response`: `"originalFile": "…port = 9090…"`, `"staleRecovered":
  true, "contentNotInModelContext": true`; result: *"(note: the file had been modified on disk since you last read it
  — the edit applied cleanly, but the file contains other changes not in your context. Read it before edits that
  depend on surrounding content.)"* No notice before the Edit.
- **2c** (landing rewrites the line being edited) and **2d** (Write the whole file from memory): refused, *"File has
  been modified since read, either by the user or by a linter. Read it again before attempting to write it."* Nothing
  clobbered.
- **2f** (as 2c, free to re-read): re-read, then **stopped and asked** "Do you want me to change `green` to `blue`, or
  leave it as it is?" **2g** (plus a PostToolUse announcement): re-read before editing, then asked too.
- **2e/2h** (the session had *edited* the file): an `edited_text_file` attachment with the new snippet arrived at the
  next prompt (2e) or right after the Bash result (2h); "The notice reports two changes on disk that I didn't make".
- Change notices come only for files the session edited, not ones it only read. Refused Edit/Write fail in
  validation: **no PreToolUse or PostToolUseFailure**. Claude Code's own git: startup only, `--no-optional-locks`.

Verdict: **nothing is clobbered, but an overlapping change turns into a question that stalls an unattended lead:
parallel phases need disjoint files, and landings must be announced.**

## Q3 — locks (`q3*.out`)

- Lead `git commit` loop ∥ worker commit + `git rebase run` loop: 850 each, **0 failures, no `cannot lock ref`**.
- Plus a host ff loop, lead committing every 0.1 s: 313 landings, 3 clean failures (1 `index.lock: File exists`,
  2 `Not possible to fast-forward`); lead and worker 0; `fsck` clean.
- A plain `git status` loop in main failed 24–50 of 50 lead commits (index.lock); with `--no-optional-locks`, 0.

Verdict: **worktrees don't contend (index per worktree; rebase only reads `run`). The only contention is main's
index.lock between host and the lead's own git: retry. Baton's git in main must use `--no-optional-locks`.**

## Q4 — afterwards

- `git status`/`git diff` show only the lead's own changes. Not confusing.
- The confusion is in history. **4a**: asked to amend "the commit you just made", the lead ran `git commit --amend` blind and rewrote
  the worker's P3 commit; `w` is no longer an ancestor of `run`. **4b**: with an announcement at UserPromptSubmit, it
  checked `git log` and refused: "HEAD is no longer my commit, so `git commit --amend` would have rewritten someone
  else's work." The system prompt's git snapshot stays stale.

## Recommended landing protocol

1. **When**: no lead tool in flight (PreToolUse without PostToolUse/Failure). Host holds `.baton/runs/<id>/land.lock`;
   PreToolUse hooks wait on it (bounded, fail open). A landing took 0.13–0.17 s.
2. **Rebase** in the idle worker's worktree: `git -C <wt> rebase run`; on conflict `rebase --abort`, back to the worker.
3. **Probe**, lock-free: `git merge-base --is-ancestor run w` (else step 2). Overlap of
   `git diff --no-renames --name-only run w` with `git --no-optional-locks diff --name-only HEAD` plus
   `git ls-files -o` (ignored too, for e2) → defer until the lead commits those files.
4. **Land**: `git -C <main> merge --ff-only w`. index.lock → retry ×5, 100–500 ms backoff; not fast-forward → step 2;
   would be overwritten → defer.
5. **Verify** `HEAD` = `w` and the lead's dirty set unchanged; later, check landed commits are still ancestors of
   `run` (amend/reset) → escalate.
6. **Announce**: `systemMessage`, `events.jsonl`, and `additionalContext` on the next PostToolUse (mid-turn) or
   UserPromptSubmit (idle):
   > baton: phase P3's work just landed on run underneath you: commit c2c2ad8 "P3: …" (not yours) changed
   > config.txt. Your uncommitted changes were not touched, and `git diff` still shows only yours. HEAD is now
   > c2c2ad8; your own last commit 0022f97 is no longer HEAD. Re-read those files before editing them. Never amend,
   > reset, rebase or revert commits you did not make; if a task needs that, stop and say so.
7. **Never**: `update-ref`/`branch -f`/push into `run`, `--autostash`, plain `git status`.

Not tested: writes inside Edit's own read→write window (step 1 closes it), gc/pack-refs, Sonnet.
