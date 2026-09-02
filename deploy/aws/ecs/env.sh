# shellcheck shell=bash
# Agent Fleet — 配備を `--profile` / `--region` で指すための共有ライブラリ。source して使う。
#
#   standup.sh / teardown.sh / pause.sh / dev-deploy.sh / capture-env.sh が読む。
#
# ## 配備の指し方は「AWS プロファイル」である
#
# `update.sh` と `release-ecr.sh` が最初からそうしているのと同じ形にする。人が別名を
# 発明する層は要らない——プロファイルは既に「どのアカウントの、どの権限で」を持って
# いて、しかも `~/.aws/config` にある＝**リポジトリの外**である。
#
# ## それでも手元に置くものが 1 つだけある: 立て直すための引数
#
# 実体（スタック名・FQDN・プール層があるか）は**生きている配備から引ける**ので、
# 畳む・眠らせる・載せるは `--profile/--region` だけで足りる。足りないのは
# **立て直す**ときで、そのときは何も生きていない:
#
#   テンプレートは repo にあるが、**そのテンプレートに何を渡したかは配備の中にしか
#   無く、`delete-stack` を出した瞬間に読めなくなる。**
#
# だから `capture-env.sh` が引数一式を `~/.config/agent-fleet/deploy/<profile>.<region>/`
# へ書き出す。⚠️ **repo の外に置く**のは、中身がアカウント固有だから（ホストゾーン ID・
# 許可メールアドレス・OAuth のクライアント ID）——このリポジトリは公開である。
#
# ## 秘密は入らない
#
# SSM SecureString の 3 つ（`<prefix>/cookie-secret` `<prefix>/master-key` と IdP の
# client secret）は CFN の引数ではないので**キャプチャもしないし復元もしない**。
# `standup.sh` は「有るか」だけを見る。

# af_state_root — 立て直し用の控えの置き場。
af_state_root() { echo "${AF_DEPLOY_STATE_DIR:-$HOME/.config/agent-fleet/deploy}"; }

# af_state_dir — この配備の控え。**名前は人が決めない**（プロファイルとリージョン、
# 既定でないときだけ ingress スタック名から導く）。
af_state_dir() {
  local key="$AF_PROFILE.$AF_REGION"
  [ "$AF_STACK_INGRESS" = "af-ecs-ingress" ] || key="$key.$AF_STACK_INGRESS"
  echo "$(af_state_root)/$key"
}

