#!/usr/bin/env bash
# Agent Fleet — ECS / ecs-ec2 の更新をひとコマンドで出す（README §Upgrade の実行版）。
#
#   VERSION=0.2.0 deploy/aws/ecs/update.sh --profile af-sandbox --region ap-northeast-1
#   VERSION=0.2.0 deploy/aws/ecs/update.sh --profile p --region r --push   # ECR push から
#
# compose 構成の `docker compose pull && docker compose up -d`（deploy/compose/README.md
# §Upgrade）、dev の `deploy/local/run-dev.sh` に当たるもの。runbook を手で叩く分には
# README §Upgrade のままでよいが、手順のうち三つが「知っていないと落とし穴」なので
# スクリプトにしてある:
#
#  1. **タグが ECR に無いまま deploy できてしまう。** CloudFormation は文字列を
#     受け取るだけなので、push を忘れた／別リージョンへ push した更新は「成功」し、
#     そのあと CP タスクが CannotPullContainerError で上がらない。ここでは deploy の
#     前に両方のイメージの存在を確かめて、先に落とす。
#  2. **可変タグ（:dev のサンドボックス運用）では CFN が「変更なし」で何も起きない。**
#     同じタグへ push し直した更新は、テンプレート上の差分がゼロなので CP は古い
#     タスクのまま走り続ける。空チェンジセットを検出して ECS 側で
#     force-new-deployment に落とす。
#  3. **ワークスペースは自動では新しくならない。** adapter はタスク定義を Start ごとに
#     作り直すので、走っているワークスペースは停止→起動まで古いイメージのまま。
#     誰がその状態なのかを最後に一覧で出す（Console 側では同じ事実が
#     WS バーの「要再起動」バッジとして各利用者に出る — control-plane/runtime_ecs_stale.go）。
#
# ワークスペースを勝手に停止することは絶対にしない。停止はセッションを落とす操作で、
# タイミングを選ぶのは利用者本人（ADR: 更新トーストが再起動を促さないのと同じ理由）。
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: VERSION=<v> update.sh --profile <p> --region <r>
                             [--stack <af-ecs-ingress>] [--template <cfn/30-ingress.yaml>]
                             [--push] [--images-tar <B.tar.gz>] [--registry <prefix>]
                             [--force] [--dry-run]
  --profile     aws cli profile (required)
  --region      region of the deployment (required)
  --stack       ingress stack name (default af-ecs-ingress) — the one with ImageTag
  --template    template file (default <script dir>/cfn/30-ingress.yaml)
  --push        run release-ecr.sh first (build must already have produced the images)
  --images-tar  passed through to release-ecr.sh (air-gap B tar)
  --registry    passed through to release-ecr.sh (local image name prefix)
  --force       force a new CP deployment even when CloudFormation reports a change
  --dry-run     print what would happen; touch nothing
EOF
}

VERSION="${VERSION:?set VERSION=<tag> (the ImageTag both images are pushed under)}"
PROFILE=""; REGION=""; STACK="af-ecs-ingress"; TEMPLATE=""
PUSH=0; IMAGES_TAR=""; LOCAL_REGISTRY=""; FORCE=0; DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)    PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)     REGION="${2:?--region needs a value}"; shift ;;
    --stack)      STACK="${2:?--stack needs a value}"; shift ;;
    --template)   TEMPLATE="${2:?--template needs a path}"; shift ;;
    --push)       PUSH=1 ;;
    --images-tar) IMAGES_TAR="${2:?--images-tar needs a path}"; shift ;;
    --registry)   LOCAL_REGISTRY="${2:?--registry needs a value}"; shift ;;
    --force)      FORCE=1 ;;
    --dry-run)    DRY=1 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
if [ -z "$PROFILE" ] || [ -z "$REGION" ]; then usage; exit 2; fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[ -n "$TEMPLATE" ] || TEMPLATE="$HERE/cfn/30-ingress.yaml"
AWS=(aws --profile "$PROFILE" --region "$REGION")

run() {  # dry-run 対応。読み取りは常に実行し、書き込みだけここを通す。
  if [ "$DRY" = 1 ]; then echo "DRY: $*"; return 0; fi
  "$@"
}

