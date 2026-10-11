#!/bin/sh
# Run claude with a clean environment (no BATON_* / CLAUDE_* from the hosting session).
exec env -i HOME="$HOME" USER="$USER" LOGNAME="$LOGNAME" PATH="$PATH" TMPDIR="$TMPDIR" LANG="${LANG:-en_US.UTF-8}" SHELL="$SHELL" TERM=xterm-256color claude "$@"
