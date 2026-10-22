#!/usr/bin/env bash
# Upload Kiro's device-flow credential DB as GitHub Actions secrets without
# printing it.  A single base64 value exceeds GitHub's 48 KiB secret cap, so the
# stream is split into eight independently redacted values and reassembled by
# .github/workflows/kiro-contract.yml.
#
# Run after `kiro-cli login` from a trusted machine that holds the dedicated CI
# account.  This deliberately updates only repository secrets; it never writes
# credentials into the working tree.  With more than one git remote, set
# GH_REPO=<owner>/<repo> so gh knows which repository's secrets to write.
set -euo pipefail

db="${KIRO_DB:-${HOME}/.local/share/kiro-cli/data.sqlite3}"
chunk_bytes=45000
max_chunks=8

[ -f "$db" ] || { echo "Kiro credential DB not found: $db" >&2; exit 1; }
command -v gh >/dev/null || { echo "gh is required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 is required" >&2; exit 1; }
gh auth status >/dev/null

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Upload a copy without the conversation and shell history tables: they hold the
# account's own chats, which the contract never reads and which must not become
# repository secrets.  They also dominate the file (measured: 300 KB of a 432 KB
# DB), and the full DB no longer fits in eight fragments.
python3 -I - "$db" "$tmp/data.sqlite3" <<'PY'
import sqlite3
import sys
import urllib.parse

src = sqlite3.connect("file:" + urllib.parse.quote(sys.argv[1]) + "?mode=ro", uri=True)
dst = sqlite3.connect(sys.argv[2])
src.backup(dst)
src.close()
tables = [r[0] for r in dst.execute("SELECT name FROM sqlite_master WHERE type = 'table'")]
if "auth_kv" not in tables:
    sys.exit("no auth_kv table: not a Kiro credential DB, or its schema changed")
for t in tables:
    if t == "history" or t.startswith("conversations"):
        dst.execute(f'DELETE FROM "{t}"')
dst.commit()
dst.execute("VACUUM")
dst.close()
PY

base64 -w 0 "$tmp/data.sqlite3" | split -b "$chunk_bytes" -d -a 2 - "$tmp/part-"

count="$(find "$tmp" -maxdepth 1 -type f -name 'part-*' | wc -l | tr -d ' ')"
[ "$count" -le "$max_chunks" ] || {
  echo "Kiro credential is too large for $max_chunks GitHub secrets; found $count chunks" >&2
  exit 1
}

# A trailing fragment left from a previous, larger DB is concatenated after the
# new ones and corrupts the stream, so its deletion must not fail silently.
existing="$(gh secret list --json name --jq '.[].name')"

for i in $(seq 1 "$max_chunks"); do
  part="$tmp/part-$(printf '%02d' "$((i - 1))")"
  name="E2E_KIRO_AUTH_DB_B64_${i}"
  if [ -f "$part" ]; then
    gh secret set "$name" < "$part"
  elif grep -qxF "$name" <<<"$existing"; then
    gh secret delete "$name"
  fi
done

echo "Kiro CI credential uploaded as $count encrypted secret fragment(s)."
