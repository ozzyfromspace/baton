#!/bin/sh
# Spike 33: can baton's mod serve `/baton board` beside the plugin's /baton skill? Runs the probe plugin
# (a skill /bt, and a mod that registers a command and hooks command.run) headless with Haiku, once for
# each mode: the mod registers /bt (same), /bt-board (other), or nothing (none). Logs land in ./out/.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$here/out" "$here/out/work"
for mode in same other none; do
  rm -f "$here/out/log-$mode.jsonl"
  (cd "$here/out/work" && env -i HOME="$HOME" USER="$USER" PATH="$PATH" TMPDIR="${TMPDIR:-/tmp}" LANG="${LANG:-en_US.UTF-8}" \
    PROBE_LOG="$here/out/log-$mode.jsonl" PROBE_MODE=$mode claude -p --model haiku --plugin-dir "$here/plugin" "/bt board" > "$here/out/out-$mode.txt" 2>&1) || true
  echo "== $mode: $(head -1 "$here/out/out-$mode.txt")"
  cat "$here/out/log-$mode.jsonl" 2>/dev/null || true
done
