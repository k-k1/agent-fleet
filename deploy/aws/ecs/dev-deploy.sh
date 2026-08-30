#!/usr/bin/env bash
# Agent Fleet — 開発配備へ **バージョンタグを打たずに** develop を載せる。
#
#   deploy/aws/ecs/dev-deploy.sh --profile <p> --region <r>
#
# 配備の指し方は `update.sh` と同じ AWS プロファイル。
# **どの配備が開発用かはこのリポジトリに書かない** —— 手元の控え
# （`capture-env.sh` が書く）の `AF_DEV_DEPLOY=1` が印である。
#
# `update.sh` が「リリース版を出す」ひとコマンドなら、これは「いまの develop を出す」
# ひとコマンド。docs/log/72 §72.6.2 が手順として書いた 4 段
# （dev-image.yml を dispatch → CP を ECR へ crane copy → workspace を同じタグに置く →
# update.sh）を 1 本にしたもので、最後は必ず `update.sh` に落ちる —— タグ不在・空チェンジ
# セット・CpArch 不整合・走行中 Workspace の一覧という前検査は、そこにしか無い。
#
# ## ここが引き受ける「知っていないと踏む」もの
#
#  1. **CI が焼くのは origin の ref であって、手元の作業ツリーではない。** ローカルに
#     コミットしていない（あるいは push していない）変更は黙って入らず、デプロイは
#     「成功」する。だからタグの sha は **リモートの ref から**取り、手元がそれと違えば
#     警告する。
#  2. **`ImageTag` は CP と workspace で共有**（ADR 0045 決定 8）。CP だけ焼き直しても
#     workspace 側を同じタグに置かないと、タグ不在で CP タスクが上がらない。ECR 内の
#     `crane copy` は実体を複製しないただの再タグなので、workspace に変更が無い回は
#     それで済ませる —— workspace を焼き直すと QEMU で +593 秒（docs/log/72 §72.5.1）。
#     焼くかどうかは **`workspace/` 配下の差分**で決める（ws イメージのビルドコンテキストは
#     `deploy/compose/release.sh` のとおり `workspace/` だけ）。
#     ⚠️ ビルド引数側（`release.sh` の `BAKE_AGENT_CLIS` など）を触ったときは差分に出ない。
#     そのときは `--image both` を明示すること。
#  3. **`docker pull` + `push` はインデックスを 1 アーキに潰す。** GHCR → ECR は
#     `crane copy` でインデックスごと運ぶ（このコンテナに docker は無い＝crane が前提）。
#  4. **同じ digest の再タグでも golden は焼き直される**（docs/log/72 §72.6.4）。golden の
#     `af-image` タグが持っているのは**参照文字列であって digest ではない**ので、中身が
#     バイト単位で同じでも「新しいタグ用の golden が無い」と判断され、2 アーキ分を
#     約 10 分・スロット 2 本かけて焼き直す。**新旧の digest が一致することを確かめてから**
#     golden の `af-image` を貼り替えて、その 10 分を丸ごと消す。
#     （CP 側を内容同一性で突合するように直せばこの貼り替えは不要になる —— そちらの
#     改修が入ったあとも、文字列タグを実物に合わせ続ける意味でここは残してよい。）
#  5. **実ユーザーが居る配備に当てない。** `ImageTag` を動かすと走っている人に
#     「要再起動」バッジが出る。だから環境ファイルで `AF_DEV_DEPLOY=1` と印を付けた
#     配備にしか当たらない（印は repo の外にあり、間違えて別の配備に向けようがない）。
#
# ## やらないこと
#
# - リリースではない。dist repo にも GitHub Release にも触らず、**forbidden-token ゲートを
#   通っていない**。タグに `-dev` が入らない値は `dev-image.yml` 側が拒否する。
# - Workspace を勝手に停止しない（`update.sh` と同じ理由）。
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: dev-deploy.sh --profile <p> --region <r> [options]

  --profile     aws cli profile (this is how a deployment is addressed)
  --region      region of the deployment
  --stack       ingress stack name (default af-ecs-ingress) — the one with ImageTag
                ⚠️ the deployment must be marked AF_DEV_DEPLOY=1 in the local capture
                (capture-env.sh writes it; see env.sh)
  --ref         git ref to bake (default develop). CI checks out ORIGIN's ref.
  --tag         image tag to use (default <latest tag + patch>-dev-<sha8>). Must contain -dev
  --image       auto | cp | both (default auto — `workspace/` に差分があれば both)
  --platforms   architectures to bake (default linux/amd64,linux/arm64)
  --skip-bake   do not dispatch dev-image.yml; the tag must already be in GHCR
  --rebake      bake even when the tag is already in GHCR
  --dry-run     print what would happen; touch nothing
  -h, --help

