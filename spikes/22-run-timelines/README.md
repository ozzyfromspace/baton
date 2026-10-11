# 22 — where a run's time went, and what a parallel schedule would have saved

2026-10-10 ([write-up](../../docs/research/parallel.md)). These read a real run's `events.jsonl` and its session
transcript, so the data they produce describes the project the run was in; keep it out of this repo.

```sh
python3 phases.py <events.jsonl>                                   # phase windows
python3 timeline.py <transcript.jsonl> <events.jsonl> <name>.json   # label every second of the run
python3 tails.py <name> <commits.txt> <events.jsonl>                # waiting before vs after a phase's last code commit
python3 segments.py <name> <events.jsonl>                           # each phase as work / verification segments
python3 tokens.py <transcript.jsonl>                                # token throughput, peak 5-hour window
```

`commits.txt` is `git log --format='C %cI %h %s' --name-only` over the run.

- `timeline.py` gives each second one label, highest priority wins: compaction; a gate (test, lint, build)
  or a poll loop running in the foreground; a question to the human; the model in a turn; idle while the
  model's own background job ran; idle waiting on the human; idle after an API error; idle, other.
- `segments.py` classifies verification by what ran. Its patterns (`stack`, `backend`, `static`) were
  written for the runs studied and need adjusting for another project.
- `sim.py` replays each phase's observed segments under a dependency graph with N concurrently working
  sessions and L verification lanes (`hold` = a session waits on its own gates, as today; `pipelined` =
  dependents start at a predecessor's last work segment). Replaying a run as a chain reproduces its actual
  duration exactly.
- `run_sims.py` runs the scenarios against dependency graphs in the shape
  `{"phases": [{"id": "P3", "deps": [{"on": "P1", "kind": "hard|gate|merge|resource|ordering-only"}]}]}`.
  The graphs used in the write-up were reconstructed by hand-checked `git blame` over every phase pair and are
  kept outside this repo.
