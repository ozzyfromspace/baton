# baton v0.2 — graduated escalation

Spikes were run before planning. They confirm most of the note and change two of its mechanisms.

| # | Question | Result |
|---|---|---|
| S1 | Can baton be sure the dialog it types into is its own? | Dialogs are a FIFO queue. |
| S3 | Does a timed ask work end to end? | Yes, and a late answer can be honoured. |
| S2 | Replay of the incident | Today's baton: 4/4 hard-blocked. |
| S4 | Can baton save uncommitted work by itself? | Yes, with a snapshot ref. |

**Decisions this plan takes:**

1. **P1 is fixed first.** The context question becomes three options.

## Standing rules

- Every phase ends with its work committed.

## P0 — Evidence and the settled design

Put the spike evidence on record.

## P1 — Dialog safety

| Step | What |
|---|---|
| P1a | The queue |

## P2 — Durability, and projects without git

## P3 — The vocabulary

## P4 — The timed proposal

## P5 — Visibility and late answers

## P6 — End to end

## P7 — Docs and release

## Verification

- `make test` at every phase end.
