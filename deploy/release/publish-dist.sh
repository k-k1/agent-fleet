#!/usr/bin/env bash
# Agent Fleet — dist repo publish (docs/log/35 §35.7.4-1 / §35.4.2).
#
#   VERSION=0.1.0 deploy/release/publish-dist.sh [--dist-dir <d>] [--repo <o/r>] [--seed] [--dry-run]
#
# Publishes the artifacts in deploy/release/dist/ to the public dist repo's
# GitHub Releases as ONE release `v<v>`: A, C (+ -bundle) and R for every
# architecture present (amd64 is required, arm64 joins when built), plus a
# SHA256SUMS that lists exactly the attached assets. Images go to the registry,
# not here (ADR 0037). An existing tag fails (releases are immutable — redo by
# bumping the version).
# Older releases carried R in a separate `rootfs-<r>` release. Those stay
# published forever — every C of those versions points at them — but no new one is
# created (docs/log/35 §35.4.2). A C whose rootfs.json names an R in another release of this repo
# (a --rootfs-json reuse build) is accepted once that release is confirmed to hold it.
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

# ---- native packages: one C per architecture, each naming its own R ----------------
# amd64 is required: install.sh and `af update` on the common host fetch it, and a
# release without it would strand them.
[ -f "$DIST/agent-fleet-native-$VERSION-linux-amd64.tar.gz" ] \
  || die "$DIST/agent-fleet-native-$VERSION-linux-amd64.tar.gz not found (run VERSION=$VERSION build.sh --native first)"
[ -f "$DIST/SHA256SUMS" ] || die "$DIST/SHA256SUMS not found (build.sh generates it)"

URL_BASE="https://github.com/$REPO/releases/download"
ARCHES=()        # architectures with a C, in a fixed order
ROOTFS_LIST=()   # <arch>=<r>, for the release notes
R_ASSETS=()      # R files attached to this release
mget() { sed -n 's/.*"'"$1"'": *"\{0,1\}\([^",}]*\)"\{0,1\}.*/\1/p' <<<"$MANIFEST" | head -1; }
for arch in amd64 arm64; do
  C_NAME="agent-fleet-native-$VERSION-linux-$arch"
  C_TAR="$DIST/$C_NAME.tar.gz"
  [ -f "$C_TAR" ] || continue
  ARCHES+=("$arch")

  # read rootfs.json inside C (same sed parser as the af launcher)
  MANIFEST="$(tar xzf "$C_TAR" -O "$C_NAME/rootfs.json")"
  R_VER="$(mget version)"
  R_SHA="$(mget sha256)"
  R_URL="$(mget url)"
  if [ -z "$R_VER" ] || [ -z "$R_SHA" ]; then die "cannot read rootfs.json inside $C_NAME"; fi
  R_NAME="agent-fleet-rootfs-$R_VER-linux-$arch.tar.zst"
  ROOTFS_LIST+=("$arch=$R_VER")

  # The rootfs URL referenced by C must point at this repo's Releases. Publishing a
  # C that disagrees would make users' `af start` hit a missing/foreign URL.
  case "$R_URL" in
    "$URL_BASE/v$VERSION/$R_NAME")
      R_TAR="$DIST/$R_NAME"
      [ -f "$R_TAR" ] || die "$C_NAME points at $R_NAME in this release, but it is not in $DIST"
      echo "$R_SHA  $R_TAR" | sha256sum -c - >/dev/null \
        || die "sha256 of R does not match rootfs.json inside $C_NAME: $R_TAR"
      [ "$(stat -c%s "$R_TAR")" -lt "$GH_MAX_ASSET" ] \
        || die "$R_NAME is over the 2GiB GitHub Releases asset limit; $C_NAME would point at nothing"
      R_ASSETS+=("$R_TAR")
      ;;
    "$URL_BASE/"*"/$R_NAME")
      r_tag="${R_URL#"$URL_BASE/"}"; r_tag="${r_tag%%/*}"
      gh release view "$r_tag" -R "$REPO" --json assets -q '.assets[].name' 2>/dev/null \
        | grep -qxF "$R_NAME" \
        || die "$C_NAME reuses $R_NAME from release $r_tag, which does not hold it
  (a --rootfs-json reuse build is only valid against an already published <r>)"
      echo "==> [publish] $arch rootfs $R_VER reused from $r_tag (no upload)"
      ;;
    *)
      die "the rootfs URL in $C_NAME does not match the publish target.
  rootfs.json: $R_URL
  expected:    $URL_BASE/v$VERSION/$R_NAME
  (a C built with a different ROOTFS_URL_BASE cannot be published to this repo)"
      ;;
  esac
done

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

# ---- app release -------------------------------------------------------------------
if gh release view "v$VERSION" -R "$REPO" >/dev/null 2>&1; then
  die "v$VERSION already exists (releases are immutable — bump the version and retry)"
fi
assets=()
# ADR 0037: the images tar is no longer published — images go to the registry.
cands=("agent-fleet-$VERSION.tar.gz")
for arch in "${ARCHES[@]}"; do
  cands+=("agent-fleet-native-$VERSION-linux-$arch.tar.gz" "agent-fleet-native-$VERSION-linux-$arch-bundle.tar.gz")
done
for f in "${cands[@]}"; do
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
assets+=("${R_ASSETS[@]+"${R_ASSETS[@]}"}")

# SHA256SUMS lists exactly what this release carries. build.sh sums everything in
# the dist dir, which can include files that are not attached (an oversized bundle
# skipped above, leftovers of an earlier build). Re-checking the lines against the
# bytes also catches a file changed after the build.
SUMS_DIR="$DIST/.publish"
rm -rf "$SUMS_DIR"; mkdir -p "$SUMS_DIR"
for p in "${assets[@]}"; do
  n="$(basename "$p")"
  line="$(awk -v n="$n" '$2 == n || $2 == "*" n' "$DIST/SHA256SUMS" | head -1)"
  [ -n "$line" ] || die "$DIST/SHA256SUMS has no line for $n (rerun build.sh so it covers every asset)"
  printf '%s\n' "$line" >> "$SUMS_DIR/SHA256SUMS"
done
(cd "$DIST" && sha256sum -c "$SUMS_DIR/SHA256SUMS" >/dev/null) \
  || die "an asset no longer matches $DIST/SHA256SUMS"
assets+=("$SUMS_DIR/SHA256SUMS")

# Release notes come from deploy/release/notes/<v>.md (+ .ja.md) — see that dir's
# README. Rendering is a hard requirement: a release with no notes is a bug, so a
# missing notes file fails here rather than publishing a bare tag. The rendered
# body is written into the dist dir so it can be reviewed after the fact.
NOTES_BODY="$DIST/RELEASE_NOTES-$VERSION.md"
echo "==> [publish] render release notes"
VERSION="$VERSION" ROOTFS="${ROOTFS_LIST[*]}" REPO="$REPO" \
  "$HERE/notes-body.sh" > "$NOTES_BODY"

echo "==> [publish] create release v$VERSION (${#assets[@]} assets)"
run gh release create "v$VERSION" -R "$REPO" \
  --title "agent-fleet $VERSION" \
  --notes-file "$NOTES_BODY" \
  "${assets[@]}"

cat <<EOF
==> [publish] done
  release:  https://github.com/$REPO/releases/tag/v$VERSION (${ARCHES[*]})
  install:  curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | bash
EOF
