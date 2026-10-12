#!/bin/sh
# Runs baton's mod tests (test/mod/*.test.ts) under Claude Code's test kit: the plugin and the tests are
# copied to a scratch folder, so the plugin baton ships holds no tests, and Claude Code's generated types
# land there too. It validates the plugin first, as Claude Code will read it.
set -eu
repo=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/baton-mod-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
cp -R "$repo/plugin/." "$tmp/"
rm -rf "$tmp/bin"
mkdir -p "$tmp/tests"
cp "$repo"/test/mod/*.test.ts "$tmp/tests/"
unset BATON_HOST BATON_INSTANCE BATON_DIR BATON_BIN CLAUDE_CODE_SESSION_ID
claude plugin validate "$repo/plugin"
claude plugin test "$tmp"
