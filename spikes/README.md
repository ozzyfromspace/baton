# Spikes

Throwaway experiments that decided baton's architecture. They were run on 2026-10-04 against Claude Code 2.1.289 on macOS, mostly with `--model haiku` to keep costs down. The findings are written up in [`docs/research/spikes.md`](../docs/research/spikes.md). The scripts are kept as the evidence behind those findings and as a starting point if a Claude Code update ever forces a re-test.

They are not part of baton and are not maintained. They are Python-stdlib-only and Unix-only.

| Dir | Question |
|---|---|
| `01-window-hot-reload/` | Does lowering `autoCompactWindow` mid-session force a compaction? (Also the launch-time controls.) |
| `02-async-rewake/` | Does an `asyncRewake` Stop hook wake an idle interactive session, and does the new turn pick up the lowered window? |
| `04-cron-compact/` | Does a scheduled task (CronCreate) with prompt `/compact` run the command? |
| `05-headless-compact/` | Does `/compact` sent over `--input-format stream-json` run as a command? |
| `06-typed-compact-rewake/` | Typed `/compact` + post-compact brief + `PostCompact` asyncRewake: a full unattended phase handoff? |
| `07-passthrough-wrapper/` | Prototype of baton's host: a transparent PTY wrapper that types `/compact` when a hook asks. |
| `08-elevation/` | Can a plain `claude` session hand itself over to the wrapper (exit, relaunch, same conversation) with no keystrokes? |
| `15-gates-and-signals/` | Does a status line refreshing every second keep the screen from settling? When does `idle_prompt` fire? ([write-up](../docs/research/reliability.md)) |
| `14-context-window/` | What does the status line input report about the context under `--autocompact`? ([write-up](../docs/research/context-window.md)) |
| `16-escalation/` | Can baton answer its own question safely (which dialog is on screen, what each key does)? Does a timed ask with a default keep an incident-like run moving, and can a late answer still undo it? Can uncommitted work be saved without the model? ([write-up](../docs/research/escalation.md)) |
| `17-sessions/` | Which hooks fire when a plan is approved or sent back, and what they carry? What happens to the session id and the plan file on `/clear`, a "clear context" approval, `--resume`, and two sessions in one directory? (2026-10-07, Claude Code 2.1.292; [write-up](../docs/research/sessions.md)) |
| `18-skill-inject/` | Can a plugin skill load its instructions from a command when it is invoked, which binary does it find, and is `$ARGUMENTS` safe there? (2026-10-07, Claude Code 2.1.293; [findings](18-skill-inject/README.md)) |
| `19-background-work/` | Does a background Bash job survive `/compact`, and wake the model when it ends? Does it survive the session being ended and resumed? Do a Monitor and a one-shot cron survive `/compact`? (2026-10-10, Claude Code 2.1.296; [findings](19-background-work/README.md)) |
| `20-tool-steering/` | Can a PreToolUse hook rewrite a Bash call or force it into the background? What does Claude Code do with a long foreground command? What do subagents, worktree isolation and the Workflow tool allow? (2026-10-10; [findings](20-tool-steering/README.md)) |
| `21-attach/` | Can a worker `claude` run in a PTY nobody watches, and be repainted in full when a human attaches a terminal? Does resizing or not reading its output hurt it? What does a worker cost? (2026-10-10; [findings](21-attach/README.md)) |
| `22-run-timelines/` | Where did a real run's time go, and how much faster would it have finished with phases run concurrently? (2026-10-10; [write-up](../docs/research/parallel.md)) |

(Spike 03 was a variant of 02 run in a clean environment; its scripts are the ones in `02-async-rewake/`.)

## Running them

`common/ptydrive.py` stands in for a human at a terminal. It runs a command in a pseudo-terminal, answers the folder-trust prompt, types any text written to `<dir>/inject.txt`, and stops once `<done-file>` reports `p2_done`:

```sh
cd spikes/07-passthrough-wrapper
python3 ../common/ptydrive.py "$PWD" 240 "$PWD/.sc/state.json" \
  python3 passthru.py --model haiku "$(cat phase-prompt.txt)"
cat .sc/wrapper.log .sc/hooks.log
```

Notes:

- `ptydrive.py` removes every `CLAUDE*` variable from the child's environment. Without that, a `claude` started from inside another Claude Code session inherits `CLAUDE_CODE_CHILD_SESSION` and doesn't save transcripts.
- Spike 01 needs a large prompt to get past the 100k minimum window. Generate it with `python3 make_prompt.py`, then run `claude -p --model haiku --output-format stream-json --verbose < prompt.txt`.
- Spike 08 runs an isolated zsh: `ZDOTDIR="$PWD/zdot" zsh -d -i`, then type `./start-raw.sh`.
- Each run costs a few cents. Haiku has no auto mode, so the spikes ran in manual mode with narrow allow rules.
