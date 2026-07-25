#!/usr/bin/env bash
# Agent Fleet — generate the dist repo's CHANGELOG.md / CHANGELOG.ja.md.
#
#   deploy/release/gen-changelog.sh [--check]
#
# Reads deploy/release/notes/index.tsv (the published release ledger) plus each
# version's notes, and writes an index into deploy/release/dist-repo/, from where
# publish-dist.sh --seed pushes it. Only the summary (the notes' first paragraph)
# goes in — the full notes live on each release page, so there is one copy of them.
# --check verifies the checked-in files are up to date instead of writing (CI).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

REPO="${REPO:-k-k1/agent-fleet-dist}"
NOTES_DIR="$HERE/notes"
OUT_DIR="$HERE/dist-repo"
INDEX="$NOTES_DIR/index.tsv"
CHECK=0
[ "${1:-}" = "--check" ] && CHECK=1

# First paragraph of a notes file = the summary line(s) up to the first blank line.
summary() { awk 'NF==0 {exit} {print}' "$1"; }

emit() { # emit <lang-suffix> <title> <intro>
  local sfx="$1" title="$2" intro="$3"
  printf '# %s\n\n%s\n' "$title" "$intro"
  # newest first
  tac "$INDEX" | while IFS=$'\t' read -r v date _commit; do
    case "$v" in ''|\#*) continue ;; esac
    local f="$NOTES_DIR/$v$sfx.md"
    [ -f "$f" ] || f="$NOTES_DIR/$v.md"
    printf '\n## [%s](https://github.com/%s/releases/tag/v%s) — %s\n\n' \
      "$v" "$REPO" "$v" "$date"
    summary "$f"
  done
}

write_or_check() { # write_or_check <path> <content-file>
  local dest="$1" tmp="$2"
  if [ "$CHECK" = 1 ]; then
    diff -u "$dest" "$tmp" >/dev/null 2>&1 && { echo "ok: $(basename "$dest") up to date"; return 0; }
    echo "ERROR: $(basename "$dest") is stale — run deploy/release/gen-changelog.sh" >&2
    diff -u "$dest" "$tmp" >&2 || true
    return 1
  fi
  mv "$tmp" "$dest"
  echo "wrote $(basename "$dest")"
}

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

emit "" "Changelog" \
"Release notes index for [Agent Fleet](https://github.com/$REPO). Each entry links
to the release, where the full notes are. 日本語は [CHANGELOG.ja.md](CHANGELOG.ja.md)。" \
  > "$TMP/CHANGELOG.md"

emit ".ja" "変更履歴" \
"[Agent Fleet](https://github.com/$REPO) のリリースノート索引です。各項目はリリース
ページへのリンクで、完全なノートはそちらにあります。English: [CHANGELOG.md](CHANGELOG.md)." \
  > "$TMP/CHANGELOG.ja.md"

rc=0
write_or_check "$OUT_DIR/CHANGELOG.md" "$TMP/CHANGELOG.md" || rc=1
write_or_check "$OUT_DIR/CHANGELOG.ja.md" "$TMP/CHANGELOG.ja.md" || rc=1
exit $rc
