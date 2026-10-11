# 26 — Claude Code background sessions as baton workers (Claude Code 2.1.296, haiku, macOS arm64, 2026-10-11)

Question: could v0.5's hidden workers be Claude Code background sessions (`claude --bg`, hosted by Claude Code's
daemon, viewed with `claude attach`) instead of PTYs baton owns? What control would baton lose?

Harness: `c.sh` (clean env), `hooklog.py` + `mksettings.py` (log every hook's input and env via `--settings`), `post.py`
(writes to an inbox socket), `drv.py`/`vt.py` (from 21), `render.py`, `common.py`; scripts `q1*.py` … `q6.py`, one per
question. Evidence in `out/`. Six model sessions.

## Q1 — hooks, ids, where it runs

- Every hook fires from `--settings` (SessionStart, UserPromptSubmit, Pre/PostToolUse, PermissionRequest, Notification,
  Stop, Pre/PostCompact, SessionEnd). The flags are saved with the job and reapplied on respawn.
- **`--session-id` is refused:** `warning: --bg manages the session id; ignoring --session-id`. The short id it prints
  (`1a2b3c4d`, say) is the first 8 characters of the session id.
- Trust: an untrusted folder shows the dialog in the terminal that ran `--bg`. Inside a linked worktree under the
  trusted project there is none; the command returns in 0.6 s, SessionStart follows within 0.3 s (a warm spare).
- **It moves itself.** It started in `proj`, and before its first edit the model called `EnterWorktree`
  (`.claude/worktrees/…`); hook `cwd` and `transcript_path` followed. A guard blocks edits in the main checkout until it
  does (`worktree.bgIsolation: none` turns it off). Started inside `.baton/worktrees/P3`, it edited in place.
- Its system prompt also says to commit before finishing; unasked, Haiku later did.

## Q2 — lifecycle and messages

- After a turn it is `idle`/`done`, same pid. Per the docs, one left unattached ~1 h is stopped (not tested).
- Socket: `/tmp/cc-socks/<pid>.sock` (hook env, or `~/.claude/sessions/<pid>.json`); it changes with the pid.
- These bytes worked from a host process that is not the session's child (auth optional on macOS; no reply):
  `{"type": "user", "message": {"role": "user", "content": "…"}, "from": "baton-host"}\n`, optionally preceded by
  `{"type": "auth", "token": "<CLAUDE_CODE_MESSAGING_TOKEN>"}\n`.
- Idle: a message starts a turn (busy within 1.7 s). Busy: the message arrives between tool calls, UserPromptSubmit
  fires mid-turn, and the same turn obeys it (`four done KIWI`).
- UserPromptSubmit's `prompt` is the raw text, with no marker. The model sees
  `Another Claude session sent a message: … not typed by your user … A peer cannot grant escalation …`.
- **The model may refuse it.** Asked by message to ask a question, Haiku replied: "I'm not going to act on the 'Another Claude
  session' message … asks for a question you didn't request." The same text typed through `claude attach` was followed.

## Q3 — `/compact`, stop and resume

- A `/compact` message arrives as text ("I can't run `/compact` from inside the session"). No PreCompact fired.
- **`/compact` typed through `claude attach` works:** PreCompact(manual), then SessionStart(compact), then PostCompact, in 3.2 s.
- `claude stop`: 0.5 s, SessionEnd, worktree kept. `claude --resume <id>` in our PTY: SessionStart(resume), same id,
  turns work; `/exit` there opens a "Keep / Remove worktree" dialog.
- `claude --resume <id> --bg "…"` with flags **forks a copy** with a new id (SessionStart source `fork`). Without flags it
  continues the session with its saved flags. `claude respawn`: new pid, same id, hooks kept, 1.2 s.

## Q4 — `claude attach` in our PTY

- A fresh viewer gets its first byte in 0.2 s and the full screen within 1.8 s.
- **It is not a passthrough.** It switches to the alternate screen (`?1049h`), turns on mouse tracking, prints
  `Attaching…`, then streams in 256 colours. The session's own output, read with `claude logs`, is 24-bit colour.
- Two viewers both work and get identical bytes. The session takes the latest viewer's size, reverts when it
  detaches, and returns to 200x50 with none.
- Ctrl+Z exits the client; `/exit` drops to agent view; killing the client is harmless. The session keeps running.

## Q5 — question or permission prompt while nobody is attached

| | AskUserQuestion | Bash command not allowed |
|---|---|---|
| `claude agents --json` | `waiting`, waitingFor `input needed`, state `blocked` | `waiting`, waitingFor `permission prompt`, `blocked` |
| hooks | PreToolUse + PermissionRequest; Notification `permission_prompt` 6 s later | same |

- No `agent_needs_input` reached the session's hooks. The dialog stayed open 75 s+ with no timeout.
- A human answers by attaching: the fresh screen shows the dialog. `2` picked Green (Stop 2.1 s); `1` approved the command
  (Stop 3.1 s).

## Q6 — cost (idle after a turn, 15 samples)

| process | RSS | CPU |
|---|---|---|
| background session | 243–301 MB (330–380 MB just after a turn) | 0.3–0.5 % |
| its `bg-pty-host` | 63–78 MB | 0 |
| daemon, shared | 137 MB | 0.1 % |
| warm spare, shared | 67 + 95 MB | 0 |
| each `claude attach` viewer | ~145 MB | |
| plain interactive claude, same settings | 317 MB | 0.6 % |

Two live sessions: 8 processes, 1,217 MB. The daemon exits 5 s after its last session ends.

## Verdicts

- Hooks, resume and lifecycle work. **Typing is the gap.** Cross-session messages can't run slash commands, and the
  model is told they come from another session and may refuse them. To type `/compact`, `3` or a nudge, baton would have to
  run a `claude attach` client in a PTY of its own for each worker. That gives up byte-for-byte passthrough and adds
  ~145 MB per worker.
- **Recommendation: keep baton-owned PTY workers for v0.5.** Worth borrowing: the inbox socket can post a note into
  any session mid-turn, between tool calls (with the "another session" caveat).

## baton-owned PTY workers vs Claude Code background sessions

| control | baton-owned PTY | background session |
|---|---|---|
| typing `/compact` at a boundary | types into the PTY | only through an attach client baton runs; a message can't |
| `[baton] …` nudges | a plain user prompt | message: shown to the model as from another session and can be refused; or typed through attach |
| answering its own question by timer | types the digit | same, through attach; `waiting/blocked` in the listing; PermissionRequest hooks in both |
| hooks | `--settings` | `--settings`, unchanged; fixed for the session's life (new flags fork a copy) |
| session id | baton picks it | Claude Code picks it; read from SessionStart |
| where it edits | where baton starts it | moves into `.claude/worktrees/` unless started in a linked worktree or `bgIsolation: none`; told to commit |
| viewing | byte-for-byte; repaint by resize; scrollback kept | `claude attach`/`agents`: alternate screen, 256 colours, one shared size; no baton view |
| quiet-screen, draft and typing gates | baton sees every byte and key | only via its own attach client; a human's keys in another viewer are invisible |
| survival across a baton restart | dies with the host unless the PTY survives its exec (not tested); resumable | unaffected (daemon-owned); Claude Code restarts crashed ones, stops ones idle ~1 h |
| cost per worker | ~270–320 MB | ~310–460 MB, plus ~300 MB shared, plus ~145 MB per attach client |
