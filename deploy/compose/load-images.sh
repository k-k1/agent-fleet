#!/usr/bin/env bash
# Air-gap distribution: load images from a tar produced by `docker save` (P3-10 §20.4).
# On a networked host just use compose's `image:` (registry pull); this script is
# not needed there.
#
#   # Producer side (built on a networked machine):
#   docker save agent-fleet/control-plane:"$VERSION" agent-fleet/workspace:"$VERSION" \
#     | gzip > agent-fleet-images-"$VERSION".tar.gz
#
#   # Target side (air-gapped):
#   deploy/compose/load-images.sh agent-fleet-images-<version>.tar.gz
set -euo pipefail
TAR="${1:-}"
if [ -z "$TAR" ] || [ ! -f "$TAR" ]; then
  echo "usage: $0 <images.tar[.gz]>" >&2
  exit 2
fi
case "$TAR" in
  *.gz) gzip -dc "$TAR" | docker load ;;
  *)    docker load -i "$TAR" ;;
esac
echo "==> loaded images:"
docker images 'agent-fleet/*' --format '  {{.Repository}}:{{.Tag}}  ({{.Size}})'
