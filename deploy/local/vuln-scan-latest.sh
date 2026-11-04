#!/usr/bin/env bash
# Print the latest published release version of the dist repo: the highest vX.Y.Z of the
# non-draft, non-prerelease releases. Fails (non-zero, nothing on stdout) when it cannot tell.
#
#   DIST_REPO=k-k1/agent-fleet-dist GH_TOKEN=... vuln-scan-latest.sh
set -euo pipefail
v="$(gh api --paginate "repos/${DIST_REPO:?}/releases" \
  --jq '.[] | select((.draft | not) and (.prerelease | not)) | .tag_name' \
  | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sed 's/^v//' | sort -V | tail -1)"
[[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "could not resolve the latest release (got '$v')" >&2; exit 1; }
echo "$v"
