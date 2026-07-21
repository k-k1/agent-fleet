#!/usr/bin/env bash
# Agent Fleet — fetch helper for the Docker Compose edition (docs/35 §35.4.2).
#
#   curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install-compose.sh | bash
#
# The Compose edition cannot be a full one-liner (you must edit .env — secrets,
# domain, Google OAuth — before `docker compose up`). This script automates the
# toil up to that point: it downloads the latest (or AF_VERSION-pinned) compose
# bundle + air-gap images tar, verifies both against the release's SHA256SUMS,
# extracts the bundle into ./agent-fleet-<v>/ and `docker load`s the images.
# Then it prints the remaining manual steps (cp .env.example .env → edit → up).
#
# env:
#   AF_VERSION        version to install (default: latest release)
#   AF_DEST           directory to extract into (default: current directory)
#   AF_DIST_REPO      distribution repo (default k-k1/agent-fleet-dist)
#   AF_DIST_URL_BASE  override the download URL base (for testing/mirrors;
#                     requires AF_VERSION)
#   AF_SKIP_IMAGES=1  skip the (large) images tar — provide it out of band
#                     (file hand-off) and run load-images.sh yourself
#   AF_LOAD_IMAGES=0  download+verify the images tar but do not `docker load` it
set -euo pipefail

REPO="${AF_DIST_REPO:-k-k1/agent-fleet-dist}"
DEFAULT_BASE="https://github.com/$REPO/releases/download"
BASE="${AF_DIST_URL_BASE:-$DEFAULT_BASE}"
DEST="${AF_DEST:-$PWD}"
VER="${AF_VERSION:-}"
SKIP_IMAGES="${AF_SKIP_IMAGES:-0}"
LOAD_IMAGES="${AF_LOAD_IMAGES:-1}"

die() { echo "ERROR: $*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || die "the Compose edition runs on a Linux Docker host"
command -v docker >/dev/null 2>&1 \
  || die "docker is not installed (need Docker Engine + the compose plugin)"
docker compose version >/dev/null 2>&1 \
  || echo "WARNING: 'docker compose' (v2 plugin) not found — install it before 'docker compose up'" >&2

fetch() { # fetch <url> <out>
  if command -v curl >/dev/null 2>&1; then
    curl -fSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    die "neither curl nor wget is available"
  fi
}

# Resolve the latest version from the releases/latest redirect target tag
# (does not depend on the API rate limit).
if [ -z "$VER" ]; then
  [ "$BASE" = "$DEFAULT_BASE" ] || die "AF_VERSION is required when AF_DIST_URL_BASE is set"
  if command -v curl >/dev/null 2>&1; then
    loc="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")"
    tag="${loc##*/}"
  else
    tag="$(wget -qO- "https://api.github.com/repos/$REPO/releases/latest" \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  fi
  case "$tag" in
    v[0-9]*) VER="${tag#v}" ;;
    *) die "cannot resolve the latest release (tag=$tag). Set AF_VERSION=<v> explicitly" ;;
  esac
fi

BUNDLE="agent-fleet-$VER.tar.gz"
IMAGES="agent-fleet-images-$VER.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> downloading agent-fleet $VER (compose bundle): $BASE/v$VER/$BUNDLE"
fetch "$BASE/v$VER/$BUNDLE" "$TMP/$BUNDLE"
fetch "$BASE/v$VER/SHA256SUMS" "$TMP/SHA256SUMS"

have_images=0
if [ "$SKIP_IMAGES" != 1 ]; then
  echo "==> downloading images tar (air-gap): $BASE/v$VER/$IMAGES"
  # The images tar can exceed the 2GiB GitHub Releases attachment limit and be
  # distributed by file hand-off instead — tolerate its absence.
  if fetch "$BASE/v$VER/$IMAGES" "$TMP/$IMAGES"; then
    have_images=1
  else
    echo "    note: $IMAGES not on the release — provide it out of band, then run" >&2
    echo "          ./agent-fleet-$VER/load-images.sh <images.tar.gz>" >&2
  fi
fi

echo "==> verifying (sha256)"
( cd "$TMP" && sha256sum -c --ignore-missing SHA256SUMS >/dev/null ) \
  || die "sha256 mismatch (possibly a corrupted download — please retry)"
grep -qE "  $BUNDLE\$" "$TMP/SHA256SUMS" \
  || die "SHA256SUMS has no entry for $BUNDLE"

TARGET="$DEST/agent-fleet-$VER"
if [ -e "$TARGET" ]; then
  echo "==> $TARGET already exists — leaving it in place (your .env is not touched)"
else
  echo "==> extracting -> $TARGET"
  mkdir -p "$TMP/x"
  tar xzf "$TMP/$BUNDLE" -C "$TMP/x"
  [ -f "$TMP/x/agent-fleet-$VER/docker-compose.yml" ] \
    || die "unexpected bundle contents (docker-compose.yml is missing)"
  mkdir -p "$DEST"
  mv "$TMP/x/agent-fleet-$VER" "$TARGET"
fi

if [ "$have_images" = 1 ] && [ "$LOAD_IMAGES" = 1 ]; then
  echo "==> loading images (docker load)"
  "$TARGET/load-images.sh" "$TMP/$IMAGES"
elif [ "$have_images" = 1 ]; then
  cp "$TMP/$IMAGES" "$DEST/$IMAGES"
  echo "==> images tar saved to $DEST/$IMAGES (AF_LOAD_IMAGES=0 — load it later with load-images.sh)"
fi

cat <<EOF

Next steps (Compose needs manual config — cannot be fully automated):
  cd agent-fleet-$VER
  cp .env.example .env
  # fill in .env: AF_MASTER_KEY / AF_COOKIE_SECRET / DOCKER_GID /
  #   PUBLIC_DOMAIN / PUBLIC_BASE_URL / GOOGLE_OAUTH_* / SUPER_ADMIN_EMAILS / DATA_DIR
  # (optional) enable the git-provider "Connect" buttons:
  #   GITHUB_OAUTH_CLIENT_ID / BITBUCKET_OAUTH_KEY / BITBUCKET_OAUTH_SECRET
  docker compose up -d
  docker compose logs -f control-plane

The extracted README.md is the full runbook (TLS/domain, backup/restore, upgrades,
AWS ECS under aws/). See it for details.
EOF
