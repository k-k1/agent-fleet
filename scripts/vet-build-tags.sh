#!/usr/bin/env bash
# build tag 付きのファイルを vet する。カレントの Go モジュールに対して実行する。
#
# ⚠️ **build tag 付きのファイルは既定ビルドに入らないので、gofmt / go vet / go build /
# go test のどれも触らない。CI の 6 ジョブも同じで、参照が腐っても緑のまま通る。**
# 実測 2026-09-02: 回収 PR #322 が opencode_contract_test.go に不要 import を残したが、
# ワーカー側の全ゲートと CI 6/6 が緑で、`go vet -tags clicontract` だけが exit 1 になった。
# タグ付きテストの**実行**には実 CLI バイナリが要るので CI では回せないが、
# **vet は型検査だけなので走る**。
#
# 使い方: scripts/vet-build-tags.sh <既知のタグ...>
#   既知のタグを 1 つも渡さない = 「このモジュールにタグ付きは無いはず」の宣言。
#   ソースにタグが現れたら赤くなる（＝増えたことに誰かが気付く）。
set -euo pipefail

known=("$@")

# プラットフォーム語（GOOS/GOARCH）は vet の対象にしてはいけない。
# ⚠️ ここを手書きにすると、`//go:build riscv64` のファイルが 1 枚増えた日に「未知のタグ」で
# 赤くなり、**直し方として known に足すという最悪の手**（linux で `go vet -tags riscv64` を
# 回す＝プラットフォーム限定ファイルを無理に取り込む）へ人を誘導する。だから Go 自身に列挙させる。
mapfile -t platform < <(go tool dist list | tr '/' '\n' | sort -u)

# ツールチェーンが定義するタグも同じ理由で対象外。`goexperiment.*` と `go1.*` は前置一致。
toolchain=(cgo gc gccgo race msan asan purego boringcrypto unix ignore)

is_excluded() {
  local t=$1
  case "$t" in goexperiment.*|go1.*) return 0 ;; esac
  local x
  for x in "${platform[@]}" "${toolchain[@]}"; do [ "$t" = "$x" ] && return 0; done
  return 1
}

# ⚠️ 抽出は「識別子の形」で弾かない。`^[a-z][a-z_]*$` のような形の allowlist は、
# **数字やドットを含むタグ（newtag2 / goexperiment.arenas）を「未知」ではなく黙って落とす**
# ＝「知らないものは落とす」設計の唯一の穴になる（レビュワーが PR #323 で実測）。
# 広く拾って、除外は上の 2 つの表だけで行う。
# ⚠️ `|| true` が要る: 1 件も残らないと pipefail でパイプライン全体が exit 1 になり、
# **理由の出ない赤**になる（タグ 0 件のモジュールでは必ず起きる）。
found=$(grep -rhE '^//go:build ' --include='*.go' . 2>/dev/null \
  | sed 's|^//go:build ||' \
  | tr ' ' '\n' | tr -d '()!' \
  | grep -E '^[A-Za-z0-9_.]+$' \
  | grep -vE '^(&&|\|\|)$' \
  | sort -u || true)

unknown=0
for t in $found; do
  is_excluded "$t" && continue
  hit=0
  for k in ${known[@]+"${known[@]}"}; do [ "$t" = "$k" ] && hit=1 && break; done
  if [ "$hit" -eq 0 ]; then
    echo "::error::未知の build tag '$t' がソースに在る。scripts/vet-build-tags.sh の呼び出しに足すこと" >&2
    unknown=1
  fi
done
[ "$unknown" -eq 0 ] || exit 1

for t in ${known[@]+"${known[@]}"}; do
  echo "== go vet -tags $t"
  go vet -tags "$t" ./...
done
