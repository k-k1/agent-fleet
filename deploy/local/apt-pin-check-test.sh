#!/usr/bin/env bash
# apt-pin-check.sh against fixture package indexes served over file://.
#
#   deploy/local/apt-pin-check-test.sh
#
# The check runs once a day against live Debian mirrors, so the only feedback on a wrong
# verdict is a release build that fails on arm64 — the failure it exists to prevent.
# Case 2 is the one that matters: the pin is still served for amd64 and gone for arm64,
# and the check has to say so. Case 5 is the other direction: an unreadable index must
# not be read as "served". Cases 7-10 pin the rest of the contract: pins are extracted
# from the Dockerfile, versions are ordered as dpkg orders them, index paragraphs are read
# in any field order and filtered by architecture, and any unreadable index makes the run
# incomplete.
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

# raw <tree> <suite> <arch> — the index body comes from stdin, verbatim.
raw() {
  local dir="$WORK/$1/dists/$2/main/binary-$3"
  mkdir -p "$dir"
  cat > "$dir/Packages"
  xz -f "$dir/Packages"
}

DOCKERFILE="$ROOT/workspace/Dockerfile"

run() {
  set +e
  APT_PIN_DOCKERFILE="$DOCKERFILE" \
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
# Positive control for the extraction: the real Dockerfile yields exactly these three.
has "all of [chromium chromium-common chromium-sandbox] on"

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

# 7. Pins come from the Dockerfile, not a list: a new ARG-pinned package in another RUN
#    (continued over lines, after an options flag), a fourth package on CHROMIUM_VERSION
#    and a literal pin are all checked; a pin whose version cannot be resolved fails; a
#    `pip install x==1` chained after `apt-get update` is not an apt pin.
CASE="7 pins extracted from the Dockerfile"
reset; plain_main
for a in amd64 arm64; do
  # shellcheck disable=SC2046
  index security trixie-security "$a" $(all3 "$PIN") lit "1.2-3"
done
DOCKERFILE="$WORK/Dockerfile"
cat "$ROOT/workspace/Dockerfile" - > "$DOCKERFILE" <<'EOF'
ARG EXTRA_VERSION=1
RUN apt-get update \
 && apt-get -y --no-install-recommends install \
      "extra=${EXTRA_VERSION}" \
      chromium-driver=$CHROMIUM_VERSION \
      lit=1.2-3 \
 && pip install foo==1.0
EOF
run
[ "$code" = 1 ] || fail "$CASE: exit $code, want 1"
has "EXTRA_VERSION      amd64   extra              MISSING (1 not served)"
has "CHROMIUM_VERSION   arm64   chromium-driver    MISSING"
has "literal            arm64   lit                ok (1.2-3)"
lacks "foo"
cat "$ROOT/workspace/Dockerfile" - > "$DOCKERFILE" <<'EOF'
RUN apt-get install -y "odd=${ODD_VERSION:-1}"
EOF
run
[ "$code" = 1 ] || fail "$CASE (unsupported): exit $code, want 1"
has "UNSUPPORTED pin 'odd=\${ODD_VERSION:-1}'"
DOCKERFILE="$ROOT/workspace/Dockerfile"

# 8. Newest by dpkg ordering, where `sort -V` is wrong: a letter after the upstream
#    digits, `~` sorting before the release, and an epoch beating any upstream version.
CASE="8 dpkg version ordering"
for set in "1.0-1 1.0a-1|1.0a-1" "1.0~rc1-1 1.0-1|1.0-1" "1:1.0-1 2.0-1|1:1.0-1"; do
  vers="${set%%|*}"; want="${set##*|}"
  reset; plain_main
  for a in amd64 arm64; do
    args=()
    for v in $vers; do
      # shellcheck disable=SC2207
      args+=($(all3 "$v"))
    done
    index security trixie-security "$a" "${args[@]}"
  done
  run
  has "on [amd64 arm64]: $want"
done

# 9. Paragraphs in any field order; only this architecture's (or `all`) count.
CASE="9 paragraph parsing"
reset; plain_main
stanza() { printf '%s\n' "$@" ""; }
{
  stanza "Version: $PIN" "Architecture: amd64" "Package: chromium"
  stanza "Architecture: all" "Package: chromium-common" "Version: $PIN"
  stanza "Package: chromium-sandbox" "Description: x" "Version: $PIN" "Architecture: amd64"
} | raw security trixie-security amd64
{
  stanza "Package: chromium" "Version: $PIN" "Architecture: arm64"
  stanza "Package: chromium-common" "Version: $PIN" "Architecture: all"
  stanza "Package: chromium-sandbox" "Version: $PIN" "Architecture: amd64"
} | raw security trixie-security arm64
run
[ "$code" = 1 ] || fail "$CASE: exit $code, want 1"
[ "$(grep -c ' ok (' "$WORK/out")" = 5 ] || fail "$CASE: want 5 ok rows"
has "arm64   chromium-sandbox   MISSING"

# 10. Pin found, but one index unreadable: rows are ok, the run is incomplete (exit 2).
CASE="10 unreadable index with the pin found elsewhere"
reset
for a in amd64 arm64; do
  index debian trixie "$a"
  # shellcheck disable=SC2046
  index security trixie-security "$a" $(all3 "$PIN")
done
index debian trixie-updates amd64
run
[ "$code" = 2 ] || fail "$CASE: exit $code, want 2"
[ "$(grep -c ' ok (' "$WORK/out")" = 6 ] || fail "$CASE: want 6 ok rows"
has "Incomplete: an index could not be read for [arm64]"

echo "OK: apt-pin-check.sh (10 cases)"
