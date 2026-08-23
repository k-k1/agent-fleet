#!/usr/bin/env bash
# Agent Fleet — 配備を「畳まずに眠らせる」／起こす。
#
#   deploy/aws/ecs/pause.sh --profile <p> --region <r>          # 縮退
#   deploy/aws/ecs/pause.sh --profile <p> --region <r> --up     # 復帰
#   deploy/aws/ecs/pause.sh --profile <p> --region <r> --status # いまどうなっているか
#
# ## なぜ撤収（teardown.sh）と別なのか
#
# 「使わない期間」に対して全削除は釣り合わない。**戻すのに再構築が要り、ECR も消える**
# （20-platform の ECR は `EmptyOnDelete: true`）。一方で縮退は数分で往復でき、home も
# データベースもそのまま残る。
#
# ⚠️ ただし**安くなる幅は限られている**。消えるのは EC2 スロットの計算費と CP の
# Fargate だけで、**NAT / ALB / RDS / EFS の固定費は残る**（実測の日額は概算 $5.5 →
# 停止で $2.6、つまり半分は固定費）。「ほぼ $0 にしたい」なら teardown.sh である。
#
# ## 順序に理由がある
#
# スロットを眠らせるのは **CP のスイーパー**である（`AF_ECS_EC2_SLOT_SLEEP_SEC`・既定
# 15 分）。だから **CP を先に落とすと、走っているスロットが起きたまま取り残される**——
# 止めたつもりで一番高いものが billing に残る、というのが一番痛い間違い方。
# 順序は「Workspace を止める → スロットが眠るのを待つ → CP を落とす」。
# 待てないときは `--fast`（既に Workspace は止まっているので、こちらで止めても
# CP が 15 分後にやることと同じ）。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"

usage() {
  cat >&2 <<'EOF'
usage: pause.sh --profile <p> --region <r> [--up|--status] [--fast] [--yes] [--dry-run]
  --profile  aws cli profile (this is how a deployment is addressed)
  --region   region of the deployment
  --stack    ingress stack name (default af-ecs-ingress)
  --up       resume: bring the Control Plane back (users start their own workspaces)
  --status   print what is running and what still bills; change nothing
  --fast     stop the slot instances directly instead of waiting for the CP's sweeper
  --yes      actually do it (without this, --down only prints the plan)
  --dry-run  print the writes without making them
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; MODE=down; FAST=0; AF_YES=0; AF_DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile) PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)  REGION="${2:?--region needs a value}"; shift ;;
    --stack)   STACK="${2:?--stack needs a value}"; shift ;;
    --up)      MODE=up ;;
    --status)  MODE=status ;;
    --fast)    FAST=1 ;;
    --yes)     AF_YES=1 ;;
    --dry-run) AF_DRY=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }
export AF_YES AF_DRY   # env.sh の af_confirm / af_run が読む
af_env_init "$PROFILE" "$REGION" "$STACK"
[ "$AF_LIVE" = 1 ] || { echo "ERROR: $STACK not found in $PROFILE/$REGION（畳んだあとの配備は standup.sh）" >&2; exit 1; }
CLUSTER="$(af_cluster)"
CP_SERVICE="af-$AF_STACK_INGRESS-cp"

