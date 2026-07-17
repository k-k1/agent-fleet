#!/usr/bin/env bash
# CLI 版ドリフト検知（P0）。
#
# なぜ要るか: workspace/Dockerfile の ARG ピンは「焼き込み時」にしか効かない。
# AF_AGENT_SELF_UPDATE_ALLOWED=1 かつ AF_AGENT_SELF_UPDATE=1 の Workspace は
# entrypoint.sh が起動毎に `npm i -g <cli>@latest` するので、**実フリートはピンより
# 先の版を走らせる**。一方 CI（e2e.yml）は build-args を渡さないため常にピン版の
# イメージを検証する ＝ 検証対象と本番が別物。
#
# 実際これで痛い目を見ている: claude の TUI フッタ文字列に依存する状態検出
# （workspace/agent/internal/tmuxx）が 2026-07-17 時点で 3 回壊れ、3 回とも CI は緑の
# まま人力で発見された（詳細は internal/tmuxx/testdata/footers/SOURCE.txt）。
#
# このスクリプトは「何が壊れたか」までは分からない。分かるのは **見に行くべき時** で、
# それが分かるだけでも現状（誰も新版に気づかない）よりは大きく前進する。
# 実際の破壊検知は実 CLI を走らせる契約テスト（P1）の仕事。
#
# 使い方:
#   deploy/local/cli-drift-check.sh            # 全 CLI を照合
#   deploy/local/cli-drift-check.sh claude     # 1 つだけ
# 終了コード: 0=ピンと latest が一致 / 1=ドリフトあり / 2=実行エラー（取得失敗等）
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKERFILE="$ROOT/workspace/Dockerfile"

# name|ARG 名|npm パッケージ
TARGETS=(
  "claude|CLAUDE_CODE_VERSION|@anthropic-ai/claude-code"
  "opencode|OPENCODE_VERSION|opencode-ai"
  "codex|CODEX_VERSION|@openai/codex"
)

arg_pin() {
  local v
  v="$(sed -n "s/^ARG $1=//p" "$DOCKERFILE" | head -1)"
  [ -n "$v" ] || return 1
  printf '%s' "$v"
}

want="${1:-}"
drift=0
errors=0
# GitHub Actions の Job Summary（あれば）に出す表。無ければ /dev/null。
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/null}"
{
  echo "## CLI 版ドリフト"
  echo
  echo "| CLI | ピン (Dockerfile ARG) | npm latest | |"
  echo "|---|---|---|---|"
} >> "$SUMMARY"

printf '%-10s %-14s %-14s %s\n' "CLI" "PIN" "LATEST" ""
for t in "${TARGETS[@]}"; do
  IFS='|' read -r name arg pkg <<< "$t"
  [ -n "$want" ] && [ "$want" != "$name" ] && continue

  if ! pin="$(arg_pin "$arg")"; then
    printf '%-10s %s\n' "$name" "ERROR: ARG $arg が $DOCKERFILE に無い"
    errors=1
    continue
  fi
  # npm view はネットワーク断で空を返しうる。空＝ドリフト無しと誤判定しないよう分ける。
  if ! latest="$(npm view "$pkg" version 2>/dev/null)" || [ -z "$latest" ]; then
    printf '%-10s %-14s %-14s %s\n' "$name" "$pin" "?" "ERROR: npm view $pkg 失敗"
    echo "| $name | \`$pin\` | ? | ⚠ 取得失敗 |" >> "$SUMMARY"
    errors=1
    continue
  fi

  if [ "$pin" = "$latest" ]; then
    printf '%-10s %-14s %-14s %s\n' "$name" "$pin" "$latest" "ok"
    echo "| $name | \`$pin\` | \`$latest\` | ✅ 一致 |" >> "$SUMMARY"
  else
    printf '%-10s %-14s %-14s %s\n' "$name" "$pin" "$latest" "DRIFT"
    echo "| $name | \`$pin\` | \`$latest\` | 🔸 ドリフト |" >> "$SUMMARY"
    drift=1
  fi
done

[ "$errors" = 1 ] && exit 2
if [ "$drift" = 1 ]; then
  cat >> "$SUMMARY" <<'EOF'

**self-update を有効にした Workspace は上記 latest を走らせています**（CI が検証しているのは
ピン版）。上流の破壊が紛れていないか確認してください:

1. 実機で `claude --version` を見て実効版を確認する（ピンを信じない）。
2. 状態検出のフッタ契約を再確認する — `workspace/agent/internal/tmuxx/testdata/footers/SOURCE.txt`
   の手順で実ペインを取り直し、コーパスと diff する。
3. 問題なければ Dockerfile の ARG を bump（＝ CI の検証対象を実フリートに追いつかせる）。
EOF
  echo
  echo "ドリフトあり: 実フリート（self-update 有効）は latest を走らせています。"
  echo "状態検出のフッタ契約を再確認してください（internal/tmuxx/testdata/footers/SOURCE.txt）。"
  exit 1
fi
exit 0
