#!/usr/bin/env bash
# Forbidden-token gate over what we are about to ship (docs/log/35 §35.7.5).
#
#   deploy/release/scan-forbidden.sh [path...]      # default: deploy/release/dist
#
# Expands every artifact — tar / gzip / zstd / xz / bzip2 / zip, recursively,
# down to the individual layers inside a `docker save` tarball — and fails if any
# byte of any of them contains a string this project removed from its history.
#
# This exists because of a real escape: a `?raw` CSS import put a source comment
# verbatim into the Console bundle, minification left string literals alone, and
# the comment shipped inside every image and native tarball of 0.1.0–0.5.0. No
# source-level check could have seen it — only looking at the artifact could.
#
# Exit 0 clean, 1 a forbidden token is in the shipping set, 2 the check could not
# run (also a failure: a gate that could not run has not passed).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

paths=("$@")
if [ "${#paths[@]}" -eq 0 ]; then
  paths=("$HERE/dist")
fi

# Built rather than `go run`: the scanner is its own module, and building it into
# a temp dir keeps the caller's working directory (which is what the paths are
# relative to) untouched.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
(cd "$HERE/scan" && go build -o "$tmp/scan" .)

"$tmp/scan" \
  --ledger "$HERE/forbidden.sha256" \
  --allow "$HERE/forbidden.allow" \
  "${paths[@]}"
