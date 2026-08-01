#!/usr/bin/env bash
# Agent Fleet — compose a GitHub release body from the checked-in release notes.
#
#   VERSION=0.3.0 ROOTFS=0acd1112b7b0 deploy/release/notes-body.sh > body.md
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
ROOTFS="${ROOTFS:?set ROOTFS=<rootfs content hash> (e.g. ROOTFS=0acd1112b7b0)}"
REPO="${REPO:-k-k1/agent-fleet-dist}"
ARCH="${ARCH:-amd64}"
# Registry the compose edition pulls from (ADR 0037).
IMAGE_BASE="${IMAGE_BASE:-ghcr.io/k-k1/agent-fleet}"
# NOTES_DIR is overridable so the stub test can point at a fixture instead of
# needing a notes file checked in for its fake version.
NOTES_DIR="${NOTES_DIR:-$HERE/notes}"

EN="$NOTES_DIR/$VERSION.md"
JA="$NOTES_DIR/$VERSION.ja.md"
[ -f "$EN" ] || { echo "ERROR: release notes not found: $EN
  Write them before publishing (English is canonical; add $VERSION.ja.md for Japanese)." >&2; exit 1; }

cat "$EN"

if [ -f "$JA" ]; then
  printf '\n---\n\n## 日本語\n\n'
  cat "$JA"
fi

cat <<EOF

---

**Install (native)** — \`curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | bash\` then \`af start\`

**Assets** — \`agent-fleet-$VERSION.tar.gz\` (Compose bundle) · \`agent-fleet-native-$VERSION-linux-$ARCH.tar.gz\` (native) · \`SHA256SUMS\`

**Container images** — \`$IMAGE_BASE/control-plane:$VERSION\` and \`$IMAGE_BASE/workspace:$VERSION\` (pulled by \`docker compose\`; the bundle's \`.env.example\` already points at them)

**Workspace rootfs** — [\`rootfs-$ROOTFS\`](https://github.com/$REPO/releases/tag/rootfs-$ROOTFS), fetched on first start and verified against the \`rootfs.json\` sha256 inside the native tar. Verify every download against \`SHA256SUMS\`.
EOF
