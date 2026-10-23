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
| `23-worker-start/` | How does a host start a hidden worker `claude` in its own worktree with no keystrokes (initial prompt, session id, trust), and how does it end and resume one? (2026-10-11, Claude Code 2.1.296; [write-up](../docs/research/workers.md)) |
| `24-relay/` | Which hook channel makes a model put a question baton gives it to the human, word for word? (2026-10-11; [write-up](../docs/research/workers.md)) |
| `25-moving-branch/` | Can finished work be fast-forwarded onto the branch a live session is editing, and what does the session make of it? (2026-10-11; [write-up](../docs/research/workers.md)) |
| `26-bg-sessions/` | Could Claude Code's background sessions (`claude --bg`, `claude attach`) be baton's workers? (2026-10-11; [write-up](../docs/research/workers.md)) |
| `27-machine/` | What can a static Go binary read about the machine's size and load on macOS and Linux, inside a container too? How does macOS's free-memory level move while a process holds memory? (2026-10-11; [findings](27-machine/README.md)) |
| `28-leftovers/` | Which processes does a hosted session leave running when its host is killed or its terminal closes, and can a static Go binary find them again by their baton marker and working directory? (2026-10-11, Claude Code 2.1.296; [findings](28-leftovers/README.md)) |
| `29-rewind/` | What does a rewind (Esc Esc, `/rewind`) leave behind that baton could read deterministically, and how far back can it go? (2026-10-12, Claude Code 2.1.296; [findings](29-rewind/README.md)) |
| `30-crash-resume/` | When the lead's `claude` dies, can baton bring the same conversation back with `--resume`, and how does a resume that cannot work fail? (2026-10-12, Claude Code 2.1.296; [findings](30-crash-resume/README.md)) |
| `31-mod-agents/` | Could a phase run as a subagent of the lead session, shown in Claude Code's agent panel, with a mod of baton's driving it and no model judgment? (2026-10-12, Claude Code 2.1.296; [write-up](../docs/research/visible-workers.md)) |
| `32-phase-agents/` | What baton's design for phase agents assumes: messages to a busy or compacting agent, what `/compact`, `/clear`, Esc, Ctrl-C and a rewind do to agents, what a mod reload keeps. (2026-10-12, Claude Code 2.1.296; [write-up](../docs/research/visible-workers.md)) |
| `33-board-command/` | Can baton's mod serve `/baton board`, opening its pane at once, when `/baton` is the plugin's own skill? (2026-10-12, Claude Code 2.1.296; [findings](33-board-command/README.md)) |
| `34-ask-deadline/` | Can baton's mod settle a question of baton's at its deadline without typing a key? (2026-10-12, Claude Code 2.1.296; [findings](34-ask-deadline/README.md)) |
| `35-mod-drives/` | What can the mod do in place of baton's keys (compact the lead, submit baton's lines), and what do those keys, Ctrl-C above all, do to agents? (2026-10-15, Claude Code 2.1.300 and 2.1.296; [findings](35-mod-drives/README.md)) |
| `36-agent-dialog/` | Under a phase agent's permission prompt on the lead's screen, does the mod's compaction and a lead turn still run, and how does an agent the human stopped look? (2026-10-15, Claude Code 2.1.300; [findings](36-agent-dialog/README.md)) |
| `37-compact-at-turn-end/` | Does Claude Code take a compaction the mod calls from the lead's `turn.complete`, where one called as the turn ends is refused? (2026-10-16, Claude Code 2.1.301; [findings](37-compact-at-turn-end/README.md)) |
| `38-plan-file-reference/` | Does renaming the session's plan-mode file stop Claude Code from attaching the plan in full after every compaction (`plan_file_reference`), and does a later planning or anything else in the session break? Hosted, does a plan baton moved stay out of the next phase's context? (2026-10-16, Claude Code 2.1.301; [findings](38-plan-file-reference/README.md)) |
| `39-escape-answers/` | What does Esc do to a question of baton's and to a permission prompt, and what does each digit mean in today's permission prompt? Can an Esc from the host touch a draft, a turn or a background agent? (2026-10-16, Claude Code 2.1.301; [findings](39-escape-answers/README.md)) |
| `40-agent-handles/` | Can the human write to a phase agent with `@<phase> <words>` at the lead's prompt: does a `UserPromptSubmit` block keep the words from the lead's model, at what cost, and what stays in the input box? What do a message and the human's typing in an agent's view do to a running, a completed, a stopped and a retired agent? Who owns an agent's name? (2026-10-16, Claude Code 2.1.301; [findings](40-agent-handles/README.md)) |
| `41-model-complete/` | Can baton's mod make a model call for the host (`$.model.complete`, from its poll, as a bridge command): does it answer while the lead's turn runs and while it is idle, how fast, does anything reach the transcript or start a turn, what do an API error, a time limit and a refused request give, do `haiku` and `opus` pass the allowlist, and how does the test kit stub it? (2026-10-17, Claude Code 2.1.302; [findings](41-model-complete/README.md)) |
| `42-usage-windows/` | What does Claude Code give baton about the account's usage windows: does the status line carry the weekly window beside the five-hour one, with which fields, and how fresh is it, with the lead idle and with only an agent at work? Does a usage stop say which window it hit and when it resets: in `StopFailure`, in the transcript? (2026-10-23, Claude Code 2.1.309; [findings](42-usage-windows/README.md)) |

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
- Each run costs a few cents. Haiku had no auto mode before Haiku 5.5, so spikes 01–18 ran in manual mode with narrow allow rules.
