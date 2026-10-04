# baton

**Run a long, multi-phase Claude Code plan unattended — compacting at every phase boundary, in one live terminal session you can watch.**

> Status: under construction (pre-v0.1). The design is settled and backed by experiments; see [`docs/plan.md`](docs/plan.md) and [`docs/research/spikes.md`](docs/research/spikes.md).

## The problem

Long plans run best when context is compacted at each phase boundary: fewer tokens, sharper output, and every phase starts from a clean set of facts. But in Claude Code only a human can type `/compact`, so someone has to sit by the terminal for hours. Asking the model to "compact yourself at the end of each phase" fails in practice: it forgets, it misreads its token budget as its context window, it says "compacting" without doing it, and after a compaction it can sit idle until someone notices.

## How baton works

- `baton` starts `claude` inside a pseudo-terminal it owns and passes every byte straight through — you see and use the normal Claude Code TUI.
- The plan's phases live in a small state file. The model reports progress with `baton done | blocked | waiting`.
- When a phase is done, baton's hooks (not the model) decide to compact; baton types `/compact` itself, confirms it ran, injects a brief for the next phase, and wakes the model up.
- If anything stalls, baton escalates: a question in the session (which reaches all your devices) plus a push notification it sends itself.
- The status line always shows that the session is running under baton, which phase it is on, and how full the context is.

## License

[MIT](LICENSE)
