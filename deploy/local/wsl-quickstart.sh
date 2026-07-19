#!/usr/bin/env bash
# WSL / 個人検証むけ即起動ランチャ。認証もテナントも無しの単一ユーザー(AUTH=dev)で
# Control Plane をホスト起動し、ワークスペースはローカル Docker(AF_RUNTIME=local)で回す。
# run-dev.sh のこの環境固有の下ごしらえ(rtk の host vendoring 等)を外し、代わりに
#   - rtk はワークスペースイメージのビルド時にコンテナ内へ取得(--build-arg BAKE_RTK=1)
#   - JDK は共有 bind-mount(WS_JVM_DIR)で提供(既定)。WS_JDK=0 で焼かず、必要時に
#     コンテナ内で `workspace-agent install-jdk <major>` する運用にもできる。
# セットアップ手順(native dockerd 導入・依存)は docs/dev/11-wsl-quickstart.md。
#
# 使い方:
#   deploy/local/wsl-quickstart.sh          # ビルドして http://localhost:8099 で起動
#   WS_JDK=0 deploy/local/wsl-quickstart.sh # 共有 JDK provision を省く(オンデマンド導入)
#   WS_SMOKE=0 deploy/local/wsl-quickstart.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

WS_IMAGE="${WS_IMAGE:-agent-fleet/workspace:wsl}"
CP_ADDR="${CP_ADDR:-:8099}"
WS_DATA="${WS_DATA:-$HOME/.local/share/agent-fleet}"
WS_MEMORY="${WS_MEMORY:-5g}"
WS_JVM_DIR="${WS_JVM_DIR:-$WS_DATA/shared/jvm}"
AF_DOCS_DIR="${AF_DOCS_DIR:-$ROOT/docs}"
RTK_VERSION="${RTK_VERSION:-0.43.0}"   # workspace/Dockerfile の既定と揃える
WS_JDK="${WS_JDK:-1}"                    # 1=共有JDKをprovisionしてbind-mount / 0=省く

# git-provider OAuth 設定（秘密を含むため git-ignore）。あれば読み込み、GitHub の
# デバイスフロー用 client_id 等を CP 経由でワークスペースへ渡す。ひな型は
# deploy/local/oauth.env.example。デバイスフローは GITHUB_OAUTH_CLIENT_ID だけでよい
# （client_id は非秘密）。この WSL プリセットは単一ユーザー固定なので AUTH は常に dev
# のまま（oauth.env が AUTH=oauth を指定していても採用しない）。
OAUTH_ENV="$ROOT/deploy/local/oauth.env"
if [ -f "$OAUTH_ENV" ]; then
  set -a; . "$OAUTH_ENV"; set +a
  gh_state="未設定"; [ -n "${GITHUB_OAUTH_CLIENT_ID:-}" ] && gh_state="設定あり"
  echo "==> loaded $OAUTH_ENV（GitHub device flow client_id: $gh_state）"
fi

echo "==> preflight（WSL / native docker 前提）"
fail=0
# Docker daemon に届くか（Docker Desktop でなく WSL 内 native dockerd を推奨）。
if ! docker info >/dev/null 2>&1; then
  echo "  ✗ docker daemon に接続できない。WSL 内で dockerd を起動し、$USER を docker グループに入れてください" >&2
  echo "     （例: sudo service docker start / sudo usermod -aG docker $USER 後に再ログイン）" >&2
  fail=1
else
  echo "  ✓ docker: $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo ok)"
fi
# cgroup v2（メモリ上限 --memory とリソース表示が依存）。
cgt="$(stat -fc %T /sys/fs/cgroup 2>/dev/null || echo unknown)"
if [ "$cgt" = "cgroup2fs" ]; then
  echo "  ✓ cgroup v2"
else
  echo "  ! cgroup が v2 でない（$cgt）。メモリ上限やリソース表示が期待通りに出ない場合があります" >&2
