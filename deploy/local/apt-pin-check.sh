#!/usr/bin/env bash
# Debian package pins in workspace/Dockerfile: is the pinned version still served for
# every architecture the image is built for?
#
# Why this exists: trixie-security keeps only the current build of a package, so a pin
# such as `chromium=<version>` stops resolving the day Debian publishes the next security
# update, and the two architectures do not necessarily move on the same day. Nothing
# notices until a build misses its layer cache: an amd64 build kept passing from cache
# while every arm64 build failed with `Version '…' for 'chromium' was not found`.
#
# A version is "served" for an architecture when any suite the image's apt sources name
# (trixie, trixie-updates, trixie-security; node:22-trixie-slim ships those three) lists
# it for every package of the pin. An index that cannot be read is never taken as
# "served" or as "missing": that architecture's answer is unknown.
#
# The output also names the newest version every package of a pin is served at on every
# architecture — the value to bump the ARG to.
#
# Usage:
#   deploy/local/apt-pin-check.sh
# Environment (tests point these at file:// fixtures):
#   APT_PIN_DEBIAN_URL    default https://deb.debian.org/debian
#   APT_PIN_SECURITY_URL  default https://deb.debian.org/debian-security
#   APT_PIN_ARCHES        default "amd64 arm64" (dev-image.yml / release.sh platforms)
# Exit codes: 0 = every pin served on every architecture / 1 = a pin is missing for at
# least one architecture / 2 = no pin is missing, but some index could not be read
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKERFILE="$ROOT/workspace/Dockerfile"
DEBIAN_URL="${APT_PIN_DEBIAN_URL:-https://deb.debian.org/debian}"
SECURITY_URL="${APT_PIN_SECURITY_URL:-https://deb.debian.org/debian-security}"
read -r -a ARCHES <<< "${APT_PIN_ARCHES:-amd64 arm64}"

# ARG name|packages installed at "${ARG}" (all of them must exist at that exact version)
PINS=(
  "CHROMIUM_VERSION|chromium chromium-common chromium-sandbox"
)

# base URL|suite
SUITES=(
  "$DEBIAN_URL|trixie"
  "$DEBIAN_URL|trixie-updates"
  "$SECURITY_URL|trixie-security"
)

CURL_RETRY=(--retry 3 --retry-delay 1 --retry-all-errors)

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Writes "<package> <version>" lines for one suite/arch to $WORK/<arch>.<n>.pv, or
# returns 1.
# Fetch and decompress are separate steps so a truncated download is a failure, not an
# index that happens to lack the package.
fetch_index() { # fetch_index <arch> <n> <base> <suite>
  local xz="$WORK/$1.$2.xz"
  curl -fsSL --max-time 60 "${CURL_RETRY[@]}" -o "$xz" \
    "$3/dists/$4/main/binary-$1/Packages.xz" 2>/dev/null || return 1
  xz -dc "$xz" > "$WORK/$1.$2.txt" 2>/dev/null || return 1
  awk '/^Package: /{p=$2} /^Version: /{if (p != "") print p, $2; p=""} /^$/{p=""}' \
    "$WORK/$1.$2.txt" > "$WORK/$1.$2.pv"
}

# unreadable[arch]=1 when at least one of that arch's suites could not be read.
declare -A unreadable=()
for arch in "${ARCHES[@]}"; do
  n=0
  for s in "${SUITES[@]}"; do
    n=$((n + 1))
    IFS='|' read -r base suite <<< "$s"
    if ! fetch_index "$arch" "$n" "$base" "$suite"; then
      echo "apt-pin-check: warning: cannot read $suite/main/binary-$arch" >&2
      unreadable[$arch]=1
      : > "$WORK/$arch.$n.pv"
    fi
  done
  cat "$WORK/$arch".*.pv > "$WORK/$arch.all"
done

missing=0
unknown=0
for p in "${PINS[@]}"; do
  IFS='|' read -r arg pkgs <<< "$p"
  out="$(sed -n "s/^ARG $arg=//p" "$DOCKERFILE")"
  pin="${out%%$'\n'*}"
  if [ -z "$pin" ]; then
    echo "$arg: ARG not found in $DOCKERFILE"
    missing=1
    continue
  fi

  for arch in "${ARCHES[@]}"; do
    for pkg in $pkgs; do
      if grep -qxF "$pkg $pin" "$WORK/$arch.all"; then
        printf '%-18s %-7s %-18s %s\n' "$arg" "$arch" "$pkg" "ok ($pin)"
      elif [ -n "${unreadable[$arch]:-}" ]; then
        printf '%-18s %-7s %-18s %s\n' "$arg" "$arch" "$pkg" "UNKNOWN ($pin; an index was unreadable)"
        unknown=1
      else
        printf '%-18s %-7s %-18s %s\n' "$arg" "$arch" "$pkg" "MISSING ($pin not served)"
        missing=1
      fi
    done
  done

  # The candidate: a version listed for every package on every architecture. Each
  # (arch, package) set is deduplicated first so `uniq -c` counts set membership.
  sets=0
  for arch in "${ARCHES[@]}"; do
    for pkg in $pkgs; do
      sets=$((sets + 1))
      awk -v p="$pkg" '$1 == p {print $2}' "$WORK/$arch.all" | sort -u
    done
  done > "$WORK/versions"
  candidates="$(sort "$WORK/versions" | uniq -c | awk -v n="$sets" '$1 == n {print $2}' | sort -V)"
  candidate="${candidates##*$'\n'}"
  echo "$arg: pinned $pin; newest version served for all of [$pkgs] on [${ARCHES[*]}]: ${candidate:-none}"
done

[ "$missing" = 0 ] || exit 1
[ "$unknown" = 0 ] || exit 2
exit 0
