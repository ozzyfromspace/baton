#!/bin/bash
# Q1(d) — probing without side effects: stale stat info, index.lock held by someone else, and a
# path-set recipe that needs no lock at all. Output: q1d.out
HERE=$(cd "$(dirname "$0")" && pwd)
W=$HERE/work/q1d
G="git -c user.name=spike -c user.email=spike@example.invalid"
set +e
run() { echo "\$ $*"; out=$("$@" 2>&1); rc=$?; [ -n "$out" ] && echo "$out" | sed 's/^/  /'; echo "  [exit $rc]"; return $rc; }

rm -rf "$W"; mkdir -p "$W"; cd "$W"
$G init -q -b run main; cd main
printf 'a1\na2\na3\n' > A; printf 'b1\n' > B; printf 'c1\nc2\n' > C
echo '.baton/' >> .git/info/exclude
$G add A B C; $G commit -qm init
$G worktree add -q -b w .baton/worktrees/w run
( cd .baton/worktrees/w && echo a4 >> A && echo new > N && $G commit -qam "worker: A" && $G add N && $G commit -qm "worker: +N" )
echo c-local >> C; echo u > U

echo "######## 1. stale stat info: rewrite A with identical content (mtime changes, content does not)"
sleep 1.1; cp A A.tmp && mv A.tmp A
run $G read-tree -m -u -n HEAD w
run $G --no-optional-locks diff --name-only HEAD
echo "-- index mtime unchanged by the two commands above? $(stat -f %m .git/index)"

echo; echo "######## 2. someone else holds main's index.lock (e.g. the lead's git commit)"
touch .git/index.lock
run $G read-tree -m -u -n HEAD w
run $G --no-optional-locks diff --name-only HEAD
run $G --no-optional-locks status --porcelain
run $G status --porcelain
run $G merge --ff-only w
rm -f .git/index.lock

echo; echo "######## 3. lock-free recipe: landing paths ∩ local paths"
echo "landing paths:   git diff --name-only HEAD w"; $G diff --name-only HEAD w | sed 's/^/  /'
echo "local (tracked): git --no-optional-locks diff --name-only HEAD"; $G --no-optional-locks diff --name-only HEAD | sed 's/^/  /'
echo "local (untracked+ignored, only matters for paths the landing ADDS):"
echo "  git ls-files -o --directory   -> $($G ls-files -o | tr '\n' ' ')"
echo "overlap: $(comm -12 <($G diff --name-only HEAD w | sort) <( ($G --no-optional-locks diff --name-only HEAD; $G ls-files -o) | sort -u) | tr '\n' ' ')"
echo; echo "-- now make the overlap real: lead edits A"
echo a-lead >> A
echo "overlap: $(comm -12 <($G diff --name-only HEAD w | sort) <( ($G --no-optional-locks diff --name-only HEAD; $G ls-files -o) | sort -u) | tr '\n' ' ')"
run $G merge --ff-only w
git checkout -q A

echo; echo "######## 4. the real thing: merge --ff-only after the probe says OK"
run $G merge-base --is-ancestor HEAD w
run $G merge --ff-only w
$G status --short | sed 's/^/  /'
