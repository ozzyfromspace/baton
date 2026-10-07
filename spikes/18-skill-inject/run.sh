#!/bin/sh
# Spike 18: does a plugin skill run an inline !`command` when invoked, which copy of a plugin binary
# does it find, does it need allowed-tools, and does $ARGUMENTS reach the shell? Print mode, Haiku.
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
cd "$work" || exit 1
strip() { env $(env | sed -n 's/^\(CLAUDE[^=]*\)=.*/-u \1/p') "$@"; }
for cmd in "/spk:spk hello" "/spk:spkopen hello" '/spk:spkargs hello $(echo PWNED) `echo TICK`'; do
  echo "=== $cmd"
  strip claude -p --model haiku --plugin-dir "$here/plugin" "$cmd" 2>&1 | head -20
done
rm -rf "$work"
