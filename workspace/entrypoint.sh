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

# A stale user-home claude install can shadow the baked /usr/local claude on PATH
# and break. After the node→dev rename, ~/.local/bin/claude is a symlink left
# pointing at the old /home/node/.local/share/claude/... (now gone) — a dangling
# link that claude flags as "missing or broken". Drop the stale user install so
# the pinned, baked /usr/local/bin/claude is used.
if [ -L "$HOME/.local/bin/claude" ] && [ ! -e "$HOME/.local/bin/claude" ]; then
  echo "[entrypoint] removing broken ~/.local claude (dangling symlink → use baked)"
  rm -rf "$HOME/.local/bin/claude" "$HOME/.local/share/claude"
fi

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
    case "$(command -v claude)" in
      "$HOME"/*)
        # User-home install (~/.local) takes PATH precedence → keep it current.
        if [ "${CLAUDE_AUTO_UPDATE:-1}" = "1" ]; then
          echo "[entrypoint] updating Claude CLI (user install) ..."
          claude update || echo "[entrypoint] WARN: claude update failed (continuing)"
        fi
        ;;
      *)
        # Baked into the image (/usr/local) → version-pinned, no self-update.
        : ;;
    esac
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

# 既定 settings.json を seed（ファイルが無い時のみ。以後は Console の Claude 設定が真実）。
#   skipDangerousModePermissionPrompt … bypass 警告での誤 exit を防ぐ
#   remoteControlAtStartup            … 起動時に Remote Control を有効化
#   agentPushNotifEnabled             … プッシュ通知を有効化
#   hooks(PreToolUse/Bash → rtk hook claude) … rtk がコンテナにあれば seed（トークン節約）
SETTINGS="$CCD/settings.json"
mkdir -p "$CCD"
if [ ! -f "$SETTINGS" ]; then
  RTK=0; command -v rtk >/dev/null 2>&1 && RTK=1
  node -e '
    const fs = require("fs"), p = process.argv[1], rtk = process.argv[2] === "1";
    const s = {
      skipDangerousModePermissionPrompt: true,
      remoteControlAtStartup: true,
      agentPushNotifEnabled: true,
    };
    if (rtk) s.hooks = { PreToolUse: [{ matcher: "Bash", hooks: [{ type: "command", command: "rtk hook claude" }] }] };
    fs.writeFileSync(p, JSON.stringify(s, null, 2) + "\n");
  ' "$SETTINGS" "$RTK" \
    && echo "[entrypoint] seeded default $SETTINGS (rtk=$RTK)" \
    || echo "[entrypoint] WARN: failed to seed $SETTINGS"
fi

# Toolchains: node (nvm, installed into the home volume) and java (pre-baked
# Temurin). The selection lives per-workspace in toolchains.json, chosen in the
# Console. We apply it HERE so the agent — and every tmux session it spawns —
# inherits JAVA_HOME and the selected node on PATH.
TOOLS="$HOME/.config/agent-fleet/toolchains.json"
NODE_VER=""; JAVA_VER=""; TZ_VAL=""
if [ -f "$TOOLS" ]; then
  NODE_VER=$(node -e 'try{process.stdout.write(String((require(process.argv[1]).node)||""))}catch{}' "$TOOLS" 2>/dev/null)
  JAVA_VER=$(node -e 'try{process.stdout.write(String((require(process.argv[1]).java)||""))}catch{}' "$TOOLS" 2>/dev/null)
  TZ_VAL=$(node -e 'try{process.stdout.write(String((require(process.argv[1]).timezone)||""))}catch{}' "$TOOLS" 2>/dev/null)
fi

# Timezone (per-user, default JST). Export TZ so the agent — and every session
# label / shell / claude it spawns — uses the user's local time. glibc and Go both
# honor TZ; tzdata is baked into the image. We can't symlink /etc/localtime as a
# non-root user, but TZ alone is sufficient.
[ -n "$TZ_VAL" ] || TZ_VAL="Asia/Tokyo"
if [ -f "/usr/share/zoneinfo/$TZ_VAL" ]; then
  export TZ="$TZ_VAL"
  echo "[entrypoint] TZ=$TZ"
else
  echo "[entrypoint] WARN: unknown timezone '$TZ_VAL' (falling back to UTC)"
fi

# java: point JAVA_HOME at the selected Temurin (if installed).
if [ -n "$JAVA_VER" ]; then
  JH=$(ls -d /usr/lib/jvm/temurin-"$JAVA_VER"-jdk* 2>/dev/null | head -1)
  if [ -n "$JH" ]; then
    export JAVA_HOME="$JH"
    export PATH="$JH/bin:$PATH"
    echo "[entrypoint] JAVA_HOME=$JH"
  else
    echo "[entrypoint] WARN: temurin-$JAVA_VER-jdk not installed"
  fi
fi

# node: install/activate the selected version via nvm (home volume → persists).
# "system" / empty keeps the image's base node.
if [ -n "$NODE_VER" ] && [ "$NODE_VER" != "system" ]; then
  export NVM_DIR="$HOME/.nvm"
  if [ ! -s "$NVM_DIR/nvm.sh" ]; then
    echo "[entrypoint] installing nvm ..."
    curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash >/dev/null 2>&1 \
      || echo "[entrypoint] WARN: nvm install failed (continuing)"
  fi
  if [ -s "$NVM_DIR/nvm.sh" ]; then
    # shellcheck disable=SC1091
    . "$NVM_DIR/nvm.sh"
    nvm install "$NODE_VER" >/dev/null 2>&1 && nvm alias default "$NODE_VER" >/dev/null 2>&1
    nvm use "$NODE_VER" >/dev/null 2>&1
    echo "[entrypoint] node $(node -v 2>/dev/null)"
  fi
fi

exec "$@"
