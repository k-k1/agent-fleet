#!/usr/bin/env bash
# Load images from a tar produced by `docker save`, for hosts that cannot reach a
# registry. Releases do not ship such a tar — images go to GHCR (ADR 0037) — so the
# producer side below is a self-help step you run yourself. On a networked host just
# use compose's `image:` (registry pull); this script is not needed there.
#
#   # Producer side (built on a networked machine):
#   VERSION=<version> deploy/compose/release.sh --save
#     -> deploy/compose/dist/agent-fleet-images-<version>.tar.gz
#
#   # Target side (no registry access):
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
