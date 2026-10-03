#!/usr/bin/env bash
# apt-pin-check.sh against fixture package indexes served over file://.
#
#   deploy/local/apt-pin-check-test.sh
#
# The check runs once a day against live Debian mirrors, so the only feedback on a wrong
# verdict is a release build that fails on arm64 — the failure it exists to prevent.
# Case 2 is the one that matters: the pin is still served for amd64 and gone for arm64,
# and the check has to say so. Case 5 is the other direction: an unreadable index must
# not be read as "served".
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
CHECK="$HERE/apt-pin-check.sh"

PIN="$(sed -n 's/^ARG CHROMIUM_VERSION=//p' "$ROOT/workspace/Dockerfile")"
[ -n "$PIN" ] || { echo "NG: ARG CHROMIUM_VERSION not found in workspace/Dockerfile"; exit 1; }
OTHER="999.0.0.1-1~deb13u1"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() {
  echo "NG: $1"
  echo "--- out ---"; cat "$WORK/out"
  echo "--- err ---"; cat "$WORK/err"
  exit 1
}

# index <tree> <suite> <arch> [<package> <version>]... — an empty list is a readable,
# empty index; a suite that is never written is an unreadable one.
index() {
  local tree="$1" suite="$2" arch="$3"; shift 3
  local dir="$WORK/$tree/dists/$suite/main/binary-$arch"
  mkdir -p "$dir"
  : > "$dir/Packages"
  while [ "$#" -gt 0 ]; do
    printf 'Package: %s\nSource: chromium\nVersion: %s\nArchitecture: %s\n\n' "$1" "$2" "$arch" >> "$dir/Packages"
    shift 2
  done
  xz -f "$dir/Packages"
}

all3() { # all3 <version> — the three chromium packages at one version
  echo chromium "$1" chromium-common "$1" chromium-sandbox "$1"
}

# A case: fresh trees, then the caller writes the indexes.
reset() { rm -rf "$WORK/debian" "$WORK/security"; }

run() {
  set +e
  APT_PIN_DEBIAN_URL="file://$WORK/debian" APT_PIN_SECURITY_URL="file://$WORK/security" \
    "$CHECK" > "$WORK/out" 2> "$WORK/err"
  code=$?
  set -e
}

has() { grep -qF -- "$1" "$WORK/out" || fail "$CASE: output lacks '$1'"; }
lacks() { ! grep -qF -- "$1" "$WORK/out" || fail "$CASE: output has '$1'"; }

# The two main-archive suites, readable and without chromium, for both arches.
plain_main() {
  for a in amd64 arm64; do
    index debian trixie "$a"
    index debian trixie-updates "$a"
  done
}

# 1. Served on both architectures from trixie-security.
CASE="1 served everywhere"
reset; plain_main
# shellcheck disable=SC2046
index security trixie-security amd64 $(all3 "$PIN")
# shellcheck disable=SC2046
index security trixie-security arm64 $(all3 "$PIN")
run
[ "$code" = 0 ] || fail "$CASE: exit $code, want 0"
[ "$(grep -c ' ok (' "$WORK/out")" = 6 ] || fail "$CASE: want 6 ok rows"
has "on [amd64 arm64]: $PIN"

# 2. Still served for amd64, gone for arm64 (issue #1577).
CASE="2 gone for arm64 only"
reset; plain_main
# shellcheck disable=SC2046
index security trixie-security amd64 $(all3 "$PIN")
# shellcheck disable=SC2046
index security trixie-security arm64 $(all3 "$OTHER")
run
[ "$code" = 1 ] || fail "$CASE: exit $code, want 1"
has "arm64   chromium           MISSING"
has "arm64   chromium-sandbox   MISSING"
lacks "amd64   chromium           MISSING"
has "on [amd64 arm64]: none"

# 3. One package of the three lags on one arch: apt needs all of them at the pin.
CASE="3 one package missing"
reset; plain_main
# shellcheck disable=SC2046
index security trixie-security amd64 $(all3 "$PIN")
index security trixie-security arm64 chromium "$PIN" chromium-common "$PIN" chromium-sandbox "$OTHER"
run
[ "$code" = 1 ] || fail "$CASE: exit $code, want 1"
has "arm64   chromium-sandbox   MISSING"
[ "$(grep -c MISSING "$WORK/out")" = 1 ] || fail "$CASE: want exactly 1 MISSING row"

# 4. Served from the main archive while security has moved on: apt resolves a pin from
#    any configured suite, so this is served.
CASE="4 served from trixie main"
reset
for a in amd64 arm64; do
  # shellcheck disable=SC2046
  index debian trixie "$a" $(all3 "$PIN")
  index debian trixie-updates "$a"
  # shellcheck disable=SC2046
  index security trixie-security "$a" $(all3 "$OTHER")
done
run
[ "$code" = 0 ] || fail "$CASE: exit $code, want 0"
has "on [amd64 arm64]: $OTHER"

# 5. arm64's security index cannot be read: unknown, not served.
CASE="5 unreadable index"
reset; plain_main
# shellcheck disable=SC2046
index security trixie-security amd64 $(all3 "$PIN")
run
[ "$code" = 2 ] || fail "$CASE: exit $code, want 2"
has "arm64   chromium           UNKNOWN"
lacks "arm64   chromium           ok"
grep -qF "cannot read trixie-security/main/binary-arm64" "$WORK/err" || fail "$CASE: no warning naming the index"

# 6. The candidate is the newest version common to every (arch, package), compared as
#    versions: .100 is newer than .99, which a plain string sort gets wrong.
CASE="6 candidate"
reset
for a in amd64 arm64; do
  # shellcheck disable=SC2046
  index debian trixie "$a" $(all3 "1.0.0.99-1")
  index debian trixie-updates "$a"
done
# shellcheck disable=SC2046
index security trixie-security amd64 $(all3 "1.0.0.100-1") $(all3 "2.0.0.0-1")
# shellcheck disable=SC2046
index security trixie-security arm64 $(all3 "1.0.0.100-1")
run
[ "$code" = 1 ] || fail "$CASE: exit $code, want 1"
has "on [amd64 arm64]: 1.0.0.100-1"

echo "OK: apt-pin-check.sh (6 cases)"