fi
# Go（CP をホストビルド）。
export PATH="$HOME/.local/go/bin:/usr/local/go/bin:$HOME/go/bin:$PATH"
if command -v go >/dev/null 2>&1; then
  echo "  ✓ go: $(go version | awk '{print $3}')"
else
  echo "  ✗ go が無い。Go を入れて PATH に通してください（https://go.dev/dl/）" >&2
  fail=1
fi
# Node/npm（console の vite ビルド）。nvm があれば読み込む。
export NVM_DIR="${NVM_DIR:-$HOME/.nvm}"
# shellcheck disable=SC1091
[ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh" >/dev/null 2>&1 || true
if command -v npm >/dev/null 2>&1; then
  echo "  ✓ npm: $(npm --version)"
else
  echo "  ✗ npm/node が無い。Node（nvm 推奨）を入れてください" >&2
  fail=1
fi
[ "$fail" = 0 ] || { echo "preflight 失敗。上記を解消してから再実行してください。" >&2; exit 1; }

# JDK: 共有 provision（一度だけ・冪等）。WS_JDK=0 なら省き、必要時にコンテナ内で
# `workspace-agent install-jdk <major>` するか Console のツール選択に任せる。
JVM_MOUNT_ARGS=()
if [ "$WS_JDK" = "1" ]; then
  echo "==> provision shared JDKs into $WS_JVM_DIR（初回のみ・重い）"
  bash "$ROOT/deploy/local/provision-jvm.sh" "$WS_JVM_DIR" || echo "WARN: jvm provision 失敗（java は install-jdk で後入れ可）"
  JVM_MOUNT_ARGS=(WS_JVM_DIR="$WS_JVM_DIR")
else
  echo "==> WS_JDK=0: 共有 JDK provision を省略（必要時に workspace-agent install-jdk <major>）"
fi

echo "==> build workspace image ($WS_IMAGE)  rtk=$RTK_VERSION を同梱"
docker build \
  --build-arg BAKE_RTK=1 \
  --build-arg "RTK_VERSION=$RTK_VERSION" \
  -t "$WS_IMAGE" "$ROOT/workspace"

if [ "${WS_SMOKE:-1}" = "1" ]; then
  bash "$ROOT/deploy/local/e2e-smoke.sh" "$WS_IMAGE"
fi

echo "==> build console (vite)"
( cd "$ROOT/console" && { [ -d node_modules ] || npm ci; } && NODE_OPTIONS="--max-old-space-size=3072" npm run build )

echo "==> build control-plane"
( cd "$ROOT/control-plane" && go build -o /tmp/af-cp . )

echo "==> control-plane on $CP_ADDR  (console: http://${CP_ADDR/#:/localhost:})  auth=dev / runtime=local"
exec env \
  CP_ADDR="$CP_ADDR" \
  AUTH=dev \
  AF_RUNTIME=local \
  WS_IMAGE="$WS_IMAGE" \
  CONSOLE_DIR="$ROOT/console/dist" \
  WS_DATA="$WS_DATA" \
  WS_MEMORY="$WS_MEMORY" \
  AF_DOCS_DIR="$AF_DOCS_DIR" \
  "${JVM_MOUNT_ARGS[@]}" \
  ${GITHUB_OAUTH_CLIENT_ID:+GITHUB_OAUTH_CLIENT_ID="$GITHUB_OAUTH_CLIENT_ID"} \
  ${BITBUCKET_OAUTH_KEY:+BITBUCKET_OAUTH_KEY="$BITBUCKET_OAUTH_KEY"} \
  ${BITBUCKET_OAUTH_SECRET:+BITBUCKET_OAUTH_SECRET="$BITBUCKET_OAUTH_SECRET"} \
  ${PUBLIC_BASE_URL:+PUBLIC_BASE_URL="$PUBLIC_BASE_URL"} \
  /tmp/af-cp
