#!/bin/sh
# S4: snapshot uncommitted work (tracked + new untracked, ignored excluded) into a private ref without
# touching the working tree, the index, HEAD, or gpg. Run inside a git repo.
set -e
T=$(mktemp); cp "$(git rev-parse --git-dir)/index" "$T"
GIT_INDEX_FILE=$T git add -A
TREE=$(GIT_INDEX_FILE=$T git write-tree); rm -f "$T"
C=$(git commit-tree --no-gpg-sign -p HEAD -m "baton: snapshot" "$TREE")
git update-ref "refs/baton/snapshots/manual-$(date +%s)" "$C"
echo "$C"
