# Working on baton

baton runs a multi-phase Claude Code plan unattended by hosting `claude` in a pseudo-terminal it owns. The design is in `docs/plan.md`, and how a run reaches the human (note, propose, blocked; snapshots; answering its own question) is in `docs/escalation.md`. The evidence behind them is in `docs/research/` (`spikes.md`, `reliability.md`, `escalation.md`, and `sessions.md` for plan approval and how runs map to sessions). Read them before changing how compaction, escalation, hooks, or the host loop work: several obvious-looking alternatives were tested and **do not work** (e.g. changing `autoCompactWindow` mid-session, scheduling `/compact` with CronCreate).

## Non-negotiables

- **The live terminal stays visible.** baton is a byte-for-byte passthrough; nothing may draw over or replace the Claude Code TUI.
- **No runtime dependencies for users.** One static Go binary (`CGO_ENABLED=0`), plus sh/PowerShell launchers. No tmux, no AppleScript, no Node/Python at runtime.
- **Control flow lives in host code, not in model judgment.** If a step only happens because the model remembered to do it, it is a bug waiting to happen; make a hook or the host do it.
- **Hooks fail open.** For Claude Code hooks, exit code 2 means *block*. A Go panic or a `flag` parse error also exits 2. Every `baton hook` path recovers panics, uses `flag.ContinueOnError`, and exits 0 on internal errors; only the deliberate rewake path exits 2. Each hook event has a test proving a panic exits 0.
- **Dormant unless hosted.** Anything that runs inside a session (hooks, CLI) does nothing unless `BATON_HOST=1` is set, apart from the explicit elevation path.

## Layout

- `cmd/baton/` — entry point and subcommand router.
- `internal/<pkg>/` — one package per concern (cli, config, host, pty, loop, hooks, decide, gitx, plan, state, brief, valve, statusline, notify, elevate, upgrade). `decide` owns the escalation policy and every model-facing sentence about it; `gitx.Usable` is the one test for whether git applies; `state.Project` maps each Claude Code session to its own run (`.baton/runs/<id>/`), and everything inside a session finds its run by session id.
- `plugin/` — the Claude Code plugin (skill, launchers, elevation Stop hook). `.claude-plugin/marketplace.json` at the repo root makes this repo its own marketplace.
- `spikes/` — archived throwaway experiments. Not maintained; don't import from them.

## Commands

```sh
make build      # ./dist/baton for this machine
make test       # unit tests
make cross      # all release targets into ./dist
make e2e        # real-claude scenarios (needs BATON_E2E=1, costs a few cents)
make release-prep VERSION=v0.1.0   # pin checksums + plugin version, then commit and tag
```

End-to-end tests run real `claude --model haiku`. Haiku has no auto mode, so tests approve dialogs with `AutoApprove`, or, where a proposal is on screen, use `acceptEdits` and narrow allow rules (AutoApprove's Enter would pick option 1). Every session gets its own `BATON_HOME` and `GNUPGHOME`, so neither your baton config nor your keyring is ever touched, and the signing tests use stand-in gpg scripts. Tests never notify, and they remove any plan file plan mode writes to `~/.claude/plans`. `BATON_E2E_SONNET=1` adds the Sonnet scenarios: auto mode at a phase boundary, and the replay of the signing outage behind graduated escalation ([`docs/escalation.md`](docs/escalation.md)).

## Conventions

- Go: standard library first; a new dependency needs a reason (current: `creack/pty`, `golang.org/x/term`, `golang.org/x/sys`, `gofrs/flock`).
- Commits: [Conventional Commits](https://www.conventionalcommits.org/) (`feat(host): …`, `fix(hooks): …`, `docs: …`), one coherent unit per commit, so `git log` reads as the story of the project.
- Tests: table-driven unit tests next to the code; time-dependent logic takes an injected clock.
- End-to-end tests assert on `.baton/runs/*/events.jsonl` and hook logs, never on screen contents.
- Every user-visible action baton takes is announced in the session (`systemMessage`) and logged to `events.jsonl`.
