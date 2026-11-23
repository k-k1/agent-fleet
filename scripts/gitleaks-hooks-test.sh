#!/usr/bin/env bash
# Positive and negative controls for the gitleaks half of .githooks/pre-commit and
# .githooks/pre-push (#4906).
#
# Everything runs in a throwaway work clone with a bare "remote" next to it, and the hooks
# are copied in from this checkout. The credential-shaped values are built at run time so
# this file itself never holds one: the pre-commit hook would refuse to commit it.
#
# Not wired into ci.yml (a change to the workflows is merged by a person, see #4906's
# follow-up); run it by hand after touching the hooks: scripts/gitleaks-hooks-test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
FAILED=0
fail() {
  echo "FAIL: $*"
  FAILED=1
}

if ! command -v gitleaks >/dev/null 2>&1; then
  echo "SKIP: no gitleaks on PATH"
  exit 0
fi
REAL_GITLEAKS="$(command -v gitleaks)"
REAL_VERSION="$("$REAL_GITLEAKS" version | head -n1 | sed 's/^v//')"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# aws-access-token shaped (AKIA + 16 characters); two values, one of them allowlisted.
LEAK="AKIA""ZXCVBNMQWERTYUIO"
ALLOWED="AKIA""QWERTYUIOPASDFGHJK"

WORK="$TMP/work"
REMOTE="$TMP/remote.git"
git init -q --bare "$REMOTE"
git init -q -b main "$WORK"
mkdir -p "$WORK/.github/workflows"
cp -R "$REPO/.githooks" "$WORK/.githooks"
cat > "$WORK/.gitleaks.toml" <<EOF
[extend]
useDefault = true

[[allowlists]]
description = "test fixture"
regexes = ['''$ALLOWED''']
EOF
# The version the hooks compare against, in the shape ci.yml has it.
pin() {
  printf 'jobs:\n  secret-scan:\n    steps:\n      - name: install gitleaks (version+checksum pinned)\n        env:\n          VERSION: %s\n' "$1" > "$WORK/.github/workflows/ci.yml"
}
pin "$REAL_VERSION"

g() { git -C "$WORK" "$@"; }
g config core.hooksPath .githooks
g config user.email test@example.invalid
g config user.name test
g remote add origin "$REMOTE"
g add .gitleaks.toml .githooks
g commit -q -m "base"
g push -q origin main

n=0
commit_file() { # <content> [commit flags...]
  local content="$1"
  shift
  n=$((n + 1))
  printf 'token = %s\n' "$content" > "$WORK/f$n.txt"
  g add "f$n.txt"
  g commit -q -m "f$n" "$@" 2> "$TMP/err" || return 1
}
remote_has() { git -C "$REMOTE" rev-parse -q --verify "refs/heads/$1" > /dev/null; }
expect_refused() { # <what> <file with stderr>
  grep -q "gitleaks found" "$2" || fail "$1: no gitleaks message in: $(cat "$2")"
  if grep -q "$LEAK" "$2"; then fail "$1: the value was printed unredacted"; fi
}

# --- pre-commit ---
if commit_file "$LEAK"; then fail "pre-commit let a credential-shaped value through"; else expect_refused pre-commit "$TMP/err"; fi
g reset -q --hard HEAD
if commit_file "clean-value"; then :; else fail "pre-commit refused a clean file: $(cat "$TMP/err")"; fi
if commit_file "$ALLOWED"; then :; else fail "pre-commit refused an allowlisted value: $(cat "$TMP/err")"; fi

# --- pre-push: a leak committed with --no-verify ---
g reset -q --hard origin/main
g switch -q -c leak
commit_file "$LEAK" --no-verify || fail "--no-verify commit failed"
if g push origin leak 2> "$TMP/err"; then fail "pre-push let a new branch with a leak through"; else expect_refused "pre-push new branch" "$TMP/err"; fi
remote_has leak && fail "the refused push still created the branch"

# A leak in a middle commit, buried under a clean one, is still an outgoing commit.
commit_file "clean-after" || fail "clean commit failed"
if g push origin leak 2> "$TMP/err"; then fail "pre-push missed a leak under a later clean commit"; else expect_refused "pre-push buried" "$TMP/err"; fi

