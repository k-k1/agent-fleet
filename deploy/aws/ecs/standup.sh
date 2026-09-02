#!/usr/bin/env bash
# Agent Fleet — 配備を立てる／立て直す（README §Stack decomposition の順序の実行版）。
#
#   deploy/aws/ecs/standup.sh --profile <p> --region <r>          # 計画と preflight（既定）
#   deploy/aws/ecs/standup.sh --profile <p> --region <r> --yes    # 実行
#
# `capture-env.sh` が書いた環境（スタック名と**各スタックへ渡した引数一式**）から、
# `00 → 10 → 20 → (イメージを ECR へ) → 40 → 30` の順で立てる。teardown.sh の逆。
#
# ## 知っていないと立たないもの
#
#  1. **順序と capability。** 各スタックは前段の export を import するので順序は固定。
#     `10-data` は `Transform: AWS::LanguageExtensions` を宣言しているので
#     **CAPABILITY_AUTO_EXPAND** が要り、`20-platform` と `40-ec2-pool` は名前付き
#     IAM ロールを作るので **CAPABILITY_NAMED_IAM** が要る（無いと即座に拒否される）。
#  2. **ECR は空の状態から始まる。** ECR リポジトリは 20-platform のリソースで
#     `EmptyOnDelete: true` ＝撤収でイメージごと消えている。**20 の後・30 の前**に
#     `crane copy` で入れ直す（`docker pull`+`push` は index を 1 アーキに潰す）。
#  3. **キャプチャした引数のうち 2 つはそのまま使えない。** `Ec2SlotLaunchTemplate` と
#     `Ec2SlotAmiArm64` は**プール層が作った物理 ID**で、立て直せば別の値になる。
#     新しい 40 の出力で上書きする。ここを見落とすと、CFN は成功し、CP は起動して、
#     **スロットだけが二度と起きない**（存在しない launch template を指しているので）。
#  4. **秘密はスタックに入っていない。** SSM SecureString の cookie-secret /
#     master-key / IdP の client secret は CFN の引数ではないので、環境ファイルにも
#     無い。**有るかどうかだけ**先に見て、無ければ立てる前に止める（30 は上がるのに
#     CP がログインを提供できない、が一番わかりにくい壊れ方）。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"

usage() {
  cat >&2 <<'EOF'
usage: standup.sh --profile <p> --region <r> [--yes] [--image-tag <tag>] [--cp-arch <a>]
                  [--from <prefix>] [--dry-run]
  --profile    aws cli profile (this is how a deployment is addressed)
  --region     region to build it in
  --stack      ingress stack name (default af-ecs-ingress)
  --yes        actually deploy (without it: preflight + plan only)
  --image-tag  image tag to run (default: the captured AF_IMAGE_TAG)
  --cp-arch    x86_64 | arm64 — which architecture the Control Plane's own task runs on
               (default: the captured CpArch). ⚠️ arm64 needs a CP image that is a
               two-architecture index at this tag; the check below refuses the pair
  --from       registry to copy the images from (default ghcr.io/k-k1/agent-fleet)
  --dry-run    print every write instead of making it
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; AF_YES=0; AF_DRY=0; TAG=""; CP_ARCH_ARG=""
FROM="ghcr.io/k-k1/agent-fleet"
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)   PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)    REGION="${2:?--region needs a value}"; shift ;;
    --stack)     STACK="${2:?--stack needs a value}"; shift ;;
    --yes)       AF_YES=1 ;;
    --image-tag) TAG="${2:?--image-tag needs a value}"; shift ;;
    --cp-arch)   CP_ARCH_ARG="${2:?--cp-arch needs x86_64|arm64}"; shift ;;
    --from)      FROM="${2:?--from needs a value}"; shift ;;
    --dry-run)   AF_DRY=1 ;;
    -h|--help)   usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }
export AF_YES AF_DRY
af_env_init "$PROFILE" "$REGION" "$STACK"
if [ ! -r "$AF_ENV_DIR/params/30-ingress" ]; then
  echo "ERROR: $AF_ENV_DIR に控えが無い。立てるには**何を渡すか**が要る:" >&2
  echo "         deploy/aws/ecs/capture-env.sh --profile $PROFILE --region $REGION   # 生きているうちに" >&2
  echo "       テンプレートは repo にあるが、そこに渡した引数は配備の中にしか無い。" >&2
  exit 1
