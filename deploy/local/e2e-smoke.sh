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

  # Chromium は Debian revision まで固定するため、4桁の upstream version しか
  # 表示しない `chromium --version` だけでなく dpkg の実版も比較する。
  chromium_pkg="$(dpkg-query -W -f='${Version}' chromium 2>/dev/null)"
  chromium_out="$(chromium --version 2>&1)"
  chromium_upstream="${EXPECT_CHROMIUM%%-*}"
  if [ "$chromium_pkg" = "$EXPECT_CHROMIUM" ] && printf '%s' "$chromium_out" | grep -Fq "$chromium_upstream"; then
    echo "ok  chromium $chromium_pkg ($chromium_out)"
  else
    echo "NG  chromium: 実版 ${chromium_pkg:-?} ≠ ピン $EXPECT_CHROMIUM（出力: $chromium_out）"
    fail=1
  fi

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

  # ビルド時ピンの写し versions.json（設定 UI「ツールのバージョン」のピン表示の源）。
  VJ=/usr/local/share/agent-fleet/versions.json
  if [ -f "$VJ" ]; then
    for pair in "claude=$EXPECT_CLAUDE" "opencode=$EXPECT_OPENCODE" "codex=$EXPECT_CODEX" \
                "go=$EXPECT_GO" "gh=$EXPECT_GH" "chromium=$EXPECT_CHROMIUM"; do
      k="${pair%%=*}"; want="${pair#*=}"
      got="$(jq -r ".$k" "$VJ" 2>/dev/null)"
      if [ "$got" = "$want" ]; then echo "ok  versions.json $k=$got"
      else echo "NG  versions.json $k: ${got:-?} ≠ $want"; fail=1; fi
    done
  else
    echo "NG  $VJ が無い"; fail=1
  fi

  # rtk は任意焼き込み（vendor 品）。期待値はホスト側の vendor/ 有無から EXPECT_RTK で渡る。
  if [ "${EXPECT_RTK:-0}" = "1" ]; then
    if command -v rtk >/dev/null; then echo "ok  rtk $(rtk --version 2>/dev/null | semver)"
    else echo "NG  rtk: vendor 済みのはずがイメージに無い"; fail=1; fi
  else
    if command -v rtk >/dev/null; then echo "ok  rtk $(rtk --version 2>/dev/null | semver)（イメージに有・現在の vendor/ は空）"
    else echo "ok  rtk なし（vendor/ 空）"; fi
  fi

  # 既定 USER=dev のまま、永続 home を profile にせず固定の日本語ページを描画する。
  # BrowserManager と同じく headless Chromium 自体が起動できることを image job で見る。
  if [ "$(id -u)" = 1000 ] && [ "$(id -un)" = dev ]; then
    echo "ok  Chromium smoke user=dev(1000)"
  else
    echo "NG  Chromium smoke user=$(id -un)($(id -u))（want dev(1000)）"; fail=1
  fi
  sandbox_mode="$(stat -c '%u:%g:%a' /usr/lib/chromium/chrome-sandbox 2>/dev/null)"
  if [ "$sandbox_mode" = "0:0:4755" ]; then
    echo "ok  Chromium setuid sandbox $sandbox_mode"
  else
    echo "NG  Chromium setuid sandbox: ${sandbox_mode:-missing}（want 0:0:4755）"; fail=1
  fi
  if grep -Eq '^NoNewPrivs:[[:space:]]+0$' /proc/self/status; then
    echo "ok  Docker runtime NoNewPrivs=0（setuid sandbox利用可）"
  else
    echo "NG  Docker runtimeがsetuid sandboxを禁止している: $(grep '^NoNewPrivs:' /proc/self/status)"; fail=1
  fi
  cap_eff="$(awk '/^CapEff:/ { print $2 }' /proc/self/status)"
  cap_bnd="$(awk '/^CapBnd:/ { print $2 }' /proc/self/status)"
  sys_admin_mask=$((1 << 21))
  if (( (16#$cap_eff & sys_admin_mask) == 0 && (16#$cap_bnd & sys_admin_mask) != 0 )); then
    echo "ok  devはSYS_ADMIN effectiveなし、setuid helperのbounding setにはあり"
  else
    echo "NG  SYS_ADMIN capability条件が不正: CapEff=$cap_eff CapBnd=$cap_bnd"; fail=1
  fi
  unexpected_suid="$(find / -xdev -type f -perm /6000 \
    ! -path /usr/lib/chromium/chrome-sandbox -print 2>/dev/null)"
  if [ -z "$unexpected_suid" ]; then
    echo "ok  setuid/setgid executableはChromium helperだけ"
  else
    echo "NG  Chromium helper以外のsetuid/setgid executable: $unexpected_suid"; fail=1
  fi
  profile="$(mktemp -d /tmp/af-chromium-smoke.XXXXXX)"
  page="$profile/page.html"
  screenshot="$profile/page.png"
  printf '%s\n' \
    '<!doctype html><meta charset="utf-8"><style>body{font-family:sans-serif;font-size:32px}</style>' \
    '<title>Agent Fleet Chromium smoke</title><p>Agent Fleet 日本語表示 ✓</p>' > "$page"
  if chromium \
      --headless=new --disable-gpu --disable-dev-shm-usage \
      --user-data-dir="$profile/data" --window-size=800,600 \
      --run-all-compositor-stages-before-draw --virtual-time-budget=1000 \
      --screenshot="$screenshot" "file://$page" >/tmp/af-chromium-smoke.log 2>&1 \
      && [ "$(od -An -tx1 -N8 "$screenshot" 2>/dev/null | tr -d ' \n')" = "89504e470d0a1a0a" ]; then
    echo "ok  Chromium headless 日本語 screenshot"
  else
    echo "NG  Chromium headless screenshot に失敗: $(tail -20 /tmp/af-chromium-smoke.log 2>/dev/null)"
    fail=1
  fi
  if fc-match 'sans-serif:lang=ja' | grep -Fq 'Noto Sans CJK'; then
    echo "ok  Japanese font $(fc-match 'sans-serif:lang=ja')"
  else
    echo "NG  Japanese font: $(fc-match 'sans-serif:lang=ja' 2>&1)"; fail=1
  fi
  # 製品バイナリのpipe CDP経路をsandbox有効のまま実行する。アニメーションする
  # 2 Pageを同時描画し、PageごとのACK pacingが設定fpsを超えないことも確認する。
  if workspace-agent browser-smoke >/tmp/af-browser-manager-smoke.log 2>&1; then
    echo "ok  $(tail -1 /tmp/af-browser-manager-smoke.log)"
  else
    echo "NG  BrowserManager sandbox/2-Page smoke に失敗: $(tail -30 /tmp/af-browser-manager-smoke.log 2>/dev/null)"
    fail=1
  fi
  rm -rf "$profile"
  rm -f /tmp/af-chromium-smoke.log
  rm -f /tmp/af-browser-manager-smoke.log

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
EXPECT_CHROMIUM="$(arg_pin CHROMIUM_VERSION)"
EXPECT_RTK=0; [ -x "$ROOT/workspace/vendor/rtk" ] && EXPECT_RTK=1
SMOKE_MEMORY="${WS_MEMORY:-1g}"

echo "==> image smoke: $IMAGE (claude=$EXPECT_CLAUDE opencode=$EXPECT_OPENCODE codex=$EXPECT_CODEX go=$EXPECT_GO gh=$EXPECT_GH chromium=$EXPECT_CHROMIUM rtk=$EXPECT_RTK)"
exec docker run --rm -i --init --network none --memory "$SMOKE_MEMORY" --cap-add=SYS_ADMIN \
  -e EXPECT_CLAUDE="$EXPECT_CLAUDE" \
  -e EXPECT_OPENCODE="$EXPECT_OPENCODE" \
  -e EXPECT_CODEX="$EXPECT_CODEX" \
  -e EXPECT_GO="$EXPECT_GO" \
  -e EXPECT_GH="$EXPECT_GH" \
  -e EXPECT_CHROMIUM="$EXPECT_CHROMIUM" \
  -e EXPECT_RTK="$EXPECT_RTK" \
  --entrypoint /bin/bash "$IMAGE" -s -- --inner < "${BASH_SOURCE[0]}"
