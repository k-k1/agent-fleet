#!/usr/bin/env bash
# Agent Fleet — dist repo publish (docs/35 §35.7.4-1 / §35.4.2).
#
#   VERSION=0.1.0 deploy/release/publish-dist.sh [--dist-dir <d>] [--repo <o/r>] [--seed] [--dry-run]
#
# Publishes the artifacts in deploy/release/dist/ to the public dist repo's
# GitHub Releases:
#   - rootfs release `rootfs-<r>` … attaches R. If the tag exists, do nothing
#     (<r> is a content hash = identical bits; image-immutable releases avoid
#     re-downloads for users).
#   - app release `v<v>`          … attaches A / B / C (+ -bundle) / SHA256SUMS.
#     An existing tag fails (releases are immutable — redo by bumping the version).
# --seed pushes the dist repo contents (README.md / install.sh) from
# deploy/release/dist-repo/ via the contents API (skipped when identical =
# idempotent). Creates the repo if it does not exist.
# Auth via gh (local = gh auth login / CI = GH_TOKEN set to DIST_PUBLISH_TOKEN).
# Runbook for a real publish: docs/35 §35.8.2.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

VERSION="${VERSION:?set VERSION=<semver> (e.g. VERSION=0.1.0)}"
REPO="k-k1/agent-fleet-dist"
DIST="$HERE/dist"
SEED=0
DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dist-dir) DIST="${2:?--dist-dir needs a path}"; shift ;;
    --repo)     REPO="${2:?--repo needs owner/repo}"; shift ;;
    --seed)     SEED=1 ;;
    --dry-run)  DRY=1 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

die() { echo "ERROR: $*" >&2; exit 1; }
# Gate only mutating gh calls (--dry-run prints them instead). Read-only calls
# run directly.
run() {
  if [ "$DRY" = 1 ]; then echo "DRY-RUN: $*" >&2; else "$@"; fi
}

# GitHub Releases per-asset limit (2GiB). Oversized assets cannot be attached —
# the air-gap path for B is file hand-off (§35.2), so warn and skip.
GH_MAX_ASSET=2147483648

ARCH=amd64
C_NAME="agent-fleet-native-$VERSION-linux-$ARCH"
C_TAR="$DIST/$C_NAME.tar.gz"
[ -f "$C_TAR" ] || die "$C_TAR not found (run VERSION=$VERSION build.sh --native first)"
[ -f "$DIST/SHA256SUMS" ] || die "$DIST/SHA256SUMS not found (build.sh generates it)"

# ---- read rootfs.json inside C (same sed parser as the af launcher) ----------------
MANIFEST="$(tar xzf "$C_TAR" -O "$C_NAME/rootfs.json")"
mget() { sed -n 's/.*"'"$1"'": *"\{0,1\}\([^",}]*\)"\{0,1\}.*/\1/p' <<<"$MANIFEST" | head -1; }
R_VER="$(mget version)"
R_SHA="$(mget sha256)"
R_URL="$(mget url)"
if [ -z "$R_VER" ] || [ -z "$R_SHA" ]; then die "cannot read rootfs.json inside C"; fi
R_NAME="agent-fleet-rootfs-$R_VER-linux-$ARCH.tar.zst"

# The rootfs URL referenced by C must point at this repo's Releases. Publishing a
# C that disagrees would make users' `af start` hit a missing/foreign URL.
WANT_URL="https://github.com/$REPO/releases/download/rootfs-$R_VER/$R_NAME"
[ "$R_URL" = "$WANT_URL" ] || die "the rootfs URL in C does not match the publish target.
  rootfs.json: $R_URL
  expected:    $WANT_URL
  (a C built with a different ROOTFS_URL_BASE cannot be published to this repo)"

# ---- --seed: dist repo contents (README.md / install.sh) ---------------------------
if [ "$SEED" = 1 ]; then
  echo "==> [publish] seed dist repo contents ($REPO)"
  if ! gh repo view "$REPO" --json name >/dev/null 2>&1; then
    run gh repo create "$REPO" --public \
      --description "Agent Fleet — distribution artifacts (no source here)"
  fi
  for f in README.md install.sh; do
    local_b64="$(base64 -w0 < "$HERE/dist-repo/$f")"
    resp="$(gh api "repos/$REPO/contents/$f" \
      --jq '.sha + " " + (.content | gsub("\n"; ""))' 2>/dev/null)" || resp=""
    sha="${resp%% *}"
    cur="${resp#* }"
    if [ -n "$resp" ] && [ "$cur" = "$local_b64" ]; then
      echo "    $f: unchanged (skipped)"
      continue
    fi
    put=(-X PUT "repos/$REPO/contents/$f" -f "message=seed: $f" -f "content=$local_b64")
    if [ -n "$sha" ]; then put+=(-f "sha=$sha"); fi
    run gh api "${put[@]}" > /dev/null
    echo "    $f: pushed"
  done
fi

# ---- rootfs release ----------------------------------------------------------------
if gh release view "rootfs-$R_VER" -R "$REPO" >/dev/null 2>&1; then
  echo "==> [publish] rootfs-$R_VER already exists — reusing (no upload)"
else
  R_TAR="$DIST/$R_NAME"
  [ -f "$R_TAR" ] || die "tag rootfs-$R_VER does not exist and R is not available locally: $R_TAR
  (a --rootfs-json reuse build is only valid against an already published <r>)"
  echo "==> [publish] verify rootfs (sha256)"
  echo "$R_SHA  $R_TAR" | sha256sum -c - >/dev/null \
    || die "sha256 of R does not match rootfs.json inside C: $R_TAR"
  echo "==> [publish] create release rootfs-$R_VER"
  run gh release create "rootfs-$R_VER" -R "$REPO" \
    --title "rootfs $R_VER (linux-$ARCH)" \
    --notes "workspace rootfs (content hash $R_VER). Referenced by the app release's native tar; not for standalone use." \
    "$R_TAR"
fi

# ---- app release -------------------------------------------------------------------
if gh release view "v$VERSION" -R "$REPO" >/dev/null 2>&1; then
  die "v$VERSION already exists (releases are immutable — bump the version and retry)"
fi
assets=()
for f in "agent-fleet-$VERSION.tar.gz" "agent-fleet-images-$VERSION.tar.gz" \
         "$C_NAME.tar.gz" "$C_NAME-bundle.tar.gz"; do
  p="$DIST/$f"
  [ -f "$p" ] || continue
  size="$(stat -c%s "$p")"
  if [ "$size" -ge "$GH_MAX_ASSET" ]; then
    echo "WARN: $f is ${size} bytes, over the 2GiB GitHub Releases asset limit — skipping" >&2
    echo "      (distribute the air-gap tar via file hand-off — docs/35 §35.2)" >&2
    continue
  fi
  assets+=("$p")
done
assets+=("$DIST/SHA256SUMS")
echo "==> [publish] create release v$VERSION (${#assets[@]} assets)"
run gh release create "v$VERSION" -R "$REPO" \
  --title "agent-fleet $VERSION" \
  --notes "Agent Fleet $VERSION. Native: install.sh (see README). Compose: agent-fleet-$VERSION.tar.gz. Rootfs: rootfs-$R_VER." \
  "${assets[@]}"

cat <<EOF
==> [publish] done
  releases: https://github.com/$REPO/releases/tag/v$VERSION
            https://github.com/$REPO/releases/tag/rootfs-$R_VER
  install:  curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | bash
EOF
