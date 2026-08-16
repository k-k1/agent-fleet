#!/bin/bash
# bake-slot-ami.sh — スロットの AMI に Workspace イメージを焼き込む。
# ADR 0045 決定 18 / docs/64 §64.22。対象は AF_RUNTIME=ecs-ec2 のデプロイのみ。
#
# 何のためか: スロットの root ボリュームはイメージキャッシュそのもので、キャッシュが
# 温まっていれば pull は 31.8s → 0.09s になる（決定 1 の実測）。ところが**新しく立てた
# スロットの root は常に冷たい**ので、その AZ の最初の 1 人・上限まで伸びるとき・
# 分散で新しい AZ に立てるとき（決定 16）が毎回 pull を払っている。
# 実測では「新規作成 148.5s」対「休止スロットを起こす 90s 前後」で、差の大半がこれである。
#
# なぜ AMI なのか（AZ ごとに種インスタンスを置くのではなく）:
#   - **AMI はリージョン資源**なので 1 個で全 AZ に効く。AZ の数だけ用意しなくてよい
#   - プールの帳尻に出てこない（poolSize にも freeSlots にも入らない）
#   - 種インスタンスなら「どのセッションがそれを使うか」の調停が要る。スロット割り当ては
#     デバイス名の attach を AWS に裁かせているだけで、インスタンスを奪い合う仕組みは無い
#   - **以後すべてのスロット作成に効く**（種方式は各 AZ の最初の 1 人だけ）
#
# ⚠️ **イメージを更新したら焼き直すこと。** golden snapshot（決定 9）と同じで、忘れても
# 壊れはせず「遅くなるだけ」なので見えない。CP は AMI の af-image タグを突合し、
# 食い違っていれば管理画面（スロットタブ）に警告を出し続ける。
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: bake-slot-ami.sh --image <image:tag> --launch-template <lt-id> --subnet <subnet-id>
                        [--pool <cluster>] [--region <region>] [--name <ami-name>]

  --image            焼き込む Workspace イメージ。CP の AF_ECS_WORKSPACE_IMAGE と
                     **完全一致**していること（CP はこの文字列で突合する）
  --launch-template  40-ec2-pool の SlotLaunchTemplateId。**本番と同じテンプレートで焼く**
                     ——手書きの起動条件で焼くと、焼いた箱と製品が立てる箱が静かにズレる
  --subnet           焼く場所。ECR と SSM に到達できるサブネットならどこでもよい
                     （AMI はリージョン資源なので、どの AZ で焼いても全 AZ で使える）
  --pool             af-pool タグの値（既定: AF_ECS_EC2_POOL、無ければ AF_ECS_CLUSTER）
EOF
  exit 2
}

IMAGE="" LT="" SUBNET="" POOL="${AF_ECS_EC2_POOL:-${AF_ECS_CLUSTER:-}}" REGION="${AWS_REGION:-}" NAME=""
while [ $# -gt 0 ]; do
  case "$1" in
    --image)           IMAGE="$2"; shift 2 ;;
    --launch-template) LT="$2"; shift 2 ;;
    --subnet)          SUBNET="$2"; shift 2 ;;
    --pool)            POOL="$2"; shift 2 ;;
    --region)          REGION="$2"; shift 2 ;;
    --name)            NAME="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$IMAGE" ] && [ -n "$LT" ] && [ -n "$SUBNET" ] && [ -n "$POOL" ] || usage
[ -n "$REGION" ] && export AWS_REGION="$REGION"
[ -n "$NAME" ] || NAME="af-slot-$(date +%Y%m%d-%H%M%S)"

