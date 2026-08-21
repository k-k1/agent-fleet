#!/usr/bin/env bash
# Agent Fleet — release orchestrator (single entry point, docs/35 §35.6.2).
#
#   VERSION=0.2.0 deploy/release/build.sh [--compose] [--native] [--save] [--all]
#
#   --compose … compose artifacts (A: bundle tar / B: air-gap images tar /
#               D: SHA256SUMS). Delegates to deploy/compose/release.sh. Builds B
#               (docker save) by default to satisfy the P1 gate (A+B+D). Images
#               are the distribution variant (workspace: BAKE_AGENT_CLIS=0 lean /
#               CP: docs distignore applied).
#   --native  … C (native tar) + R (lean rootfs) — docs/35 §35.7.2-7.
#   --bundle-rootfs     … with --native, also produce the self-contained variant
#                          bundling R (-bundle tar).
#   --rootfs-json <path> … reuse an existing rootfs.json and skip generating R
#                          (image-immutable release: no re-download for users).
#   --save    … explicit B for --compose (default ON; accepted for compatibility).
#   --all     … --compose + --native.
#
# env: ROOTFS_URL_BASE … base of the R distribution URL baked into rootfs.json
#      (default https://github.com/k-k1/agent-fleet-dist/releases/download — §35.4.2).
#      WS_PLATFORMS … CPU architectures for the WORKSPACE image, e.g.
#      "linux/amd64,linux/arm64" (docs/70 §70.9). Empty = the host's, as before.
#      Needs --push. The native package (C/R) stays amd64 (docs/35 §35.3.1).
#
# Output: deploy/release/dist/ (each artifact + SHA256SUMS).
# Run real release builds on hosted CI (release-gate.yml) or a host with enough
# memory. Build one artifact at a time, serially (shared-host memory limits —
# docs/35 §35.4).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

VERSION="${VERSION:?set VERSION=<semver> (e.g. VERSION=0.2.0)}"
DIST="$HERE/dist"
# Distribution coordinates baked into the native package as dist.json, so
# `af update` resolves the same repo/URL base the one-liner installer uses
# (install.sh defaults). Overridable for mirrors/testing (must match --native's
# ROOTFS_URL_BASE if both point at a custom host).
DIST_REPO_META="${AF_DIST_REPO:-k-k1/agent-fleet-dist}"
DIST_URL_BASE_META="${AF_DIST_URL_BASE:-https://github.com/$DIST_REPO_META/releases/download}"

usage() { echo "usage: VERSION=<v> $0 [--compose] [--native] [--all] [--bundle-rootfs] [--rootfs-json <path>]" >&2; }

# Registry the released images are published to and that the bundled .env.example
# points at (ADR 0037). Overridable for mirrors and for local builds.
DEFAULT_REGISTRY="${AF_REGISTRY:-ghcr.io/k-k1/agent-fleet}"

DO_COMPOSE=0; DO_NATIVE=0; BUNDLE_ROOTFS=0; ROOTFS_JSON=""; DO_PUSH=0; DO_IMAGES_TAR=0
while [ $# -gt 0 ]; do
  case "$1" in
    --compose) DO_COMPOSE=1 ;;
    --native)  DO_NATIVE=1 ;;
    --all)     DO_COMPOSE=1; DO_NATIVE=1 ;;
    --bundle-rootfs) BUNDLE_ROOTFS=1 ;;
    --rootfs-json) ROOTFS_JSON="${2:?--rootfs-json needs a path}"; shift ;;
    --push)    DO_PUSH=1 ;;        # publish images to the registry (ADR 0037)
    --images-tar) DO_IMAGES_TAR=1 ;; # local docker-save tar (not a released asset)
    --save)    : ;; # compat no-op (B used to be part of --compose)
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
if [ "$DO_COMPOSE" = 0 ] && [ "$DO_NATIVE" = 0 ]; then
  usage
  exit 2
fi

mkdir -p "$DIST"

