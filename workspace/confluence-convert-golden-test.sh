#!/usr/bin/env bash
# Markdown <-> storage round trip of the pinned confluence-cli on CJK text, against goldens.
#
#   AF_CONFLUENCE_REAL=/path/to/confluence workspace/confluence-convert-golden-test.sh
#
# The page commands send what `confluence convert` produces, with no conversion of ours in
# between, so a CLI bump that regresses emphasis next to Japanese or Korean (`**「括弧」**の`,
# `**(주)**는` left as literal asterisks) would reach every page an agent writes. Each golden
# pins the Markdown -> storage output and that storage -> Markdown gives the input back.
# `convert` is local: no connection, no token.
#
# With no CLI at AF_CONFLUENCE_REAL (default: the image's /usr/local/libexec/confluence) the test
# says so and exits 0, so it can run anywhere; CI installs the pinned version first.
set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLI="${AF_CONFLUENCE_REAL:-/usr/local/libexec/confluence}"
DATA="$HERE/testdata/confluence"

if [ ! -x "$CLI" ]; then
  echo "confluence-convert-golden-test: skipped (no confluence CLI at $CLI)"
  exit 0
fi

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
fails=0
fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
# The CLI's two directions disagree on a final newline; compare with trailing newlines removed.
norm() { printf '%s\n' "$(cat "$1")"; }

n=0
for md in "$DATA"/*.md; do
  name="$(basename "$md" .md)"
  n=$((n + 1))
  if ! "$CLI" convert --input-format markdown --output-format storage -i "$md" >"$T/$name.storage" 2>"$T/err"; then
    fail "$name: markdown -> storage failed: $(cat "$T/err")"
    continue
  fi
  if ! diff <(norm "$DATA/$name.storage") <(norm "$T/$name.storage") >"$T/diff"; then
    fail "$name: storage differs from the golden:"$'\n'"$(cat "$T/diff")"
  fi
  if grep -q '\*\*' "$T/$name.storage"; then
    fail "$name: literal ** left in storage"
  fi
  if ! "$CLI" convert --input-format storage --output-format markdown -i "$DATA/$name.storage" >"$T/$name.md" 2>"$T/err"; then
    fail "$name: storage -> markdown failed: $(cat "$T/err")"
    continue
  fi
  if ! diff <(norm "$md") <(norm "$T/$name.md") >"$T/diff"; then
    fail "$name: round trip does not give the Markdown back:"$'\n'"$(cat "$T/diff")"
  fi
done

[ "$n" -gt 0 ] || fail "no goldens under $DATA"
if [ "$fails" -gt 0 ]; then
  echo "confluence-convert-golden-test: $fails failure(s)" >&2
  exit 1
fi
echo "confluence-convert-golden-test: ok ($n goldens, $("$CLI" --version))"
