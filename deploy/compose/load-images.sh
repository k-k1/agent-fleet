#!/usr/bin/env bash
# エアギャップ配布用: `docker save` で作った tar からイメージを読み込む（P3-10 §20.4）。
# ネット可の環境では compose の `image:`（registry pull）を使えばよく、これは不要。
#
#   # 配布側（ネット可の環境で作る）:
#   docker save agent-fleet/control-plane:"$VERSION" agent-fleet/workspace:"$VERSION" \
#     | gzip > agent-fleet-images-"$VERSION".tar.gz
#
#   # 設置側（エアギャップ）:
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
