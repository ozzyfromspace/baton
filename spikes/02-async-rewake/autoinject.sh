#!/bin/bash
# After the rewake turn has ended without compaction, type "continue" as a genuine user turn.
cd "$(dirname "$0")"
until grep -q '"no rewake"' log.jsonl 2>/dev/null; do sleep 1; done
sleep 5
if ! grep -q '"compacted": true' state.json; then echo continue > inject.txt; echo "injected at $(date +%T)" >> autoinject.log; fi
