#!/bin/sh
# Runs the real-claude scenarios as one stage of a baton gate: `baton verify -- sh test/e2e/run.sh
# '^(TestA|TestB)$'` runs the scenarios its pattern names (every one without it), and `baton verify
# --failed` reruns only the scenarios that failed, from the pattern baton gives the stage then. Each run
# costs a few cents.
set -eu
cd "$(dirname "$0")"
exec baton stage e2e --runner go -- sh -c 'BATON_E2E=1 exec go test . -count=1 -v -timeout 150m -run "${BATON_FAILED_RE:-$0}"' "${1:-.}"