env:
  AF_DEV_GHCR   GHCR prefix (default ghcr.io/k-k1/agent-fleet)
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; REF="develop"; TAG=""
IMAGE="auto"; PLATFORMS="linux/amd64,linux/arm64"
SKIP_BAKE=0; REBAKE=0; DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)    PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)     REGION="${2:?--region needs a value}"; shift ;;
    --stack)      STACK="${2:?--stack needs a value}"; shift ;;
    --ref)        REF="${2:?--ref needs a value}"; shift ;;
    --tag)        TAG="${2:?--tag needs a value}"; shift ;;
    --image)      IMAGE="${2:?--image needs auto|cp|both}"; shift ;;
    --platforms)  PLATFORMS="${2:?--platforms needs a value}"; shift ;;
    --skip-bake)  SKIP_BAKE=1 ;;
    --rebake)     REBAKE=1 ;;
    --dry-run)    DRY=1 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }
case "$IMAGE" in auto|cp|both) ;; *) echo "--image takes auto|cp|both (got '$IMAGE')" >&2; exit 2 ;; esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"
af_env_init "$PROFILE" "$REGION" "$STACK"
GHCR="${AF_DEV_GHCR:-ghcr.io/k-k1/agent-fleet}"
GIT=(git -C "$ROOT")

run() { if [ "$DRY" = 1 ]; then echo "DRY: $*"; return 0; fi; "$@"; }

for tool in gh crane aws git; do
  command -v "$tool" >/dev/null || { echo "ERROR: $tool is not installed" >&2; exit 1; }
done

# --- 1) デプロイ先を確かめる（落とし穴 5） ----------------------------------
# ★ 「開発配備かどうか」は**環境ファイルの印**で決める。スクリプトに特定の FQDN を
# 書けば、それは配備の身元をリポジトリに残すことでもある（このリポジトリは公開）。
if [ "${AF_DEV_DEPLOY:-0}" != 1 ]; then
  echo "ERROR: この配備（$PROFILE / $REGION）は開発配備として印が付いていない。" >&2
  echo "       ImageTag を動かすのは、そこで走っている人に「要再起動」バッジを出す操作である。" >&2
  echo "       開発配備なら:" >&2
  echo "         deploy/aws/ecs/capture-env.sh --profile $PROFILE --region $REGION" >&2
  echo "         \$EDITOR $AF_ENV_DIR/env   # AF_DEV_DEPLOY=1" >&2
  exit 1
fi
params="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$STACK" \
  --query 'Stacks[0].Parameters[].[ParameterKey,ParameterValue]' --output text)"
param() { echo "$params" | awk -v k="$1" '$1==k {sub(/^[^\t]*\t/,""); print; exit}'; }
FQDN="$(param Fqdn)"; CUR_TAG="$(param ImageTag)"; CP_ARCH="$(param CpArch)"; WS_RUNTIME="$(param WsRuntime)"
[ -n "$CUR_TAG" ] || { echo "ERROR: stack $STACK has no ImageTag parameter" >&2; exit 1; }
echo "==> target: $FQDN (stack=$STACK, now ImageTag=$CUR_TAG, CpArch=${CP_ARCH:-x86_64}, WsRuntime=${WS_RUNTIME:-ecs})"

# --- 2) 焼く commit（落とし穴 1） -------------------------------------------
# **リモートの ref** が正。CI は `actions/checkout` で origin の ref を取るので、ここで
# 手元の HEAD を使うと「手元のコードをデプロイしたつもり」になる。
SHA="$("${GIT[@]}" ls-remote origin "refs/heads/$REF" | awk '{print $1; exit}')"
[ -n "$SHA" ] || { echo "ERROR: origin has no branch '$REF'" >&2; exit 1; }
SHORT="${SHA:0:8}"
"${GIT[@]}" fetch --quiet origin "$REF" || true   # 差分を取るためにオブジェクトだけ手元へ
if [ -n "$("${GIT[@]}" status --porcelain)" ] || [ "$("${GIT[@]}" rev-parse HEAD)" != "$SHA" ]; then
  echo "⚠️  焼くのは origin/$REF ($SHORT)。この作業ツリーの HEAD や未コミットの変更は入らない。"
fi

if [ -z "$TAG" ]; then
  base="$("${GIT[@]}" tag --sort=-v:refname | head -1)"; base="${base#v}"
  if [ -n "$base" ]; then
    IFS=. read -r maj min pat <<EOF