# --- 0) push（任意） --------------------------------------------------------
if [ "$PUSH" = 1 ]; then
  args=(--profile "$PROFILE" --region "$REGION")
  [ -n "$IMAGES_TAR" ] && args+=(--images-tar "$IMAGES_TAR")
  [ -n "$LOCAL_REGISTRY" ] && args+=(--registry "$LOCAL_REGISTRY")
  echo "==> release-ecr.sh (VERSION=$VERSION)"
  run env VERSION="$VERSION" "$HERE/release-ecr.sh" "${args[@]}"
fi

# --- 1) タグが本当に ECR にあるか（落とし穴 1） -----------------------------
missing=""
for repo in af-control-plane af-workspace; do
  if ! "${AWS[@]}" ecr describe-images --repository-name "$repo" \
        --image-ids "imageTag=$VERSION" >/dev/null 2>&1; then
    missing="$missing $repo:$VERSION"
  fi
done
if [ -n "$missing" ]; then
  echo "ERROR: not in ECR:$missing" >&2
  echo "       push first (--push, or VERSION=$VERSION $HERE/release-ecr.sh --profile $PROFILE --region $REGION)." >&2
  echo "       Deploying a tag that is not there succeeds, and the CP task then fails to pull." >&2
  exit 1
fi
echo "==> ECR has af-control-plane:$VERSION and af-workspace:$VERSION"

# --- 1b) CP イメージのアーキが CpArch と噛み合っているか（落とし穴 1 の兄弟） -----
# 落とし穴 1 は「タグが無い」。こちらは「タグはあるが、そのアーキが無い」で、症状は
# もっと悪い: CannotPullContainerError ですらなく、ECS はタスクを**配置できない**まま
# desired=1 / running=0 で回り続ける（docs/72）。CpArch=arm64 にできるのは
# publish-dist を control_plane_arm64 で回した版だけで、それは既定 OFF。
# ⚠️ 判定は「証明できたときだけ落とす」。確かめられなかったときに通すのは、
# ここが公開ゲートではなく更新の前検査だからで、AF_CP_ARCH_CHECK=0 で丸ごと外せる。
if [ "${AF_CP_ARCH_CHECK:-1}" = 1 ]; then
  # パラメータ自体が無い旧スタックは x86_64（Fargate が省略時に入れる既定と同じ）。
  cp_arch="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$STACK" \
    --query "Stacks[0].Parameters[?ParameterKey=='CpArch'].ParameterValue" \
    --output text 2>/dev/null || true)"
  case "$cp_arch" in ""|None) cp_arch=x86_64 ;; esac
  want=amd64; [ "$cp_arch" = arm64 ] && want=arm64   # CFN の語彙 → OCI の語彙
  manifest="$("${AWS[@]}" ecr batch-get-image --repository-name af-control-plane \
    --image-ids "imageTag=$VERSION" \
    --accepted-media-types \
      "application/vnd.docker.distribution.manifest.v2+json" \
      "application/vnd.oci.image.manifest.v1+json" \
      "application/vnd.docker.distribution.manifest.list.v2+json" \
      "application/vnd.oci.image.index.v1+json" \
    --query 'images[0].imageManifest' --output text 2>/dev/null || true)"
  case "$manifest" in
    *'"manifests"'*)   # マニフェストリスト＝中身のアーキが読める。ここは断定できる。
      # jq を足さないための読み方（運用者の手元に必ずあるとは限らない）。platform 側の
      # "architecture" しか出てこないので、カンマで割って拾えば十分。
      archs="$(printf '%s' "$manifest" | tr ',' '\n' \
        | sed -n 's/.*"architecture"[[:space:]]*:[[:space:]]*"\([a-z0-9_]*\)".*/\1/p' \
        | sort -u | tr '\n' ' ')"
      echo "==> af-control-plane:$VERSION is an index of: ${archs:-<none read>}"
      case " $archs " in
        *" $want "*) ;;
        *)
          echo "ERROR: CpArch=$cp_arch needs a '$want' entry, and af-control-plane:$VERSION has: ${archs:-<none>}" >&2
          echo "       Deploying this puts the service in desired=1 / running=0 with no pull error to read." >&2
          echo "       Publish with control_plane_arm64, or set CpArch back (docs/72)." >&2
          exit 1 ;;
      esac
      ;;
    *)                 # 単一マニフェスト＝1 アーキ分しか無い。中身は読めない。
      if [ "$want" = arm64 ]; then
        echo "ERROR: CpArch=arm64 but af-control-plane:$VERSION is a SINGLE manifest — it can only" >&2
        echo "       serve one architecture, and this pipeline's single-arch builds are the build" >&2
        echo "       host's (amd64). Re-publish with control_plane_arm64 (docs/72)." >&2
        echo "       Check by hand: crane manifest <ecr>/af-control-plane:$VERSION" >&2
        echo "       Override with AF_CP_ARCH_CHECK=0 if you know better." >&2
        exit 1
      fi
      ;;
  esac
