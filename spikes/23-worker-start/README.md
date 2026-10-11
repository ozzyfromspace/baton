# 23: starting a hidden worker (Claude Code 2.1.296, haiku, macOS arm64, 2026-10-11)

Question: how should the v0.5 host start a worker `claude` in its own git worktree and PTY? This covers the initial prompt, session id,
trust, ending, resume and concurrency.

Harness: `drv.py` is the spike-21 driver. It starts every claude as `env -i HOME USER LOGNAME PATH TMPDIR LANG SHELL TERM [PWD] claude
--model haiku --settings <hooks> …` and has the end methods. `hook.sh` logs each event's stdin to `logs/<run>.log`; with
`CODEWORD`/`HOOK_SLEEP` set, it answers SessionStart(startup) with `additionalContext`, optionally after a delay. `setup.sh` builds `proj`
(a git repo) and worktrees P1–P6. The `q*.py` scripts each answer one question. `summarize.py` prints the timings. Screens, raw
recordings and pids are in `out/`. 22 launches.

## Q1: initial prompt as an argument

| run | SessionStart | UserPromptSubmit | first Stop | keys typed |
|---|---|---|---|---|
| q1 (worktree P1) | 0.78 s | 2.03 s | 3.75 s | 0 |
| 9 other fresh starts | 0.66–0.90 | 1.84–1.95 | 3.2–7.7 (with tools) | 0 |
| q1b: hook sleeps 4 s | 0.67 (returns 4.68) | **4.71** | 6.28 | 0 |
| q0: untrusted folder | 2.75 (after "Yes") | 3.78 | 5.22 | trust answer only |

- The prompt is submitted with no keystroke. `additionalContext` reached the first turn every time: the model answered `PELICAN-7731`,
  `OSPREY-5150` and `HERON-3344`.
- **The prompt waits for SessionStart hooks to finish**: 30 ms after a 4 s hook returned. Behind a trust dialog nothing runs, not even
  SessionStart, until "Yes"; then the prompt goes through.
- **Trap:** `--allowedTools` is variadic. In `--allowedTools Bash "<prompt>"` the prompt was swallowed as a tool name. The session then
  sat with an empty input box for 90 s, with no error and no UserPromptSubmit (`out/screen-q3c-P3-swallowed.txt`). Putting `--` before
  the prompt fixes it.

Verdict: **pass the prompt last, after `--`; the first Stop comes ~3–4 s after launch for a trivial turn. SessionStart context is
reliable.**

## Q2: `--session-id <uuid>`

- Every payload carried exactly that id. The transcript is `~/.claude/projects/<escaped cwd>/<uuid>.jsonl`. `--resume <id>` keeps the
  id (`source: "resume"`).
- Reusing an id that has a transcript fails: `Error: Session ID … is already in use.`, exit 1, no hooks.

Verdict: **the host picks the id and knows the transcript path before launch; a restart uses `--resume`.**

## Q3: trust and layout (`proj` trusted once)

| case | trust dialog | recorded cwd / transcript dir | git in session |
|---|---|---|---|
| (a) `proj/.baton/worktrees/P1` | no | that path | phase/P1 ✓ |
| (b) `outside/P2`, a worktree outside proj | **no** | that path | phase/P2 ✓ |
| control: `outside/clone`, a clone | **yes**; "No, exit" → exit 1, no hooks | – | – |
| (c1) cwd = symlink `proj/.baton/w/P3` → `outside/P3` | no | **real path** (also Bash `pwd`) | phase/P3 ✓ |
| (c2) same with `PWD=<symlink>` (shell `cd`, Go `exec.Cmd.Dir`) | no | payload `cwd`, transcript, `$CLAUDE_PROJECT_DIR`: real; **hook `$PWD` and Bash `pwd`: symlink** | toplevel = real path ✓ |
| (d) `claude -w spikeP1` from proj | no | `proj/.claude/worktrees/spikeP1`, branch `worktree-spikeP1` from HEAD, git-`locked` | ✓ |

- **Trust follows the repository**: any worktree of a trusted repo skips the dialog wherever it lives; a clone does not. No
  `~/.claude.json` entry is written per worktree.
