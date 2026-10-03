#!/usr/bin/env bash
# Agent Fleet — compose a GitHub release body from the checked-in release notes.
#
#   VERSION=0.3.0 ROOTFS="amd64=0acd1112b7b0 arm64=5c0ffee00000" deploy/release/notes-body.sh > body.md
#
# ROOTFS lists <arch>=<r> for every native package in the release; a bare <r>
# means amd64 alone (the older single-architecture releases).
#
# Reads deploy/release/notes/<version>.md (English, canonical) and, when present,
# deploy/release/notes/<version>.ja.md (Japanese), and appends an artifact footer.
# The footer is generated here rather than stored in the notes because <r> (the
# rootfs content hash) is only known at build time.
#
# Used by publish-dist.sh; also usable standalone to re-render the body of an
# already published release (`gh release edit v<v> --notes-file -`).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

VERSION="${VERSION:?set VERSION=<semver> (e.g. VERSION=0.3.0)}"
ROOTFS="${ROOTFS:?set ROOTFS=<arch>=<rootfs content hash> ... (e.g. ROOTFS=amd64=0acd1112b7b0)}"
REPO="${REPO:-k-k1/agent-fleet-dist}"
# Registry the compose edition pulls from (ADR 0037).
IMAGE_BASE="${IMAGE_BASE:-ghcr.io/k-k1/agent-fleet}"
# NOTES_DIR is overridable so the stub test can point at a fixture instead of
# needing a notes file checked in for its fake version.
NOTES_DIR="${NOTES_DIR:-$HERE/notes}"

EN="$NOTES_DIR/$VERSION.md"
JA="$NOTES_DIR/$VERSION.ja.md"
[ -f "$EN" ] || { echo "ERROR: release notes not found: $EN
  Write them before publishing (English is canonical; add $VERSION.ja.md for Japanese)." >&2; exit 1; }

# A release body is not rendered like a Markdown file: GitHub turns every newline in it into
# a <br>, so the notes' hard wrap at ~100 columns showed as ragged lines and each issue link
# on a line of its own. Join the lines of a paragraph or list item; a blank line, a list
# marker, a heading, a quote, a table row, a rule, an HTML line or a code fence starts a new
# block, and fenced code is left alone. Japanese takes no space where it joins after a
# non-ASCII character and before another, or after full-width punctuation.
unwrap() {
  LC_ALL=C awk '
    function flush() { if (have) print buf; have = 0; buf = "" }
    function opens(s) { return s ~ /^[ \t]*([-*+]|[0-9]+[.)])[ \t]/ || s ~ /^[ \t]*(#|>|\||---|```|<)/ }
    function wide_punct(s,  t) { t = substr(s, length(s) - 2); return index(PUNCT, "/" t "/") > 0 }
    BEGIN { PUNCT = "/、/。/，/．/）/」/』/】/：/；/！/？/" }
    {
      if ($0 ~ /^[ \t]*```/) { flush(); print; fence = !fence; next }
      if (fence) { print; next }
      if ($0 ~ /^[ \t]*$/) { flush(); print; next }
      if (have && !block && !opens($0)) {
        cur = $0; sub(/^[ \t]+/, "", cur)
        sep = ((buf ~ /[\200-\377]$/ && cur ~ /^[\200-\377]/) || wide_punct(buf)) ? "" : " "
        buf = buf sep cur; next
      }
      flush(); buf = $0; have = 1
      block = ($0 ~ /^[ \t]*(#|\||---|<)/)
    }
    END { flush() }
  ' "$1"
}

unwrap "$EN"

if [ -f "$JA" ]; then
  printf '\n---\n\n## 日本語\n\n'
  unwrap "$JA"
fi

natives="" rootfs=""
for pair in $ROOTFS; do
  case "$pair" in *=*) arch="${pair%%=*}"; r="${pair#*=}" ;; *) arch=amd64; r="$pair" ;; esac
  natives+=" · \`agent-fleet-native-$VERSION-linux-$arch.tar.gz\` (native, $arch)"
  rootfs+="${rootfs:+ · }\`agent-fleet-rootfs-$r-linux-$arch.tar.zst\`"
done

cat <<EOF

---

**Install (native)** — \`curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | bash\` then \`af start\`

**Assets** — \`agent-fleet-$VERSION.tar.gz\` (Compose bundle)$natives · \`SHA256SUMS\`

**Container images** — \`$IMAGE_BASE/control-plane:$VERSION\` and \`$IMAGE_BASE/workspace:$VERSION\` (pulled by \`docker compose\`; the bundle's \`.env.example\` already points at them)

**Workspace rootfs** — $rootfs, fetched by \`af start\` on first start from the URL in the native tar's \`rootfs.json\` and verified against its sha256. Verify every download against \`SHA256SUMS\`.
EOF
