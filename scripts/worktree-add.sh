#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ] || [[ ! "$1" =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]*$ ]] || ! git check-ref-format --branch "$1" >/dev/null 2>&1; then
    echo "usage: task worktree-create name=<branch-name> (single directory name)" >&2
    exit 64
fi

name="$1"
if git show-ref --verify --quiet "refs/heads/$name"; then
    git worktree add "../$name" "$name"
else
    git worktree add -b "$name" "../$name"
fi
