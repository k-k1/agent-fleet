#!/usr/bin/env bash
# Agent Fleet — fetch helper for the Docker Compose edition (docs/35 §35.4.2).
#
#   curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install-compose.sh | bash
#
# The Compose edition cannot be a full one-liner (you must edit .env — secrets,
# domain, Google OAuth — before `docker compose up`). This script automates the
# toil up to that point: it downloads the latest (or AF_VERSION-pinned) compose
# bundle, verifies it against the release's SHA256SUMS, extracts it into
# ./agent-fleet-<v>/ and pulls the images from the registry (ADR 0037).
# Then it prints the remaining manual steps (cp .env.example .env → edit → up).
#
# env:
#   AF_VERSION        version to install (default: latest release)
#   AF_DEST           directory to extract into (default: current directory)
#   AF_DIST_REPO      distribution repo (default k-k1/agent-fleet-dist)
#   AF_DIST_URL_BASE  override the download URL base (for testing/mirrors;
#                     requires AF_VERSION)
#   AF_SKIP_PULL=1    do not `docker compose pull` after extracting (images are
#                     pulled by `docker compose up` anyway)
set -euo pipefail

REPO="${AF_DIST_REPO:-k-k1/agent-fleet-dist}"
DEFAULT_BASE="https://github.com/$REPO/releases/download"
BASE="${AF_DIST_URL_BASE:-$DEFAULT_BASE}"
DEST="${AF_DEST:-$PWD}"
VER="${AF_VERSION:-}"
PULL_IMAGES="${AF_SKIP_PULL:+0}"; PULL_IMAGES="${PULL_IMAGES:-1}"

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
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> downloading agent-fleet $VER (compose bundle): $BASE/v$VER/$BUNDLE"
fetch "$BASE/v$VER/$BUNDLE" "$TMP/$BUNDLE"
fetch "$BASE/v$VER/SHA256SUMS" "$TMP/SHA256SUMS"

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

# ADR 0037: images come from the registry the bundle's .env.example points at.
# Pulling here is a convenience and a connectivity check; `docker compose up`
# would pull anyway. A failure is not fatal — the operator still has to edit .env
# before bringing anything up, and may be pointing at a mirror.
if [ "$PULL_IMAGES" = 1 ]; then
  ok=1
  echo "==> pulling images (docker compose pull)"
  ( cd "$TARGET" && docker compose --env-file .env.example pull ) || ok=0
  # The workspace image is not a compose service (the CP launches it per user with
  # `docker run`), so `compose pull` skips it. Fetch it here too, otherwise the
  # first person to press Start pays for the whole download.
  ws="$(sed -n 's/^WS_IMAGE=//p' "$TARGET/.env.example" | head -1)"
  if [ -n "$ws" ]; then
    echo "==> pulling the workspace image ($ws)"
    docker pull "$ws" || ok=0
  fi
  if [ "$ok" = 0 ]; then
    echo "    note: a pull failed — check registry access, or set REGISTRY in .env to a" >&2
    echo "          mirror. Hosts that cannot reach any registry can build the images" >&2
    echo "          from source (deploy/compose/release.sh --save) and docker load them." >&2
  fi
fi

cat <<EOF

Next steps (Compose needs manual config — cannot be fully automated):
  cd agent-fleet-$VER
  cp .env.example .env
  # fill in .env: AF_MASTER_KEY / AF_COOKIE_SECRET / DOCKER_GID /
  #   PUBLIC_DOMAIN / PUBLIC_BASE_URL / SUPER_ADMIN_EMAILS / DATA_DIR
  #   and the login IdP: GOOGLE_OAUTH_* and/or AF_OIDC_PROVIDERS + AF_OIDC_<ID>_*
  #   (Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab), and/or GitHub via
  #   AF_GITHUB_ALLOWED_ORGS + GITHUB_OAUTH_CLIENT_ID/_SECRET
  #   plus AF_OAUTH_ALLOWED_DOMAINS (or _EMAILS) so your first administrator can
  #   sign in — after that, people invited in the Admin panel get in without it
  # the git-provider "Connect with OAuth" buttons are NOT set here: a tenant
  #   administrator registers the app in the Console (Tenant settings ->
  #   Integrations -> Git provider OAuth). Token paste works without it.
  docker compose up -d
  docker compose logs -f cp

The extracted README.md is the full runbook (TLS/domain, backup/restore, upgrades,
AWS ECS under aws/). See it for details.
EOF
