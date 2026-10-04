#!/bin/sh
# Run the passthrough prototype yourself: a normal-looking Claude session that compacts itself after Phase 1.
cd "$(dirname "$0")" && rm -rf .sc && exec python3 passthru.py "$(cat prompt.txt)" "$@"
