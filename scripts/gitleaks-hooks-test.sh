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

# Hermetic git: nothing from the user's or the system's configuration (commit.gpgSign, a
# global core.hooksPath, templates with their own hooks) may reach the throwaway repos.
mkdir "$TMP/home"
export HOME="$TMP/home" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

# aws-access-token shaped (AKIA + 16 characters); two values, one of them allowlisted.
LEAK="AKIA""ZXCVBNMQWERTYUIO"
ALLOWED="AKIA""MNBVCXZLKJHGFDSA"

WORK="$TMP/work"
REMOTE="$TMP/remote.git"
git init -q --template= --bare "$REMOTE"
git init -q --template= -b main "$WORK"
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
# Positive control for the allowlist: without it the same value is a finding, so a hook that
# ignores .gitleaks.toml cannot pass the next check by accident.
printf '[extend]\nuseDefault = true\n' > "$WORK/.gitleaks.toml"
if commit_file "$ALLOWED"; then fail "the allowlist control value is not credential-shaped"; else expect_refused "pre-commit without the allowlist" "$TMP/err"; fi
g reset -q --hard HEAD # also restores the committed .gitleaks.toml
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
printf '[extend]\nuseDefault = true\n' > "$WORK/.gitleaks.toml"
if g push origin leak 2> "$TMP/err"; then fail "pre-push: the allowlist control value is not credential-shaped"; else expect_refused "pre-push without the allowlist" "$TMP/err"; fi
g checkout -q -- .gitleaks.toml
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

# A stale remote-tracking ref (the server never had, or dropped, that commit) must not
# exclude it from the scan.
g switch -q -C stale origin/main
commit_file "$LEAK" --no-verify || fail "--no-verify commit failed"
g update-ref refs/remotes/origin/ghost "$(g rev-parse HEAD)"
if g push origin stale 2> "$TMP/err"; then fail "pre-push trusted a stale remote-tracking ref"; else expect_refused "pre-push stale ref" "$TMP/err"; fi
g update-ref -d refs/remotes/origin/ghost

# `git push <url-or-path>` has no remote name: the URL of a configured remote resolves to it,
# and an unknown destination is still scanned.
if g push "$REMOTE" stale:refs/heads/urlpush 2> "$TMP/err"; then fail "pre-push let a leak through a push by URL"; else expect_refused "pre-push by URL" "$TMP/err"; fi
git init -q --template= --bare "$TMP/other.git"
if g push "$TMP/other.git" stale:refs/heads/x 2> "$TMP/err"; then fail "pre-push let a leak through a push to an unknown destination"; else expect_refused "pre-push unknown destination" "$TMP/err"; fi
g switch -q -C urlclean origin/main
commit_file "clean-value" || fail "clean commit failed"
g push -q "$REMOTE" urlclean 2> "$TMP/err" || fail "pre-push refused a clean push by URL: $(cat "$TMP/err")"
g push -q "$TMP/other.git" urlclean 2> "$TMP/err" || fail "pre-push refused a clean push to an unknown destination: $(cat "$TMP/err")"

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

# History already on the remote's default branch is not rescanned (CI covers it): a leak that
# is already public must not block every later push, by name or by URL.
g switch -q -C pub origin/main
pin "$REAL_VERSION"
commit_file "$LEAK" --no-verify || fail "--no-verify commit failed"
g push -q --no-verify origin pub:main
g switch -q -C after
commit_file "clean-value" || fail "clean commit failed"
g push -q origin after 2> "$TMP/err" || fail "pre-push rescanned history already on the remote's main: $(cat "$TMP/err")"
g push -q "$REMOTE" after:refs/heads/after-url 2> "$TMP/err" || fail "pre-push rescanned published history on a push by URL: $(cat "$TMP/err")"
# ...but a destination that is no configured remote has none of it: the same history, which
# reaches origin/main, is scanned in full there.
git init -q --template= --bare "$TMP/fresh.git"
if g push "$TMP/fresh.git" after:refs/heads/x 2> "$TMP/err"; then fail "pre-push excluded origin/main's history from a push to an unknown destination"; else expect_refused "pre-push unknown destination, published history" "$TMP/err"; fi

# A remote that fetches from one repository and pushes to another: its tracking refs describe
# the fetch side only. `pub` carries a leak that the fetch side (src.git) has as its main.
git init -q --template= --bare "$TMP/src.git"
git init -q --template= --bare "$TMP/pushonly.git"
g push -q --no-verify "$TMP/src.git" pub:refs/heads/main
g remote add split "$TMP/src.git"
g remote set-url --push split "$TMP/pushonly.git"
g fetch -q split
if g push split after:refs/heads/x 2> "$TMP/err"; then fail "pre-push trusted the fetch side of a remote with a different pushurl"; else expect_refused "pre-push pushurl" "$TMP/err"; fi

# Several urls on one remote: git fetches from the first and pushes to every one.
git init -q --template= --bare "$TMP/second.git"
g remote add multi "$TMP/src.git"
g remote set-url --add multi "$TMP/second.git"
g fetch -q multi
if g push multi after:refs/heads/y 2> "$TMP/err"; then fail "pre-push trusted the first url of a remote with several"; else expect_refused "pre-push several urls" "$TMP/err"; fi

if [ "$FAILED" = 0 ]; then
  echo "ok"
else
  exit 1
fi