- When `-w` exits with no changes, it deletes the worktree and its branch, moves the transcript to `<esc>-proj`, and reports SessionEnd
  `cwd` = proj. It leaves an untracked `.claude/` in the checkout.

Verdict: **baton creates worktrees itself, anywhere, with no trust step. Start claude with the real path as cwd and no PWD override.
Avoid symlinks (split view) and `-w` (removes the worktree and moves the transcript).**

## Q4: ending a worker

| method | when | SessionEnd reason | exit | time | left behind |
|---|---|---|---|---|---|
| `/exit` + Enter | idle ×13 | `prompt_input_exit` | 0 | 0.7–1.4 s | nothing |
| `/exit` + Enter | Bash tool running | `prompt_input_exit` (not queued) | 0 | 1.45 s | nothing |
| SIGTERM | idle, mid-tool | `other` | 143 | 0.8 s | nothing |
| close PTY master (SIGHUP) | idle, mid-tool | `other` | 0 | 1.0 s | nothing |
| SIGKILL | mid-tool | **none** | sig 9 | 0.05 s | **the tool's zsh + child, ppid 1** |

- The Bash tool's shell runs in **its own session** (`Ss`, pgid = its own pid), outside claude's process group. Claude kills it on /exit,
  SIGTERM and SIGHUP, but not after SIGKILL. `caffeinate -i -t 300` (a child in claude's group) always went away with claude.
- The transcript survives SIGTERM and SIGHUP. A command killed mid-tool is recorded as `Exit code 137`.

Verdict: **end with `/exit`, or SIGTERM as the fallback. If SIGKILL is ever used, also sweep descendants by ppid.**

## Q5: resuming

| `--resume <id>` from | remembered code word + number | session cwd |
|---|---|---|
| same worktree | ✓ | P1 |
| `proj` (same repo) | ✓ | **proj**: its tools would edit the main checkout |
| `elsewhere/` (not git) | ✓ after the trust dialog | elsewhere |
| same worktree, after a mid-tool SIGHUP | ✓ and "the sleep was killed (137)" | P1 |

The lookup by id works from any folder, and the id is unchanged. The transcript keeps growing in its original dir. From another cwd,
SessionStart's `transcript_path` names a file that does not exist; later hooks name the real one.

Verdict: **always resume from the worker's own worktree. Store the worktree with the id.**

## Q6: two workers and a session in proj at once

All three were launched back to back, submitted at 1.93–1.95 s and stopped at 6.2–7.7 s. Each kept its own files, code word and session
id. No warning, lock or corruption text appeared on any screen, and `~/.claude.json` still parses.

Verdict: **no interference at N=3.**

## Also seen

- The input box shows ghost text when idle: a prompt suggestion ("commit this") or a placeholder (`Try "edit <filepath> to..."`). A
  screen-based draft gate must not mistake it for typing.
- On a fresh start the submit comes ~1.1 s after SessionStart; on resume, ~0.1 s.
- This version also ships `claude --bg` / `attach` / `agents` (background sessions). Not tested.

## Recommended worker start protocol

1. The project is trusted once (the main session runs there). Run `git worktree add -b <branch> <path>`, preferably under
   `<proj>/.baton/worktrees/` with `.baton/` excluded.
2. Generate a UUID. Persist `{phase, worktree, session_id, transcript path}` before launch.
3. Spawn a PTY with cwd = the worktree's real path and a clean env (no `CLAUDE_*`/`BATON_*` and no PWD override), running `claude
   --settings <hooks> --session-id <id> --permission-mode <mode> [--allowedTools …] -- "<phase prompt>"`.
4. Put turn-1 knowledge in SessionStart(startup) `additionalContext`.
5. The worker counts as started when UserPromptSubmit for this id arrives, within ~5 s. A "trust this folder" screen means a bad launch.
   Never answer it automatically: its default is "No, exit".
6. End with `/exit` + Enter, even mid-tool. Fall back to SIGTERM, and use SIGKILL plus a descendant sweep only as a last resort.
   Confirm SessionEnd for the id.
7. Restart with `claude --resume <id> -- "<prompt>"` from the same worktree.
