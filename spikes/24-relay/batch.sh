#!/bin/sh
# usage: batch.sh <model> TAG:CHANNEL[:PRIMER:TEXT] ...   (sequential; appends to batch-<model>.log)
cd "$(dirname "$0")"
m=$1; shift
for t in "$@"; do
  IFS=: read tag ch primer text <<END
$t
END
  python3 relay.py "$ch" "$m" "$tag" $primer $text >> "batch-$m.log" 2>&1
done
echo BATCH_DONE >> "batch-$m.log"
