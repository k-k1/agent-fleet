#!/usr/bin/env bash
# Provision the shared Temurin JDKs into a host directory ($1), to be bind-mounted
# read-only into every workspace at /usr/lib/jvm (WS_JVM_DIR). Idempotent: skips if
# the directory already holds temurin-*-jdk*. Re-run after editing jvm.Dockerfile.
set -euo pipefail

DEST="${1:?usage: provision-jvm.sh <dest-dir>}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

mkdir -p "$DEST"
if ls -d "$DEST"/temurin-*-jdk* >/dev/null 2>&1; then
  echo "==> JDKs already provisioned in $DEST (skip; rm -rf to rebuild)"
  exit 0
fi

echo "==> building JVM provisioner image"
docker build -t agent-fleet/jvm:dev -f "$ROOT/workspace/jvm.Dockerfile" "$ROOT/workspace"

echo "==> extracting JDKs into $DEST"
docker rm -f af-jvm-extract >/dev/null 2>&1 || true
docker create --name af-jvm-extract agent-fleet/jvm:dev >/dev/null
docker cp af-jvm-extract:/usr/lib/jvm/. "$DEST/"
docker rm af-jvm-extract >/dev/null

echo "==> provisioned:"
ls -d "$DEST"/temurin-*-jdk* 2>/dev/null || echo "(none — check jvm.Dockerfile / network)"
