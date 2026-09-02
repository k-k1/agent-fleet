#!/usr/bin/env bash
# Agent Fleet — 生きている配備の「立て直しに要る引数」を手元に控える（env.sh を読むこと）。
#
#   deploy/aws/ecs/capture-env.sh --profile <p> --region <r>
#
# 出力は `~/.config/agent-fleet/deploy/<profile>.<region>/`（**リポジトリの外**）。
#   env             … スタック名 / FQDN / 実行時の性質（立て直しのときの土台）
#   params/<slug>   … 各スタックの引数を **1 行 1 個**（`Key=Value`）
#
# ## これは「畳む前に必ず走らせるもの」である
#
# 2026-08-22 に 2 配備を削除したとき、引数一式は**手で JSON に退避**した。それが無ければ
# 再構築は不可能に近い——テンプレートは repo にあるが、**そのテンプレートに何を渡したかは
# 配備の中にしか無い**。しかも `delete-stack` を出した瞬間に読めなくなる。
# だから撤収の第 0 歩をスクリプトにする。
#
# ⚠️ **秘密は入らない。** SSM SecureString（cookie-secret / master-key / IdP の client
# secret）は CFN の引数ではないのでここには現れず、復元もされない。ただし**アカウント
# 固有の値は入る**（ホストゾーン ID・許可メール・OAuth クライアント ID）。だから置き場は
# repo の外で、パーミッションも絞る。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"

usage() {
  cat >&2 <<'EOF'
usage: capture-env.sh --profile <p> --region <r> [--stack af-ecs-ingress] [--force]
  --profile  aws cli profile (this is how a deployment is addressed)
  --region   region of the deployment
  --stack    the stack that has ImageTag (default af-ecs-ingress). Everything else is
             discovered from it
  --force    overwrite what was captured before
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; FORCE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile) PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)  REGION="${2:?--region needs a value}"; shift ;;
    --stack)   STACK="${2:?--stack needs a value}"; shift ;;
    --force)   FORCE=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }

af_env_init "$PROFILE" "$REGION" "$STACK"
[ "$AF_LIVE" = 1 ] || {
  echo "ERROR: stack '$STACK' not found in $PROFILE/$REGION — nothing to capture" >&2
  exit 1
}
OUT="$AF_ENV_DIR"
if [ -e "$OUT/env" ] && [ "$FORCE" != 1 ]; then
  echo "ERROR: $OUT is already captured (--force to refresh)" >&2
  exit 1
fi

mkdir -p "$OUT/params"
chmod 700 "$OUT" "$OUT/params" 2>/dev/null || true

save_params() {  # save_params <stack> <slug>
  local stack="$1" slug="$2" f
  f="$OUT/params/$slug"
  if ! af_stack_exists "$stack"; then
    echo "  - $slug: stack '$stack' not found — skipped"
    return 0
  fi
  # `join` で `Key=Value` の 1 行にする。値そのものに改行は入らない（CFN の引数は
  # 単一行）ので、これで空白・括弧・`|`・カンマを含む値がそのまま往復する。
  "${AWS[@]}" cloudformation describe-stacks --stack-name "$stack" \
    --query "Stacks[0].Parameters[].join('=',[ParameterKey,ParameterValue])" \
    --output text | tr '\t' '\n' | grep -v '^$' > "$f"

  # 🔴 **空の引数は、その空値が「自分で作る」分岐を選んだという意味であることがある。**
  # その場合、作られた実体の id は引数ではなく **Output** 側にしかない。引数だけを写すと、
  # 立て直したときに同じ空値がもう一度「作る」を選び、**前の実体は孤児になる**。
  #
  # 実例（2026-09-02 の一巡で踏んだ）: `NatEipAllocationId`。空なら 00-network が EIP を
  # 自分で確保し（Retain なので撤収でも残る）、その allocation id は Output にだけ出る。
  # 控えが引数しか持っていないと、次の stand-up が **2 本目の EIP** を取り、
  # **顧客が許可リストに載せた egress アドレスが黙って変わる**（孤児は月 $3.6）。
  #
  # ★ 規則は 1 つだけ: **空で捕まえた引数と同じ名前の Output があれば、Output の値を採る。**
  # 引数に無い名前の Output は写さない —— CFN は知らない引数を拒むし、Output の大半
  # （VpcId など）はそもそも引数ではない。
  local outs key val
  outs="$("${AWS[@]}" cloudformation describe-stacks --stack-name "$stack" \
    --query "Stacks[0].Outputs[].join('=',[OutputKey,OutputValue])" \
    --output text 2>/dev/null | tr '\t' '\n' | grep -v '^$' || true)"
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    key="${line%%=*}"; val="${line#*=}"
    [ -n "$val" ] && [ "$val" != "None" ] || continue
    grep -q "^$key=\$" "$f" || continue          # 空で捕まえた引数だけが対象
    sed -i "s|^$key=\$|$key=$val|" "$f"
    echo "  - $slug: $key は Output から採った（空の引数は「作る」分岐の印）"
  done <<EOF
$outs
EOF

  chmod 600 "$f" 2>/dev/null || true
  echo "  - $slug: $(wc -l < "$f") parameters ($stack)"
}

