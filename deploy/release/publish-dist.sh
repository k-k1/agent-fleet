#!/usr/bin/env bash
# Agent Fleet — dist repo publish (docs/log/35 §35.7.4-1 / §35.4.2).
#
#   VERSION=0.1.0 deploy/release/publish-dist.sh [--dist-dir <d>] [--repo <o/r>] [--seed] [--dry-run]
#
# Publishes the artifacts in deploy/release/dist/ to the public dist repo's
# GitHub Releases:
#   - rootfs release `rootfs-<r>` … attaches R. If the tag exists, do nothing
#     (<r> is a content hash = identical bits; image-immutable releases avoid
#     re-downloads for users).
#   - app release `v<v>`          … attaches A / C (+ -bundle) / SHA256SUMS.
#                                     Images go to the registry, not here (ADR 0037).
#     An existing tag fails (releases are immutable — redo by bumping the version).
#     The body is rendered from deploy/release/notes/<v>.md (+ .ja.md) by
#     notes-body.sh; a missing notes file is a hard error.
# --seed pushes the dist repo contents (README.md / README.ja.md / CHANGELOG.md /
# CHANGELOG.ja.md / LICENSE / NOTICE / install.sh / install-compose.sh + the README
# screenshots under docs/img/) via the contents API (skipped when identical =
# idempotent). Creates the repo if it does not exist.
# Sources are deploy/release/dist-repo/, except LICENSE / NOTICE / docs/img which come
# from the repo root so the public copy matches the one bundled in the tars. The CHANGELOGs are
# generated — run gen-changelog.sh after adding the notes/index.tsv row, before publishing.
# Auth via gh (local = gh auth login / CI = GH_TOKEN set to DIST_PUBLISH_TOKEN).
# Runbook for a real publish: docs/log/35 §35.8.2.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

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

# ---- --seed: dist repo contents (README.md / install.sh / install-compose.sh) -----
if [ "$SEED" = 1 ]; then
  echo "==> [publish] seed dist repo contents ($REPO)"
  if ! gh repo view "$REPO" --json name >/dev/null 2>&1; then
    run gh repo create "$REPO" --public \
      --description "Agent Fleet — distribution artifacts (no source here)"
  fi
  # LICENSE / NOTICE are seeded from the repo root rather than a copy under
  # dist-repo/, so the public copy cannot drift from the one bundled in the tars.
  # NOTICE carries the primary-distribution URL that Apache-2.0 §4(d) makes
  # redistributors propagate, so the dist repo must show it too.
  # The README screenshots live in the repo's docs/img/ (regenerate with
  # console/scripts/shots — docs/log/35 §35.7.4-1) and are pushed under the same path,
  # so both READMEs can reference them relatively. Binary content rides the same
  # base64 contents API as the text files.
  shots=()
  for p in "$ROOT"/docs/img/*.webp; do
    [ -f "$p" ] && shots+=("docs/img/$(basename "$p")")
  done
  for f in README.md README.ja.md CHANGELOG.md CHANGELOG.ja.md LICENSE NOTICE \
           install.sh install-compose.sh ${shots[@]+"${shots[@]}"}; do
    case "$f" in
      LICENSE|NOTICE|docs/img/*) src="$ROOT/$f" ;;
      *)                         src="$HERE/dist-repo/$f" ;;
    esac
    # base64 and JSON body stay in files — screenshots exceed Linux
    # MAX_ARG_STRLEN (~128KiB per argv), so neither -f content= nor jq --arg
    # can carry the payload on the command line.
    b64f="$(mktemp)"; body="$(mktemp)"
    base64 -w0 < "$src" > "$b64f"
    local_b64="$(cat "$b64f")"
    resp="$(gh api "repos/$REPO/contents/$f" \
      --jq '.sha + " " + (.content | gsub("\n"; ""))' 2>/dev/null)" || resp=""
    sha="${resp%% *}"
    cur="${resp#* }"
    if [ -n "$resp" ] && [ "$cur" = "$local_b64" ]; then
      rm -f "$b64f" "$body"
      echo "    $f: unchanged (skipped)"
      continue
    fi
    if [ -n "$sha" ]; then
      jq -n --arg message "seed: $f" --arg sha "$sha" --rawfile content "$b64f" \
        '{message: $message, content: $content, sha: $sha}' > "$body"
    else
      jq -n --arg message "seed: $f" --rawfile content "$b64f" \
        '{message: $message, content: $content}' > "$body"
    fi
    if [ "$DRY" = 1 ]; then
      echo "DRY-RUN: gh api -X PUT repos/$REPO/contents/$f --input - (seed body)" >&2
    else
      gh api -X PUT "repos/$REPO/contents/$f" --input "$body" > /dev/null
    fi
    rm -f "$b64f" "$body"
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
# ADR 0037: the images tar is no longer published — images go to the registry.
for f in "agent-fleet-$VERSION.tar.gz" \
         "$C_NAME.tar.gz" "$C_NAME-bundle.tar.gz"; do
  p="$DIST/$f"
  [ -f "$p" ] || continue
  size="$(stat -c%s "$p")"
  if [ "$size" -ge "$GH_MAX_ASSET" ]; then
    echo "WARN: $f is ${size} bytes, over the 2GiB GitHub Releases asset limit — skipping" >&2
    echo "      (hand the file over out of band — docs/log/35 §35.2)" >&2
    continue
  fi
  assets+=("$p")
done
assets+=("$DIST/SHA256SUMS")

# Release notes come from deploy/release/notes/<v>.md (+ .ja.md) — see that dir's
# README. Rendering is a hard requirement: a release with no notes is a bug, so a
# missing notes file fails here rather than publishing a bare tag. The rendered
# body is written into the dist dir so it can be reviewed after the fact.
NOTES_BODY="$DIST/RELEASE_NOTES-$VERSION.md"
echo "==> [publish] render release notes"
VERSION="$VERSION" ROOTFS="$R_VER" REPO="$REPO" ARCH="$ARCH" \
  "$HERE/notes-body.sh" > "$NOTES_BODY"

echo "==> [publish] create release v$VERSION (${#assets[@]} assets)"
run gh release create "v$VERSION" -R "$REPO" \
  --title "agent-fleet $VERSION" \
  --notes-file "$NOTES_BODY" \
  "${assets[@]}"

cat <<EOF
==> [publish] done
  releases: https://github.com/$REPO/releases/tag/v$VERSION
            https://github.com/$REPO/releases/tag/rootfs-$R_VER
  install:  curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | bash
EOF
