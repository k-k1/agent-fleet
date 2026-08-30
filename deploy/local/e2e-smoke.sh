#!/usr/bin/env bash
# L1 image smoke test: verify what is baked into a built Workspace image.
#
# The centerpiece is asserting "actual CLI version = Dockerfile ARG pin". In the
# unpinned days we had an incident where the npm layer hit the build cache and a
# rebuilt image still shipped stale CLIs, so this detects that mechanically right
# after a build (expected values are parsed from the Dockerfile ARGs, so bumping
# a pin needs no update to this script).
#
# Usage:
#   deploy/local/e2e-smoke.sh [image]     # default agent-fleet/workspace:dev (or WS_IMAGE)
# run-dev.sh runs this automatically right after the image build (skip with WS_SMOKE=0).
#
# How it works: the script pipes itself into the container via
# `docker run -i ... bash -s -- --inner < $0` (no script baked into the image, no
# bind mount needed). --inner is the in-container execution path; expected values
# arrive via env. The entrypoint is bypassed (no seeding or self-update — inspect
# the baked state as-is).
set -euo pipefail

# ---- inner: verification body that runs inside the container --------------
if [ "${1:-}" = "--inner" ]; then
  set +e
  fail=0
  semver() { grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1; }
  # check_ver <name> <expected> <cmd...>: extract semver from output, compare to expected
  check_ver() {
    local name="$1" expected="$2" out ver; shift 2
    if ! out="$("$@" 2>&1)"; then
      echo "NG  $name: command failed: $out"; fail=1; return
    fi
    ver="$(printf '%s' "$out" | semver)"
    if [ "$ver" = "$expected" ]; then
      echo "ok  $name $ver"
    else
      echo "NG  $name: actual ${ver:-?} != pin $expected (output: $out)"; fail=1
    fi
  }
  check_file() {  # check_file <f|d|x> <path>
    local mode="$1" path="$2"
    if test -"$mode" "$path"; then echo "ok  $path"; else echo "NG  $path missing"; fail=1; fi
  }

  if [ "${EXPECT_AGENT_CLIS:-1}" = "1" ]; then
    check_ver claude   "$EXPECT_CLAUDE"   claude --version
    check_ver opencode "$EXPECT_OPENCODE" opencode --version
    check_ver codex    "$EXPECT_CODEX"    codex --version
    check_ver copilot  "$EXPECT_COPILOT"  copilot --version
    # cursor の版数は日付+sha 文字列（2026.07.20-8cc9c0b）で semver でない — check_ver は
    # semver 抽出で -sha 接尾辞を落とすので、--version 出力を丸ごとピンと突き合わせる。
    cursor_out="$(cursor-agent --disable-auto-update --version 2>&1 | head -1)"
    if [ "$cursor_out" = "$EXPECT_CURSOR" ]; then echo "ok  cursor $cursor_out"
    else echo "NG  cursor: actual ${cursor_out:-?} != pin $EXPECT_CURSOR"; fail=1; fi
    # kiro is baked only under BAKE_AGENT_CLIS=1 (like cursor/agy); when the agent
    # CLIs are baked it must be present at the pinned version.
    check_ver kiro "$EXPECT_KIRO" kiro-cli --version
  else
    # Lean distribution variant (BAKE_AGENT_CLIS=0, docs/log/35 §35.7.1-7): verify the
    # agent CLIs really are absent (= we do not redistribute proprietary CLIs).
    # Whether the pinned versions are installable is covered by the versions.json
    # all-pins check (below) and the separate P1-gate boot-install run (needs network).
    for c in claude opencode codex copilot cursor-agent agy rtk kiro-cli; do
      if command -v "$c" >/dev/null 2>&1; then
        echo "NG  lean: $c is baked in (expected BAKE_AGENT_CLIS=0)"; fail=1
      else
        echo "ok  lean: $c absent"
      fi
    done
  fi
  check_ver go       "$EXPECT_GO"       /usr/local/go/bin/go version
  check_ver gh       "$EXPECT_GH"       /usr/local/libexec/gh --version

  # Chromium is pinned down to the Debian revision, so compare the dpkg version too —
  # `chromium --version` only prints the 4-part upstream version.
  chromium_pkg="$(dpkg-query -W -f='${Version}' chromium 2>/dev/null)"
  chromium_out="$(chromium --version 2>&1)"
  chromium_upstream="${EXPECT_CHROMIUM%%-*}"
  if [ "$chromium_pkg" = "$EXPECT_CHROMIUM" ] && printf '%s' "$chromium_out" | grep -Fq "$chromium_upstream"; then
    echo "ok  chromium $chromium_pkg ($chromium_out)"
  else
    echo "NG  chromium: actual ${chromium_pkg:-?} != pin $EXPECT_CHROMIUM (output: $chromium_out)"
    fail=1
  fi

  check_file x /usr/local/bin/workspace-agent
  check_file x /usr/local/bin/entrypoint.sh
  check_file x /usr/local/bin/gh                # transparent-auth wrapper (real binary in libexec)
  check_file f /etc/claude-code/CLAUDE.md
  check_file f /etc/tmux.conf
  check_file d /usr/local/share/agent-fleet/opencode-plugin

  command -v tmux >/dev/null && echo "ok  $(tmux -V 2>/dev/null)" \
    || { echo "NG  tmux missing"; fail=1; }
  [ "${DISABLE_AUTOUPDATER:-}" = "1" ] && echo "ok  DISABLE_AUTOUPDATER=1" \
    || { echo "NG  DISABLE_AUTOUPDATER is not 1"; fail=1; }

  # versions.json mirrors the build-time pins (source of the pin display in the
  # settings UI "tool versions"; on the lean variant, the versions boot-install /
  # on-demand install fetches). All pins must be listed regardless of the BAKE
  # knobs (docs/log/35 §35.7.1-5/-7).
  VJ=/usr/local/share/agent-fleet/versions.json
  if [ -f "$VJ" ]; then
    for pair in "claude=$EXPECT_CLAUDE" "opencode=$EXPECT_OPENCODE" "codex=$EXPECT_CODEX" "copilot=$EXPECT_COPILOT" \
                "cursor=$EXPECT_CURSOR" "kiro=$EXPECT_KIRO" \
                "agy=$EXPECT_AGY" "agy_build=$EXPECT_AGY_BUILD" "rtk=$EXPECT_RTK_VER" \
                "go=$EXPECT_GO" "gh=$EXPECT_GH" "chromium=$EXPECT_CHROMIUM" \
                "chromium_cft=$EXPECT_CHROMIUM_CFT" \
                "chromium_dl=$EXPECT_CHROMIUM_DL" "noto_cjk=$EXPECT_NOTO_CJK" \
                "mcp_grafana=$EXPECT_MCP_GRAFANA" \
                "cloudwatch_mcp=$EXPECT_CLOUDWATCH_MCP" "aws_mcp_proxy=$EXPECT_AWS_MCP_PROXY" \
                "awscli=$EXPECT_AWSCLI" \
                "session_manager_plugin=$EXPECT_SMP"; do
      k="${pair%%=*}"; want="${pair#*=}"
      got="$(jq -r ".$k" "$VJ" 2>/dev/null)"
      if [ "$got" = "$want" ]; then echo "ok  versions.json $k=$got"
      else echo "NG  versions.json $k: ${got:-?} != $want"; fail=1; fi
    done
    # agy_sha256 depends on the image arch (verification value for boot-install;
    # docs/log/35 §35.4.1).
    case "$(dpkg --print-architecture)" in
      amd64) agy_sha_want="$EXPECT_AGY_SHA_X64" ;;
      arm64) agy_sha_want="$EXPECT_AGY_SHA_ARM64" ;;
      *)     agy_sha_want="" ;;
    esac
    got="$(jq -r .agy_sha256 "$VJ" 2>/dev/null)"
    if [ -n "$agy_sha_want" ] && [ "$got" = "$agy_sha_want" ]; then
      echo "ok  versions.json agy_sha256=$got"
    else
      echo "NG  versions.json agy_sha256: ${got:-?} != ${agy_sha_want:-?}"; fail=1
    fi
    # cursor_sha256 も arch 依存の焼き込み値（boot-install の検証材料）。
    case "$(dpkg --print-architecture)" in
      amd64) cursor_sha_want="$EXPECT_CURSOR_SHA_X64" ;;
      arm64) cursor_sha_want="$EXPECT_CURSOR_SHA_ARM64" ;;
      *)     cursor_sha_want="" ;;
    esac
    got="$(jq -r .cursor_sha256 "$VJ" 2>/dev/null)"
    if [ -n "$cursor_sha_want" ] && [ "$got" = "$cursor_sha_want" ]; then
      echo "ok  versions.json cursor_sha256=$got"
    else
      echo "NG  versions.json cursor_sha256: ${got:-?} != ${cursor_sha_want:-?}"; fail=1
    fi
    # kiro_sha256 も arch 依存の焼き込み値（on-demand install-kiro の検証材料）。
    # x86_64=gnu 版 sha、aarch64=musl 版 sha（install-kiro が arch でアセットを選ぶのと対）。
    case "$(dpkg --print-architecture)" in
      amd64) kiro_sha_want="$EXPECT_KIRO_SHA_X64" ;;
      arm64) kiro_sha_want="$EXPECT_KIRO_SHA_ARM64" ;;
      *)     kiro_sha_want="" ;;
    esac
    got="$(jq -r .kiro_sha256 "$VJ" 2>/dev/null)"
    if [ -n "$kiro_sha_want" ] && [ "$got" = "$kiro_sha_want" ]; then
      echo "ok  versions.json kiro_sha256=$got"
    else
      echo "NG  versions.json kiro_sha256: ${got:-?} != ${kiro_sha_want:-?}"; fail=1
    fi
  else
    echo "NG  $VJ missing"; fail=1
  fi

  # rtk is baked in by default (BAKE_RTK=1). Pass EXPECT_RTK=0 only when verifying
  # an air-gapped BAKE_RTK=0 build. The lean variant (EXPECT_AGENT_CLIS=0) is
  # covered by the absence check above.
  if [ "${EXPECT_AGENT_CLIS:-1}" = "1" ]; then
    if [ "${EXPECT_RTK:-1}" = "1" ]; then
      if command -v rtk >/dev/null; then echo "ok  rtk $(rtk --version 2>/dev/null | semver)"
      # ⚠️ Absent WITH a reason is a pass, absent without one is not. On arm64 upstream
      # ships no runnable binary for this base image (docs/log/70 §70.9.2), and the build
      # records that instead of shipping something that cannot start — but "rtk quietly
      # stopped being baked" must still fail, so the marker is what tells them apart.
      elif [ -f /usr/local/share/agent-fleet/rtk-unavailable ]; then
        echo "ok  rtk absent: $(cat /usr/local/share/agent-fleet/rtk-unavailable)"
      else echo "NG  rtk: should always be baked in but is missing from the image"; fail=1; fi
    else
      if command -v rtk >/dev/null; then echo "ok  rtk $(rtk --version 2>/dev/null | semver) (present in image)"
      else echo "ok  rtk absent (BAKE_RTK=0 build)"; fi
    fi
  fi

  # As the default USER=dev, render a fixed Japanese page without turning the
  # persistent home into a profile. The image job checks that headless Chromium
  # itself can start, the same way BrowserManager does.
  if [ "$(id -u)" = 1000 ] && [ "$(id -un)" = dev ]; then
    echo "ok  Chromium smoke user=dev(1000)"
  else
    echo "NG  Chromium smoke user=$(id -un)($(id -u)) (want dev(1000))"; fail=1
  fi
  sandbox_mode="$(stat -c '%u:%g:%a' /usr/lib/chromium/chrome-sandbox 2>/dev/null)"
  if [ "$sandbox_mode" = "0:0:4755" ]; then
    echo "ok  Chromium setuid sandbox $sandbox_mode"
  else
    echo "NG  Chromium setuid sandbox: ${sandbox_mode:-missing} (want 0:0:4755)"; fail=1
  fi
  if grep -Eq '^NoNewPrivs:[[:space:]]+0$' /proc/self/status; then
    echo "ok  Docker runtime NoNewPrivs=0 (setuid sandbox usable)"
  else
    echo "NG  Docker runtime forbids the setuid sandbox: $(grep '^NoNewPrivs:' /proc/self/status)"; fail=1
  fi
  cap_eff="$(awk '/^CapEff:/ { print $2 }' /proc/self/status)"
  cap_bnd="$(awk '/^CapBnd:/ { print $2 }' /proc/self/status)"
  sys_admin_mask=$((1 << 21))
  if (( (16#$cap_eff & sys_admin_mask) == 0 && (16#$cap_bnd & sys_admin_mask) != 0 )); then
    echo "ok  dev lacks effective SYS_ADMIN; setuid helper's bounding set has it"
  else
    echo "NG  unexpected SYS_ADMIN capability state: CapEff=$cap_eff CapBnd=$cap_bnd"; fail=1
  fi
  unexpected_suid="$(find / -xdev -type f -perm /6000 \
    ! -path /usr/lib/chromium/chrome-sandbox -print 2>/dev/null)"
  if [ -z "$unexpected_suid" ]; then
    echo "ok  the Chromium helper is the only setuid/setgid executable"
  else
    echo "NG  setuid/setgid executables other than the Chromium helper: $unexpected_suid"; fail=1
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
    echo "ok  Chromium headless Japanese-text screenshot"
  else
    echo "NG  Chromium headless screenshot failed: $(tail -20 /tmp/af-chromium-smoke.log 2>/dev/null)"
    fail=1
  fi
  if fc-match 'sans-serif:lang=ja' | grep -Fq 'Noto Sans CJK'; then
    echo "ok  Japanese font $(fc-match 'sans-serif:lang=ja')"
  else
    echo "NG  Japanese font: $(fc-match 'sans-serif:lang=ja' 2>&1)"; fail=1
  fi
  # Exercise the product binary's pipe-CDP path with the sandbox enabled. Render two
  # animating Pages at once and confirm per-Page ACK pacing stays within the set fps.
  if workspace-agent browser-smoke >/tmp/af-browser-manager-smoke.log 2>&1; then
    echo "ok  $(tail -1 /tmp/af-browser-manager-smoke.log)"
  else
    echo "NG  BrowserManager sandbox/2-Page smoke failed: $(tail -30 /tmp/af-browser-manager-smoke.log 2>/dev/null)"
    fail=1
  fi
  rm -rf "$profile"
  rm -f /tmp/af-chromium-smoke.log
  rm -f /tmp/af-browser-manager-smoke.log

  [ "$fail" = 0 ] && echo "== smoke OK ==" || echo "== smoke FAILED ==" >&2
  exit "$fail"
fi

# ---- outer: read expected values from the Dockerfile, then docker run -----
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE="${1:-${WS_IMAGE:-agent-fleet/workspace:dev}}"
DOCKERFILE="$ROOT/workspace/Dockerfile"

arg_pin() {
  local v
  v="$(sed -n "s/^ARG $1=//p" "$DOCKERFILE" | head -1)"
  [ -n "$v" ] || { echo "ERROR: ARG $1 not found in $DOCKERFILE" >&2; exit 1; }
  printf '%s' "$v"
}
EXPECT_CLAUDE="$(arg_pin CLAUDE_CODE_VERSION)"
EXPECT_OPENCODE="$(arg_pin OPENCODE_VERSION)"
EXPECT_CODEX="$(arg_pin CODEX_VERSION)"
EXPECT_COPILOT="$(arg_pin COPILOT_VERSION)"
EXPECT_CURSOR="$(arg_pin CURSOR_VERSION)"
EXPECT_CURSOR_SHA_X64="$(arg_pin CURSOR_SHA256_X64)"
EXPECT_CURSOR_SHA_ARM64="$(arg_pin CURSOR_SHA256_ARM64)"
EXPECT_KIRO="$(arg_pin KIRO_VERSION)"
EXPECT_KIRO_SHA_X64="$(arg_pin KIRO_SHA256_X64)"
EXPECT_KIRO_SHA_ARM64="$(arg_pin KIRO_SHA256_ARM64)"
EXPECT_AGY="$(arg_pin AGY_VERSION)"
EXPECT_AGY_BUILD="$(arg_pin AGY_RELEASE_BUILD)"
EXPECT_AGY_SHA_X64="$(arg_pin AGY_SHA256_X64)"
EXPECT_AGY_SHA_ARM64="$(arg_pin AGY_SHA256_ARM64)"
EXPECT_RTK_VER="$(arg_pin RTK_VERSION)"
EXPECT_GO="$(arg_pin GO_VERSION)"
EXPECT_GH="$(arg_pin GH_VERSION)"
EXPECT_CHROMIUM="$(arg_pin CHROMIUM_VERSION)"
EXPECT_CHROMIUM_CFT="$(arg_pin CHROMIUM_CFT_VERSION)"
EXPECT_CHROMIUM_DL="$(arg_pin CHROMIUM_DL_VERSION)"
EXPECT_NOTO_CJK="$(arg_pin NOTO_CJK_VERSION)"
EXPECT_MCP_GRAFANA="$(arg_pin MCP_GRAFANA_VERSION)"
EXPECT_CLOUDWATCH_MCP="$(arg_pin CLOUDWATCH_MCP_VERSION)"
EXPECT_AWS_MCP_PROXY="$(arg_pin AWS_MCP_PROXY_VERSION)"
EXPECT_AWSCLI="$(arg_pin AWSCLI_VERSION)"
EXPECT_SMP="$(arg_pin SESSION_MANAGER_PLUGIN_VERSION)"
EXPECT_RTK="${EXPECT_RTK:-1}" # default = always baked in; pass 0 only to verify a BAKE_RTK=0 build
# Pass 0 when verifying a lean distribution variant image (BAKE_AGENT_CLIS=0;
# docs/log/35 §35.7.1-7 — verification switches to CLI absence + versions.json listing all pins).
EXPECT_AGENT_CLIS="${EXPECT_AGENT_CLIS:-1}"
SMOKE_MEMORY="${WS_MEMORY:-1g}"

echo "==> image smoke: $IMAGE (agent_clis=$EXPECT_AGENT_CLIS claude=$EXPECT_CLAUDE opencode=$EXPECT_OPENCODE codex=$EXPECT_CODEX copilot=$EXPECT_COPILOT cursor=$EXPECT_CURSOR go=$EXPECT_GO gh=$EXPECT_GH chromium=$EXPECT_CHROMIUM rtk=$EXPECT_RTK)"
exec docker run --rm -i --init --network none --memory "$SMOKE_MEMORY" --cap-add=SYS_ADMIN \
  -e EXPECT_CLAUDE="$EXPECT_CLAUDE" \
  -e EXPECT_OPENCODE="$EXPECT_OPENCODE" \
  -e EXPECT_CODEX="$EXPECT_CODEX" \
  -e EXPECT_COPILOT="$EXPECT_COPILOT" \
  -e EXPECT_CURSOR="$EXPECT_CURSOR" \
  -e EXPECT_CURSOR_SHA_X64="$EXPECT_CURSOR_SHA_X64" \
  -e EXPECT_CURSOR_SHA_ARM64="$EXPECT_CURSOR_SHA_ARM64" \
  -e EXPECT_KIRO="$EXPECT_KIRO" \
  -e EXPECT_KIRO_SHA_X64="$EXPECT_KIRO_SHA_X64" \
  -e EXPECT_KIRO_SHA_ARM64="$EXPECT_KIRO_SHA_ARM64" \
  -e EXPECT_AGY="$EXPECT_AGY" \
  -e EXPECT_AGY_BUILD="$EXPECT_AGY_BUILD" \
  -e EXPECT_AGY_SHA_X64="$EXPECT_AGY_SHA_X64" \
  -e EXPECT_AGY_SHA_ARM64="$EXPECT_AGY_SHA_ARM64" \
  -e EXPECT_RTK_VER="$EXPECT_RTK_VER" \
  -e EXPECT_GO="$EXPECT_GO" \
  -e EXPECT_GH="$EXPECT_GH" \
  -e EXPECT_CHROMIUM="$EXPECT_CHROMIUM" \
  -e EXPECT_CHROMIUM_CFT="$EXPECT_CHROMIUM_CFT" \
  -e EXPECT_CHROMIUM_DL="$EXPECT_CHROMIUM_DL" \
  -e EXPECT_NOTO_CJK="$EXPECT_NOTO_CJK" \
  -e EXPECT_MCP_GRAFANA="$EXPECT_MCP_GRAFANA" \
  -e EXPECT_CLOUDWATCH_MCP="$EXPECT_CLOUDWATCH_MCP" \
  -e EXPECT_AWS_MCP_PROXY="$EXPECT_AWS_MCP_PROXY" \
  -e EXPECT_AWSCLI="$EXPECT_AWSCLI" \
  -e EXPECT_SMP="$EXPECT_SMP" \
  -e EXPECT_RTK="$EXPECT_RTK" \
  -e EXPECT_AGENT_CLIS="$EXPECT_AGENT_CLIS" \
  --entrypoint /bin/bash "$IMAGE" -s -- --inner < "${BASH_SOURCE[0]}"
