#!/usr/bin/env bash
# Workspace 起動時に最新の Claude CLI を用意してから Agent を起動する。
# claude はイメージに焼き込まず、永続ホーム(~/.local)へ install し、次回以降は
# update で最新化する。ネットワーク不通でも Agent 起動は止めない（端末は使える）。
#
# 制御 env:
#   CLAUDE_INSTALL=0      … claude の用意をスキップ（オフライン/軽量検証）
#   CLAUDE_AUTO_UPDATE=0  … 既存 claude の起動時 update を抑止
set -e
export PATH="$HOME/.local/bin:$PATH"

# Relocate Claude state out of the browsable home BEFORE claude runs (docs/17
# P3-5 段2): when CLAUDE_CONFIG_DIR points outside home, migrate a pre-existing
# ~/.claude into it once (must precede claude install/update, which would
# otherwise populate the new dir first and skip the migration). Auth also works
# via the per-session env token, so a glitch here is non-fatal. The Console file
# browser denylists .claude/.claude.json regardless, so this is hardening.
CCD="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
mkdir -p "$CCD"
if [ "$CCD" != "$HOME/.claude" ] && [ -d "$HOME/.claude" ] && [ -z "$(ls -A "$CCD" 2>/dev/null)" ]; then
  echo "[entrypoint] migrating ~/.claude -> $CCD"
  if cp -a "$HOME/.claude/." "$CCD/" 2>/dev/null; then
    rm -rf "$HOME/.claude"
  fi
fi

if [ "${CLAUDE_INSTALL:-1}" = "1" ]; then
  if command -v claude >/dev/null 2>&1; then
    if [ "${CLAUDE_AUTO_UPDATE:-1}" = "1" ]; then
      echo "[entrypoint] updating Claude CLI ..."
      claude update || echo "[entrypoint] WARN: claude update failed (continuing)"
    fi
  else
    echo "[entrypoint] installing latest Claude CLI ..."
    curl -fsSL https://claude.ai/install.sh | bash || echo "[entrypoint] WARN: claude install failed (continuing)"
  fi
  if command -v claude >/dev/null 2>&1; then
    echo "[entrypoint] claude $(claude --version 2>/dev/null || echo '?')"
  else
    echo "[entrypoint] WARN: claude not on PATH (sessions will fail until installed)"
  fi
fi

# 既定 settings.json を seed（無い場合のみ）。--dangerously-skip-permissions の
# bypass 警告で誤って exit するのを防ぐ（skipDangerousModePermissionPrompt）。
SETTINGS="$CCD/settings.json"
if [ ! -f "$SETTINGS" ]; then
  printf '{\n  "skipDangerousModePermissionPrompt": true\n}\n' > "$SETTINGS"
  echo "[entrypoint] seeded default $SETTINGS"
fi

exec "$@"