fi
: "${TAG:=${AF_IMAGE_TAG:-}}"
[ -n "$TAG" ] || { echo "ERROR: no image tag (env has no AF_IMAGE_TAG; pass --image-tag)" >&2; exit 2; }
# CFN_DIR はテスト（deploy/local/ecs-lifecycle-stub-test.sh）が差し替えられるようにする。
# 51,200 バイトの分岐は**サイズで**決まるので、太らせたテンプレートを食わせて実際に
# --s3-bucket が付くところまで踏ませないと、この経路はまた走らないまま腐る。
CFN_DIR="${AF_STANDUP_CFN_DIR:-$HERE/cfn}"

echo "==> standup plan: ${AF_FQDN:-<from the captured parameters>} (profile=$AF_PROFILE region=$AF_REGION)"
echo "    order : $AF_STACK_NETWORK → $AF_STACK_DATA → $AF_STACK_PLATFORM → images:$TAG${AF_STACK_POOL:+ → $AF_STACK_POOL} → $AF_STACK_INGRESS"

# --- 0) preflight（立て始めてから足りないと分かるのが一番高い） --------------
fail=0
say_missing() { echo "    ✗ $1"; fail=1; }

for slug in 00-network 10-data 20-platform 30-ingress; do
  [ -r "$(af_params_file "$slug")" ] || say_missing "params/$slug が無い（capture-env.sh を走らせたか？）"
done
[ -z "$AF_STACK_POOL" ] || [ -r "$(af_params_file 40-ec2-pool)" ] || say_missing "params/40-ec2-pool が無いのに AF_STACK_POOL=$AF_STACK_POOL"

# ECS のサービスリンクロール。新しいアカウントには無く、Service Connect の既定名前空間
# つきクラスタ作成が "ECS Service Linked Role is not ready" で落ちる。作るのは冪等。
if ! "${AWS[@]}" iam get-role --role-name AWSServiceRoleForECS >/dev/null 2>&1; then
  echo "    · creating AWSServiceRoleForECS (once per account)"
  af_run "${AWS[@]}" iam create-service-linked-role --aws-service-name ecs.amazonaws.com >/dev/null 2>&1 || true
fi

af_read_params 30-ingress
p30() { local p; for p in ${AF_PARAMS[@]+"${AF_PARAMS[@]}"}; do case "$p" in "$1"=*) echo "${p#*=}"; return ;; esac; done; }
SSM_PREFIX="$(p30 SsmPrefix)"; : "${SSM_PREFIX:=/af-cp}"
HOSTED_ZONE="$(p30 HostedZoneId)"
CP_ARCH="${CP_ARCH_ARG:-$(p30 CpArch)}"; : "${CP_ARCH:=x86_64}"
case "$CP_ARCH" in x86_64|arm64) ;; *) echo "--cp-arch takes x86_64|arm64 (got '$CP_ARCH')" >&2; exit 2 ;; esac

have_ssm() { "${AWS[@]}" ssm get-parameter --name "$1" --query 'Parameter.Name' --output text >/dev/null 2>&1; }
# ⚠️ 値は読まない（--with-decryption を付けない）。要るのは「有るか」だけで、
# 秘密をこのプロセスの出力やログに通す理由はひとつも無い。
for s in cookie-secret master-key; do
  have_ssm "$SSM_PREFIX/$s" || say_missing "SSM $SSM_PREFIX/$s が無い（README §30-ingress stand-up の 2）"
done
[ -z "$(p30 GoogleClientId)" ] || have_ssm "$SSM_PREFIX/google-client-secret" || say_missing "SSM $SSM_PREFIX/google-client-secret が無い（GoogleClientId が設定されている）"
[ -z "$(p30 OidcClientId)" ]   || have_ssm "$SSM_PREFIX/oidc-client-secret"   || say_missing "SSM $SSM_PREFIX/oidc-client-secret が無い（OidcClientId が設定されている）"
[ -z "$(p30 GithubClientId)" ] || have_ssm "$SSM_PREFIX/github-client-secret" || say_missing "SSM $SSM_PREFIX/github-client-secret が無い（GithubClientId が設定されている）"

