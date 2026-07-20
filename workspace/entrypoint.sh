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

# gh 透過認証（§8.3）: 焼き込みの /usr/local/bin/gh は git と同一トークンを注入する
# ラッパー。home volume に実体の ~/.local/bin/gh が残っていると PATH 先頭で焼き込み
# ラッパーを隠し、透過認証が効かなくなる。シンボリックリンク以外（=実バイナリ）なら
# 除去して PATH をラッパーへ通す（標準イメージは ~/.local/bin に gh を置かない）。
if [ -e "$HOME/.local/bin/gh" ] && [ ! -L "$HOME/.local/bin/gh" ]; then
  echo "[entrypoint] removing shadowing $HOME/.local/bin/gh (use baked gh auth wrapper)"
  rm -f "$HOME/.local/bin/gh"
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

# Agent CLI self-update (opt-in + operator-gated). The CLIs (claude/opencode/codex/
# agy) and rtk are baked at /usr/local, pinned to the image version. Both gates come
# from the CP as env at container start: AF_AGENT_SELF_UPDATE_ALLOWED=1 (the tenant
# policy) AND AF_AGENT_SELF_UPDATE=1 (the member's per-workspace opt-in, stored in the
# CP DB so it can be toggled while the container is stopped). When both are set the
# CLIs are updated to latest IN PLACE here — no image rebuild. The npm-global tree is
# dev-owned (Dockerfile chown), so the npm trio needs no root; agy and rtk are root-
# owned bakes, so their updates land in ~/.local/bin as PATH-first shadows instead
# (removed by the else branch below when the opt-in is off). Stop→Start recreates the
# container from the image, so turning the toggle off reverts to the baked versions.
if [ "${AF_AGENT_SELF_UPDATE_ALLOWED:-0}" = "1" ] && [ "${AF_AGENT_SELF_UPDATE:-0}" = "1" ]; then
  echo "[entrypoint] agent self-update: checking versions (member opt-in, operator-allowed) ..."
  # 版比較スキップ: レジストリの latest とグローバル導入版が全一致なら再インストールを
  # 丸ごと省く（毎起動の tarball 取得を新リリース時だけに）。判定不能時は従来どおり更新。
  NPM_NEED=$(node -e '
    const { execSync } = require("child_process");
    const run = (c) => execSync(c, { stdio: ["ignore", "pipe", "ignore"] }).toString().trim();
    try {
      const ls = JSON.parse(run("npm ls -g --depth=0 --json"));
      let need = 0;
      for (const p of ["@anthropic-ai/claude-code", "opencode-ai", "@openai/codex"]) {
        const cur = ((ls.dependencies || {})[p] || {}).version || "";
        const latest = run("npm view " + p + " version");
        if (!cur || !latest || cur !== latest) { need = 1; break; }
      }
      process.stdout.write(String(need));
    } catch (e) { process.stdout.write("1"); }
  ' 2>/dev/null || echo 1)
  if [ "$NPM_NEED" = "0" ]; then
    echo "[entrypoint] agent CLIs already latest; skip"
  elif npm install -g @anthropic-ai/claude-code@latest opencode-ai@latest @openai/codex@latest >/dev/null 2>&1; then
    echo "[entrypoint] agent CLIs updated: claude $(claude --version 2>/dev/null | head -1) | opencode $(opencode --version 2>/dev/null | head -1) | codex $(codex --version 2>/dev/null | head -1)"
  else
    echo "[entrypoint] WARN: agent CLI update failed (using baked versions)"
  fi
  # agy (Antigravity) も同じ opt-in で最新へ。npm でなく Google の install.sh 供給で、
  # 焼き込みは root 所有の /usr/local/bin のため ~/.local/bin へ入れて PATH 先勝ちで
  # 差し替える（shadow 方式）。版比較スキップ: install.sh と同じ配布 manifest（軽量
  # JSON）から latest を取り、前回導入時に記録したマーカーと一致なら ~187MB の再取得を
  # 省く。`agy --version` は RDRAND 非提示ホストで SIGABRT する（decisions/0008）ため、
  # 実バイナリでなくマーカー比較にしている（agy 自身の自己更新で進んでいたら比較が
  # ズレて再導入されるだけで無害）。install.sh は既存バイナリがあると更新せず即 exit 0
  # する仕様なので、空の temp dir へ導入してから差し替える（失敗時は旧 shadow 温存）。
  AGY_MARK="$HOME/.local/bin/.agy.version"
  agy_arch="$(dpkg --print-architecture 2>/dev/null || uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
  agy_latest="$(curl -fsSL --max-time 15 \
    "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_${agy_arch}.json" 2>/dev/null \
    | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
  if [ -n "$agy_latest" ] && [ -x "$HOME/.local/bin/agy" ] \
     && [ "$(cat "$AGY_MARK" 2>/dev/null)" = "$agy_latest" ]; then
    echo "[entrypoint] agy already latest ($agy_latest); skip"
  else
    agy_tmp="$(mktemp -d)"
    if curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir "$agy_tmp" >/dev/null 2>&1 \
       && [ -x "$agy_tmp/agy" ]; then
      install -D -m 0755 "$agy_tmp/agy" "$HOME/.local/bin/agy"
      if [ -n "$agy_latest" ]; then printf '%s\n' "$agy_latest" > "$AGY_MARK"; else rm -f "$AGY_MARK"; fi
      echo "[entrypoint] agy updated: ${agy_latest:-latest}"
    else
      echo "[entrypoint] WARN: agy update failed (using $([ -x "$HOME/.local/bin/agy" ] && echo previous || echo baked) version)"
    fi
    rm -rf "$agy_tmp"
  fi
  # rtk も同じ opt-in で最新へ。焼き込みの /usr/local/bin/rtk は root 所有で上書き
  # できないため、latest release を ~/.local/bin へ入れて PATH 先勝ちで差し替える
  # （claude の user-install と同じ構図）。checksum 検証つき・失敗はソフト（焼き込み
  # 版のまま続行）。OFF に戻すと下の分岐がこの shadow を除去し、焼き込み版へ戻る。
  # 版比較スキップ: GitHub の /releases/latest リダイレクトから latest タグを取り、
  # PATH 先勝ちの `rtk --version`（shadow か焼き込み）と一致なら取得を省く。
  rtk_latest="$(curl -fsSI -o /dev/null -w '%{redirect_url}' --max-time 15 \
    https://github.com/rtk-ai/rtk/releases/latest 2>/dev/null | sed -n 's#.*/tag/v##p')"
  rtk_cur="$(rtk --version 2>/dev/null | head -1 | awk '{print $2}')"
  if [ -n "$rtk_latest" ] && [ "$rtk_cur" = "$rtk_latest" ]; then
    echo "[entrypoint] rtk already latest ($rtk_cur); skip"
  else (
    set -e
    arch="$(dpkg --print-architecture 2>/dev/null || uname -m)"
    case "$arch" in
      amd64 | x86_64) asset="rtk-x86_64-unknown-linux-musl.tar.gz" ;;
      arm64 | aarch64) asset="rtk-aarch64-unknown-linux-gnu.tar.gz" ;;
      *) echo "unsupported arch: $arch" >&2; exit 1 ;;
    esac
    base="https://github.com/rtk-ai/rtk/releases/latest/download"
    tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
    cd "$tmp"
    curl -fsSL "${base}/${asset}" -o "${asset}"
    curl -fsSL "${base}/checksums.txt" -o checksums.txt
    grep " ${asset}\$" checksums.txt | sha256sum -c - >/dev/null
    tar xzf "${asset}"
    install -D -m 0755 rtk "$HOME/.local/bin/rtk"
  ) && echo "[entrypoint] rtk updated: $("$HOME/.local/bin/rtk" --version 2>/dev/null | head -1)" \
    || echo "[entrypoint] WARN: rtk update failed (using baked version)"
  fi