# ws_services — desired>0 の Workspace サービス（＝いま誰かが使っている）。
# list-services のページングは CLI に任せる（--max-items を付けると黙って打ち切られ、
# 「全員見た」ように読めてしまう）。describe-services は 1 回 10 件まで。
ws_services() {
  local arns names="" batch i got
  arns="$("${AWS[@]}" ecs list-services --cluster "$CLUSTER" --query 'serviceArns' --output text 2>/dev/null || true)"
  for a in $arns; do
    case "${a##*/}" in af-ws-*) names="$names ${a##*/}" ;; esac
  done
  # shellcheck disable=SC2086  # word splitting is the batching
  set -- $names
  while [ $# -gt 0 ]; do
    batch=""; i=0
    while [ $# -gt 0 ] && [ $i -lt 10 ]; do batch="$batch $1"; shift; i=$((i + 1)); done
    # shellcheck disable=SC2086,SC2016  # splitting is the batching; backticks are JMESPath literals
    got="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services $batch \
      --query 'services[?desiredCount>`0`].serviceName' --output text 2>/dev/null || true)"
    for g in $got; do echo "$g"; done
  done
}

slots() {  # <state> ...
  "${AWS[@]}" ec2 describe-instances \
    --filters "Name=tag:af-pool,Values=$CLUSTER" "Name=tag:af-role,Values=slot" \
      "Name=instance-state-name,Values=$(IFS=,; echo "$*")" \
    --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true
}

cp_counts() {
  "${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$CP_SERVICE" \
    --query 'services[0].[desiredCount,runningCount]' --output text 2>/dev/null || echo "? ?"
}

status() {
  local running stopped ws
  running="$(slots pending running | tr '\t' ' ')"; stopped="$(slots stopping stopped | tr '\t' ' ')"
  ws="$(ws_services | tr '\n' ' ')"
  echo "==> $AF_FQDN (profile=$AF_PROFILE region=$AF_REGION)"
  echo "    control plane : desired/running = $(cp_counts | tr '\t' '/')"
  echo "    workspaces up : ${ws:-（無し）}"
  echo "    slots running : ${running:-（無し）}"
  echo "    slots stopped : ${stopped:-（無し）}"
  echo ""
  echo "    止めても残る固定費: NAT / ALB / RDS / EFS ＋ home の EBS（縮退では消えない）"
}

case "$MODE" in
  status)
    status
    exit 0
    ;;

  up)
    echo "==> resuming $AF_FQDN"
    af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" \
      --desired-count 1 >/dev/null
    if [ "$AF_DRY" != 1 ]; then
      echo "==> waiting for $CP_SERVICE"
      "${AWS[@]}" ecs wait services-stable --cluster "$CLUSTER" --services "$CP_SERVICE"
    fi
    cat <<EOF

==> up: https://$AF_FQDN
    Workspace は利用者が自分で起動する（止まっているスロットは Start で起き直す）。
EOF
    ;;

  down)
    status
    ws="$(ws_services | tr '\n' ' ')"
    if ! af_confirm "$AF_FQDN を縮退する（走っている Workspace は停止＝そのセッションは落ちる）"; then
      echo ""
      echo "計画: ${ws:-（停止する Workspace は無し）} → スロット就寝 → CP desired 0"
      exit 0
    fi

    for s in $ws; do
      echo "==> stopping workspace $s"
      af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$s" --desired-count 0 >/dev/null
    done

    if [ "$FAST" = 1 ]; then
      ids="$(slots pending running)"
      if [ -n "${ids// /}" ]; then
        echo "==> stopping slots directly: $ids"
        # shellcheck disable=SC2086
        af_run "${AWS[@]}" ec2 stop-instances --instance-ids $ids >/dev/null
      fi
    elif [ "$AF_DRY" != 1 ]; then
      # CP のスイーパーが眠らせるのを待つ。待ち上限は「就寝時間 ＋ 1 スイープ分」。
      sleep_s="$(af_stack_param "$AF_STACK_INGRESS" Ec2SlotSleepSec)"; : "${sleep_s:=900}"
      deadline=$(( $(date +%s) + sleep_s + 300 ))
      echo "==> waiting for the CP to put the slots to sleep (up to $(( (sleep_s + 300) / 60 )) min; --fast skips this)"
      while :; do
        ids="$(slots pending running)"
        [ -z "${ids// /}" ] && break
        [ "$(date +%s)" -ge "$deadline" ] && {
          echo "⚠️  まだ起きているスロットがある: $ids"
          echo "    CP を落とすとこのまま取り残される。--fast で止めるか、CP は動かしたままにすること。"
          exit 1
        }
        sleep 30
      done
      echo "==> slots are asleep"
    fi

    echo "==> stopping the control plane"
    af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" \
      --desired-count 0 >/dev/null
    cat <<EOF

==> paused: $AF_FQDN は応答しなくなる
    戻す:   deploy/aws/ecs/pause.sh --profile $AF_PROFILE --region $AF_REGION --up
    残る費用: NAT / ALB / RDS / EFS ＋ home の EBS。ほぼ 0 にするなら teardown.sh。
EOF
    ;;
esac