fi

# どのクラスタのどのサービスが CP か。スタックが持っている実体（AWS::ECS::Service の
# physical id ＝ arn:…:service/<cluster>/<name>）から引く。名前の規約
# （`af-${AWS::StackName}-cp`）を書き写すと、スタック名を変えた瞬間に静かに外れて
# 「force も待機も別のサービスに当てて成功した」ことになるため、規約は最後の保険。
CP_ARN="$("${AWS[@]}" cloudformation describe-stack-resource --stack-name "$STACK" \
  --logical-resource-id Service --query 'StackResourceDetail.PhysicalResourceId' \
  --output text 2>/dev/null || true)"
CLUSTER=""; CP_SERVICE=""
case "$CP_ARN" in
  *:service/*/*)                       # arn:aws:ecs:<r>:<a>:service/<cluster>/<name>
    CP_SERVICE="${CP_ARN##*/}"
    rest="${CP_ARN%/*}"
    CLUSTER="${rest##*/}"
    ;;
esac
if [ -z "$CLUSTER" ]; then             # 保険: 20-platform の export ＋ 命名規約
  PLATFORM_STACK="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$STACK" \
    --query "Stacks[0].Parameters[?ParameterKey=='PlatformStackName'].ParameterValue" --output text)"
  CLUSTER="$("${AWS[@]}" cloudformation list-exports \
    --query "Exports[?Name=='${PLATFORM_STACK}-ClusterName'].Value" --output text)"
  CP_SERVICE="af-${STACK}-cp"
fi
if [ -z "$CLUSTER" ] || [ "$CLUSTER" = "None" ] || [ -z "$CP_SERVICE" ]; then
  echo "ERROR: could not resolve the ECS cluster / CP service of stack $STACK" >&2
  exit 1
fi
echo "==> stack=$STACK cluster=$CLUSTER cp-service=$CP_SERVICE"

# --- 2) CFN deploy：ImageTag だけ上書き（他は前回値のまま） -------------------
# `cloudformation deploy` は指定しなかったパラメータを UsePreviousValue で保つので、
# ここに他のパラメータを書き足してはならない（書けば「前回値」を上書きしてしまう）。
echo "==> cloudformation deploy $STACK (ImageTag=$VERSION)"
out=""
if [ "$DRY" = 1 ]; then
  echo "DRY: aws cloudformation deploy --stack-name $STACK --template-file $TEMPLATE --parameter-overrides ImageTag=$VERSION"
else
  set +e
  out="$("${AWS[@]}" cloudformation deploy --stack-name "$STACK" \
    --template-file "$TEMPLATE" --parameter-overrides "ImageTag=$VERSION" \
    --no-fail-on-empty-changeset 2>&1)"
  rc=$?
  set -e
  echo "$out"
  [ $rc -eq 0 ] || exit $rc
fi

# --- 3) 可変タグで「変更なし」だった場合の取りこぼし（落とし穴 2） -----------
# 同じタグへ push し直した更新はテンプレート差分がゼロ。CFN は何もせず成功するので、
# ここで検出して ECS 側の force-new-deployment に落とす（新しいイメージを引き直す）。
if [ "$FORCE" = 1 ] || echo "$out" | grep -qi "No changes to deploy"; then
  echo "==> forcing a new CP deployment (mutable tag / --force): $CP_SERVICE"
  run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" \
    --force-new-deployment >/dev/null
fi

# --- 4) CP が入れ替わるまで待つ ---------------------------------------------
if [ "$DRY" != 1 ]; then
  echo "==> waiting for $CP_SERVICE to stabilise (blue/green behind the ALB)"
  "${AWS[@]}" ecs wait services-stable --cluster "$CLUSTER" --services "$CP_SERVICE"
  echo "==> CP is running the new task definition"
fi

