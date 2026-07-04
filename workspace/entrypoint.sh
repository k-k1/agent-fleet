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

# claude records installMethod="native" and self-checks its launcher at
# ~/.local/bin/claude on every start, warning "claude command … missing or broken"
# when it is gone/dangling. After the node→dev rename that launcher dangled (it
# pointed at the old /home/node/.local/share/claude/…). Removing it isn't enough —
# claude still expects a native install — so REPAIR it via the baked claude
# (`claude install`), which reinstalls a valid ~/.local install (and keeps it
# auto-updatable). Gated on installMethod=native so fresh homes just use the baked
# /usr/local claude. Best-effort (needs network); claude still runs if it fails.
CCD_EARLY="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
if [ -x /usr/local/bin/claude ] && [ ! -e "$HOME/.local/bin/claude" ] \
   && grep -q '"installMethod"[[:space:]]*:[[:space:]]*"native"' "$CCD_EARLY/.claude.json" 2>/dev/null; then
  rm -f "$HOME/.local/bin/claude" # clear a dangling symlink first
  echo "[entrypoint] repairing native claude install (claude install) ..."
  /usr/local/bin/claude install >/dev/null 2>&1 \
    && echo "[entrypoint] claude install ok" \
    || echo "[entrypoint] WARN: claude install failed (using baked /usr/local)"
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

# Agent CLI self-update (opt-in + operator-gated). The CLIs (claude/opencode/codex)
# are baked at /usr/local, pinned to the image version. Both gates come from the CP as
# env at container start: AF_AGENT_SELF_UPDATE_ALLOWED=1 (the tenant policy) AND
# AF_AGENT_SELF_UPDATE=1 (the member's per-workspace opt-in, stored in the CP DB so it
# can be toggled while the container is stopped). When both are set the CLIs are
# updated to latest IN PLACE here — no image rebuild. The npm-global tree is dev-owned
# (Dockerfile chown), so this needs no root. Stop→Start recreates the container from
# the image, so turning the toggle off reverts to the baked versions.
if [ "${AF_AGENT_SELF_UPDATE_ALLOWED:-0}" = "1" ] && [ "${AF_AGENT_SELF_UPDATE:-0}" = "1" ]; then
  echo "[entrypoint] updating agent CLIs to latest (member opt-in, operator-allowed) ..."
  if npm install -g @anthropic-ai/claude-code@latest opencode-ai@latest @openai/codex@latest >/dev/null 2>&1; then
    echo "[entrypoint] agent CLIs updated: claude $(claude --version 2>/dev/null | head -1) | opencode $(opencode --version 2>/dev/null | head -1) | codex $(codex --version 2>/dev/null | head -1)"
  else
    echo "[entrypoint] WARN: agent CLI update failed (using baked versions)"
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