# af_env_init <profile> <region> <ingress-stack> — 配備を解決する。
#
# ★ **生きている配備が正**。スタック名も FQDN も永続性も、そこから引く（別名や規約に
# 書き写すと、名前を変えた瞬間に静かに別のものを触る）。生きていない＝立て直しの
# 場面だけ、キャプチャした控えに落ちる。
af_env_init() {
  AF_PROFILE="${1:?--profile is required}"
  AF_REGION="${2:?--region is required}"
  AF_STACK_INGRESS="${3:-af-ecs-ingress}"
  AWS=(aws --profile "$AF_PROFILE" --region "$AF_REGION")
  AF_ENV_DIR="$(af_state_dir)"
  AF_LIVE=0

  if af_stack_exists "$AF_STACK_INGRESS"; then
    AF_LIVE=1
    AF_FQDN="$(af_stack_param "$AF_STACK_INGRESS" Fqdn)"
    AF_STACK_NETWORK="$(af_stack_param "$AF_STACK_INGRESS" NetworkStackName)"
    AF_STACK_DATA="$(af_stack_param "$AF_STACK_INGRESS" DataStackName)"
    AF_STACK_PLATFORM="$(af_stack_param "$AF_STACK_INGRESS" PlatformStackName)"
    AF_WS_RUNTIME="$(af_stack_param "$AF_STACK_INGRESS" WsRuntime)"
    AF_IMAGE_TAG="$(af_stack_param "$AF_STACK_INGRESS" ImageTag)"
    AF_STACK_POOL="$(af_pool_stack)"
    AF_PERSISTENCE="$(af_stack_param "${AF_STACK_DATA:-af-ecs-data}" Persistence)"
  elif [ -r "$AF_ENV_DIR/env" ]; then
    # 生きていない＝立て直し。ここでだけ控えを読む。
    #
    # ⚠️ **生きているときに控えを読んではいけない。** 控えは古くなる（タグを動かした、
    # スタックを差し替えた）ので、上書きされた瞬間に「実物ではない配備」に対して
    # 実行することになる。生きている配備の値は常に AWS が正である。
    # shellcheck disable=SC1091  # the operator's own captured file
    . "$AF_ENV_DIR/env"
  fi
  # 開発配備の印だけは、生きていても控えから読む（AWS 側には無い情報なので）。
  # ⚠️ source ではなく 1 行の照合にするのは、生きている配備の値を控えで塗り替えないため。
  AF_DEV_DEPLOY=0
  if [ -r "$AF_ENV_DIR/env" ] && grep -q '^AF_DEV_DEPLOY=1' "$AF_ENV_DIR/env"; then
    AF_DEV_DEPLOY=1
  fi
  AF_STACK_NETWORK="${AF_STACK_NETWORK:-af-ecs-network}"
  AF_STACK_DATA="${AF_STACK_DATA:-af-ecs-data}"
  AF_STACK_PLATFORM="${AF_STACK_PLATFORM:-af-ecs-platform}"
  AF_WS_RUNTIME="${AF_WS_RUNTIME:-ecs}"
  AF_PERSISTENCE="${AF_PERSISTENCE:-delete}"
  # 呼び出し側（source した本体）が読む値。export しておかないと shellcheck からは
  # 「書いて誰も読まない」に見える。
  export AF_PROFILE AF_REGION AF_FQDN AF_ENV_DIR AF_LIVE AF_IMAGE_TAG AF_STACK_POOL
  export AF_STACK_NETWORK AF_STACK_DATA AF_STACK_PLATFORM AF_STACK_INGRESS
  export AF_WS_RUNTIME AF_PERSISTENCE AF_DEV_DEPLOY
}

# af_pool_stack — プール層のスタック名。
#
# ⚠️ 30 の引数に**書かれていない**（渡ってくるのは launch template の物理 ID だけ）。
# しかも実測で `af-ecs-pool` と `af-ecs-ec2-pool` のように配備ごとに違うので、規約で
# 決め打ちにはできない。エクスポート `<stack>-SlotLaunchTemplateId` の値がその物理 ID と
# 一致するスタックを探す＝**実体から名前を引く**。
af_pool_stack() {
  local lt name
  lt="$(af_stack_param "$AF_STACK_INGRESS" Ec2SlotLaunchTemplate)"
  [ -n "$lt" ] || return 0
  name="$("${AWS[@]}" cloudformation list-exports \
    --query "Exports[?Value=='$lt'&&ends_with(Name,'-SlotLaunchTemplateId')].Name" \
    --output text 2>/dev/null | head -1 || true)"
  case "$name" in None|"") return 0 ;; esac
  echo "${name%-SlotLaunchTemplateId}"
}

# af_cluster — ECS クラスタ名。**規約（`af-<platform stack>`）は最後の保険**にして、
# まず 20-platform が実際にエクスポートしている値を読む（update.sh が CP サービスを
# 物理 ID から引くのと同じ理由——名前を書き写すと、スタック名を変えた瞬間に静かに
# 外れて「別のクラスタに当てて成功した」ことになる）。
af_cluster() {
  local v
  v="$("${AWS[@]}" cloudformation list-exports \
    --query "Exports[?Name=='${AF_STACK_PLATFORM}-ClusterName'].Value" --output text 2>/dev/null || true)"
  case "$v" in ""|None) v="af-$AF_STACK_PLATFORM" ;; esac
  echo "$v"
}

