# Spike 18: inline commands in a plugin skill

Can the `/baton` skill load its instructions from the `baton` binary when it is invoked, so that an
update reaches sessions that loaded an older plugin? (2026-10-07, Claude Code 2.1.293, print mode, Haiku;
`./run.sh`.)

| Question | Finding |
|---|---|
| Does a plugin skill run an inline `` !`command` `` when invoked? | Yes, before the model sees the skill: the command's output replaces it. |
| Does it need permission? | Yes. With `allowed-tools: Bash(<command>)` in the skill's frontmatter it runs with no prompt. Without it, the command does not run; the model gets `[run this first, exactly as written, and use its output: …]` instead. |
| Which copy of a binary does it find? | The session's PATH, with each plugin's `bin/` appended at the end. So `~/.baton/bin` (put on the PATH by `baton init`) wins over the plugin's pinned launcher. |
| Is `$ARGUMENTS` safe inside the command? | **No.** It is substituted into the command line before the shell runs it: `/spk:spkargs hi $(echo PWNED)` printed `ARGS:[hi PWNED]`. Never put arguments in an inline command. |

baton's stub runs a fixed `baton skill` and leaves the arguments to the model, as text.