$base
EOF
    base="${maj:-0}.${min:-0}.$(( ${pat:-0} + 1 ))"
  else
    base="0.0.1"
  fi
  TAG="$base-dev-$SHORT"
fi
case "$TAG" in
  *-dev*) ;;
  *) echo "ERROR: '$TAG' does not contain '-dev'. dev-image.yml refuses it, and a release tag must never be overwritten from here." >&2; exit 1 ;;
esac
echo "==> tag: $TAG (origin/$REF = $SHA)"

# --- 3) workspace も焼くか（落とし穴 2） ------------------------------------
# 判定の基準は「いま走っているタグの commit」。dev タグなら末尾の sha、リリースタグなら
# `v<semver>` の指す commit。どちらでも無い（＝手元に無い）ときは自動判定を諦めて明示を促す。
if [ "$IMAGE" = auto ]; then
  case "$CUR_TAG" in
    *-dev-*) base_sha="${CUR_TAG##*-dev-}" ;;
    *)       base_sha="v$CUR_TAG" ;;
  esac
  if "${GIT[@]}" cat-file -e "${base_sha}^{commit}" 2>/dev/null; then
    changed="$("${GIT[@]}" diff --name-only "${base_sha}^{commit}" "$SHA" -- workspace/ | head -5)"
    if [ -n "$changed" ]; then
      IMAGE=both
      echo "==> workspace/ changed since $CUR_TAG — baking BOTH images (+~10min, QEMU):"
      while IFS= read -r f; do [ -n "$f" ] && echo "     $f"; done <<EOF
$changed
EOF
    else
      IMAGE="cp"
      echo "==> workspace/ unchanged since $CUR_TAG — baking the control-plane only, re-tagging the workspace image"
    fi
  else
    echo "ERROR: cannot resolve what '$CUR_TAG' was built from ($base_sha is not in this clone)," >&2
    echo "       so 'is the workspace image still current?' cannot be answered. Say it: --image cp | both" >&2
    exit 1
  fi
fi

# --- 4) 焼く（dev-image.yml） -----------------------------------------------
have_ghcr() { crane digest "$GHCR/$1:$TAG" >/dev/null 2>&1; }
need_bake=1
if [ "$SKIP_BAKE" = 1 ]; then
  need_bake=0
elif [ "$REBAKE" != 1 ] && have_ghcr control-plane && { [ "$IMAGE" = cp ] || have_ghcr workspace; }; then
  echo "==> $TAG is already in GHCR — skipping the bake (--rebake to force)"
  need_bake=0
