---
name: baton
description: Drive a multi-phase plan with baton, which hosts this Claude Code session and compacts the context at every phase boundary. Use when the user runs /baton (help, status, plan, attach, pause, resume, elevate, setup).
---

# /baton

baton runs an approved multi-phase plan phase by phase in this session and compacts the context between phases. It works through a small command-line tool, `baton`, that ships with this plugin (in its `bin/` directory, so it is on the Bash tool's PATH).

The user ran `/baton $ARGUMENTS`. Handle the first word:

- **help** (or nothing): explain the subcommands below in a few lines.
- **status**: run `baton status` with the Bash tool and show the output verbatim.

The other subcommands (plan, attach, pause, resume, elevate, setup) are being built; say so if asked.
