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
# The pins are read out of the Dockerfile itself: every `pkg=${ARG}`, `pkg=$ARG` or
# `pkg=<version>` argument of an `apt-get install` / `apt install` instruction. A pin
# whose version is any other shell expression cannot be resolved here and fails the run
# rather than being skipped.
#
# A version is "served" for an architecture when any suite the image's apt sources name
# (trixie, trixie-updates, trixie-security; node:22-trixie-slim ships those three) lists
# it for that architecture (or `all`). An index that cannot be read is never taken as
# "served" or as "missing", and it makes the whole run incomplete: the newest version the
# output names may be stale.
#
# The output also names the newest version (dpkg ordering) every package of a pin is
# served at on every architecture — the value to bump the ARG to.
#
# Usage:
#   deploy/local/apt-pin-check.sh
# Environment (tests point these at fixtures):
#   APT_PIN_DOCKERFILE    default workspace/Dockerfile
#   APT_PIN_DEBIAN_URL    default https://deb.debian.org/debian
#   APT_PIN_SECURITY_URL  default https://deb.debian.org/debian-security
#   APT_PIN_ARCHES        default "amd64 arm64" (dev-image.yml / release.sh platforms)
# Exit codes: 0 = every pin served on every architecture, every index read / 1 = a pin is
# missing for at least one architecture, or cannot be resolved / 2 = nothing missing, but
# some index could not be read
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKERFILE="${APT_PIN_DOCKERFILE:-$ROOT/workspace/Dockerfile}"
DEBIAN_URL="${APT_PIN_DEBIAN_URL:-https://deb.debian.org/debian}"
SECURITY_URL="${APT_PIN_SECURITY_URL:-https://deb.debian.org/debian-security}"
read -r -a ARCHES <<< "${APT_PIN_ARCHES:-amd64 arm64}"

# base URL|suite
SUITES=(
  "$DEBIAN_URL|trixie"
  "$DEBIAN_URL|trixie-updates"
  "$SECURITY_URL|trixie-security"
)

CURL_RETRY=(--retry 3 --retry-delay 1 --retry-all-errors)

