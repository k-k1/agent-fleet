#!/usr/bin/env bash
# L1 イメージスモーク: ビルド済み Workspace イメージの焼き込み内容を検証する。
#
# 目玉は「CLI の実版 = Dockerfile の ARG ピン」のアサート。未ピン時代に
# 「npm レイヤがキャッシュに当たり、再ビルドしても CLI が古いまま」という事故が
# あったため、ビルド直後にここで機械検出する（期待値は Dockerfile の ARG を parse
# するので、bump してもこのスクリプトの更新は不要）。
#
# 使い方:
#   deploy/local/e2e-smoke.sh [image]     # 既定 agent-fleet/workspace:dev（WS_IMAGE でも可）
# run-dev.sh がイメージビルド直後に自動実行する（WS_SMOKE=0 でスキップ）。
#
# 仕組み: スクリプト自身を `docker run -i ... bash -s -- --inner < $0` でコンテナへ
# 流し込む（イメージにスクリプトを含めず・bind mount も不要）。--inner はコンテナ内
# 実行パスで、期待値は env で受け取る。entrypoint は通さない（seed や自己更新を
# 走らせず、焼き込み状態そのものを見る）。
set -euo pipefail

# ---- inner: コンテナ内で実行される検証本体 -------------------------------
if [ "${1:-}" = "--inner" ]; then
  set +e
  fail=0
  semver() { grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1; }
  # check_ver <名前> <期待版> <コマンド...> : 出力から semver を抜いて期待値と比較
  check_ver() {
    local name="$1" expected="$2" out ver; shift 2
    if ! out="$("$@" 2>&1)"; then
      echo "NG  $name: コマンド失敗: $out"; fail=1; return
    fi
    ver="$(printf '%s' "$out" | semver)"
    if [ "$ver" = "$expected" ]; then
      echo "ok  $name $ver"
    else
      echo "NG  $name: 実版 ${ver:-?} ≠ ピン $expected（出力: $out）"; fail=1
    fi
  }
  check_file() {  # check_file <f|d|x> <path>
    local mode="$1" path="$2"
    if test -"$mode" "$path"; then echo "ok  $path"; else echo "NG  $path が無い"; fail=1; fi
  }

  check_ver claude   "$EXPECT_CLAUDE"   claude --version
  check_ver opencode "$EXPECT_OPENCODE" opencode --version
  check_ver codex    "$EXPECT_CODEX"    codex --version
  check_ver go       "$EXPECT_GO"       /usr/local/go/bin/go version
  check_ver gh       "$EXPECT_GH"       /usr/local/libexec/gh --version

  check_file x /usr/local/bin/workspace-agent
  check_file x /usr/local/bin/entrypoint.sh
  check_file x /usr/local/bin/gh                # 透過認証ラッパー（実体は libexec）
  check_file f /etc/claude-code/CLAUDE.md
  check_file f /etc/tmux.conf
  check_file d /usr/local/share/agent-fleet/opencode-plugin

  command -v tmux >/dev/null && echo "ok  $(tmux -V 2>/dev/null)" \
    || { echo "NG  tmux が無い"; fail=1; }
  [ "${DISABLE_AUTOUPDATER:-}" = "1" ] && echo "ok  DISABLE_AUTOUPDATER=1" \
    || { echo "NG  DISABLE_AUTOUPDATER が 1 でない"; fail=1; }

  # rtk は任意焼き込み（vendor 品）。期待値はホスト側の vendor/ 有無から EXPECT_RTK で渡る。
  if [ "${EXPECT_RTK:-0}" = "1" ]; then
    if command -v rtk >/dev/null; then echo "ok  rtk $(rtk --version 2>/dev/null | semver)"
    else echo "NG  rtk: vendor 済みのはずがイメージに無い"; fail=1; fi
  else
    if command -v rtk >/dev/null; then echo "ok  rtk $(rtk --version 2>/dev/null | semver)（イメージに有・現在の vendor/ は空）"
    else echo "ok  rtk なし（vendor/ 空）"; fi
  fi

  [ "$fail" = 0 ] && echo "== smoke OK ==" || echo "== smoke FAILED ==" >&2
  exit "$fail"
fi

# ---- outer: 期待値を Dockerfile から拾って docker run --------------------
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE="${1:-${WS_IMAGE:-agent-fleet/workspace:dev}}"
DOCKERFILE="$ROOT/workspace/Dockerfile"

arg_pin() {
  local v
  v="$(sed -n "s/^ARG $1=//p" "$DOCKERFILE" | head -1)"
  [ -n "$v" ] || { echo "ERROR: ARG $1 が $DOCKERFILE に無い" >&2; exit 1; }
  printf '%s' "$v"
}
EXPECT_CLAUDE="$(arg_pin CLAUDE_CODE_VERSION)"
EXPECT_OPENCODE="$(arg_pin OPENCODE_VERSION)"
EXPECT_CODEX="$(arg_pin CODEX_VERSION)"
EXPECT_GO="$(arg_pin GO_VERSION)"
EXPECT_GH="$(arg_pin GH_VERSION)"
EXPECT_RTK=0; [ -x "$ROOT/workspace/vendor/rtk" ] && EXPECT_RTK=1

echo "==> image smoke: $IMAGE (claude=$EXPECT_CLAUDE opencode=$EXPECT_OPENCODE codex=$EXPECT_CODEX go=$EXPECT_GO gh=$EXPECT_GH rtk=$EXPECT_RTK)"
exec docker run --rm -i --network none --memory 512m \
  -e EXPECT_CLAUDE="$EXPECT_CLAUDE" \
  -e EXPECT_OPENCODE="$EXPECT_OPENCODE" \
  -e EXPECT_CODEX="$EXPECT_CODEX" \
  -e EXPECT_GO="$EXPECT_GO" \
  -e EXPECT_GH="$EXPECT_GH" \
  -e EXPECT_RTK="$EXPECT_RTK" \
  --entrypoint /bin/bash "$IMAGE" -s -- --inner < "${BASH_SOURCE[0]}"