# --- 5) 走っているワークスペースの一覧（落とし穴 3） -------------------------
# ここで停止はしない。誰が古いイメージのまま走っているかを出すだけ。
# list-services はページングを CLI に任せる（--max-items を付けると黙って打ち切られ、
# 「全員見た」ように読めてしまう）。describe-services は 1 回 10 件までなので束ねる。
arns="$("${AWS[@]}" ecs list-services --cluster "$CLUSTER" --query 'serviceArns' --output text 2>/dev/null || true)"
names=""
for arn in $arns; do
  name="${arn##*/}"
  case "$name" in af-ws-*) names="$names $name" ;; esac
done
running=""
# shellcheck disable=SC2086  # word splitting is the batching
set -- $names
while [ $# -gt 0 ]; do
  batch=""
  i=0
  while [ $# -gt 0 ] && [ $i -lt 10 ]; do batch="$batch $1"; shift; i=$((i + 1)); done
  # shellcheck disable=SC2086
  got="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services $batch \
    --query 'services[?desiredCount>`0`].serviceName' --output text 2>/dev/null || true)"
  running="$running $got"
done

# --- 6) ecs-ec2 の golden snapshot（更新でもう一つ静かに古くなるもの） ---------
# 新規ユーザーの home の種。イメージが上がると CP は af-image の不一致で golden を
# 「使わずに空 home を作る」側へ倒す（ADR 0045 決定 9）。壊れはしないが新規の初回起動が
# 目に見えて遅くなり、気づけるのは CP のログだけ — だから更新のたびにここで出す。
#
# 0.9.2 以降、焼き直しは既定で CP がやる（決定 9-1）。それでもここで出すのは、自動焼きが
# 「始まらない」条件が二つあるから: AF_ECS_EC2_GOLDEN_AUTOBAKE=0 と、プールの空きが
# 2 スロット未満。どちらも静かに何も起きないだけなので、直後は必ず古いままに見える
# （数分で追いつく）ことと合わせて、下のメッセージで断り書きにしている。
ACCOUNT="$("${AWS[@]}" sts get-caller-identity --query Account --output text)"
WS_IMAGE="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com/af-workspace:$VERSION"
golden_stale="$("${AWS[@]}" ec2 describe-snapshots --owner-ids self \
  --filters "Name=tag:af-role,Values=golden" \
  --query "Snapshots[?Tags[?Key=='af-image'&&Value!='$WS_IMAGE']].[SnapshotId,Tags[?Key=='af-image']|[0].Value]" \
  --output text 2>/dev/null || true)"

URL="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='Url'].OutputValue" --output text 2>/dev/null || true)"
cat <<EOF

==> done: $STACK is on ImageTag=$VERSION${URL:+  ($URL)}

Console（各利用者のブラウザ）は数分以内に「新しいバージョンがあります」を出し、
リロードだけで新しくなる（セッションは止まらない）。

ワークスペースは自動では入れ替わらない。adapter は Start のたびにタスク定義を作り直す
ので、いま走っているものは停止→起動まで古いイメージのまま走り続ける。該当する利用者の
Console には WS バーの「要再起動」バッジが出る（押すのは本人・停止はセッションを落とす）。

※ バッジが誰にも出ないときは 20-platform が古い。CpTaskRole の ecr:BatchGetImage が
   無いとドリフト判定は「不明」に倒れ、エラーも出さずにバッジだけが消える:
     aws cloudformation deploy --stack-name <platform> --template-file cfn/20-platform.yaml \\
       --capabilities CAPABILITY_NAMED_IAM --profile $PROFILE --region $REGION
EOF
if [ -n "${running// /}" ]; then
  echo "いま走っているワークスペース（停止→起動まで旧イメージ）:"
  for n in $running; do echo "  - $n"; done
else
  echo "いま走っているワークスペースは無し（次の Start から新しいイメージ）。"
fi

if [ -n "${golden_stale// /}" ]; then
  cat <<EOF

⚠️ ecs-ec2: golden snapshot が古い（新規ユーザーの home の種）。CP は一致しない golden を
   使わず空 home を作るので、壊れはしないが**新規の初回起動だけが遅くなり**、気づけるのは
   CP のログだけ（ADR 0045 決定 9）:
$(echo "$golden_stale" | sed 's/^/     /')
   いま走るべき image: $WS_IMAGE

   通常はこのあと数分で CP が自分で焼き直す（決定 9-1）。焼き始めないのは
   AF_ECS_EC2_GOLDEN_AUTOBAKE=0 のときと、プールの空きが 2 スロット未満のとき
   （その 2 つは CP のログが理由を言う）。手で焼くなら bake-golden.sh。
EOF
fi
