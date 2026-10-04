# Security

## What baton does that you should know about

baton is a terminal host. Please understand these behaviors before installing it:

- **It types into your terminal session.** baton runs `claude` inside a pseudo-terminal it owns. At phase boundaries it types `/compact`, and when a session has stalled it types a short nudge message. It only does this inside sessions you started through `baton` (or explicitly elevated), and only while a plan you attached is running. Every keystroke it sends is announced in the session first and logged to `.baton/**/events.jsonl`.
- **It downloads a binary on first run.** The plugin's launcher fetches the release binary for your OS/architecture from this repository's GitHub Releases and verifies it against the sha256 checksums committed in the plugin before running it. Set `BATON_BIN` to use a binary you built yourself instead.
- **It can stop and restart `claude`.** Elevation (handing a plain `claude` session over to baton) sends SIGTERM to that `claude` process after its turn ends, and your shell relaunches the same conversation under baton. This only happens when you run `/baton` in that session and have added the opt-in `baton init` line to your shell.
- **Notifications leave your machine only if you configure them.** If you set an ntfy topic, baton sends content-free pushes ("waiting on you", project name) to that topic. ntfy topics are public by name: treat the topic as a password.

baton never edits your Claude Code settings files. Its hooks and status line are passed to `claude` per session via `--settings`.

## Reporting a vulnerability

Please report security issues privately via [GitHub security advisories](https://github.com/ozzyfromspace/baton/security/advisories/new) rather than in a public issue. You should get a response within a few days.
