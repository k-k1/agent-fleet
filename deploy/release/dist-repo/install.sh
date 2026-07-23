#!/usr/bin/env bash
# Agent Fleet — one-liner installer for the native package (docs/35 §35.4.2).
#
#   curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh | bash
#
# Downloads the latest (or AF_VERSION-pinned) native tar, verifies it against the
# release's SHA256SUMS, extracts it to ~/.local/opt/agent-fleet/<v>/ and symlinks
# ~/.local/bin/af. Updating uses the same command (switches the version directory —
# user data under WS_DATA is never touched).
#
# env:
#   AF_VERSION        version to install (default: latest release)
#   AF_PREFIX         install prefix (default ~/.local)
#   AF_DIST_REPO      distribution repo (default k-k1/agent-fleet-dist)
#   AF_DIST_URL_BASE  override the download URL base (for testing/mirrors;
#                     requires AF_VERSION)
#   AF_NO_AUTOUPDATE  set to 1 to skip enabling the daily update timer (docs/42)
set -euo pipefail

REPO="${AF_DIST_REPO:-k-k1/agent-fleet-dist}"
DEFAULT_BASE="https://github.com/$REPO/releases/download"
BASE="${AF_DIST_URL_BASE:-$DEFAULT_BASE}"
PREFIX="${AF_PREFIX:-$HOME/.local}"
VER="${AF_VERSION:-}"
ARCH=amd64

die() { echo "ERROR: $*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || die "Linux only (including WSL2)"
[ "$(uname -m)" = x86_64 ] || die "only linux-amd64 is distributed for now ($(uname -m) is unsupported)"

fetch() { # fetch <url> <out>
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
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

NAME="agent-fleet-native-$VER-linux-$ARCH"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "==> downloading agent-fleet $VER: $BASE/v$VER/$NAME.tar.gz"
fetch "$BASE/v$VER/$NAME.tar.gz" "$TMP/$NAME.tar.gz"
fetch "$BASE/v$VER/SHA256SUMS" "$TMP/SHA256SUMS"

echo "==> verifying (sha256)"
grep -E "  $NAME\.tar\.gz\$" "$TMP/SHA256SUMS" > "$TMP/want.sum" \
  || die "SHA256SUMS has no entry for $NAME.tar.gz"
(cd "$TMP" && sha256sum -c want.sum >/dev/null) \
  || die "sha256 mismatch (possibly a corrupted download — please retry)"

echo "==> extracting -> $PREFIX/opt/agent-fleet/$VER"
mkdir -p "$TMP/x"
tar xzf "$TMP/$NAME.tar.gz" -C "$TMP/x"
[ -x "$TMP/x/$NAME/af" ] || die "unexpected tar contents (af is missing)"
DEST_ROOT="$PREFIX/opt/agent-fleet"
STAGING="$DEST_ROOT/.staging-$VER.$$"
mkdir -p "$DEST_ROOT"
rm -rf "$STAGING"
mv "$TMP/x/$NAME" "$STAGING"
rm -rf "${DEST_ROOT:?}/${VER:?}"
mv "$STAGING" "$DEST_ROOT/$VER"

mkdir -p "$PREFIX/bin"
ln -sfn "$DEST_ROOT/$VER/af" "$PREFIX/bin/af"

echo "==> installed: $PREFIX/bin/af -> $DEST_ROOT/$VER/af"
case ":$PATH:" in
  *":$PREFIX/bin:"*) ;;
  *) echo "    note: $PREFIX/bin is not on PATH (re-login or add it to PATH)" ;;
esac

# Automatic updates (docs/42): a systemd *user* timer that runs `af update` daily
# to STAGE the newest release (sha256-verified). It never restarts a running
# service — the new version applies when you restart agent-fleet (systemctl or the
# Console 'restart to apply' button), so live agent sessions are never dropped
# behind your back. Best-effort: only when the user systemd bus is reachable.
# Opt out with AF_NO_AUTOUPDATE=1 (or disable the timer later).
if [ "${AF_NO_AUTOUPDATE:-}" != 1 ] \
  && command -v systemctl >/dev/null 2>&1 \
  && systemctl --user show-environment >/dev/null 2>&1; then
  UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
  mkdir -p "$UNIT_DIR"
  cat > "$UNIT_DIR/agent-fleet-update.service" <<EOF
[Unit]
Description=Agent Fleet — stage the latest release (does not restart the running service)
[Service]
Type=oneshot
ExecStart=$PREFIX/bin/af update --yes
EOF
  cat > "$UNIT_DIR/agent-fleet-update.timer" <<EOF
[Unit]
Description=Agent Fleet — daily update check
[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=1h
[Install]
WantedBy=timers.target
EOF
  systemctl --user daemon-reload 2>/dev/null || true
  if systemctl --user enable --now agent-fleet-update.timer >/dev/null 2>&1; then
    echo "==> auto-update: daily 'af update' timer enabled (stages only; apply via restart)"
    echo "    disable: systemctl --user disable --now agent-fleet-update.timer"
  fi
fi
cat <<EOF
Next steps:
  af start        # first run downloads, verifies and extracts the rootfs (~200 MB)
  # then open http://localhost:8099 in your browser
Optional text-to-speech (VOICEVOX / Zundamon): see the bundled guide
  $DEST_ROOT/$VER/README.md
Cleanup of old versions (optional): remove unused version directories under
  $DEST_ROOT/ (user data lives elsewhere and is not affected)
EOF
