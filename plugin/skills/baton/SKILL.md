---
name: baton
description: Run a multi-phase plan with baton, which hosts this Claude Code session and compacts the context at every phase boundary so a long plan runs unattended. Use when the user runs /baton (plan, run, start, status, pause, resume, drop, exit, drafts, version, setup, update, help), or asks to plan, run, pause, resume or drop a plan with baton, to start baton in this session, or to leave baton.
allowed-tools: Bash(baton skill)
---

# /baton

The user ran: `/baton $ARGUMENTS`

What to do comes from baton itself, so it always matches the baton this session runs:

!`baton skill`

If baton's instructions are not above, run `baton skill` with the Bash tool and follow what it prints. If that fails too, run `baton setup` and show its output.
