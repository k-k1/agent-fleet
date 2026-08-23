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
#      （boot-install が終わるまで）
#   2. その Workspace を停止する
#   3. **スロットが眠るまで待つ**（AF_ECS_EC2_SLOT_SLEEP_SEC・既定 15 分 ＋ 1 スイープ）。
#      待つ対象は「インスタンスが stopped になること」であって、**ボリュームが外れることではない**
#      —— 下の ★ を読むこと
#   4. このスクリプトを実行する
#   5. 種の Workspace を破棄する（DELETE /api/admin/workspaces）。golden は af-role=golden
#      なので、per-membership の掃除には巻き込まれない
#
# ★ **停止しても home は外れない。** Stop はボリュームを付けたままにする設計で（affinity ＝
# 「その人のスロット」そのもの）、スイーパーが 15 分後にやるのも**インスタンスの停止だけ**である
# （runtime_ecs_ec2.go の `(home stays attached)`）。実際に外すのは eviction / Destroy /
# ドリフト修復 / 退避だけなので、**「detached になるのを待つ」といつまでも始められない**。
# 代わりに「**stopped なスロットに付いたまま**撮る」で正しい: インスタンスの停止は通常の
# シャットダウンで、降りる途中でファイルシステムを umount する —— 製品自身が
# releaseSlotSince で同じ根拠に立っている（停止済みスロットは SSM が届かないので umount を
# 省く）。だから下のガードが拒むのは **running なスロットに付いている場合だけ**である。
#
# ⚠️ **リリースのたびに焼き直すこと。** イメージや CLI のピンが上がると golden は古くなる。
# ⚠️ **ここで焼いた golden は `af-image-fp`（内容の指紋）を持たない。** CP は指紋が両側に
# あるときだけ内容で突合し、無ければこれまでどおり af-image の**文字列**で比べる（docs/73
# 決定 3）ので、手焼きの golden も普通に使われる。ただし文字列で比べる以上、**同じ中身を
# 別タグに置き直した瞬間に「無い」ことになり、CP が焼き直しに入る**（docs/72 §72.6.4 —
# 開発デプロイは毎回それをやる）。CP に焼かせた golden にはその心配は無い。
#
# CP は af-image を突合し、一致しない golden は**使わずに空 home を作る**（起動が遅くなるだけで
# 壊れはしないが、ログに警告が出続ける）。
#
# ⚠️ **種に repo を clone しないこと。** `~/repos` は home 上にあるので、種で clone すると
# **その clone が新規ユーザー全員の home に配られる**。焼く範囲は boot-install までとする。
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

# **動いているスロットに付いたまま**撮ると、マウント中のファイルシステムのクラッシュ一貫コピーに
# なる。全ユーザーの初期状態になるものでそれをやる理由は無い。
#
# ★ stopped なスロットに付いたままは OK（ヘッダの ★ 参照）。ここを「detached でなければ拒否」
# にしていた頃は、手順どおり停止して待った操作者が**絶対に抜けられなかった** —— 停止では外れず、
# スイーパーも外さないため。live test は Go から releaseSlot() を直接呼んでこのガードを満たして
# いたので、その穴は最後まで見えていなかった。
attached=$(aws ec2 describe-volumes --volume-ids "$vol" \
  --query 'Volumes[0].Attachments[0].InstanceId' --output text)
if [ "$attached" != "None" ] && [ -n "$attached" ]; then
  state=$(aws ec2 describe-instances --instance-ids "$attached" \
    --query 'Reservations[0].Instances[0].State.Name' --output text)
  # stopping / pending の途中は駄目: シャットダウンが終わって初めて umount が済んでいる。
  if [ "$state" != "stopped" ]; then
    echo "$vol is attached to $attached, which is $state." >&2
    echo "Stop the seed workspace and wait for the sweeper to put the slot to sleep" >&2
    echo "(AF_ECS_EC2_SLOT_SLEEP_SEC, default 15m, + one sweep); the CP logs" >&2
    echo "'stopping slot <id> (home stays attached)' when that happens. The home staying" >&2
    echo "attached is expected — what has to be true is that the slot is stopped." >&2
    exit 1
  fi
  echo "$vol is attached to the stopped slot $attached — its filesystem was unmounted by"
  echo "that shutdown, so the snapshot is consistent."
fi

echo "baking $vol → golden (pool=$POOL image=$IMAGE)"
snap=$(aws ec2 create-snapshot --volume-id "$vol" \
  --description "agent-fleet golden home ($IMAGE)" \
  --tag-specifications \
    "ResourceType=snapshot,Tags=[{Key=af-pool,Value=$POOL},{Key=af-role,Value=golden},{Key=af-image,Value=$IMAGE},{Key=Name,Value=af-golden}]" \
  --query SnapshotId --output text)
# 待ち時間は**ボリュームのサイズではなく、使っているブロックの量**で決まる。boot-install だけの
# home（種として正しい状態）なら 50 GiB のボリュームでも実測 **3 分弱**である。
# 退避 snapshot の「45 GiB で 30〜40 分」を種にも当てはめて 30 分待つ気でいると、
# 「進んでいないのでは」と余計な手を出す方に転ぶ。
echo "snapshot $snap started; waiting for it to complete (~3 min for a boot-install-only home)"
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
