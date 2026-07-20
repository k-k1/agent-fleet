#!/usr/bin/env bash
# ローカル起動の単一エントリポイント（旧 run-dev.sh と wsl-quickstart.sh を一本化）。
# Workspace の動かし方をサブコマンドで選び、Console と Control Plane をビルドして
# ホストで CP を起動する。ブラウザで http://localhost:8099 を開く。
#
# 使い方:
#   deploy/local/run-dev.sh [local]   # Docker ランタイム（開発既定）
#   deploy/local/run-dev.sh wsl       # WSL 個人利用プリセット（Docker 必須。
#                                     # docker/cgroup preflight・AUTH=dev 固定）
#   deploy/local/run-dev.sh native    # Docker なしコンテナレス（単一ユーザー・docs/34）
#   deploy/local/run-dev.sh reset [--all] [--yes]
#                                     # ローカルデータ初期化。既定は dev ユーザーの
#                                     # ワークスペースのみ（$WS_DATA/<DEV_USER>）。
#                                     # --all は DB・共有 JDK を含む $WS_DATA 全体。
#
# 補足:
#   - 旧 deploy/local/wsl-quickstart.sh は `run-dev.sh wsl` の後方互換ラッパー。
#   - サブコマンド無しのときは env AF_RUNTIME で分岐（native|wsl → コンテナレス）。
#     ※ env の AF_RUNTIME=wsl は CP 側では「コンテナレス」の別名で、サブコマンド
#       `wsl`（Docker プリセット）とは別物。紛れるのでサブコマンド指定を推奨。
#   - claude / opencode / codex / agy / rtk はイメージに焼き込み（Dockerfile の ARG でピン止め）。
#     版の bump 手順（runbook）は docs/dev/10-development.md §10.2.1。最新への追従は
#     設定モーダルの自己更新 opt-in（AF_AGENT_SELF_UPDATE）でも可（rtk 含む）。
#
# 例:
#   deploy/local/run-dev.sh
#   WS_ENV=CLAUDE_INSTALL=0 WS_SESSION_CMD=bash deploy/local/run-dev.sh   # claude 抜き軽量検証
#   WS_JDK=0 WS_SMOKE=0 deploy/local/run-dev.sh wsl
#   deploy/local/run-dev.sh reset --all --yes
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Go / Node は user-local 導入が普通なので、非ログインシェル（sg docker 等）でも
# 見えるよう PATH を通す。
export PATH="$HOME/.local/go/bin:/usr/local/go/bin:$HOME/go/bin:$PATH"

CP_ADDR="${CP_ADDR:-:8099}"
# Persistent data root (DB + per-user homes + shared JDKs). NOT under /tmp: that's
# tmpfs (RAM) here, so it would be wiped on reboot and permanently occupy RAM.
WS_DATA="${WS_DATA:-$HOME/.local/share/agent-fleet}"
# Per-workspace RAM cap (docker --memory; native は不適用). Raise with care.
WS_MEMORY="${WS_MEMORY:-5g}"
# Shared Temurin JDKs live here on the host and are bind-mounted read-only into
# every workspace at /usr/lib/jvm（docker ランタイムのみ）。
WS_JVM_DIR="${WS_JVM_DIR:-$WS_DATA/shared/jvm}"
# The host-run Control Plane is not built from control-plane/Dockerfile, so it
# does not have the baked docs tree. Point staging at this checkout by default.
AF_DOCS_DIR="${AF_DOCS_DIR:-$ROOT/docs}"
WS_JDK="${WS_JDK:-1}"                  # 1=共有JDKをprovision / 0=省略（install-jdk の on-demand に寄せる）
RTK_VERSION="${RTK_VERSION:-}"         # 焼き込み rtk 版の上書き（空=Dockerfile の ARG 既定ピン）
DEV_KEY="${DEV_USER:-dev}"

# 冒頭コメントブロック（shebang の次〜最初の非コメント行の手前）をヘルプとして表示。
usage() { awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "$0"; }