echo "==> capturing $AF_FQDN into $OUT"
save_params "$AF_STACK_NETWORK"  00-network
save_params "$AF_STACK_DATA"     10-data
save_params "$AF_STACK_PLATFORM" 20-platform
[ -n "${AF_STACK_POOL:-}" ] && save_params "$AF_STACK_POOL" 40-ec2-pool
save_params "$AF_STACK_INGRESS"  30-ingress

# 既に印が付いていたら残す（--force の再取得で開発配備の印が消えないように）。
DEV_MARK=0
if [ -r "$OUT/env" ] && grep -q '^AF_DEV_DEPLOY=1' "$OUT/env"; then DEV_MARK=1; fi

cat > "$OUT/env" <<EOF
# agent-fleet deployment — captured $(date +%Y-%m-%dT%H:%M:%S%z) from $AF_FQDN.
# Written by deploy/aws/ecs/capture-env.sh. Read when the deployment is NOT live
# (i.e. by standup.sh, to build it back). Not a secret store — see env.sh.
AF_FQDN=$AF_FQDN
AF_STACK_NETWORK=$AF_STACK_NETWORK
AF_STACK_DATA=$AF_STACK_DATA
AF_STACK_PLATFORM=$AF_STACK_PLATFORM
AF_STACK_POOL=${AF_STACK_POOL:-}
AF_STACK_INGRESS=$AF_STACK_INGRESS
AF_WS_RUNTIME=$AF_WS_RUNTIME
AF_PERSISTENCE=$AF_PERSISTENCE
AF_IMAGE_TAG=$AF_IMAGE_TAG
# 開発配備なら 1 にする。dev-deploy.sh（develop をタグ無しで載せる）はこの印が付いた
# 配備にしか当たらない —— ImageTag を動かすのは、そこで走っている人に「要再起動」
# バッジを出す操作だから。★この印を repo ではなくここに置くのは、「どの配備が開発用か」
# もまた配備の身元だからである（このリポジトリは公開）。
AF_DEV_DEPLOY=$DEV_MARK
EOF
chmod 600 "$OUT/env" 2>/dev/null || true

cat <<EOF

==> captured: $AF_FQDN  (profile=$AF_PROFILE region=$AF_REGION)
    stacks: $AF_STACK_NETWORK / $AF_STACK_DATA / $AF_STACK_PLATFORM${AF_STACK_POOL:+ / $AF_STACK_POOL} / $AF_STACK_INGRESS
    runtime=$AF_WS_RUNTIME persistence=$AF_PERSISTENCE image=$AF_IMAGE_TAG

配備を触る道具はどれも --profile / --region で同じ配備を指す:
    deploy/aws/ecs/pause.sh      --profile $AF_PROFILE --region $AF_REGION [--up]
    deploy/aws/ecs/teardown.sh   --profile $AF_PROFILE --region $AF_REGION
    deploy/aws/ecs/standup.sh    --profile $AF_PROFILE --region $AF_REGION
    deploy/aws/ecs/dev-deploy.sh --profile $AF_PROFILE --region $AF_REGION   # 要 AF_DEV_DEPLOY=1

⚠️ SSM の秘密（$(af_stack_param "$AF_STACK_INGRESS" SsmPrefix)/cookie-secret ・ master-key ・ IdP の client secret）は
   ここには入っていない。撤収してもそれらを消さなければ、再構築時にそのまま使える。
EOF