# opencode status plugin: copy the bundled plugin into the user's opencode plugin
# dir (home, persists) so opencode reports session working/idle state back to the
# agent — the opencode analog of claude's settings.json hooks. Refreshed each start
# so it tracks the image version. opencode auto-loads ~/.config/opencode/plugin/*.js.
OC_PLUG_SRC="/usr/local/share/agent-fleet/opencode-plugin"
OC_PLUG_DST="$HOME/.config/opencode/plugin"
if [ -d "$OC_PLUG_SRC" ]; then
  mkdir -p "$OC_PLUG_DST"
  cp -f "$OC_PLUG_SRC"/*.js "$OC_PLUG_DST"/ 2>/dev/null \
    && echo "[entrypoint] seeded opencode status plugin" \
    || echo "[entrypoint] WARN: failed to seed opencode plugin"
fi

# opencode permission config: run fully unattended like claude/codex (the container IS
# the sandbox). The `--auto` launch flag auto-approves most permissions, but NOT
# `external_directory` (access outside the project dir, e.g. ~/repos siblings) — that
# stays "ask" and stalls the TUI on a prompt the Console user can't answer. Set every
# permission to "allow" in ~/.config/opencode/opencode.jsonc, preserving any other keys.
# Best-effort: skips if the file isn't plain JSON (e.g. the user added comments).
OC_CFG="$HOME/.config/opencode/opencode.jsonc"
mkdir -p "$HOME/.config/opencode"
python3 - "$OC_CFG" <<'PY' && echo "[entrypoint] set opencode permission=allow" || echo "[entrypoint] WARN: skipped opencode permission config"
import json, os, sys
p = sys.argv[1]
cfg = {}
if os.path.exists(p):
    try:
        with open(p) as f:
            cfg = json.load(f)
    except Exception:
        sys.exit(1)  # not plain JSON (comments?) — don't clobber
if not isinstance(cfg, dict):
    sys.exit(1)
cfg.setdefault("$schema", "https://opencode.ai/config.json")
perm = cfg.get("permission")
if not isinstance(perm, dict):
    perm = {}
for k in ("edit", "bash", "webfetch", "doom_loop", "external_directory"):
    perm[k] = "allow"
cfg["permission"] = perm
tmp = p + ".af-tmp"
with open(tmp, "w") as f:
    json.dump(cfg, f, indent=2)
os.replace(tmp, p)
PY
# The opencode rtk plugin (rtk.ts) and codex's AGENTS.md rtk block are applied by
# the agent (reconcileAgentRTK in agent_rtk.go) from the durable ~/.config/agent-
# fleet/rtk.json toggle — NOT seeded here — so the Console on/off choice survives
# restarts. The agent runs immediately after this entrypoint (exec workspace-agent).

# Workspace 利用ガイド（やってはいけないこと等）を各エージェントが常時読み込む位置へ配置。
#   claude   … /etc/claude-code/CLAUDE.md（イメージに焼込済の managed policy。毎セッション読込）
#   codex    … ~/.codex/AGENTS.md（$CODEX_HOME/AGENTS.md。全セッションに適用）
#   opencode … ~/.config/opencode/AGENTS.md（全セッションに適用）
# codex/opencode 分は home 永続のため、毎起動で最新イメージの内容へ refresh する。
WS_NOTES="/usr/local/share/agent-fleet/workspace-notes.md"
if [ -f "$WS_NOTES" ]; then
  mkdir -p "$HOME/.codex" "$HOME/.config/opencode"
  cp -f "$WS_NOTES" "$HOME/.codex/AGENTS.md" 2>/dev/null \
    && echo "[entrypoint] seeded ~/.codex/AGENTS.md" \
    || echo "[entrypoint] WARN: failed to seed ~/.codex/AGENTS.md"
  cp -f "$WS_NOTES" "$HOME/.config/opencode/AGENTS.md" 2>/dev/null \
    && echo "[entrypoint] seeded ~/.config/opencode/AGENTS.md" \
    || echo "[entrypoint] WARN: failed to seed opencode AGENTS.md"
  # The agent appends codex's rtk-usage block to ~/.codex/AGENTS.md after this (see
  # reconcileAgentRTK), driven by the durable rtk toggle. We seed the base file fresh
  # here; the agent re-applies the block so the toggle survives restarts.
fi

# Gradle defaults for a shared, memory-constrained host (seed only when missing, so
# user/project tuning persists). Real harm seen: builds ballooned RAM and the daemon
# stayed resident (Gradle's idle-timeout defaults to 3h). Cap the heap, reap idle
# daemons after 2min, and disable parallelism. Projects can override these in their
# own gradle.properties (project + CLI flags take precedence over $HOME/.gradle).
GRADLE_PROPS="$HOME/.gradle/gradle.properties"
if [ ! -f "$GRADLE_PROPS" ]; then
  mkdir -p "$HOME/.gradle"
  cat > "$GRADLE_PROPS" <<'EOF'
# agent-fleet defaults for a shared, memory-constrained workspace.
# Override per project in the project's own gradle.properties when a build needs more.
org.gradle.jvmargs=-Xmx768m -XX:MaxMetaspaceSize=384m
org.gradle.daemon.idletimeout=120000
org.gradle.parallel=false
org.gradle.workers.max=2
org.gradle.caching=true
EOF
  echo "[entrypoint] seeded $GRADLE_PROPS"
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
