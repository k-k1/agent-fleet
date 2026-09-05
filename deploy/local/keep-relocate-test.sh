#!/usr/bin/env bash
# keep-relocate-test.sh — regression test for entrypoint.sh's block that relocates identity
# into AF_WS_KEEP and links it back (ADR 0045 decisions 3-6). Needs neither the real image
# nor AWS: home and keep are built in temporary directories and only the block is sliced out
# and run.
#
# There is exactly one invariant to protect:
#
#   after the block returns, `mkdir -p "$HOME/.config/<anything>"` must succeed.
#
# Breaking it is what took down the first golden image in a production deployment: a home
# built from golden carries over the symlinks the seed created (~/.config ->
# $AF_WS_KEEP/.config) whole, while the EFS behind keep is empty for each new user. The
# block then judged "this is already the right symlink", took an early continue and skipped
# the mkdir that creates the target. ~/.config was left dangling, the later
# `mkdir -p "$HOME/.config/opencode"` failed with `File exists`, and `set -e` killed the
# whole entrypoint. The only symptom is a task restarting forever; the cause appears in no
# log.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
ENTRY="$ROOT/workspace/entrypoint.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Slice the block out. If entrypoint.sh is restructured and the slice comes up empty, this
# would quietly report "every case passed", so inspect the contents before using them.
BLOCK="$WORK/keep-block.sh"
awk '/^if \[ -n "\$\{AF_WS_KEEP:-\}" \]/,/^fi$/' "$ENTRY" > "$BLOCK"
for needle in 'ln -sfn' 'AF_WS_KEEP' 'mkdir -p'; do
  grep -q -- "$needle" "$BLOCK" || {
    echo "FAIL: could not slice the keep block out of entrypoint.sh (missing $needle)" >&2
    echo "      entrypoint.sh was restructured — fix the awk range in this test." >&2
    exit 1
  }
done

fail() { echo "FAIL: $1" >&2; exit 1; }

# run_case <name> — run the block against a prepared home/keep pair and check the invariant.
run_case() {
  local name="$1" home="$2" keep="$3"
  if ! HOME="$home" AF_WS_KEEP="$keep" bash -c '
        set -e
        source "$1"
        mkdir -p "$HOME/.config/opencode"
      ' _ "$BLOCK" 2>"$WORK/err.txt"; then
    echo "--- stderr ---" >&2; cat "$WORK/err.txt" >&2
    fail "$name: the block left ~/.config unusable"
  fi
  # Directories must exist for real under keep. Files (.gitconfig and friends) need not:
  # the dangling symlink is the design, so that writing to them later normally creates them
  # on the EFS side.
  for rel in .config .ssh .claude .codex; do
    [ -d "$keep/$rel" ] || fail "$name: $keep/$rel was not created"
    [ -L "$home/$rel" ] || fail "$name: ~/$rel is not a symlink"
    [ "$(readlink "$home/$rel")" = "$keep/$rel" ] || fail "$name: ~/$rel points somewhere else"
  done
  echo "ok: $name"
}

# 1) home built from golden (the regression itself): the symlinks are right, keep is empty.
G_HOME="$WORK/g/home"; G_KEEP="$WORK/g/keep"; mkdir -p "$G_HOME" "$G_KEEP"
for rel in .config .ssh .claude .codex .git-credentials .gitconfig .claude.json; do
  ln -s "$G_KEEP/$rel" "$G_HOME/$rel"
done
run_case "golden-seeded home, empty keep" "$G_HOME" "$G_KEEP"

# 2) pristine home: the real directories live in home, get moved to keep and are replaced
#    by symlinks.
F_HOME="$WORK/f/home"; F_KEEP="$WORK/f/keep"; mkdir -p "$F_HOME" "$F_KEEP"
mkdir -p "$F_HOME/.config/agent-fleet" "$F_HOME/.ssh"
echo "seeded" > "$F_HOME/.config/agent-fleet/marker"
run_case "fresh home, empty keep" "$F_HOME" "$F_KEEP"
[ -f "$F_KEEP/.config/agent-fleet/marker" ] || fail "fresh home: the real ~/.config was not relocated"

# 3) second run (an ordinary restart): breaks nothing and is still usable.
run_case "second boot (idempotent)" "$F_HOME" "$F_KEEP"
[ -f "$F_KEEP/.config/agent-fleet/marker" ] || fail "second boot: the relocated ~/.config was lost"

# 4) on runtimes that inject no AF_WS_KEEP (docker / native / Fargate) it is a complete
#    no-op.
N_HOME="$WORK/n/home"; mkdir -p "$N_HOME/.config"
echo "untouched" > "$N_HOME/.config/marker"
HOME="$N_HOME" bash -c 'set -e; unset AF_WS_KEEP; source "$1"; mkdir -p "$HOME/.config/opencode"' _ "$BLOCK"
[ -f "$N_HOME/.config/marker" ] || fail "no AF_WS_KEEP: ~/.config was touched anyway"
# Never write `[ … ] && fail`: when the condition is false the list returns 1, and under
# set -e that gives the nastiest shape of all — failing because the test passed.
if [ -L "$N_HOME/.config" ]; then fail "no AF_WS_KEEP: ~/.config became a symlink"; fi
echo "ok: no AF_WS_KEEP (docker / native / Fargate) — no-op"

echo "PASS: keep relocation holds ~/.config usable on every path"