# ---- reset: ローカルデータ初期化 --------------------------------------------
# docker / native どちらで動かした後でも、残骸（コンテナ・network / agent プロセス・
# 専用 tmux ソケット）を掃除してからデータを消す。既定は dev ユーザーのみ（DB と
# 共有 JDK は温存＝再 provision 不要）。--all は $WS_DATA ごと消す完全初期化。
do_reset() {
  local all=0 yes=0 a
  for a in "$@"; do
    case "$a" in
      --all) all=1 ;;
      --yes | -y) yes=1 ;;
      *)
        echo "reset の不明なオプション: $a" >&2
        usage
        exit 1
        ;;
    esac
  done
  local target="$WS_DATA/$DEV_KEY"
  [ "$all" = 1 ] && target="$WS_DATA"
  if [ ! -e "$target" ]; then
    echo "削除対象がありません（初期化済み）: $target"
    exit 0
  fi
  # CP がデータを掴んだまま消さない（DB/home を書き戻されて中途半端になる）。
  if pgrep -f '^/tmp/af-cp' >/dev/null 2>&1; then
    echo "ERROR: control-plane（/tmp/af-cp）が稼働中です。Ctrl-C で停止してから再実行してください。" >&2
    exit 1
  fi
  echo "==> 削除対象: $target"
  if [ "$all" = 1 ]; then
    echo "    完全初期化: DB・全ユーザー home・claude-config・共有 JDK を含む（JDK は次回 provision し直し）"
  else
    echo "    $DEV_KEY ユーザーのみ: home / claude-config（Claude ログイン含む）。DB と共有 JDK は温存"
  fi
  if [ "$yes" != 1 ]; then
    if [ -t 0 ]; then
      read -r -p "本当に削除しますか？ [y/N] " ans
      case "$ans" in y | Y | yes) ;; *)
        echo "中止しました"
        exit 1
        ;;
      esac
    else
      echo "ERROR: 非対話実行では --yes を付けてください" >&2
      exit 1
    fi
  fi
  # docker ランタイムの残骸（docker 未導入の環境では素通り）。
  docker rm -f "af-ws-$DEV_KEY" >/dev/null 2>&1 || true
  docker network rm "af-net-$DEV_KEY" >/dev/null 2>&1 || true
  # native ランタイムの残骸: agent プロセスグループ停止 → 専用 tmux ソケット掃除。
  local pidf="$WS_DATA/$DEV_KEY/agent.pid" pid=""
  if pid="$(cat "$pidf" 2>/dev/null)" && [ -n "$pid" ]; then
    kill -TERM -- "-$pid" 2>/dev/null || true
    sleep 1
    kill -KILL -- "-$pid" 2>/dev/null || true
  fi
  tmux -L "af-ws-$DEV_KEY" kill-server 2>/dev/null || true
  rm -rf "$target"
  echo "==> 初期化完了（次回起動時に再作成されます）"
}

# ---- モード決定 --------------------------------------------------------------
MODE=""
case "${1:-}" in
  local | docker) MODE=local ;;
  wsl | quickstart) MODE=wsl ;;
  native) MODE=native ;;
  reset)
    shift
    do_reset "$@"
    exit 0
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  "") ;;
  *)
    echo "不明なサブコマンド: $1" >&2
    usage
    exit 1
    ;;
esac
if [ -z "$MODE" ]; then
  # サブコマンド無し: 後方互換で env AF_RUNTIME を見る（native|wsl は CP 的にコンテナレス）。
  case "${AF_RUNTIME:-local}" in native | wsl) MODE=native ;; *) MODE=local ;; esac
fi
# CP へ渡すランタイムを確定（サブコマンド優先）。wsl プリセットは docker ランタイム。
case "$MODE" in native) AF_RUNTIME=native ;; *) AF_RUNTIME=local ;; esac

WS_IMAGE_DEFAULT="agent-fleet/workspace:dev"
[ "$MODE" = wsl ] && WS_IMAGE_DEFAULT="agent-fleet/workspace:wsl"
WS_IMAGE="${WS_IMAGE:-$WS_IMAGE_DEFAULT}"

