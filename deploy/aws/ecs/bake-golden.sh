#!/bin/bash
# bake-golden.sh — 新規ユーザーの home の種になる「golden snapshot」を焼く。
# ADR 0045 決定 9 / docs/64 §64.18。対象は AF_RUNTIME=ecs-ec2 のデプロイのみ。
#
# 何をするか: **既に立ち上げ済みの「種」Workspace の home ボリュームを snapshot に取り、
# af-role=golden と af-image を刻む**だけ。boot-install（4CLI 41s ＋ rtk 1s ＋ agy 6s）と
# npm キャッシュの温めは、製品そのものに任せる——このスクリプトが entrypoint を再実装すると、
# 「焼いた home」と「製品が作る home」が静かにズレていく。
#
# 手順（全体）:
#   1. 種にするメンバーを 1 人用意し、Console から Workspace を起動して**完全に立ち上がる**まで待つ
#      （boot-install が終わり、必要なら一度 npm ci まで通しておく）
#   2. その Workspace を停止する
#   3. スロットから外れるまで待つ（掃除ループが返却するか、破棄する直前でもよい）
#   4. このスクリプトを実行する
#   5. 種の Workspace を破棄する（DELETE /api/admin/workspaces）。golden は af-role=golden
#      なので、per-membership の掃除には巻き込まれない
#
# ⚠️ **リリースのたびに焼き直すこと。** イメージや CLI のピンが上がると golden は古くなる。
# CP は af-image を突合し、一致しない golden は**使わずに空 home を作る**（起動が遅くなるだけで
# 壊れはしないが、ログに警告が出続ける）。
#
# ⚠️ **未実機検証。** 中身は AWS CLI 4 コマンドだが、実際に焼いて新規ユーザーを起こすところまでは
# まだ通していない。
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: bake-golden.sh --workspace <af-ws-name> --image <image:tag> [--pool <cluster>] [--region <region>]

  --workspace  種にした Workspace のコンテナ名（= ECS サービス名 / af-workspace タグの値）
  --image      この golden が対応する Workspace イメージ。CP の AF_ECS_WORKSPACE_IMAGE と
               **完全一致**していること（CP はこの文字列で突合する）
  --pool       af-pool タグの値（既定: AF_ECS_EC2_POOL、無ければ AF_ECS_CLUSTER）
EOF
  exit 2
}

WS="" IMAGE="" POOL="${AF_ECS_EC2_POOL:-${AF_ECS_CLUSTER:-}}" REGION="${AWS_REGION:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --workspace) WS="$2"; shift 2 ;;
    --image)     IMAGE="$2"; shift 2 ;;
    --pool)      POOL="$2"; shift 2 ;;
    --region)    REGION="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$WS" ] && [ -n "$IMAGE" ] && [ -n "$POOL" ] || usage
[ -n "$REGION" ] && export AWS_REGION="$REGION"

vol=$(aws ec2 describe-volumes \
  --filters "Name=tag:af-workspace,Values=$WS" "Name=tag:af-role,Values=home" \
  --query 'Volumes[0].VolumeId' --output text)
if [ "$vol" = "None" ] || [ -z "$vol" ]; then
  echo "no home volume tagged af-workspace=$WS — is the seed workspace still there?" >&2
  exit 1
fi

# 付いたままのボリュームを撮ると、マウント中のファイルシステムのクラッシュ一貫コピーになる。
# 全ユーザーの初期状態になるものでそれをやる理由は無い。
attached=$(aws ec2 describe-volumes --volume-ids "$vol" \
  --query 'Volumes[0].Attachments[0].InstanceId' --output text)
if [ "$attached" != "None" ] && [ -n "$attached" ]; then
  echo "$vol is still attached to $attached. Stop the seed workspace and wait for the" >&2
  echo "sweeper to release the slot (Ec2SlotSleepSec + a sweep) before baking." >&2
  exit 1
fi

echo "baking $vol → golden (pool=$POOL image=$IMAGE)"
snap=$(aws ec2 create-snapshot --volume-id "$vol" \
  --description "agent-fleet golden home ($IMAGE)" \
  --tag-specifications \
    "ResourceType=snapshot,Tags=[{Key=af-pool,Value=$POOL},{Key=af-role,Value=golden},{Key=af-image,Value=$IMAGE},{Key=Name,Value=af-golden}]" \
  --query SnapshotId --output text)
echo "snapshot $snap started; waiting for it to complete (30–40 min for a 45 GiB home)"
aws ec2 wait snapshot-completed --snapshot-ids "$snap"
echo "$snap completed."

# 古い golden は消す。CP は完了済みかつ af-image が一致する最新を選ぶので、放置しても
# 誤って使われはしないが、$0.05/GB-月 を払い続ける理由も無い。
for old in $(aws ec2 describe-snapshots --owner-ids self \
  --filters "Name=tag:af-pool,Values=$POOL" "Name=tag:af-role,Values=golden" \
  --query "Snapshots[?SnapshotId!='$snap'].SnapshotId" --output text); do
  echo "deleting the superseded golden $old"
  aws ec2 delete-snapshot --snapshot-id "$old"
done

echo
echo "done. New homes on pool=$POOL will be seeded from $snap while the CP runs $IMAGE."
echo "Re-bake when that image changes — the CP refuses a golden stamped with another one."