# The same branch, once the leak is rewritten away, goes through.
g reset -q --hard origin/main
g switch -q -C leak
commit_file "clean-value" || fail "clean commit failed"
g push -q origin leak 2> "$TMP/err" || fail "pre-push refused a clean branch: $(cat "$TMP/err")"
remote_has leak || fail "the clean push did not arrive"

# An existing branch: only the commits the remote lacks are scanned.
commit_file "$LEAK" --no-verify || fail "--no-verify commit failed"
if g push origin leak 2> "$TMP/err"; then fail "pre-push let a leak onto an existing branch"; else expect_refused "pre-push existing branch" "$TMP/err"; fi
g reset -q --hard HEAD~1
commit_file "$ALLOWED" --no-verify || fail "commit failed"
g push -q origin leak 2> "$TMP/err" || fail "pre-push refused an allowlisted value: $(cat "$TMP/err")"

# A value that entered only while resolving a merge conflict lives in the merge commit alone,
# which gitleaks skips without `-m`.
g switch -q -C mrg origin/main
printf 'x = base\n' > "$WORK/m.txt"; g add m.txt; g commit -q -m "m base"
g switch -q -c side
printf 'x = side\n' > "$WORK/m.txt"; g commit -q -am "m side"
g switch -q mrg
printf 'x = main\n' > "$WORK/m.txt"; g commit -q -am "m main"
g merge side > /dev/null 2>&1 || true
printf 'token = %s\n' "$LEAK" > "$WORK/m.txt"
g add m.txt
g commit -q --no-verify -m "merge" || fail "merge commit failed"
if g push origin mrg 2> "$TMP/err"; then fail "pre-push missed a leak that only a merge commit carries"; else expect_refused "pre-push merge" "$TMP/err"; fi

# Deleting a branch sends no commits.
g push -q origin :leak 2> "$TMP/err" || fail "pre-push refused a branch deletion: $(cat "$TMP/err")"

# --- no gitleaks: one warning, exit 0 ---
bin="$TMP/nogitleaks"
mkdir "$bin"
for t in bash env git sed awk grep head cat find mktemp rm dirname tr sh cut; do
  ln -s "$(command -v "$t")" "$bin/$t"
done
g switch -q -C nogl origin/main
n=$((n + 1))
printf 'token = %s\n' "$LEAK" > "$WORK/f$n.txt"
g add "f$n.txt"
PATH="$bin" g commit -q -m "no gitleaks" 2> "$TMP/err" || fail "pre-commit blocked without gitleaks: $(cat "$TMP/err")"
[ "$(grep -c "no gitleaks on PATH" "$TMP/err")" = 1 ] || fail "pre-commit: want exactly one warning, got: $(cat "$TMP/err")"
PATH="$bin" g push -q origin nogl 2> "$TMP/err" || fail "pre-push blocked without gitleaks: $(cat "$TMP/err")"
[ "$(grep -c "no gitleaks on PATH" "$TMP/err")" = 1 ] || fail "pre-push: want exactly one warning, got: $(cat "$TMP/err")"
g push -q origin :nogl 2> /dev/null

# --- a version other than the pinned one: warn, still scan, never block on that alone ---
pin "0.0.1"
g switch -q -C ver origin/main
if commit_file "clean-value"; then :; else fail "a version mismatch blocked a clean commit: $(cat "$TMP/err")"; fi
grep -q "ci.yml pins 0.0.1" "$TMP/err" || fail "no version warning: $(cat "$TMP/err")"
g push -q origin ver 2> "$TMP/err" || fail "a version mismatch blocked a clean push: $(cat "$TMP/err")"
grep -q "ci.yml pins 0.0.1" "$TMP/err" || fail "no version warning on push: $(cat "$TMP/err")"
if commit_file "$LEAK"; then fail "a version mismatch switched the scan off"; else expect_refused "pre-commit, other version" "$TMP/err"; fi

if [ "$FAILED" = 0 ]; then
  echo "ok"
else
  exit 1
fi
