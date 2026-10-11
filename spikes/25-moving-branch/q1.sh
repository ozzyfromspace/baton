#!/bin/bash
# Q1 — pure git: can the run branch fast-forward under a main checkout with local changes?
# Layout mirrors baton: main checkout on `run`, worker worktree at .baton/worktrees/w on branch `w`
# (.baton/ excluded via .git/info/exclude). Output: q1.out
HERE=$(cd "$(dirname "$0")" && pwd)
W=$HERE/work/q1
G="git -c user.name=spike -c user.email=spike@example.invalid -c advice.detachedHead=false"
set +e

run() { # print a command, run it in $PWD, show output + exit code
  echo "\$ $*"; out=$("$@" 2>&1); rc=$?; [ -n "$out" ] && echo "$out" | sed 's/^/  /'; echo "  [exit $rc]"; return $rc; }

mk() { # fresh repo: A B C committed on run; worktree w
  rm -rf "$W/$1"; mkdir -p "$W/$1"; cd "$W/$1"
  $G init -q -b run main; cd main
  printf 'a1\na2\na3\n' > A; printf 'b1\n' > B; printf 'c1\nc2\n' > C
  echo '.baton/' >> .git/info/exclude
  $G add A B C; $G commit -qm init
  $G worktree add -q -b w .baton/worktrees/w run
  WT=$PWD/.baton/worktrees/w
}
wcommit() { # worker commits the given edits (shell snippet) in its worktree
  ( cd "$WT" && eval "$1" && $G add -A && $G commit -qm "worker: $2" )
}
snap() { echo "-- state: run=$($G rev-parse --short run) HEAD=$($G rev-parse --short HEAD) w=$($G rev-parse --short w)"
  echo "-- git status --short:"; $G status --short | sed 's/^/  /'
  for f in "$@"; do echo "-- $f: $(tr '\n' ' ' < "$f" 2>/dev/null || echo '<missing>')"; done; }
probe() { # side-effect-free checks: ancestry + read-tree dry run (+ index unchanged)
  local before=$(shasum .git/index | cut -c1-12)
  run $G merge-base --is-ancestor HEAD w
  run $G read-tree -m -u -n HEAD w
  echo "  (index sha before/after dry run: $before / $(shasum .git/index | cut -c1-12))"
}

echo "################ (a) worker touches A,B; main has unstaged C edit + untracked U"
mk a
echo c-local >> C; echo untracked > U
wcommit "echo a4 >> A; echo b2 >> B; echo new > N" "A B +N"
snap A B C U
probe
run $G merge --ff-only w
snap A B C U N

echo; echo "################ (b) worker touches C, which main has edited (unstaged)"
mk b
echo c-local >> C
wcommit "echo a4 >> A; echo c-worker >> C" "A C"
probe
run $G merge --ff-only w
snap A C
echo "-- half-applied? A on disk: $(tr '\n' ' ' < A)  (worker's A would end in a4)"

echo; echo "################ (c1) staged change to A in main; worker touches A"
mk c1
echo a-staged >> A; $G add A
wcommit "echo a4 >> A" "A"
probe
run $G merge --ff-only w
snap A

echo; echo "################ (c2) staged change to C in main; worker touches A,B only"
mk c2
echo c-staged >> C; $G add C
wcommit "echo a4 >> A; echo b2 >> B" "A B"
probe
run $G merge --ff-only w
snap A B C
echo "-- staged diff after ff:"; $G diff --cached --stat | sed 's/^/  /'

echo; echo "################ (c3) staged + unstaged edits to A identical to the worker's result"
mk c3
echo a4 >> A
wcommit "echo a4 >> A" "A same content"
probe
run $G merge --ff-only w
snap A

echo; echo "################ (e1) worker adds N; main has an UNTRACKED N"
mk e1
echo mine > N
wcommit "echo theirs > N" "+N"
probe
run $G merge --ff-only w
snap N

echo; echo "################ (e2) worker adds N; main has an IGNORED N (in .git/info/exclude)"
mk e2
echo N >> .git/info/exclude; echo mine-ignored > N
wcommit "echo theirs > N && git add -f N" "+N (forced past ignore)"
probe
run $G merge --ff-only w
snap N
echo "-- ignored N was silently overwritten if it now says 'theirs'"

echo; echo "################ (f) run moved on (lead committed) after the worker's rebase -> ff impossible"
mk f
wcommit "echo a4 >> A" "A"
echo b-lead >> B; $G commit -qam "lead: B"
probe
run $G merge --ff-only w
snap A B

echo; echo "################ (g) other ways to move run from the worker side (anti-patterns)"
mk g
echo c-local >> C
wcommit "echo a4 >> A" "A"
echo "-- push/fetch into the checked-out branch from the worktree:"
( cd "$WT"; run $G push . w:run; run $G fetch . w:run )
echo "-- git branch -f run w from the worktree:"
( cd "$WT"; run $G branch -f run w )
echo "-- git update-ref refs/heads/run w (moves the ref only):"
( cd "$WT"; run $G update-ref refs/heads/run w $( $G rev-parse run ) )
snap A C
echo "-- git diff --cached (what a 'git commit -a' by the lead would now record):"; $G diff --cached | sed 's/^/  /'

echo; echo "################ (h) dirty file touched by the worker -> stash-free alternative: wait. What does merge --autostash do?"
mk h
echo c-local >> C
wcommit "echo c-worker >> C" "C appended"
run $G merge --ff-only --autostash w
snap C