if [ "$DO_COMPOSE" = 1 ]; then
  # ADR 0037: A only. Images go to the registry (--push), not into a released
  # tar; --images-tar still produces one locally for hand-off.
  echo "==> [build.sh] compose artifacts (A) -> $DIST"
  extra=()
  [ "$DO_PUSH" = 1 ] && extra+=(--push)
  [ "$DO_IMAGES_TAR" = 1 ] && extra+=(--save)
  # WS_PLATFORMS passes straight through (docs/70 §70.9): the workspace image is the
  # only artifact that has to exist for more than one CPU architecture, and it is
  # release.sh that knows how to build one.
  DIST_DIR="$DIST" VERSION="$VERSION" REGISTRY="${REGISTRY:-$DEFAULT_REGISTRY}" \
    WS_PLATFORMS="${WS_PLATFORMS:-}" \
    bash "$ROOT/deploy/compose/release.sh" "${extra[@]+"${extra[@]}"}"
fi

if [ "$DO_NATIVE" = 1 ]; then
  # C (native tar) + R (lean rootfs) — docs/35 §35.7.2-7. amd64 first (§35.3.1).
  ARCH=amd64
  PKG_NAME="agent-fleet-native-$VERSION-linux-$ARCH"
  NATIVE_DIR="$HERE/native"
  WORK="$DIST/.native-work"
  OUT="$DIST/$PKG_NAME"
  echo "==> [build.sh] native artifacts (C+R) -> $DIST"
  rm -rf "$WORK" "$OUT" "$DIST/$PKG_NAME.tar.gz" "$DIST/$PKG_NAME-bundle.tar.gz"
  mkdir -p "$WORK" "$OUT/bin"

  # (i) af-cp static build (golang container, ldflags VERSION — same recipe as the
  # CP Dockerfile).
  echo "==> [native] build af-cp (static)"
  docker build -f "$NATIVE_DIR/Dockerfile.afcp" --build-arg "VERSION=$VERSION" \
    --output "type=local,dest=$WORK/afcp" "$ROOT"
  install -m 0755 "$WORK/afcp/af-cp" "$OUT/bin/af-cp"

  # (ii) console dist (node container).
  echo "==> [native] build console dist"
  docker build -f "$NATIVE_DIR/Dockerfile.console" \
    --output "type=local,dest=$WORK/console" "$ROOT"
  cp -R "$WORK/console/console" "$OUT/console"

  # (iii) stage docs (internal denylist applied — same rules as compose/release.sh).
  echo "==> [native] stage docs (distignore applied)"
  mkdir -p "$OUT/docs"
  EXCLUDES=()
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%%#*}"
    line="${line#"${line%%[![:space:]]*}"}"; line="${line%"${line##*[![:space:]]}"}"
    if [ -n "$line" ]; then EXCLUDES+=(--exclude="./$line"); fi
  done < "$ROOT/docs/.distignore"
  tar -C "$ROOT/docs" -cf - "${EXCLUDES[@]}" . | tar -C "$OUT/docs" -xf -

  # (iv) static bwrap / git+git-http-backend / zstd (alpine builder; versions are
  # pinned via ARGs in Dockerfile.tools).
  echo "==> [native] build static tools (bwrap/git/zstd)"
  docker build -f "$NATIVE_DIR/Dockerfile.tools" \
    --output "type=local,dest=$WORK/tools" "$NATIVE_DIR"
  for b in bwrap git git-http-backend zstd; do
    install -m 0755 "$WORK/tools/$b" "$OUT/bin/$b"
  done

  # (v)(vi) lean rootfs (R) and rootfs.json. With --rootfs-json, reuse the given
  # manifest and skip generating R entirely (<r>-immutable release).
  R_TAR=""
  if [ -n "$ROOTFS_JSON" ]; then
    echo "==> [native] reuse rootfs manifest: $ROOTFS_JSON"
    cp "$ROOTFS_JSON" "$OUT/rootfs.json"
  else
    WS_NATIVE_IMAGE="agent-fleet/workspace:native-$VERSION"
    echo "==> [native] build lean rootfs image ($WS_NATIVE_IMAGE)"
    docker build -t "$WS_NATIVE_IMAGE" \
      --build-arg "VERSION=$VERSION" \
      --build-arg "BAKE_AGENT_CLIS=0" \
      --build-arg "BAKE_OPTIONAL_TOOLS=0" \
      "$ROOT/workspace"
    echo "==> [native] export rootfs"
    cid="$(docker create "$WS_NATIVE_IMAGE")"
    docker export "$cid" > "$WORK/rootfs.tar"
    docker rm "$cid" >/dev/null
    # Docker ENV lives in the image config and is not part of a filesystem export.
    # Inject the manifest into the tar so the CP's rootfs mode can reconstruct the
    # env (§35.7.2-2).
    docker image inspect --format '{{json .Config.Env}}' "$WS_NATIVE_IMAGE" \
      > "$WORK/image-env.json"
    mkdir -p "$WORK/inject/usr/local/share/agent-fleet"
    cp "$WORK/image-env.json" "$WORK/inject/usr/local/share/agent-fleet/image-env.json"
    tar --append -f "$WORK/rootfs.tar" -C "$WORK/inject" usr/local/share/agent-fleet/image-env.json
    # <r> derives from the content hash (an unchanged image can be reused, so
    # users never re-download).
    R_VER="$(sha256sum "$WORK/rootfs.tar" | cut -c1-12)"
    R_TAR="$DIST/agent-fleet-rootfs-$R_VER-linux-$ARCH.tar.zst"
    echo "==> [native] compress rootfs -> $R_TAR"
    "$OUT/bin/zstd" -T0 -15 -f -q "$WORK/rootfs.tar" -o "$R_TAR"
    R_SHA="$(sha256sum "$R_TAR" | awk '{print $1}')"
    R_SIZE="$(stat -c%s "$R_TAR")"
    URL_BASE="${ROOTFS_URL_BASE:-https://github.com/k-k1/agent-fleet-dist/releases/download}"
    # Formatting pairs with the af launcher's sed parser (keep one key per line).
    cat > "$OUT/rootfs.json" <<EOF
{
  "version": "$R_VER",
  "url": "$URL_BASE/rootfs-$R_VER/agent-fleet-rootfs-$R_VER-linux-$ARCH.tar.zst",
  "sha256": "$R_SHA",
  "size": $R_SIZE
}
EOF
  fi

  # (vii) assemble C.
  echo "==> [native] assemble $PKG_NAME"
  install -m 0755 "$ROOT/deploy/native/af" "$OUT/af"
  cp "$ROOT/deploy/native/README.md" "$ROOT/LICENSE" "$ROOT/NOTICE" "$OUT/"
  printf '%s\n' "$VERSION" > "$OUT/VERSION"
  # dist.json — where `af update` fetches new releases from (docs/42). Formatting
  # pairs with the af launcher's dist_get sed parser (one key per line).
  cat > "$OUT/dist.json" <<EOF
{
  "repo": "$DIST_REPO_META",
  "url_base": "$DIST_URL_BASE_META"
}
EOF
  tar -czf "$DIST/$PKG_NAME.tar.gz" -C "$DIST" "$PKG_NAME"
  if [ "$BUNDLE_ROOTFS" = 1 ]; then
    [ -n "$R_TAR" ] || { echo "ERROR: --bundle-rootfs cannot be combined with --rootfs-json (R is not available locally)" >&2; exit 2; }
    mkdir -p "$OUT/rootfs"
    cp "$R_TAR" "$OUT/rootfs/"
    tar -czf "$DIST/$PKG_NAME-bundle.tar.gz" -C "$DIST" "$PKG_NAME"
    rm -rf "$OUT/rootfs"
  fi
  rm -rf "$WORK"
  echo "==> [native] done: $DIST/$PKG_NAME.tar.gz${R_TAR:+  +  $R_TAR}"
fi

# D: rebuild SHA256SUMS over everything directly under dist (overwrites what
# release.sh wrote — always covers the full set even as native artifacts appear).
echo "==> [build.sh] SHA256SUMS"
(
  cd "$DIST"
  rm -f SHA256SUMS
  find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%P\n' | LC_ALL=C sort \
    | xargs -r sha256sum > SHA256SUMS
  cat SHA256SUMS
)
echo "==> [build.sh] done: $DIST"