# git-provider OAuth config (contains a secret -> git-ignored). If present, export
# GITHUB_OAUTH_CLIENT_ID / BITBUCKET_OAUTH_KEY / BITBUCKET_OAUTH_SECRET / PUBLIC_BASE_URL.
# See deploy/local/oauth.env.example.
OAUTH_ENV="$ROOT/deploy/local/oauth.env"
if [ -f "$OAUTH_ENV" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$OAUTH_ENV"
  set +a
  gh_state="未設定"
  [ -n "${GITHUB_OAUTH_CLIENT_ID:-}" ] && gh_state="設定あり"
  echo "==> loaded $OAUTH_ENV（GitHub device flow client_id: $gh_state）"
fi
# wsl プリセットは単一ユーザー固定: oauth.env が AUTH=oauth を書いても採用しない。
[ "$MODE" = wsl ] && AUTH=dev

# ---- preflight ---------------------------------------------------------------
fail=0
if ! command -v go >/dev/null 2>&1; then
  echo "✗ go が無い。Go を入れて PATH に通してください（https://go.dev/dl/）" >&2
  fail=1
fi
export NVM_DIR="${NVM_DIR:-$HOME/.nvm}"
# shellcheck disable=SC1091
[ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh" >/dev/null 2>&1 || true
if ! command -v npm >/dev/null 2>&1; then
  echo "✗ npm/node が無い。Node（nvm 推奨）を入れてください" >&2
  fail=1
fi
if [ "$MODE" = wsl ]; then
  # Docker daemon に届くか（Docker Desktop でなく WSL 内 native dockerd を推奨）。
  if ! docker info >/dev/null 2>&1; then
    echo "✗ docker daemon に接続できない。WSL 内で dockerd を起動し、${USER:-$(id -un)} を docker グループに入れてください" >&2
    echo "   （例: sudo service docker start / sudo usermod -aG docker ${USER:-$(id -un)} 後に再ログイン）" >&2
    echo "   Docker を導入できない場合は: deploy/local/run-dev.sh native（docs/34）" >&2
    fail=1
  fi
  # cgroup v2（メモリ上限 --memory とリソース表示が依存）。
  cgt="$(stat -fc %T /sys/fs/cgroup 2>/dev/null || echo unknown)"
  [ "$cgt" = "cgroup2fs" ] || echo "! cgroup が v2 でない（$cgt）。メモリ上限やリソース表示が期待通りに出ない場合があります" >&2
fi
[ "$fail" = 0 ] || {
  echo "preflight 失敗。上記を解消してから再実行してください。" >&2
  exit 1
}

# ---- Workspace 実行環境の準備（モード別） ------------------------------------
# rtk は常にイメージ焼き込み（Dockerfile の BAKE_RTK=1 既定・ARG ピン止め）。かつての
# ホスト vendoring（update-rtk.sh → vendor/rtk）は廃止した。
if [ "$MODE" != native ]; then
  # Provision the shared JDKs into WS_JVM_DIR (idempotent; first run is slow).
  # WS_JDK=0 なら省き、コンテナ内 `workspace-agent install-jdk` の on-demand に任せる。
  if [ "$WS_JDK" = "1" ]; then
    bash "$ROOT/deploy/local/provision-jvm.sh" "$WS_JVM_DIR" || echo "WARN: jvm provision failed (java unavailable)"
  else
    WS_JVM_DIR=""
    echo "==> WS_JDK=0: 共有 JDK provision を省略（必要時に workspace-agent install-jdk <major>）"
  fi

  echo "==> build workspace image ($WS_IMAGE)"
  docker build \
    ${RTK_VERSION:+--build-arg "RTK_VERSION=$RTK_VERSION"} \
    -t "$WS_IMAGE" "$ROOT/workspace"

  # イメージスモーク（焼き込み CLI の版 = Dockerfile の ARG ピン等を検証、数秒）。
  # WS_SMOKE=0 でスキップ。
  if [ "${WS_SMOKE:-1}" = "1" ]; then
    bash "$ROOT/deploy/local/e2e-smoke.sh" "$WS_IMAGE"
  fi
else
  # native: no image — build the workspace-agent for this host instead, and check
  # the host provides what the Dockerfile normally would (warn-only; docs/34).
  echo "==> build workspace-agent (native runtime)"
  (cd "$ROOT/workspace/agent" && go build -o /tmp/af-agent .)
  AF_NATIVE_AGENT_BIN=/tmp/af-agent
  for c in tmux git claude; do
    command -v "$c" >/dev/null 2>&1 || echo "WARN: '$c' not found on host PATH (native workspaces need it)"
  done
fi

# ---- Console / Control Plane ビルドと起動（全モード共通） ---------------------
# Console is a Vite + React app: build it to console/dist, which the CP serves
# statically (no-store). Run `npm --prefix console run dev` (vite build --watch) in a
# separate shell during active UI work; this script does a one-shot production build.
# mermaid is large; raise the Node heap so the build doesn't OOM on a RAM-constrained host.
echo "==> build console (vite)"
(cd "$ROOT/console" && { [ -d node_modules ] || npm ci; } && NODE_OPTIONS="--max-old-space-size=3072" npm run build)

echo "==> build control-plane"
(cd "$ROOT/control-plane" && go build -o /tmp/af-cp .)

echo "==> control-plane on $CP_ADDR  (console: http://${CP_ADDR/#:/localhost:})  mode=$MODE runtime=$AF_RUNTIME auth=${AUTH:-dev}"
exec env \
  CP_ADDR="$CP_ADDR" \
  AF_RUNTIME="$AF_RUNTIME" \
  ${AF_NATIVE_AGENT_BIN:+AF_NATIVE_AGENT_BIN="$AF_NATIVE_AGENT_BIN"} \
  WS_IMAGE="$WS_IMAGE" \
  CONSOLE_DIR="$ROOT/console/dist" \
  WS_DATA="$WS_DATA" \
  WS_MEMORY="$WS_MEMORY" \
  ${WS_JVM_DIR:+WS_JVM_DIR="$WS_JVM_DIR"} \
  ${AF_DOCS_DIR:+AF_DOCS_DIR="$AF_DOCS_DIR"} \
  ${AUTH:+AUTH="$AUTH"} \
  ${DEV_USER:+DEV_USER="$DEV_USER"} \
  ${AUTH_EMAIL_HEADER:+AUTH_EMAIL_HEADER="$AUTH_EMAIL_HEADER"} \
  ${WS_SESSION_CMD:+WS_SESSION_CMD="$WS_SESSION_CMD"} \
  ${WS_ENV:+WS_ENV="$WS_ENV"} \
  ${GITHUB_OAUTH_CLIENT_ID:+GITHUB_OAUTH_CLIENT_ID="$GITHUB_OAUTH_CLIENT_ID"} \
  ${BITBUCKET_OAUTH_KEY:+BITBUCKET_OAUTH_KEY="$BITBUCKET_OAUTH_KEY"} \
  ${BITBUCKET_OAUTH_SECRET:+BITBUCKET_OAUTH_SECRET="$BITBUCKET_OAUTH_SECRET"} \
  ${PUBLIC_BASE_URL:+PUBLIC_BASE_URL="$PUBLIC_BASE_URL"} \
  ${GOOGLE_OAUTH_CLIENT_ID:+GOOGLE_OAUTH_CLIENT_ID="$GOOGLE_OAUTH_CLIENT_ID"} \
  ${GOOGLE_OAUTH_CLIENT_SECRET:+GOOGLE_OAUTH_CLIENT_SECRET="$GOOGLE_OAUTH_CLIENT_SECRET"} \
  ${AF_COOKIE_SECRET:+AF_COOKIE_SECRET="$AF_COOKIE_SECRET"} \
  ${AF_SESSION_TTL:+AF_SESSION_TTL="$AF_SESSION_TTL"} \
  ${AF_OAUTH_ALLOWED_EMAILS:+AF_OAUTH_ALLOWED_EMAILS="$AF_OAUTH_ALLOWED_EMAILS"} \
  ${AF_OAUTH_ALLOWED_EMAILS_FILE:+AF_OAUTH_ALLOWED_EMAILS_FILE="$AF_OAUTH_ALLOWED_EMAILS_FILE"} \
  ${AF_MASTER_KEY:+AF_MASTER_KEY="$AF_MASTER_KEY"} \
  ${AF_DB:+AF_DB="$AF_DB"} \
  ${AF_PROVISION:+AF_PROVISION="$AF_PROVISION"} \
  ${SUPER_ADMIN_EMAILS:+SUPER_ADMIN_EMAILS="$SUPER_ADMIN_EMAILS"} \
  ${AF_MCP_ENABLED:+AF_MCP_ENABLED="$AF_MCP_ENABLED"} \
  /tmp/af-cp
