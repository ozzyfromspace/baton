#!/bin/sh
# Builds the scratch repo `proj` (one commit on main) and the worktrees the questions use.
set -e
S=$(cd "$(dirname "$0")" && pwd -P)
cd "$S"; rm -rf proj outside elsewhere logs out; mkdir -p proj outside elsewhere logs out
cd proj
git init -q -b main
echo '# proj' > README.md
git add README.md
git -c user.name=spike -c user.email=spike@example.invalid -c commit.gpgsign=false commit -q -m init
echo '.baton/' >> .git/info/exclude
git worktree add -q -b phase/P1 .baton/worktrees/P1      # (a) under the project
git worktree add -q -b phase/P2 "$S/outside/P2"          # (b) outside the project
git worktree add -q -b phase/P3 "$S/outside/P3"          # (c1) via symlink, PWD not set
git worktree add -q -b phase/P4 "$S/outside/P4"          # (c2) via symlink, PWD set
mkdir -p .baton/w
ln -s "$S/outside/P3" .baton/w/P3
ln -s "$S/outside/P4" .baton/w/P4
git worktree add -q -b phase/P5 .baton/worktrees/P5      # Q6
git worktree add -q -b phase/P6 .baton/worktrees/P6      # Q6
git worktree list
