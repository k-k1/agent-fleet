#!/usr/bin/env bash
# Make the Markdown files of a release archive readable where they are unpacked.
#
#   deploy/release/pin-md-links.sh pin   <bundle-dir> <ref> <docs-dir|-> <file>:<repo-path>...
#   deploy/release/pin-md-links.sh check <bundle-dir>
#
# A README copied into a release bundle keeps its repository-relative links, and most of
# them (the guide pages it indexes, scripts and docs elsewhere in the tree) are not in
# the bundle. `pin` rewrites each prose link of the named files that does not resolve
# inside the bundle: a link into guide/ goes to the staged guide when the bundle carries
# one (<docs-dir>, e.g. `docs` in the native tar), anything else to its GitHub URL at
# <ref> (the release tag), so the reader lands on the text of the version they unpacked.
# `check` fails on any relative prose link in the bundle's *.md files that does not
# resolve; run it on the unpacked archive. Links inside code are left alone
# (md-links.awk).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
REPO_URL="https://github.com/k-k1/agent-fleet"
MD_AWK="$HERE/md-links.awk"
md_links() { awk -v mode=links -f "$MD_AWK" "$1"; }
md_rewrite() { awk -v mode=rewrite -v map="$2" -f "$MD_AWK" "$1" > "$1.tmp" && mv "$1.tmp" "$1"; }
relative() { case "$1" in http://*|https://*|mailto:*|\#*|/*) return 1 ;; esac; }

cmd="${1:?usage: pin-md-links.sh pin|check <bundle-dir> ...}"
B="$(cd "${2:?bundle dir}" && pwd)"
case "$cmd" in
  pin)
    ref="${3:?ref}"; docs="${4:?docs dir or -}"; shift 4
    for spec in "$@"; do
      f="$B/${spec%%:*}"; src="${spec#*:}"
      [ -f "$f" ] || { echo "ERROR: $f is not in the bundle" >&2; exit 1; }
      map="$(mktemp)"
      while IFS= read -r t; do
        relative "$t" || continue
        path="${t%%#*}"; frag=""
        [ "$path" = "$t" ] || frag="#${t#*#}"
        [ -e "$(dirname "$f")/$path" ] && continue
        resolved="$(realpath -m --relative-to="$ROOT" "$ROOT/$(dirname "$src")/$path")"
        new="$REPO_URL/blob/$ref/$resolved"
        if [ "$docs" != - ] && [ "${resolved#guide/}" != "$resolved" ] && [ -e "$B/$docs/${resolved#guide/}" ]; then
          new="$(realpath -m --relative-to="$(dirname "$f")" "$B/$docs/${resolved#guide/}")"
        fi
        printf '%s\t%s\n' "$t" "$new$frag" >> "$map"
      done < <(md_links "$f" | sort -u)
      md_rewrite "$f" "$map"
      rm -f "$map"
    done
    ;;
  check)
    dead=0
    while IFS= read -r -d "" f; do
      while IFS= read -r t; do
        relative "$t" || continue
        [ -e "$(dirname "$f")/${t%%#*}" ] || {
          echo "ERROR: ${f#"$B"/} links to '$t', which is not in the archive" >&2
          dead=1
        }
      done < <(md_links "$f")
    done < <(find "$B" -name "*.md" -print0)
    [ "$dead" = 0 ] || exit 1
    ;;
  *) echo "usage: pin-md-links.sh pin|check <bundle-dir> ..." >&2; exit 1 ;;
esac