if [ -n "$HOSTED_ZONE" ]; then
  "${AWS[@]}" route53 get-hosted-zone --id "$HOSTED_ZONE" >/dev/null 2>&1 \
    || say_missing "ホストゾーン $HOSTED_ZONE が引けない（ACM の DNS 検証と alias がここに入る）"
fi
command -v crane >/dev/null || say_missing "crane が無い（GHCR → ECR を index ごと運ぶのに要る）"

# ★ 控えのパラメータが**いまのテンプレート**と噛み合うか。
#
# 🔥 これが無いと、00〜20 を立てた**後で** CFN が「必須パラメータが足りない」と拒む —— 立て
# 始めてから足りないと分かるのが一番高い、というこの preflight の趣旨そのものの穴だった。
# 控えは配備を撮った日のもので、テンプレートはそれから育つ（実例: 2026-08-23 の控えと
# 2026-09-02 のテンプレート）。**Default を持たない引数**だけが必須である。
#
# ⚠️ **数え方はインデントの深さを固定しない。** 以前は「2 スペース = パラメータ名 /
# 4 スペース = Default」で読んでいたので、書き方の違うテンプレートを食わせると
# **1 件も拾えず「必須 0 件」で静かに通った**——検査が空振りしていることが誰にも
# 見えない形で、#307 の「走っていない経路は壊れていても分からない」と同型である。
# いまは Parameters: 節に**最初に現れたキーの深さ**をパラメータ名の深さとして採り、
# それより深いキーを属性として読む。そして **Parameters: 節があるのに 1 件も読めな
# かったら異常として言う**（全件が Default 持ちの「必須 0 件」とは区別できる）。
for slug in 00-network 10-data 20-platform 30-ingress 40-ec2-pool; do
  f="$(af_params_file "$slug")"; t="$CFN_DIR/$slug.yaml"
  [ -r "$f" ] && [ -r "$t" ] || continue
  missing=""; has_section=0; parsed=0
  while read -r kind a b; do
    case "$kind" in
      req)  missing="$missing $a" ;;
      stat) has_section="$a"; parsed="$b" ;;
    esac
  done < <(awk '
    # トップレベルのキーで節の出入りを決める（Parameters: 以外は読み飛ばす）。
    /^[A-Za-z]/          { inp = ($0 ~ /^Parameters:/); if (inp) sect = 1; name = ""; ind = 0; next }
    !inp                 { next }
    /^[[:space:]]*(#|$)/ { next }
    {
      match($0, /^ */); w = RLENGTH
      key = substr($0, w + 1)
      if (key !~ /^[A-Za-z0-9_]+:/) next      # リスト要素・ブロックスカラの本文
      sub(/:.*$/, "", key)
      if (!ind) ind = w                       # 最初のキーの深さ = パラメータ名の深さ
      if (w == ind) { name = key; req[name] = 1; total++ }
      else if (key == "Default" && name != "") delete req[name]
    }
    END {
      for (k in req) print "req " k
      print "stat " sect+0 " " total+0
    }
  ' "$t")
  if [ "$has_section" = 1 ] && [ "$parsed" -eq 0 ]; then
    say_missing "$slug.yaml の Parameters: を 1 件も読めなかった（この検査が空振りしている。テンプレートの書き方が変わったか、この awk が古い）"
    continue
  fi
  for k in $missing; do
    grep -q "^$k=" "$f" || say_missing "params/$slug に必須パラメータ $k が無い（テンプレートが控えより新しい）"
  done
done

# ★ テンプレートが 51,200 バイトを超えるなら S3 経由が要る（af_cfn_deploy が切り替える）。
# ここでは「切り替え先が用意できるか」だけを先に言う —— 20-platform を立てた後でしか
# バケットは引けないので、まだ無いのは正常。**af_cfn_deploy は毎回サイズを測る**ので、
# 将来どのテンプレが太っても黙って落ちることはない。
for t in "$CFN_DIR"/*.yaml; do
  sz="$(wc -c < "$t" | tr -d ' ')"
  [ "$sz" -gt "$AF_CFN_TEMPLATE_MAX" ] && echo "    · $(basename "$t") は $sz バイト > $AF_CFN_TEMPLATE_MAX — S3 経由で渡す（20-platform の CfnTemplatesBucket）"
done

# ★ イメージが**両方**そのタグで手に入るか。ここを見ないと 00〜20 を立てたあと、
# 「workspace が GHCR に無い」で止まる。実際にそうなる経路がある: dev-deploy は
# workspace を焼かない回に **ECR 内で再タグ**するだけなので、その dev タグの workspace は
# GHCR に存在しない。撤収で ECR ごと消えた時点で、その実体はどこにも無くなる。
if [ "$AF_DRY" != 1 ] && command -v crane >/dev/null; then
  for pair in "control-plane=af-control-plane" "workspace=af-workspace"; do
    if "${AWS[@]}" ecr describe-images --repository-name "${pair#*=}" --image-ids "imageTag=$TAG" >/dev/null 2>&1; then
      continue    # 既に ECR にある（立て直しの途中からの再実行）
    fi
    crane manifest "$FROM/${pair%%=*}:$TAG" >/dev/null 2>&1 \
      || say_missing "$FROM/${pair%%=*}:$TAG が無い（ECR にも GHCR にも）。dev-image.yml を image=both で焼くか、両方が揃っているタグを --image-tag で指定する"
  done
fi

# arm64 のスロットクラスを宣言しているなら、arm64 の AMI と **arm64 の workspace イメージ**の
# 両方が要る。どちらが欠けても CFN は成功し、症状だけが後から出る（前者は CP が起動を拒否、
# 後者は arm スロットが pull できない）。⚠️ **証明できたときだけ落とす**——イメージのアーキが
# 読めなかったことを理由に止めない。
case "$(p30 Ec2SlotTypes)" in
  *"|arm64|"*)
    [ -n "$(p30 Ec2SlotAmiArm64)" ] || say_missing "Ec2SlotTypes が arm64 クラスを宣言しているのに Ec2SlotAmiArm64 が空（CP が起動を拒否する）"
    if [ "$AF_DRY" != 1 ] && command -v crane >/dev/null; then
      ws_manifest="$(crane manifest "$FROM/workspace:$TAG" 2>/dev/null || true)"
      case "$ws_manifest" in
        "") ;;                       # 読めなかった（GHCR に無い＝別の検査が既に言っている）
        *'"manifests"'*)             # インデックス＝中身のアーキが読める。ここは断定できる
          ws_archs="$(printf '%s' "$ws_manifest" | tr ',' '\n' \
            | sed -n 's/.*"architecture"[[:space:]]*:[[:space:]]*"\([a-z0-9_]*\)".*/\1/p' | sort -u | tr '\n' ' ')"
          case " $ws_archs " in
            *" arm64 "*) ;;
            *) say_missing "Ec2SlotTypes が arm64 クラスを宣言しているのに workspace:$TAG は ${ws_archs}のみ（arm スロットがイメージを引けない）" ;;
          esac ;;
        *)                           # 単一マニフェスト＝1 アーキ分しか無い。この経路の
                                     # 単一ビルドはビルドホストの amd64 である（update.sh と同じ判断）
          say_missing "Ec2SlotTypes が arm64 クラスを宣言しているのに workspace:$TAG は単一マニフェスト（arm スロットがイメージを引けない）。arm クラスを外すか、両アーキで焼いたタグを使う" ;;
      esac
    fi
    ;;
