#!/bin/sh
# Runs kit.test.ts against spike 41's mod under Claude Code's test kit, in a scratch copy.
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/spk41-kit.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
cp -R "$here/mod/." "$tmp/"
mkdir -p "$tmp/tests"
cp "$here/kit/kit.test.ts" "$tmp/tests/"
claude plugin test "$tmp"
