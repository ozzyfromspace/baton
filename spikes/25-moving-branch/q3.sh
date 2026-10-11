#!/bin/bash
# Q3 — lock contention between the main checkout and a worker worktree, all at once.
#   lead:   in main, append to L and `git commit -am` (N times)
#   worker: in the worktree, append to its own file, commit, `git rebase run` (N times)
#   host:   (variant b/c) in main, `git merge --ff-only w` when w is ahead (N times)
#   variant d: lead + status only; variant e: lead + `git --no-optional-locks status` (how Claude Code polls)
#   status: (variant c/d) in main, plain `git status` in a tight loop (what a session's own git use looks like)
# Every failing command's stderr is logged to q3-<variant>/<actor>.err. Output: q3.out
HERE=$(cd "$(dirname "$0")" && pwd)
N=${N:-50}
G="git -c user.name=spike -c user.email=spike@example.invalid"

setup() {
  W=$HERE/work/q3-$1; rm -rf "$W"; mkdir -p "$W"; cd "$W"
  $G init -q -b run main; cd main
  echo l0 > L; echo s0 > S; echo '.baton/' >> .git/info/exclude
  $G add L S; $G commit -qm init
  $G worktree add -q -b w .baton/worktrees/w run
  MAIN=$PWD; WT=$PWD/.baton/worktrees/w; LOG=$W
}
lead() { cd "$MAIN"; local ok=0 bad=0; touch $LOG/lead.running
  for i in $(seq $N); do sleep ${LEAD_SLEEP:-0}; echo "l$i" >> L
    if out=$($G commit -qam "lead $i" 2>&1); then ok=$((ok+1)); else bad=$((bad+1)); echo "[$i] $out" >> $LOG/lead.err; fi
  done; echo "lead: commits ok=$ok failed=$bad" > $LOG/lead.sum; rm -f $LOG/lead.running; }
worker() { cd "$WT"; local ok=0 bad=0 rok=0 rbad=0; touch $LOG/worker.running
  for i in $(seq $N); do echo "w$i" > "W$i"; $G add "W$i"
    if out=$($G commit -qm "worker $i" 2>&1); then ok=$((ok+1)); else bad=$((bad+1)); echo "[commit $i] $out" >> $LOG/worker.err; fi
    if out=$($G rebase -q run 2>&1); then rok=$((rok+1)); else rbad=$((rbad+1)); echo "[rebase $i] $out" >> $LOG/worker.err
      $G rebase --abort >/dev/null 2>&1; fi
  done; echo "worker: commits ok=$ok failed=$bad; rebases ok=$rok failed=$rbad" > $LOG/worker.sum; rm -f $LOG/worker.running; }
host() { cd "$MAIN"; local ok=0 noop=0 diverged=0 bad=0 i=0
  sleep 0.2; while [ -n "$(ls $LOG/*.running 2>/dev/null)" ]; do i=$((i+1))
    if [ "$($G rev-parse run)" = "$($G rev-parse w)" ]; then noop=$((noop+1)); sleep 0.02; continue; fi
    if ! $G merge-base --is-ancestor run w; then diverged=$((diverged+1)); sleep 0.02; continue; fi
    if out=$($G merge -q --ff-only w 2>&1); then ok=$((ok+1)); else bad=$((bad+1)); echo "[$i] $out" >> $LOG/host.err; fi
  done; echo "host ($i tries): landed=$ok nothing-to-land=$noop not-ff-yet=$diverged merge-failed=$bad" > $LOG/host.sum; }
status() { cd "$MAIN"; local ok=0 bad=0
  for i in $(seq $((N*4))); do
    if out=$($G $NOLOCK status --porcelain 2>&1 >/dev/null); [ $? -eq 0 ]; then ok=$((ok+1)); else bad=$((bad+1)); echo "[$i] $out" >> $LOG/status.err; fi
  done; echo "status: ok=$ok failed=$bad" > $LOG/status.sum; }

report() { echo "== variant $1 (N=$N) took ${2}s"; cat $LOG/*.sum
  for f in $LOG/*.err; do [ -f "$f" ] || continue; echo "-- $(basename $f): $(grep -c '^\[' $f) failures; distinct messages:"
    grep -hoE "(fatal|error): [^']*('[^']*')?[^.]*" "$f" | sed -E 's#/[^ ]*/work/#…/work/#; s/[0-9a-f]{40}/<sha>/g' | sort | uniq -c | sed 's/^/   /'; done
  cd "$MAIN"; echo "-- end: run=$($G rev-parse --short run) w=$($G rev-parse --short w)  fsck: $($G fsck --no-progress 2>&1 | head -3 | tr '\n' ' ')ok"
  echo "-- main status: $($G status --porcelain | tr '\n' ' ')"; echo; }

for v in ${VARIANTS:-a b c d}; do
  setup $v; t0=$(date +%s)
  case $v in
    a) lead & worker & wait ;;
    b) lead & worker & host & wait ;;
    c) lead & worker & host & status & wait ;;
    d) lead & status & wait ;;
    e) NOLOCK=--no-optional-locks; lead & status & wait; NOLOCK= ;;
  esac
  report $v $(( $(date +%s) - t0 ))
done
