#!/usr/bin/env bash
# Agent Fleet — build a versioned on-prem release bundle (maintainers).
#
# Produces, under ${DIST_DIR:-./dist}/ (A/B/D of docs/35 §35.2):
#   agent-fleet-<VERSION>/           ... bundle dir (the deploy surface below)
#   agent-fleet-<VERSION>.tar.gz     ... A: compose deploy surface + aws/ (ec2-single,
#                                       ecs cfn, release-ecr) + LICENSE/NOTICE
#   agent-fleet-images-<VERSION>.tar.gz ... B (with --save): docker save (CP+Workspace,
#                                       for air-gap; an artifact independent of A)
#   SHA256SUMS                       ... D: checksums of the above
#
# Images are tagged ${REGISTRY}/agent-fleet/{control-plane,workspace}:${VERSION}.
# Distribution variants (docs/35 §35.4.1/§35.4.3): by default
#   - workspace is lean (BAKE_AGENT_CLIS=0; agent CLIs are pin-installed at start).
#     The fully-baked internal build must be requested with BAKE_AGENT_CLIS=1.
#   - CP docs come from a staged tree with the internal denylist (docs/.distignore)
#     applied.
#
#   VERSION=1.0.0 deploy/compose/release.sh            # build images + bundle (A+D)
#   VERSION=1.0.0 deploy/compose/release.sh --push     # + push images to $REGISTRY (ADR 0037)
#   VERSION=1.0.0 deploy/compose/release.sh --save     # + local docker save tar (self-help
#                                                      #   for hosts that cannot reach a
#                                                      #   registry; not a released artifact)
#   VERSION=1.0.0 deploy/compose/release.sh --no-build # bundle only (images exist)
#   BAKE_AGENT_CLIS=1 VERSION=... deploy/compose/release.sh   # fully baked (internal use)
#   WS_PLATFORMS=linux/amd64,linux/arm64 VERSION=... ... --push  # multi-arch workspace
#                                                      #   image (docs/70 §70.9). Implies
#                                                      #   --push: buildx cannot load a
#                                                      #   manifest list locally.
#   CP_PLATFORMS=linux/amd64,linux/arm64 VERSION=... ... --push  # same, for the control
#                                                      #   plane image (docs/72). Also
#                                                      #   implies --push.
#
# Normally invoked via deploy/release/build.sh, the single entry point (docs/35 §35.6.2).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

VERSION="${VERSION:?set VERSION=<tag> (e.g. VERSION=1.0.0)}"
# Image refs match docker-compose.yml (${REGISTRY}/control-plane) and the
# WS_IMAGE the CP launches (${REGISTRY}/workspace).
REGISTRY="${REGISTRY:-agent-fleet}"
CP_IMAGE="$REGISTRY/control-plane:$VERSION"
WS_IMAGE="$REGISTRY/workspace:$VERSION"
# Distribution default is lean (no baked CLIs). BAKE_AGENT_CLIS=1 restores the
# fully-baked build.
BAKE_AGENT_CLIS="${BAKE_AGENT_CLIS:-0}"
# Which CPU architectures the WORKSPACE image is built for (docs/70 §70.9). Empty =
# the host's, which is what every release so far has published. Set to
# "linux/amd64,linux/arm64" to publish one tag holding both — ECS and docker then pull
# whichever matches the box, and the CP needs no second image reference.
WS_PLATFORMS="${WS_PLATFORMS:-}"
# Same, for the CONTROL PLANE image (docs/72). Independent of WS_PLATFORMS on
# purpose: the two images answer different questions. The workspace image needs
# arm64 because a slot can be Graviton; the CP image needs it because an operator
# may want to run the Fargate service itself on ARM64. A deployment can want
# either, both, or neither.
CP_PLATFORMS="${CP_PLATFORMS:-}"

DO_BUILD=1; DO_SAVE=0; DO_PUSH=0
for a in "$@"; do
  case "$a" in
    --no-build) DO_BUILD=0 ;;
    --save)     DO_SAVE=1 ;;
    --push)     DO_PUSH=1 ;;
    *) echo "unknown arg: $a" >&2; exit 2 ;;
  esac
done

DIST="${DIST_DIR:-$HERE/dist}"
OUT="$DIST/agent-fleet-$VERSION"
rm -rf "$OUT"; mkdir -p "$OUT"