esac

if [ "$fail" = 1 ]; then
  echo ""
  echo "ERROR: 前提が足りない。上の ✗ を潰してから。" >&2
  exit 1
fi
echo "    ✓ preflight"

if ! af_confirm "この配備を立てる（${AF_FQDN:-?}・イメージ $TAG）"; then
  echo ""
  echo "（何もしていない。実行するには --yes）"
  exit 0
fi

# --- 1〜3) 静的基盤 ----------------------------------------------------------
deploy_stack() {  # deploy_stack <stack> <template> <slug> [capability...]
  local stack="$1" tpl="$2" slug="$3"; shift 3
  af_read_params "$slug"
  local caps=()
  [ $# -gt 0 ] && caps=(--capabilities "$@")
  echo "==> deploy $stack ($slug)"
  # dry-run は「何を渡すか」を見せるためのものなので、値は伏せた形で出す
  # （af_run に生の配列を渡すと BitbucketOauthKey のような値が端末とログに残る）。
  if [ "$AF_DRY" = 1 ]; then
    echo "DRY: cloudformation deploy --stack-name $stack --template-file $CFN_DIR/$tpl ${caps[*]+${caps[*]}} \\"
    echo "     --parameter-overrides $(af_params_masked | tr '\n' ' ')"
    return 0
  fi
  af_cfn_deploy "$stack" "$CFN_DIR/$tpl" \
    ${caps[@]+"${caps[@]}"} \
    --parameter-overrides ${AF_PARAMS[@]+"${AF_PARAMS[@]}"} \
    --no-fail-on-empty-changeset
}

deploy_stack "$AF_STACK_NETWORK"  00-network.yaml  00-network
deploy_stack "$AF_STACK_DATA"     10-data.yaml     10-data     CAPABILITY_AUTO_EXPAND
deploy_stack "$AF_STACK_PLATFORM" 20-platform.yaml 20-platform CAPABILITY_NAMED_IAM

# --- 4) イメージ（ECR は空から始まる） ---------------------------------------
ACCOUNT="$("${AWS[@]}" sts get-caller-identity --query Account --output text)"
ECR_HOST="$ACCOUNT.dkr.ecr.$AF_REGION.amazonaws.com"
echo "==> images $TAG -> $ECR_HOST"
if [ "$AF_DRY" != 1 ]; then
  "${AWS[@]}" ecr get-login-password | crane auth login "$ECR_HOST" -u AWS --password-stdin
fi
for pair in "control-plane=af-control-plane" "workspace=af-workspace"; do
  src="$FROM/${pair%%=*}:$TAG"; dst="$ECR_HOST/${pair#*=}:$TAG"
  if "${AWS[@]}" ecr describe-images --repository-name "${pair#*=}" --image-ids "imageTag=$TAG" >/dev/null 2>&1; then
    echo "    · ${pair#*=}:$TAG is already in ECR"
  else
    echo "    · crane copy $src"
    af_run crane copy "$src" "$dst"
  fi
done
# CpArch と CP イメージのアーキが噛み合っているか。⚠️ 噛み合わないと
# CannotPullContainerError ですらなく **desired=1 / running=0 の配置不能**（pull エラーの
# ログすら出ない）。update.sh と同じ規律で、**証明できたときだけ落とす**。
if [ "$AF_DRY" != 1 ]; then
  want=amd64; [ "$CP_ARCH" = arm64 ] && want=arm64
  archs="$(crane manifest "$ECR_HOST/af-control-plane:$TAG" 2>/dev/null \
    | tr ',' '\n' | sed -n 's/.*"architecture"[[:space:]]*:[[:space:]]*"\([a-z0-9_]*\)".*/\1/p' | sort -u | tr '\n' ' ')"
  case " $archs " in
    *" $want "*) echo "    ✓ af-control-plane:$TAG covers $want (CpArch=$CP_ARCH)" ;;
    "  ")        echo "    · af-control-plane:$TAG is a single manifest — assuming the build host's amd64"
                 [ "$want" = arm64 ] && { echo "ERROR: CpArch=arm64 but the image is single-arch" >&2; exit 1; } ;;
    *)           echo "ERROR: CpArch=$CP_ARCH needs '$want' and the image has: $archs" >&2; exit 1 ;;
  esac
