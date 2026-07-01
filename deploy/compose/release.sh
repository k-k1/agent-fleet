#!/usr/bin/env bash
# Agent Fleet — build a versioned on-prem release bundle (maintainers).
#
# Produces, under ./dist/agent-fleet-<VERSION>/:
#   docker-compose.yml, Caddyfile, .env.example, backup.sh, restore.sh,
#   load-images.sh, README.md, LICENSE   (the deploy surface)
# and, with --save, an air-gap image tar:
#   agent-fleet-images-<VERSION>.tar.gz   (docker save of cp + workspace)
# then tars the whole thing to  ./dist/agent-fleet-<VERSION>.tar.gz.
#
# Images are tagged ${REGISTRY}/agent-fleet/{control-plane,workspace}:${VERSION}.
#
#   VERSION=1.0.0 deploy/compose/release.sh            # build images + bundle
#   VERSION=1.0.0 deploy/compose/release.sh --save     # + docker save tar (air-gap)
#   VERSION=1.0.0 deploy/compose/release.sh --no-build # bundle only (images exist)
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

VERSION="${VERSION:?set VERSION=<tag> (e.g. VERSION=1.0.0)}"
# Image refs match docker-compose.yml (${REGISTRY}/control-plane) and the
# WS_IMAGE the CP launches (${REGISTRY}/workspace).
REGISTRY="${REGISTRY:-agent-fleet}"
CP_IMAGE="$REGISTRY/control-plane:$VERSION"
WS_IMAGE="$REGISTRY/workspace:$VERSION"

DO_BUILD=1; DO_SAVE=0
for a in "$@"; do
  case "$a" in
    --no-build) DO_BUILD=0 ;;
    --save)     DO_SAVE=1 ;;
    *) echo "unknown arg: $a" >&2; exit 2 ;;
  esac
done

OUT="$HERE/dist/agent-fleet-$VERSION"
rm -rf "$OUT"; mkdir -p "$OUT"

if [ "$DO_BUILD" = 1 ]; then
  echo "==> build $CP_IMAGE (context=repo root)"
  docker build -f "$ROOT/control-plane/Dockerfile" -t "$CP_IMAGE" "$ROOT"
  echo "==> build $WS_IMAGE"
  docker build -t "$WS_IMAGE" "$ROOT/workspace"
fi

echo "==> assemble deploy surface -> $OUT"
cp "$HERE/docker-compose.yml" "$HERE/Caddyfile" "$HERE/.env.example" \
   "$HERE/backup.sh" "$HERE/restore.sh" "$HERE/load-images.sh" \
   "$HERE/README.md" "$ROOT/LICENSE" "$OUT/"
# Pin the bundle to this VERSION so `compose up` uses the released images by
# default (operators still override REGISTRY/VERSION in their .env).
sed -i "s/^VERSION=.*/VERSION=$VERSION/; s#^REGISTRY=.*#REGISTRY=$REGISTRY#; s#^WS_IMAGE=.*#WS_IMAGE=$WS_IMAGE#" "$OUT/.env.example"

if [ "$DO_SAVE" = 1 ]; then
  echo "==> docker save (air-gap) $CP_IMAGE + $WS_IMAGE"
  docker save "$CP_IMAGE" "$WS_IMAGE" | gzip > "$OUT/agent-fleet-images-$VERSION.tar.gz"
fi

echo "==> tar bundle"
tar -czf "$HERE/dist/agent-fleet-$VERSION.tar.gz" -C "$HERE/dist" "agent-fleet-$VERSION"
echo "==> done:"
echo "   bundle dir: $OUT"
echo "   bundle tar: $HERE/dist/agent-fleet-$VERSION.tar.gz  ($(du -h "$HERE/dist/agent-fleet-$VERSION.tar.gz" | cut -f1))"
[ "$DO_SAVE" = 1 ] && echo "   images tar: $OUT/agent-fleet-images-$VERSION.tar.gz  ($(du -h "$OUT/agent-fleet-images-$VERSION.tar.gz" | cut -f1))"
echo
echo "next: push images to \$REGISTRY (docker push $CP_IMAGE; $WS_IMAGE),"
echo "      or ship the images tar for air-gap (load-images.sh)."
