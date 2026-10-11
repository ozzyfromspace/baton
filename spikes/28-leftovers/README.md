# 28: what a session leaves running, and finding it again (Claude Code 2.1.296, macOS 26.3 arm64, 2026-10-11)

Question: when a hosted session ends, which of the processes its Bash tool started keep running? The endings
tried are the host SIGKILLed, the host and claude both SIGKILLed, and the terminal closing. Can a static Go
binary find those processes again afterwards, and tell which baton instance and project they came from? This
is for `baton clean`, which has to stop what a dead session left without touching anything else.

Harness:

- **`run.py MODE`** hosts `claude --model haiku --allowedTools Bash` with a dev build of baton
  (`../../dist/baton`, `make build`) in `work/proj`, with `BATON_HOME=work/home`. A stand-in worker worktree
  sits at `work/home/worktrees/k/r/P1`.
- **The model's calls.** The model runs `start.sh` in one Bash call. That starts three things: a background
  `sleep 3601 &`; a `python3 -m http.server` launched from the worktree, standing in for a dev server; and a
  daemon that calls `setsid` and `chdir /` before `sleep 3603`. The model then makes a `run_in_background`
  call, `sleep 3604`, and a foreground call, `sleep 3605`.
- **The ending.** While `sleep 3605` runs, the driver ends the host:
  - `host9`: `kill -9` of baton;
  - `both9`: `kill -9` of baton and its claude;
  - `hup`: SIGHUP to baton, as when a terminal window closes.
- **Reading the processes.** Five seconds later, it reads every process with `procs/` and compares the result
  with `ps -E -ww` and `lsof -d cwd`. Then it stops everything the spike started.
- **`procs/`** is Go with no cgo, in a module of its own; `procs show PID…` and `procs scan [BATON_DIR]` print
  what they can read.
  - On macOS it reads `kern.proc.pid`/`kern.proc.uid` through `unix.SysctlKinfoProc(Slice)` for the parent,
    the group, the owner and the command, and calls `getsid`. The environment comes from `kern.procargs2`.
    The working directory comes from `proc_info(PROC_PIDVNODEPATHINFO)`, through `unix.Syscall6`, as lsof
    does.
  - On Linux it reads `/proc/<pid>/{stat,status,environ,cwd}`.
- **Outputs.** `out/<mode>.txt` holds each run's summary; the JSON per process is in `out/<mode>.jsonl`.
  Each run is one Haiku session.

## Q1: what survives

Where each process was before the host was ended. The runs differed only in pids:

| process | parent, group, session |
|---|---|
| `sleep 3601 &`, the dev server | **pid 1** already; group and session of the Bash call's shell, which had exited |
| the daemon | pid 1; its own session |
| the `run_in_background` task | its shell (a session of its own) under claude |
| the foreground call | its shell (a session of its own) under claude |

What was left 5 seconds after the ending:

| | host SIGKILL | host and claude SIGKILL | host SIGHUP |
|---|---|---|---|
| claude | exited (its terminal closed) | killed | exited |
| `sleep 3601 &` | **survives** | **survives** | **survives** |
| dev server | **survives** | **survives** | **survives** |
| daemon | **survives** | **survives** | **survives** |
| `run_in_background` task | ended | **survives**, with its shell, under pid 1 | ended |
| foreground call | ended | **survives**, with its shell, under pid 1 | ended |

- **Background processes outlive every ending.** Whatever a Bash call starts with `&` is orphaned to pid 1 as
  soon as the call ends, before anything is killed. After that, nothing ends it: not Claude Code exiting
  cleanly, and not baton. So every session leaves its `&` processes behind, however it ends.
- **Claude Code ends its own running calls and background tasks** when its terminal closes. Only a SIGKILLed
  claude also leaves those behind, with their shells, as spike 23 found.

## Q2: reading what was left

| process | environment (`BATON_INSTANCE`, `BATON_DIR`) | working directory |
|---|---|---|
| python3 (Command Line Tools), a Go binary, node (claude) | read through `kern.procargs2`, the same as `ps -E` | read through `proc_info`, the same as lsof |
| `/bin/sleep`, `/bin/zsh`, `/bin/sh`: macOS's own platform binaries | **hidden**: no environment strings at all, and no error; `ps -E` shows none either | read |
| a zombie | `EINVAL` | `ESRCH` |
| Linux (a container, uid 1000): busybox `sleep`, before and after `setsid` and `chdir /` | read from `/proc/<pid>/environ` | read from `/proc/<pid>/cwd` |

- **A working directory that has been removed still reads.** macOS goes on reporting the old path, unchanged;
  Linux reports it with ` (deleted)` appended. So "under `$BATON_HOME`" still finds a dev server whose
  worktree was removed, provided the reader strips the suffix and compares strings. Resolving the path's
  symlinks fails once it is gone.
- A scan of all 302 of this user's processes read every one of them. The only error came from a zombie.
  macOS's own binaries return their arguments but an empty environment, and that emptiness is how a reader
  knows the environment is hidden: a real process always has some.
- **Every process a hosted session's tools start carries its marker.** That holds unless the program clears
  its environment. The marker is the session's `BATON_INSTANCE` and `BATON_DIR`.
  - A worker's processes carry `BATON_INSTANCE=<host instance>-<phase>`, `BATON_ROLE=worker` and
    `BATON_WORKTREE` as well. This was seen on a live worker: its claude and the node processes it started.
  - A Linux process that makes itself undumpable (ssh-agent, gpg-agent) hides `/proc/<pid>/environ` too.
    This is from proc(5); it was not tried.
- **A marker means nothing without the project.** A baton session hosted in another project was live on this
  machine, and its browser processes carried that session's instance. So the lookup has to match
  `BATON_DIR` to the project's `.baton`, and never go by `BATON_INSTANCE` alone.

## Verdict

- **The lookup.** On macOS, the sysctl and `proc_info` reader above; on Linux, `/proc`. Both are in Go, with
  no cgo, `ps` or `lsof`. On Windows there is no lookup, and the step says it can't look.
- **A candidate** is a process of this user that meets two tests:
  - `BATON_DIR` is this project's `.baton`;
  - `BATON_INSTANCE` names an instance no live run owns: a lead's instance, or `<instance>-<phase>` for a
    worker.
- **Stopping and listing:**
  - **stopped:** candidates whose working directory is under `$BATON_HOME`, which means a worker's worktree or
    a lane;
  - **listed, with the `kill` line:** every other candidate, such as the lead's dev server in the checkout;
  - **listed as unidentified:** processes with a hidden environment whose working directory is under the
    project's worktrees or lanes, or whose process group contains a candidate.
  - Nothing is stopped by its process group: each pid on its own, SIGTERM and then SIGKILL.
- **Retiring a worker can do the same for that worker alone.** It looks for its own instance and worktree, so
  its dev server does not outlive its worktree.