else
  # Opt-in が無効（テナント不許可 or メンバー OFF）: 過去の opt-in が残した
  # ~/.local/bin の rtk / agy shadow は焼き込み版を PATH で隠すので除去し、CLI 群と
  # 同じ「OFF に戻して Stop→Start で焼き込み版へ復帰」の意味論に揃える。
  rm -f "$HOME/.local/bin/rtk" "$HOME/.local/bin/agy" "$HOME/.local/bin/.agy.version"
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

# java: point JAVA_HOME at the selected Temurin. JDKs come from the deployment-
# provided /usr/lib/jvm (baked image or local bind-mount) or the per-user home
# volume that `install-jdk` populates — the latter being the only source on ECS,
# where nothing is mounted at /usr/lib/jvm. Search both; if the selection is absent
# everywhere, download it into the home volume now (persists on the volume / EFS, so
# only the first launch pays the download). Soft-fail: no network → keep going.
if [ -n "$JAVA_VER" ]; then
  find_jh() {
    for d in /usr/lib/jvm "$HOME/.local/share/agent-fleet/jvm"; do
      jh=$(ls -d "$d"/temurin-"$JAVA_VER"-jdk* 2>/dev/null | head -1)
      [ -n "$jh" ] && { printf '%s\n' "$jh"; return 0; }
    done
    return 1
  }
  JH=$(find_jh || true)
  if [ -z "$JH" ]; then
    echo "[entrypoint] temurin-$JAVA_VER not present; installing into home volume ..."
    workspace-agent install-jdk "$JAVA_VER" || echo "[entrypoint] WARN: install-jdk $JAVA_VER failed"
    JH=$(find_jh || true)
  fi
  if [ -n "$JH" ]; then
    export JAVA_HOME="$JH"
    export PATH="$JH/bin:$PATH"
    echo "[entrypoint] JAVA_HOME=$JH"
  else
    echo "[entrypoint] WARN: temurin-$JAVA_VER-jdk unavailable"
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
