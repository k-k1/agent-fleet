#!/usr/bin/env bash
# Upload Kiro's device-flow credential DB as GitHub Actions secrets without
# printing it.  A single base64 value exceeds GitHub's 48 KiB secret cap, so the
# stream is split into eight independently redacted values and reassembled by
# .github/workflows/kiro-contract.yml.
#
# Run after `kiro-cli login` from a trusted machine that holds the dedicated CI
# account.  This deliberately updates only repository secrets; it never writes
# credentials into the working tree.
set -euo pipefail

db="${HOME}/.local/share/kiro-cli/data.sqlite3"
chunk_bytes=45000
max_chunks=8

[ -f "$db" ] || { echo "Kiro credential DB not found: $db" >&2; exit 1; }
command -v gh >/dev/null || { echo "gh is required" >&2; exit 1; }
gh auth status >/dev/null

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
base64 -w 0 "$db" | split -b "$chunk_bytes" -d -a 1 - "$tmp/part-"

count="$(find "$tmp" -maxdepth 1 -type f -name 'part-*' | wc -l | tr -d ' ')"
[ "$count" -le "$max_chunks" ] || {
  echo "Kiro credential is too large for $max_chunks GitHub secrets; found $count chunks" >&2
  exit 1
}

for i in $(seq 1 "$max_chunks"); do
  part="$tmp/part-$((i - 1))"
  name="E2E_KIRO_AUTH_DB_B64_${i}"
  if [ -f "$part" ]; then
    gh secret set "$name" < "$part"
  else
    # Clear stale trailing fragments from a previous, larger credential DB.
    gh secret set "$name" --body ''
  fi
done

echo "Kiro CI credential uploaded as $count encrypted secret fragment(s)."