fi

# --- 5) プール層（ecs-ec2 のみ） ---------------------------------------------
if [ -n "$AF_STACK_POOL" ]; then
  deploy_stack "$AF_STACK_POOL" 40-ec2-pool.yaml 40-ec2-pool CAPABILITY_NAMED_IAM
fi

# --- 6) ingress（物理 ID を持つ引数は新しい出力で上書き） --------------------
af_read_params 30-ingress
af_param_override ImageTag "$TAG"
# ★ **フラグは検査だけでなく、渡す値そのものを動かさなければ意味が無い。**
# ここを忘れると `--cp-arch arm64` は「arm64 で立てられることを確かめて x86_64 で立てる」
# という、成功して見える最悪の形になる（実測: 立ち上がった CP の runtimePlatform が
# x86_64 だった）。控えに CpArch の行が無い配備もあるので、無ければ足す形で上書きする。
af_param_override CpArch "$CP_ARCH"
if [ -n "$AF_STACK_POOL" ] && [ "$AF_DRY" != 1 ]; then
  lt="$(af_stack_output "$AF_STACK_POOL" SlotLaunchTemplateId)"
  ami="$(af_stack_output "$AF_STACK_POOL" SlotAmiIdArm64)"
  [ -n "$lt" ] || { echo "ERROR: $AF_STACK_POOL に SlotLaunchTemplateId の出力が無い" >&2; exit 1; }
  echo "==> slot launch template: $lt${ami:+ / arm64 ami $ami}"
  af_param_override Ec2SlotLaunchTemplate "$lt"
  # ⚠️ **空で上書きしない。** arm64 AMI はプール層の出力から来るとは限らず、30 へ直接
  # 渡されている配備が実在する（40 の SlotAmiIdArm64 が空のまま、30 の Ec2SlotAmiArm64 に
  # ami-… が入っている）。ここで空を被せると、arm64 クラスを宣言しているのに arm64 AMI が
  # 無い状態になり、**CP は起動を拒否する**（runtime_ecs_ec2.go: "refuses to start when a
  # declared arm64 class has no launch template to run on"）。捕まえた値の方を残す。
  if [ -n "$ami" ]; then
    af_param_override Ec2SlotAmiArm64 "$ami"
  else
    echo "    · $AF_STACK_POOL は arm64 AMI を出力しない — 控えの Ec2SlotAmiArm64 をそのまま使う"
  fi