# af_run — --dry-run 対応。読み取りは直接呼び、**書き込みは必ずここを通す**。
af_run() {
  if [ "${AF_DRY:-0}" = 1 ]; then echo "DRY: $*"; return 0; fi
  "$@"
}

# af_confirm <一行の説明> — 取り返しのつかない操作の前に置く。
#
# ★ `--yes` を「確認を飛ばす」ためだけのフラグにしない。**端末があるときは FQDN を
# 打たせる**——配備が複数あり、片方には実利用者が居る以上、「どちらに向かって実行して
# いるか」を目で確かめさせるのが唯一効く防具である（プロファイル名は似ていて、履歴から
# 引いた 1 行は取り違えても気づけない）。
# 端末が無い（エージェント/CI）ときは --yes を意思表示として受け取る。
af_confirm() {
  local what="$1"
  echo ""
  echo "⚠️  $what"
  echo "    deployment: ${AF_FQDN:-<unknown fqdn>}  (profile=$AF_PROFILE region=$AF_REGION)"
  if [ "${AF_YES:-0}" != 1 ]; then
    echo "    → 実行するには --yes を付ける（いまは何もしない）"
    return 1
  fi
  if [ -t 0 ] && [ -n "${AF_FQDN:-}" ]; then
    local typed=""
    printf '    confirm by typing the FQDN (%s): ' "$AF_FQDN"
    read -r typed
    [ "$typed" = "$AF_FQDN" ] || { echo "    mismatch — aborted"; return 1; }
  fi
  return 0
}

# af_stack_exists <stack>
af_stack_exists() {
  "${AWS[@]}" cloudformation describe-stacks --stack-name "$1" >/dev/null 2>&1
}

# af_stack_param <stack> <key> — 生きているスタックの引数を 1 つ読む（無ければ空）。
af_stack_param() {
  local v
  v="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$1" \
    --query "Stacks[0].Parameters[?ParameterKey=='$2'].ParameterValue" --output text 2>/dev/null || true)"
  case "$v" in None) v="" ;; esac
  echo "$v"
}

# af_stack_output <stack> <key>
af_stack_output() {
  local v
  v="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$1" \
    --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text 2>/dev/null || true)"
  case "$v" in None) v="" ;; esac
  echo "$v"
}

# --- CFN テンプレートの受け渡し（51,200 バイトの壁） -------------------------------
#
# 🔥 CloudFormation はテンプレート本文が **51,200 バイト**を超えると受け取らない。超える分は
# S3 経由（`aws cloudformation deploy --s3-bucket`）で渡す。2026-09-01 に 30-ingress が
# 54,681 バイトへ育った瞬間、**それを配備する経路が全部止まった** —— standup.sh（立てる）も
# update.sh（リリース）も同じ `cloudformation deploy --template-file` を呼ぶからである。
#
# ⚠️ 気付けなかった形がここの肝: AWS CLI は **API を叩く前にファイルサイズで断る**ので、
# 症状は CFN のエラーではなく CLI のエラーとして出る。しかも撤収→再構築を 1 往復して
# いなかった（docs/log/73 §73.7.2）ため、ingress を「作る」経路は 09-01 以降 1 度も
# 走っていなかった。**だから閾値を人が覚えるのではなく、下の af_cfn_deploy が毎回測る。**
AF_CFN_TEMPLATE_MAX=51200

# af_cfn_bucket — テンプレート受け渡し用バケット。解決順は 1 か所にまとめる:
#   1. 環境ファイル / env の AF_CFN_BUCKET（手で上書きしたいとき）
#   2. 20-platform スタックの出力 CfnTemplatesBucket（通常はこれ）
# 見つからなければ空を返す（呼び側が「S3 が要るのに無い」と言って落ちる）。
af_cfn_bucket() {
  if [ -n "${AF_CFN_BUCKET:-}" ]; then echo "$AF_CFN_BUCKET"; return; fi
  local stack="${AF_STACK_PLATFORM:-af-ecs-platform}"
  af_stack_output "$stack" CfnTemplatesBucket
}