# Picking the newest version needs Debian's ordering (epochs, `~`, letters after digits);
# `sort -V` gets all three wrong.
command -v dpkg > /dev/null || { echo "apt-pin-check: dpkg is required for version ordering" >&2; exit 2; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Prints "<key> <package>" per apt pin, in order of appearance: key is `$NAME` for a
# version taken from ARG NAME and `=<version>` for a literal one, and
# "UNSUPPORTED <token>" for a version this script cannot resolve. Continuation lines are
# joined first and comment lines inside them dropped, as the Dockerfile parser does.
extract_pins() {
  awk '
    /^[[:space:]]*#/ { next }
    {
      line = $0
      cont = sub(/\\[[:space:]]*$/, "", line)
      buf = buf " " line
      if (!cont) { print buf; buf = "" }
    }
    END { if (buf != "") print buf }
  ' "$DOCKERFILE" | awk '
    {
      # One shell command at a time, so a `pip install x==1` chained after an
      # `apt-get update` is never read as an apt argument.
      ncmd = split($0, cmd, /&&|\|\||[;|()]/)
      for (c = 1; c <= ncmd; c++) {
        if (!match(cmd[c], /(^|[[:space:]])apt(-get)?([[:space:]]+-[^[:space:]]+)*[[:space:]]+install([[:space:]]|$)/)) continue
        rest = substr(cmd[c], RSTART + RLENGTH)
        gsub(/["'\'']/, "", rest)
        n = split(rest, tok, /[[:space:]]+/)
        for (i = 1; i <= n; i++) {
          t = tok[i]
          if (t !~ /^[a-z0-9][a-z0-9.+-]*=/) continue
          eq = index(t, "=")
          pkg = substr(t, 1, eq - 1); ver = substr(t, eq + 1)
          if (ver ~ /^\$\{[A-Za-z_][A-Za-z0-9_]*\}$/) key = "$" substr(ver, 3, length(ver) - 3)
          else if (ver ~ /^\$[A-Za-z_][A-Za-z0-9_]*$/) key = "$" substr(ver, 2)
          else if (ver ~ /^[0-9]/) key = "=" ver
          else { print "UNSUPPORTED " t; continue }
          if (!seen[key " " pkg]++) print key, pkg
        }
      }
    }
  '
}

# Writes "<package> <version>" lines for one suite/arch to $WORK/<arch>.<n>.pv, or
# returns 1. Paragraphs are read whole, in any field order, and only those built for
# <arch> or `all` count. Fetch and decompress are separate steps so a truncated download
# is a failure, not an index that happens to lack the package.
fetch_index() { # fetch_index <arch> <n> <base> <suite>
  local xz="$WORK/$1.$2.xz"
  curl -fsSL --max-time 60 "${CURL_RETRY[@]}" -o "$xz" \
    "$3/dists/$4/main/binary-$1/Packages.xz" 2>/dev/null || return 1
  xz -dc "$xz" > "$WORK/$1.$2.txt" 2>/dev/null || return 1
  awk -v want="$1" '
    BEGIN { RS = ""; FS = "\n" }
    {
      p = v = a = ""
      for (i = 1; i <= NF; i++) {
        if ($i ~ /^Package:/) { p = $i; sub(/^Package:[[:space:]]*/, "", p) }
        else if ($i ~ /^Version:/) { v = $i; sub(/^Version:[[:space:]]*/, "", v) }
        else if ($i ~ /^Architecture:/) { a = $i; sub(/^Architecture:[[:space:]]*/, "", a) }
      }
      if (p != "" && v != "" && (a == want || a == "all")) print p, v
    }
  ' "$WORK/$1.$2.txt" > "$WORK/$1.$2.pv"
}

# Newest of the versions on stdin, by dpkg ordering.
newest() {
  local best="" v
  while read -r v; do
    [ -n "$v" ] || continue
    if [ -z "$best" ] || dpkg --compare-versions "$v" gt "$best"; then best="$v"; fi
  done
  printf '%s' "$best"
}

missing=0
pins="$(extract_pins)"
unsupported="$(grep '^UNSUPPORTED ' <<< "$pins")"
if [ -n "$unsupported" ]; then
  while read -r _ tok; do
    echo "UNSUPPORTED pin '$tok': its version is not \${ARG}, \$ARG or a literal"
  done <<< "$unsupported"
  missing=1
fi
pins="$(grep -v '^UNSUPPORTED ' <<< "$pins")"
if [ -z "$pins" ]; then
  echo "No apt version pins in $DOCKERFILE."
  [ "$missing" = 0 ] || exit 1
  exit 0
fi

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

# One group per key, packages in order of appearance.
keys="$(awk '!seen[$1]++ {print $1}' <<< "$pins")"
while read -r key; do
  pkgs="$(awk -v k="$key" '$1 == k {printf "%s%s", sep, $2; sep = " "}' <<< "$pins")"
  if [ "${key:0:1}" = "=" ]; then
    label="literal"
    pin="${key:1}"
  else
    label="${key:1}"
    out="$(sed -n "s/^ARG $label=//p" "$DOCKERFILE")"
    pin="${out%%$'\n'*}"
    if [ -z "$pin" ]; then
      echo "$label: ARG not found in $DOCKERFILE (used by $pkgs)"
      missing=1
      continue
    fi
  fi

  for arch in "${ARCHES[@]}"; do
    for pkg in $pkgs; do
      if grep -qxF "$pkg $pin" "$WORK/$arch.all"; then
        printf '%-18s %-7s %-18s %s\n' "$label" "$arch" "$pkg" "ok ($pin)"
      elif [ -n "${unreadable[$arch]:-}" ]; then
        printf '%-18s %-7s %-18s %s\n' "$label" "$arch" "$pkg" "UNKNOWN ($pin; an index was unreadable)"
      else
        printf '%-18s %-7s %-18s %s\n' "$label" "$arch" "$pkg" "MISSING ($pin not served)"
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
  candidate="$(sort "$WORK/versions" | uniq -c | awk -v n="$sets" '$1 == n {print $2}' | newest)"
  echo "$label: pinned $pin; newest version served for all of [$pkgs] on [${ARCHES[*]}]: ${candidate:-none}"
done <<< "$keys"

if [ "${#unreadable[@]}" -gt 0 ]; then
  echo "Incomplete: an index could not be read for [${!unreadable[*]}]; the newest versions above may be stale."
fi
[ "$missing" = 0 ] || exit 1
[ "${#unreadable[@]}" = 0 ] || exit 2
exit 0