fi
echo "==> deploy $AF_STACK_INGRESS (30-ingress)"
if [ "$AF_DRY" = 1 ]; then
  echo "DRY: cloudformation deploy --stack-name $AF_STACK_INGRESS --template-file $CFN_DIR/30-ingress.yaml \\"
  echo "     --parameter-overrides $(af_params_masked | tr '\n' ' ')"
else
  af_cfn_deploy "$AF_STACK_INGRESS" "$CFN_DIR/30-ingress.yaml" \
    --parameter-overrides ${AF_PARAMS[@]+"${AF_PARAMS[@]}"} \
    --no-fail-on-empty-changeset
fi

# --- 7) 上がったか -----------------------------------------------------------
URL="$(af_stack_output "$AF_STACK_INGRESS" Url)"
if [ "$AF_DRY" != 1 ]; then
  echo "==> healthz"
  for _ in $(seq 1 30); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "https://$AF_FQDN/healthz" || true)"
    [ "$code" = 200 ] && { echo "    ✓ https://$AF_FQDN/healthz 200"; break; }
    sleep 10
  done
  [ "${code:-}" = 200 ] || cat >&2 <<EOF
⚠️  /healthz がまだ 200 を返さない。よくある順に:
    - ACM の DNS 検証と DNS の伝播（初回は数分）
    - CP タスクが配置できない（CpArch とイメージのアーキ・上の検査を通していれば別）
    - CP が起動時に止まっている: aws logs tail /af/$AF_STACK_INGRESS/cp --follow
EOF
fi

cat <<EOF

==> up: ${URL:-https://$AF_FQDN}  (ImageTag=$TAG)
    次に効くもの:
      - golden はまだ無い。新規ユーザーの初回起動が遅いだけで壊れてはいない（CP が自分で焼く）
      - コスト配分タグは Workspace を 1 つ起こすまで AWS に発見されない（README §Prerequisites）
      - 開発配備なら以後の更新は deploy/aws/ecs/dev-deploy.sh、リリース版なら update.sh
EOF