if [ "$DO_BUILD" = 1 ]; then
  # Bake the distribution image's docs from a staged tree with the internal denylist
  # (docs/.distignore, docs/35 §35.4.3) applied. The stage must live inside the build
  # context (repo root), so it goes in deploy/release/.docs-stage (gitignored).
  DOCS_STAGE_REL="deploy/release/.docs-stage"
  DOCS_STAGE="$ROOT/$DOCS_STAGE_REL"
  rm -rf "$DOCS_STAGE"; mkdir -p "$DOCS_STAGE"
  EXCLUDES=()
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%%#*}"
    line="${line#"${line%%[![:space:]]*}"}"; line="${line%"${line##*[![:space:]]}"}"
    if [ -n "$line" ]; then EXCLUDES+=(--exclude="./$line"); fi
  done < "$ROOT/docs/.distignore"
  tar -C "$ROOT/docs" -cf - "${EXCLUDES[@]}" . | tar -C "$DOCS_STAGE" -xf -
  echo "==> staged docs (distignore applied) -> $DOCS_STAGE_REL"

  # Same rule as the workspace image below: a multi-platform build produces a
  # manifest LIST, which buildx can only push. ⚠️ Unlike the workspace image, the
  # CP Dockerfile pins its console and Go stages to $BUILDPLATFORM and cross-compiles
  # (docs/72 §72.3), so the second architecture costs an emulated `apt-get install`
  # and nothing else — do not "simplify" that away.
  if [ -n "$CP_PLATFORMS" ]; then
    if [ "$DO_PUSH" != 1 ]; then
      echo "ERROR: CP_PLATFORMS needs --push (a manifest list cannot be loaded into the local docker)" >&2
      exit 1
    fi
    echo "==> buildx $CP_IMAGE (platforms=$CP_PLATFORMS, context=repo root, docs=staged) -> pushed"
    docker buildx build --platform "$CP_PLATFORMS" --push \
      -f "$ROOT/control-plane/Dockerfile" -t "$CP_IMAGE" \
      --build-arg "VERSION=$VERSION" \
      --build-arg "DOCS_SRC=$DOCS_STAGE_REL" \
      --provenance=false \
      "$ROOT"
  else
    echo "==> build $CP_IMAGE (context=repo root, docs=staged)"
    docker build -f "$ROOT/control-plane/Dockerfile" -t "$CP_IMAGE" \
      --build-arg "VERSION=$VERSION" \
      --build-arg "DOCS_SRC=$DOCS_STAGE_REL" \
      "$ROOT"
  fi
  # The workspace image needs a second CPU architecture for its own reason: on the
  # ecs-ec2 runtime a slot can be Graviton (docs/70). That is a different question from
  # CP_PLATFORMS above (which architecture the CP's own Fargate task runs on, docs/72),
  # which is why the two are separate switches. WS_PLATFORMS is empty by default, so a
  # plain build is byte-for-byte what it has always been.
  #
  # ⚠️ A multi-platform build produces a manifest LIST, and buildx cannot `--load` one
  # into the local docker — it can only push it. So this path implies --push, and the
  # docker push below is skipped for the workspace image (it is already in the
  # registry, under the same tag, as an index).
  if [ -n "$WS_PLATFORMS" ]; then
    if [ "$DO_PUSH" != 1 ]; then
      echo "ERROR: WS_PLATFORMS needs --push (a manifest list cannot be loaded into the local docker)" >&2
      exit 1
    fi
    echo "==> buildx $WS_IMAGE (BAKE_AGENT_CLIS=$BAKE_AGENT_CLIS, platforms=$WS_PLATFORMS) -> pushed"
    docker buildx build --platform "$WS_PLATFORMS" --push -t "$WS_IMAGE" \
      --build-arg "VERSION=$VERSION" \
      --build-arg "BAKE_AGENT_CLIS=$BAKE_AGENT_CLIS" \
      --provenance=false \
      "$ROOT/workspace"
  else
    echo "==> build $WS_IMAGE (BAKE_AGENT_CLIS=$BAKE_AGENT_CLIS)"
    docker build -t "$WS_IMAGE" \
      --build-arg "VERSION=$VERSION" \
      --build-arg "BAKE_AGENT_CLIS=$BAKE_AGENT_CLIS" \
      "$ROOT/workspace"
  fi
  rm -rf "$DOCS_STAGE"
fi

echo "==> assemble deploy surface -> $OUT"
cp "$HERE/docker-compose.yml" "$HERE/Caddyfile" "$HERE/.env.example" \
   "$HERE/backup.sh" "$HERE/restore.sh" "$HERE/load-images.sh" \
   "$HERE/README.md" "$ROOT/LICENSE" "$ROOT/NOTICE" "$OUT/"
# AWS deploy surface (docs/35 §35.2 A): ec2-single cfn + ecs cfn/ + runbooks.
# Explicitly strip local key material (gitignored) so it cannot slip into the bundle.
cp -R "$ROOT/deploy/aws" "$OUT/aws"
rm -f "$OUT"/aws/ec2-single/*-key "$OUT"/aws/ec2-single/*-key.pub "$OUT"/aws/ec2-single/*.pem
# Pin the bundle to this VERSION so `compose up` uses the released images by
# default (operators still override REGISTRY/VERSION in their .env).
sed -i "s/^VERSION=.*/VERSION=$VERSION/; s#^REGISTRY=.*#REGISTRY=$REGISTRY#; s#^WS_IMAGE=.*#WS_IMAGE=$WS_IMAGE#" "$OUT/.env.example"

# ADR 0037: images are distributed through a registry. The save tar stays as a
# self-help path for hosts that cannot reach one — it is no longer published.
if [ "$DO_PUSH" = 1 ]; then
  echo "==> docker push $CP_IMAGE + $WS_IMAGE"
  case "$REGISTRY" in
    */*) ;;
    *) echo "ERROR: --push needs REGISTRY to be a real registry path (got '$REGISTRY')" >&2; exit 1 ;;
  esac
  if [ -n "$CP_PLATFORMS" ]; then
    echo "    ($CP_IMAGE was pushed by buildx as a manifest list)"
  else
    docker push "$CP_IMAGE"
  fi
  if [ -n "$WS_PLATFORMS" ]; then
    echo "    ($WS_IMAGE was pushed by buildx as a manifest list)"
  else
    docker push "$WS_IMAGE"
  fi
fi

if [ "$DO_SAVE" = 1 ]; then
  # ⚠️ A manifest list is not in the local docker at all, so there is nothing to save.
  # The air-gap tar stays single-architecture (the host's) on purpose — it is a
  # hand-off for one machine, not a distribution channel (ADR 0037).
  if [ -n "$WS_PLATFORMS" ] || [ -n "$CP_PLATFORMS" ]; then
    echo "ERROR: --save cannot be combined with WS_PLATFORMS/CP_PLATFORMS (a manifest list is never loaded locally)" >&2
    exit 1
  fi
  echo "==> docker save (local hand-off) $CP_IMAGE + $WS_IMAGE"
  docker save "$CP_IMAGE" "$WS_IMAGE" | gzip > "$DIST/agent-fleet-images-$VERSION.tar.gz"
fi

echo "==> tar bundle"
tar -czf "$DIST/agent-fleet-$VERSION.tar.gz" -C "$DIST" "agent-fleet-$VERSION"

echo "==> SHA256SUMS"
(
  cd "$DIST"
  rm -f SHA256SUMS
  sums=("agent-fleet-$VERSION.tar.gz")
  [ "$DO_SAVE" = 1 ] && sums+=("agent-fleet-images-$VERSION.tar.gz")
  sha256sum "${sums[@]}" > SHA256SUMS
)

echo "==> done:"
echo "   bundle dir: $OUT"
echo "   bundle tar: $DIST/agent-fleet-$VERSION.tar.gz  ($(du -h "$DIST/agent-fleet-$VERSION.tar.gz" | cut -f1))"
[ "$DO_SAVE" = 1 ] && echo "   images tar: $DIST/agent-fleet-images-$VERSION.tar.gz  ($(du -h "$DIST/agent-fleet-images-$VERSION.tar.gz" | cut -f1))"
echo "   checksums:  $DIST/SHA256SUMS"
echo
if [ "$DO_PUSH" = 1 ]; then
  echo "next: images are in \$REGISTRY — publish the bundle, and make sure the"
  echo "      packages are public (a first push creates them private)."
else
  echo "next: re-run with --push to publish the images to \$REGISTRY ($REGISTRY),"
  echo "      or hand off an images tar with --save (load-images.sh on the target)."
fi