say() { echo "=== $* ==="; }
cleanup() {
  if [ -n "${INST:-}" ]; then
    say "terminating the bake instance $INST"
    aws ec2 terminate-instances --instance-ids "$INST" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# af-role=bake で立てる。**af-role=slot にしてはいけない** —— CP の freeSlots / poolSize は
# そのタグで世界を作るので、焼いている最中の箱に誰かの home が載る。
say "launching a bake instance from $LT in $SUBNET"
INST=$(aws ec2 run-instances \
  --launch-template "LaunchTemplateId=$LT,Version=\$Latest" \
  --subnet-id "$SUBNET" --min-count 1 --max-count 1 \
  --tag-specifications "ResourceType=instance,Tags=[{Key=af-pool,Value=$POOL},{Key=af-role,Value=bake},{Key=Name,Value=$NAME}]" \
  --query 'Instances[0].InstanceId' --output text)
echo "instance $INST"
aws ec2 wait instance-running --instance-ids "$INST"

# SSM が応答するまで待つ。起動直後はエージェントがまだ登録されていない。
say "waiting for SSM"
for _ in $(seq 60); do
  st=$(aws ssm describe-instance-information \
    --filters "Key=InstanceIds,Values=$INST" \
    --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null || echo None)
  [ "$st" = Online ] && break
  sleep 5
done
[ "${st:-}" = Online ] || { echo "SSM never came online for $INST" >&2; exit 1; }

run() {
  local cid
  cid=$(aws ssm send-command --instance-ids "$INST" --document-name AWS-RunShellScript \
    --parameters "commands=[$1]" --query 'Command.CommandId' --output text)
  for _ in $(seq 120); do
    sleep 5
    s=$(aws ssm get-command-invocation --command-id "$cid" --instance-id "$INST" \
      --query 'Status' --output text 2>/dev/null || echo Pending)
    case "$s" in
      Success) aws ssm get-command-invocation --command-id "$cid" --instance-id "$INST" --query 'StandardOutputContent' --output text; return 0 ;;
      Failed|Cancelled|TimedOut)
        aws ssm get-command-invocation --command-id "$cid" --instance-id "$INST" --query 'StandardErrorContent' --output text >&2
        return 1 ;;
    esac
  done
  echo "ssm command never finished" >&2
  return 1
}

ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
say "pulling $IMAGE (this is the whole point of the bake)"
run "\"set -e\",\"aws ecr get-login-password --region $AWS_REGION | docker login --username AWS --password-stdin $ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com\",\"docker pull $IMAGE\",\"docker image ls --digests\""

# ⚠️ 決定 3-1 の罠。/var/lib/ecs/data を残したまま焼くと、この AMI から立てた
# インスタンスは「自分は登録済み」と思い込み、クラスタに入り直せない。
say "clearing the ECS agent's identity (決定 3-1: leaving /var/lib/ecs/data behind wedges every instance from this AMI)"
run "\"set -e\",\"systemctl stop ecs || true\",\"rm -rf /var/lib/ecs/data/*\",\"rm -f /var/log/ecs/*\",\"ls -la /var/lib/ecs/data || true\""

say "stopping the instance so the image is taken from a quiesced filesystem"
aws ec2 stop-instances --instance-ids "$INST" >/dev/null
aws ec2 wait instance-stopped --instance-ids "$INST"

say "creating the AMI $NAME"
AMI=$(aws ec2 create-image --instance-id "$INST" --name "$NAME" \
  --description "agent-fleet slot AMI with $IMAGE pre-pulled" \
  --tag-specifications \
    "ResourceType=image,Tags=[{Key=af-pool,Value=$POOL},{Key=af-role,Value=slot-ami},{Key=af-image,Value=$IMAGE},{Key=Name,Value=$NAME}]" \
    "ResourceType=snapshot,Tags=[{Key=af-pool,Value=$POOL},{Key=af-role,Value=slot-ami},{Key=Name,Value=$NAME}]" \
  --query ImageId --output text)
echo "image $AMI"
aws ec2 wait image-available --image-ids "$AMI"

cat <<EOF

=== done ===
AMI: $AMI  (af-image=$IMAGE)

Point the pool at it and the pull disappears from every new slot:

  aws cloudformation deploy --stack-name <net>-pool \\
    --template-file deploy/aws/ecs/cfn/40-ec2-pool.yaml \\
    --capabilities CAPABILITY_NAMED_IAM \\
    --parameter-overrides SlotAmiId=$AMI NetworkStackName=<net> PlatformStackName=<plat>

Slots already running keep the AMI they were launched from — this takes effect on the
NEXT slot the CP creates. Existing slots are not replaced; nothing needs them to be.

⚠️ Re-bake when the workspace image changes. The CP compares the AMI's af-image tag
against what it runs and warns in Settings > Admin > Slots when they differ; forgetting
costs a pull per new slot, silently.
⚠️ The old AMI and its snapshot keep billing. Deregister the image and delete its
snapshot once no slot was launched from it.
EOF
