#!/bin/sh
[ -n "$BATON_HOST" ] || [ -n "$BATON_FORCE" ] || exit 0
exec "$(dirname "$0")/hookbin" "$@"