# af_cfn_deploy <stack> <template> [追加の引数...] — `cloudformation deploy` の唯一の入口。
#
# ★ 判定はファイルサイズで機械的に行う。「30-ingress は大きいから S3」と名前で覚えると、
# 次に太るテンプレートで同じ事故を繰り返す。
af_cfn_deploy() {
  local stack="$1" tpl="$2"; shift 2
  local size bucket extra=()
  size="$(wc -c < "$tpl" | tr -d ' ')"
  if [ "$size" -gt "$AF_CFN_TEMPLATE_MAX" ]; then
    bucket="$(af_cfn_bucket)"
    if [ -z "$bucket" ]; then
      echo "ERROR: $(basename "$tpl") は $size バイト（上限 $AF_CFN_TEMPLATE_MAX）で S3 経由が要るが、" >&2
      echo "       受け渡し用バケットが引けない。20-platform（出力 CfnTemplatesBucket）を先に" >&2
      echo "       立てるか、AF_CFN_BUCKET で明示すること。" >&2
      return 1
    fi
    echo "    · $(basename "$tpl") は $size バイト > $AF_CFN_TEMPLATE_MAX — s3://$bucket 経由で渡す"
    extra=(--s3-bucket "$bucket" --s3-prefix cfn)
  fi
  "${AWS[@]}" cloudformation deploy --stack-name "$stack" \
    --template-file "$tpl" ${extra[@]+"${extra[@]}"} "$@"
}

# af_params_file <slug> — キャプチャした Key=Value 行のファイル（1 行 1 引数）。
#
# ★ この形式は「値に空白・括弧・`|`・カンマが入る」から選んでいる。実物の
# `Ec2SlotTypes` は `standard|Standard (Intel)|x86_64|m7i.large:8192:2,…` で、
# 空白区切りの 1 行に詰めると読み戻せない。JSON にすると jq か python が要るが、
# ここは AWS CLI だけで完結させたい（README の前提）ので **1 行 1 引数**にする。
af_params_file() { echo "$AF_ENV_DIR/params/$1"; }

# af_read_params <slug> — ファイルを配列 AF_PARAMS[] に読む。値の空白を壊さない。
af_read_params() {
  local f line
  f="$(af_params_file "$1")"
  AF_PARAMS=()
  [ -r "$f" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ""|\#*) continue ;; esac
    AF_PARAMS+=("$line")
  done < "$f"
}

# af_params_masked — AF_PARAMS[] を**表示用**に整える。
#
# ⚠️ CFN の引数は「秘密ではない」ことになっているが、実物にはそう読めない値が混じる
# （`BitbucketOauthKey` など）。計画表示や dry-run は**端末とログに残る**もので、
# そこへ値を流す理由は無い。鍵らしい名前のものは伏せる（実行には伏せない値を使う）。
af_params_masked() {
  local p
  for p in ${AF_PARAMS[@]+"${AF_PARAMS[@]}"}; do
    case "${p%%=*}" in
      *Secret*|*Password*|*Token*|*OauthKey*|*PrivateKey*) echo "${p%%=*}=***" ;;
      *) echo "$p" ;;
    esac
  done
}

# af_param_override <key> <value> — AF_PARAMS[] の 1 件を差し替える（無ければ足す）。
# 物理 ID を持つ引数（新しく作り直したプールの launch template など）はキャプチャ値を
# そのまま使えないので、ここで上書きする。
af_param_override() {
  local key="$1" val="$2" out=() p found=0
  for p in ${AF_PARAMS[@]+"${AF_PARAMS[@]}"}; do
    case "$p" in
      "$key"=*) out+=("$key=$val"); found=1 ;;
      *) out+=("$p") ;;
    esac
  done
  [ "$found" = 1 ] || out+=("$key=$val")
  AF_PARAMS=("${out[@]}")
}
