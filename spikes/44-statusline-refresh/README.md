# 44 — the status line's refreshInterval under `--settings` (Claude Code 2.1.318, Haiku, macOS arm64, 2026-11-05)

Question: an idle lead's status line kept saying what baton's state said at its last turn. Does a `refreshInterval`
in the status line baton passes with `--settings` make it catch up while idle, does a run whose text has not changed
redraw the screen (serial mode's 1.5-second quiet gate), and how does it combine with a project's own
`statusLine.refreshInterval`?

Harness: `refresh.py <variant> <workroot>`, spike 15's on `common/scenario.py`: an interactive claude in a PTY in a
fresh directory, the status line passed with `--settings` as baton does, a command that appends a timestamp to a file
on each run. After the trust prompt and 3 seconds of quiet, 60 idle seconds: the command's runs, the screen's writes
and the gaps between them. The five variants ran side by side.

| Variant | `--settings` | Project's settings | Runs in 60 s | Screen writes | Gaps (s) |
|---|---|---|---|---|---|
| `none` | no `refreshInterval` (baton before v0.8.20) | none | **0** | 1 | 9.8 |
| `static` | `refreshInterval: 10`, text never changes | none | 6 | 1 | 9.87 |
| `ticking` | `refreshInterval: 10`, text changes each run | none | 6 | 8 | 9.77, 0.02, 0.29, 10.01, 9.99, 10.0, 10.0, 10.0 |
| `project30` | `refreshInterval: 10` | `refreshInterval: 30` | 6 | 1 | 9.88 |
| `project5` | `refreshInterval: 10` | `refreshInterval: 5` | 6 | 1 | 9.83 |

The one write about 9.8 seconds in, in every variant, is Claude Code's own and unrelated to the status line.

Findings:

- Without `refreshInterval` an idle session never runs its status line: 0 runs in 60 seconds.
- With 10, it runs every 10 seconds. Text that has not changed is not drawn again (`static`: no write beyond the one
  every variant has), so baton's segment, which changes only when the run's state does, leaves the screen still.
  Text that changes is drawn once per run, 10 seconds apart: far above the 1.5-second quiet gate.
- `--settings` wins over the project's `refreshInterval`, slower (30) and faster (5) alike: Claude Code merges
  `statusLine` key by key, the flag's settings over every file's. (Spike 15, on 2.1.289, saw the project's value
  merge into a flag status line that set none.) So baton passes on the user's own interval as it is, the first set in
  local, project, then user settings (`statusline.UserRefresh`), and 10 only when they set none.