fi
if [ "$need_bake" = 1 ]; then
  wf_image=control-plane; [ "$IMAGE" = both ] && wf_image=both
  echo "==> gh workflow run dev-image.yml (tag=$TAG, image=$wf_image, platforms=$PLATFORMS, ref=$REF)"
  run gh -R "$("${GIT[@]}" remote get-url origin | sed -E 's#.*github\.com[:/]##; s#\.git$##')" \
    workflow run dev-image.yml --ref "$REF" \
    -f tag="$TAG" -f image="$wf_image" -f platforms="$PLATFORMS"
  if [ "$DRY" != 1 ]; then
    # dispatch は run id を返さないので、`run-name: dev-image <tag>` で自分の run を探す
    # （dev-image.yml が run-name を出すのはこのため）。タグは一意なので取り違えない。
    run_id=""
    for _ in $(seq 1 30); do
      sleep 4
      run_id="$(gh run list --workflow dev-image.yml --limit 20 \
        --json databaseId,displayTitle,headSha \
        --jq "[.[] | select(.displayTitle | contains(\"$TAG\"))] | first | .databaseId" 2>/dev/null || true)"
      [ -n "$run_id" ] && [ "$run_id" != "null" ] && break
    done
    [ -n "$run_id" ] && [ "$run_id" != "null" ] || {
      echo "ERROR: could not find the dev-image run for $TAG (look at the Actions tab)" >&2; exit 1; }
    echo "==> watching run $run_id"
    gh run watch "$run_id" --exit-status --interval 20
    # 焼いたのが本当にその commit か。dispatch と checkout の間に develop が動いていれば
    # 別物が焼かれている（タグの sha は嘘になる）。
    got_sha="$(gh run view "$run_id" --json headSha --jq .headSha)"
    [ "$got_sha" = "$SHA" ] || {
      echo "ERROR: the run built $got_sha but the tag says $SHA — origin/$REF moved. Re-run." >&2; exit 1; }
  fi
fi

# --- 5) ECR へ運ぶ（落とし穴 3） --------------------------------------------
ACCOUNT="$("${AWS[@]}" sts get-caller-identity --query Account --output text)"
ECR_HOST="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com"
ecr_digest() {  # repo tag -> digest ("" when absent)
  "${AWS[@]}" ecr describe-images --repository-name "$1" --image-ids "imageTag=$2" \
    --query 'imageDetails[0].imageDigest' --output text 2>/dev/null || true
}
echo "==> crane auth login $ECR_HOST"
if [ "$DRY" != 1 ]; then
  "${AWS[@]}" ecr get-login-password | crane auth login "$ECR_HOST" -u AWS --password-stdin
fi
echo "==> crane copy control-plane (index and all)"
run crane copy "$GHCR/control-plane:$TAG" "$ECR_HOST/af-control-plane:$TAG"

WS_OLD="$ECR_HOST/af-workspace:$CUR_TAG"
WS_NEW="$ECR_HOST/af-workspace:$TAG"
ws_same_content=0
if [ "$IMAGE" = both ]; then
  echo "==> crane copy workspace (index and all)"
  run crane copy "$GHCR/workspace:$TAG" "$WS_NEW"
else
  # ECR 内の再タグ。blob は既にそこにあるので実体は複製されない。
  echo "==> re-tag the workspace image $CUR_TAG -> $TAG (same bytes)"
  run crane copy "$WS_OLD" "$WS_NEW"
  ws_same_content=1
fi

# --- 6) golden を貼り替える（落とし穴 4） -----------------------------------
# **digest が一致するときだけ**。ここは「中身が同じだと確かめられたから、同じ golden を
# 使ってよい」と言っているのであって、タグを揃えているのではない。
if [ "${WS_RUNTIME:-}" = "ecs-ec2" ] && [ "$ws_same_content" = 1 ]; then
  old_d="$(ecr_digest af-workspace "$CUR_TAG")"
  new_d="$(ecr_digest af-workspace "$TAG")"
  if [ "$DRY" = 1 ]; then
    echo "DRY: would re-stamp af-image on golden snapshots ($WS_OLD -> $WS_NEW) if the digests match"
  elif [ -n "$old_d" ] && [ "$old_d" = "$new_d" ]; then
    ids="$("${AWS[@]}" ec2 describe-snapshots --owner-ids self \
      --filters "Name=tag:af-role,Values=golden" "Name=tag:af-image,Values=$WS_OLD" \
      --query 'Snapshots[].SnapshotId' --output text 2>/dev/null || true)"
    if [ -n "${ids// /}" ]; then
      echo "==> re-stamping af-image on golden snapshots (same digest $new_d): $ids"
      # shellcheck disable=SC2086  # word splitting is the list
      "${AWS[@]}" ec2 create-tags --resources $ids --tags "Key=af-image,Value=$WS_NEW"
      echo "    （これをしないと CP は同一内容の home を約 10 分・スロット 2 本で焼き直す）"
    else
      echo "==> no golden stamped with $WS_OLD — nothing to re-stamp"
    fi
  else
    echo "⚠️  workspace の digest が一致しない（old=$old_d new=$new_d）。golden は貼り替えない。"
  fi
fi

# --- 7) 反映（前検査・force・待機・走行中 WS の一覧は update.sh の仕事） -----
echo "==> update.sh (VERSION=$TAG)"
args=(--profile "$PROFILE" --region "$REGION" --stack "$STACK")
if [ "$DRY" = 1 ]; then
  # ⚠️ update.sh の最初の検査は「そのタグが ECR にあるか」で、dry-run では**本当に
  # 押していない**のだから当然無い。ここで落ちるのは筋書きどおりなので、そう言って
  # 続ける（黙って exit 1 すると、段取りの確認が失敗に見える）。
  echo "    （dry-run なので ECR にタグは無い。update.sh の前検査がそれを言うのは正常）"
  args+=(--dry-run)
  VERSION="$TAG" "$HERE/update.sh" "${args[@]}" || true
else
  VERSION="$TAG" "$HERE/update.sh" "${args[@]}"
fi

if [ "$DRY" = 1 ]; then
  cat <<EOF

==> dry-run: 何も変えていない。上の DRY: 行が実際に走る操作。
EOF
else
  cat <<EOF

==> dev deploy: $FQDN は origin/$REF ($SHORT) で走っている（ImageTag=$TAG）
    CP のログで確かめる: control-plane $TAG on 0.0.0.0:...
EOF
fi
