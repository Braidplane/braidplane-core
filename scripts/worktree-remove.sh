#!/usr/bin/env bash
# Remove only a clean registered sibling worktree, preserving its branch.
set -euo pipefail

fail() {
  echo "worktree-remove: $*" >&2
  exit 1
}

[ "$#" = 1 ] || fail "usage: scripts/worktree-remove.sh <worktree-name>"
name="$1"
[[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || fail "expected a sibling worktree name, not a path"

root="$(git rev-parse --show-toplevel)"
root="$(cd "$root" && pwd -P)"
[ ! -L "$root/../$name" ] || fail "target is a symbolic link"
target="$(cd "$root/../$name" && pwd -P)" || fail "worktree directory not found"
[ "$target" != "$root" ] || fail "cannot remove the current checkout"
[ "$(dirname "$target")" = "$(dirname "$root")" ] || fail "target is not a sibling checkout"

trees=()
registered=false
while IFS= read -r -d '' field; do
  case "$field" in
    'worktree '*)
      tree="${field#worktree }"
      trees+=("$tree")
      [ "$tree" != "$target" ] || registered=true
      ;;
    locked*) [ "$tree" != "$target" ] || fail "target worktree is locked" ;;
  esac
done < <(git worktree list --porcelain -z)
[ "${trees[0]:-}" = "$root" ] || fail "run removal from the main worktree"
[ "$registered" = true ] || fail "target is not registered in this repository"

branch="$(git -C "$target" symbolic-ref --quiet HEAD)" || fail "target HEAD is detached; preserve it on a branch first"
head="$(git -C "$target" rev-parse HEAD)"
[ "$(git rev-parse "$branch")" = "$head" ] || fail "target HEAD is not preserved by its branch"
status="$(git -C "$target" status --porcelain)" || fail "cannot inspect target changes"
[ -z "$status" ] || fail "target has staged, unstaged, or untracked changes"

if [ -e "$target/.worktree/community" ]; then
  fail "preserve the retained Community keys and runtime state before removing this worktree"
fi

git worktree remove -- "$target"
echo "worktree-remove: removed $name; preserved $branch at $head"
