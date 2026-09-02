#!/usr/bin/env bash
# Agent Fleet — 配備をまるごと撤収する（README §Teardown の実行版）。
#
#   deploy/aws/ecs/teardown.sh --profile <p> --region <r>          # 一覧と計画だけ（既定）
#   deploy/aws/ecs/teardown.sh --profile <p> --region <r> --yes    # 本当に消す
#
# 2026-08-22 に 2 配備（`Persistence=delete` と `=retain`）を手で削除し、両アカウントで
# スイープの全カウンタがゼロになった。その順序と落とし穴を写したもの。
#
# ## この順序でないと終わらない
#
# 配備が持っているものの大半は**スタックの中に無い**。CP が実行時に作る（Workspace
# サービス・EFS アクセスポイント・SSM・そして ecs-ec2 ではスロット・home の EBS・
# golden スナップショット）。**先にスタックを消すとそれらが残り、依存で止まる。**
#
#  1. **CP を最初に止める。** 2〜7 は全部その CP の帳簿で、しかも CP はスロットを
#     オンデマンドで起こす——動いている CP は、たったいま消したものを作り直す。
#  2. Workspace サービス（`delete-service --force` が Cloud Map のエントリも消す。
#     手で消そうとすると ServiceNotFound）
#  3. スロットを terminate。**home の EBS は道連れにならない**（遅延返却の設計どおり
#     ＝残って課金され続ける）
#  4. コンテナインスタンスの登録解除（残るとクラスタ削除が落ちる。実測 4 本中 3 本）
#  5. EFS アクセスポイント（**消し忘れると 10-data の削除が止まる**）
#  6. SSM `/af-ws/*`（`/af-cp/*` は既定で残す＝同じアカウントに立て直せる）
#  7. スナップショット（golden・退避・バックアップ）
#  8. スタックを逆順に**1 本ずつ待って**。⚠️ まとめて発行すると、importer が居る間
#     exporting stack の削除が**無言でキャンセル**され、待ちループだけが回る
#  9. `Persistence=retain` は削除保護を外さないと 8 が落ちる。**最終スナップショットと
#     EFS は残す**（それが retain の意味なので、`--purge-retained` を明示しない限り消さない）
# 10. タスク定義（費用は 0 だがスタックより長生きする）
# 11. ACM の検証 CNAME はゾーンに残る（消さないと次の配備の証明書が「速すぎて」通り、
#     発行経路を検証したことにならない）
#
# ## 触らないもの
#
# ホストゾーンそのもの・`/af-cp/*`（`--purge-secrets` を付けたときだけ消す）・
# retain で残した RDS 最終スナップショットと EFS（`--purge-retained` のときだけ）・
# そして**この配備のタグや名前に一致しないもの一切**（Control Tower 配下のアカウントでは
# `StackSet-*` / `<org>-baseline-*` / Account Factory の VPC が同居している）。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/aws/ecs/env.sh
. "$HERE/env.sh"

usage() {
  cat >&2 <<'EOF'
usage: teardown.sh --profile <p> --region <r> [--yes] [--purge-retained] [--purge-secrets] [--dry-run]
  --profile         aws cli profile (this is how a deployment is addressed)
  --region          region of the deployment
  --stack           ingress stack name (default af-ecs-ingress)
                    ⚠️ run capture-env.sh first — the parameters are unreadable once the
                    stacks are gone, and they are what a rebuild needs
  --yes             actually delete (without it: inventory + plan only)
  --purge-retained  also delete what Persistence=retain deliberately kept (RDS final
                    snapshot, EFS filesystem). Ignored when persistence=delete
  --purge-secrets   also delete /af-cp/* (cookie-secret, master-key, IdP client secret).
                    Keep them to redeploy into the same account
  --dry-run         print every write instead of making it
EOF
}

PROFILE=""; REGION=""; STACK="af-ecs-ingress"; AF_YES=0; AF_DRY=0; PURGE_RETAINED=0; PURGE_SECRETS=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)        PROFILE="${2:?--profile needs a value}"; shift ;;
    --region)         REGION="${2:?--region needs a value}"; shift ;;
    --stack)          STACK="${2:?--stack needs a value}"; shift ;;
    --yes)            AF_YES=1 ;;
    --purge-retained) PURGE_RETAINED=1 ;;
    --purge-secrets)  PURGE_SECRETS=1 ;;
    --dry-run)        AF_DRY=1 ;;
    -h|--help)        usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
  shift
done
[ -n "$PROFILE" ] && [ -n "$REGION" ] || { usage; exit 2; }
export AF_YES AF_DRY
af_env_init "$PROFILE" "$REGION" "$STACK"
CLUSTER="$(af_cluster)"
CP_SERVICE="af-$AF_STACK_INGRESS-cp"
# ⚠️ **撤収は途中から再実行される。** そのとき ingress スタックはもう無いので、引数は
# 読めない——そして読めないまま黙って先へ進むと、**ホストゾーンの ACM 検証 CNAME が
# 消し残る**（実際に残した）。残っても壊れはしないが、次に立てたとき証明書が
# 「速すぎて」通り、発行経路を検証したことにならない。生きていなければ控えから読む。
captured_param() {  # captured_param <key> — キャプチャした 30-ingress の引数
  local f line
  f="$(af_params_file 30-ingress)"
  [ -r "$f" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in "$1"=*) echo "${line#*=}"; return ;; esac
  done < "$f"
}
SSM_PREFIX="$(af_stack_param "$AF_STACK_INGRESS" SsmPrefix)"
[ -n "$SSM_PREFIX" ] || SSM_PREFIX="$(captured_param SsmPrefix)"
: "${SSM_PREFIX:=/af-cp}"
HOSTED_ZONE="$(af_stack_param "$AF_STACK_INGRESS" HostedZoneId)"
[ -n "$HOSTED_ZONE" ] || HOSTED_ZONE="$(captured_param HostedZoneId)"
EFS_ID="$(af_stack_output "$AF_STACK_DATA" EfsId)"
DB_ID=""
if af_stack_exists "$AF_STACK_DATA"; then
  DB_ID="$("${AWS[@]}" cloudformation describe-stack-resource --stack-name "$AF_STACK_DATA" \
    --logical-resource-id Db --query 'StackResourceDetail.PhysicalResourceId' --output text 2>/dev/null || true)"
  case "$DB_ID" in None) DB_ID="" ;; esac
fi

txt() { tr '\t' '\n' | grep -v '^$' || true; }

# --- 0) いま何があるか（--yes が無ければここまで） ---------------------------
echo "==> teardown plan: ${AF_FQDN:-<no live ingress stack>} (profile=$AF_PROFILE region=$AF_REGION)"
echo "    stacks   : $AF_STACK_INGRESS${AF_STACK_POOL:+ / $AF_STACK_POOL} / $AF_STACK_PLATFORM / $AF_STACK_DATA / $AF_STACK_NETWORK"
echo "    cluster  : $CLUSTER   persistence=$AF_PERSISTENCE   runtime=$AF_WS_RUNTIME"

list_ws_svcs() { "${AWS[@]}" ecs list-services --cluster "$CLUSTER" --query 'serviceArns' --output text 2>/dev/null | txt | grep '/af-ws-' || true; }
list_slots() { "${AWS[@]}" ec2 describe-instances \
  --filters "Name=tag:af-pool,Values=$CLUSTER" \
    "Name=instance-state-name,Values=pending,running,stopping,stopped" \
  --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null | txt || true; }
list_homes() { "${AWS[@]}" ec2 describe-volumes --filters "Name=tag:af-pool,Values=$CLUSTER" \
  --query 'Volumes[].VolumeId' --output text 2>/dev/null | txt || true; }
list_snaps() { "${AWS[@]}" ec2 describe-snapshots --owner-ids self --filters "Name=tag:af-pool,Values=$CLUSTER" \
  --query 'Snapshots[].SnapshotId' --output text 2>/dev/null | txt || true; }
list_aps() {
  [ -n "$EFS_ID" ] || return 0
  "${AWS[@]}" efs describe-access-points --file-system-id "$EFS_ID" \
    --query 'AccessPoints[].AccessPointId' --output text 2>/dev/null | txt || true
}
WS_SVCS="$(list_ws_svcs)"; SLOTS="$(list_slots)"; HOMES="$(list_homes)"; SNAPS="$(list_snaps)"; APS="$(list_aps)"
count() {
  if [ -z "${1// /}" ]; then echo 0; else printf '%s\n' "$1" | wc -l | tr -d ' '; fi
}
echo "    runtime residue: workspaces=$(count "$WS_SVCS") slots=$(count "$SLOTS") volumes=$(count "$HOMES") snapshots=$(count "$SNAPS") efs-access-points=$(count "$APS")"
echo "    keeping        : hosted zone $HOSTED_ZONE / $SSM_PREFIX/* $([ "$PURGE_SECRETS" = 1 ] && echo '(NO — --purge-secrets)')"
if [ "$AF_PERSISTENCE" = retain ]; then
  echo "    retain         : RDS final snapshot ＋ EFS $EFS_ID は残す $([ "$PURGE_RETAINED" = 1 ] && echo '(NO — --purge-retained)')"
fi
echo ""
echo "⚠️ ECR（af-control-plane / af-workspace）は 20-platform のリソースで EmptyOnDelete: true。"
echo "   スタックを消すとイメージごと消える。立て直しは crane copy からやり直しになる。"

if ! af_confirm "この配備をまるごと削除する（取り返しがつかない。home の中身も消える）"; then
  echo ""
  echo "（何もしていない。実行するには --yes）"
  exit 0
fi

# 引数を退避していない状態で消すと、立て直しの材料が失われる。
if [ ! -r "$AF_ENV_DIR/params/30-ingress" ]; then
  echo "ERROR: $AF_ENV_DIR/params/30-ingress が無い。capture-env.sh を先に走らせること" >&2
  echo "       （テンプレートは repo にあるが、何を渡したかは配備の中にしか無い）" >&2
  exit 1
fi

# --- 1) CP を止める ----------------------------------------------------------
echo "==> 1. stopping the control plane ($CP_SERVICE)"
af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$CP_SERVICE" --desired-count 0 >/dev/null 2>&1 || true

# ★ **止まったことを確かめてから数える。** desired=0 はタスクに死ねと言うだけで、死ぬまでの
# 間 CP は普通に働き続ける——そしてこの掃除そのものが CP を働かせる: golden スナップショット
# を消せば「この配備には golden が無い」に見えるので、**CP は焼き直しを始めてスロットを
# 起こす**（実測: 撤収の最中に m7i と m8g が 1 台ずつ生えた）。走る前に数えた一覧で
# terminate すると、そのあとに生えたものが**課金され続ける孤児**として残る。
if [ "$AF_DRY" != 1 ]; then
  for _ in $(seq 1 30); do
    running="$("${AWS[@]}" ecs describe-services --cluster "$CLUSTER" --services "$CP_SERVICE" \
      --query 'services[0].runningCount' --output text 2>/dev/null || echo 0)"
    # ⚠️ 読めなかったとき（サービスが既に消えている・権限が無い）に粘らない。
    # 数でない答えは「もう見えない」であって「まだ走っている」ではない。
    case "$running" in ""|None|0) break ;; *[!0-9]*) break ;; esac
    sleep 10
  done
  echo "==> 1b. re-reading the residue now that the CP is down"
  WS_SVCS="$(list_ws_svcs)"; SLOTS="$(list_slots)"; HOMES="$(list_homes)"; SNAPS="$(list_snaps)"; APS="$(list_aps)"
  echo "    workspaces=$(count "$WS_SVCS") slots=$(count "$SLOTS") volumes=$(count "$HOMES") snapshots=$(count "$SNAPS") efs-access-points=$(count "$APS")"
fi

# --- 2) Workspace サービス ---------------------------------------------------
echo "==> 2. deleting workspace services ($(count "$WS_SVCS"))"
for s in $WS_SVCS; do
  af_run "${AWS[@]}" ecs update-service --cluster "$CLUSTER" --service "$s" --desired-count 0 >/dev/null 2>&1 || true
  af_run "${AWS[@]}" ecs delete-service --cluster "$CLUSTER" --service "$s" --force >/dev/null 2>&1 || true
done

# --- 3) スロットと home ------------------------------------------------------
if [ -n "$SLOTS" ]; then
  echo "==> 3. terminating slots"
  # shellcheck disable=SC2086
  af_run "${AWS[@]}" ec2 terminate-instances --instance-ids $SLOTS >/dev/null
  # shellcheck disable=SC2086
  [ "$AF_DRY" = 1 ] || "${AWS[@]}" ec2 wait instance-terminated --instance-ids $SLOTS
fi
if [ -n "$HOMES" ]; then
  echo "==> 3b. deleting home volumes（terminate では消えない）"
  for v in $HOMES; do
    af_run "${AWS[@]}" ec2 delete-volume --volume-id "$v" >/dev/null 2>&1 || echo "    (skip $v)"
  done
fi

# --- 4) コンテナインスタンスの登録解除 --------------------------------------
CIS="$("${AWS[@]}" ecs list-container-instances --cluster "$CLUSTER" --query 'containerInstanceArns' --output text 2>/dev/null | txt || true)"
if [ -n "$CIS" ]; then
  echo "==> 4. deregistering container instances ($(count "$CIS"))"
  for ci in $CIS; do
    af_run "${AWS[@]}" ecs deregister-container-instance --cluster "$CLUSTER" --container-instance "$ci" --force >/dev/null 2>&1 || true
  done
fi

# --- 5) EFS アクセスポイント -------------------------------------------------
if [ -n "$APS" ]; then
  echo "==> 5. deleting EFS access points ($(count "$APS"))"
  for ap in $APS; do
    af_run "${AWS[@]}" efs delete-access-point --access-point-id "$ap" >/dev/null 2>&1 || true
  done
fi

# --- 6) SSM ------------------------------------------------------------------
WS_PARAMS="$("${AWS[@]}" ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=/af-ws/" \
  --query 'Parameters[].Name' --output text 2>/dev/null | txt || true)"
if [ -n "$WS_PARAMS" ]; then
  echo "==> 6. deleting /af-ws/* ($(count "$WS_PARAMS"))"
  for p in $WS_PARAMS; do
    af_run "${AWS[@]}" ssm delete-parameter --name "$p" >/dev/null 2>&1 || true
  done
fi
if [ "$PURGE_SECRETS" = 1 ]; then
  CP_PARAMS="$("${AWS[@]}" ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=$SSM_PREFIX/" \
    --query 'Parameters[].Name' --output text 2>/dev/null | txt || true)"
  echo "==> 6b. deleting $SSM_PREFIX/* ($(count "$CP_PARAMS")) — 立て直すときは作り直しになる"
  for p in $CP_PARAMS; do
    af_run "${AWS[@]}" ssm delete-parameter --name "$p" >/dev/null 2>&1 || true
  done
fi

# --- 7) スナップショット -----------------------------------------------------
if [ -n "$SNAPS" ]; then
  echo "==> 7. deleting snapshots ($(count "$SNAPS")): golden / hibernation / backup"
  for s in $SNAPS; do
    af_run "${AWS[@]}" ec2 delete-snapshot --snapshot-id "$s" >/dev/null 2>&1 || echo "    (skip $s)"
  done
fi

# --- 9 前半) retain は削除保護を先に外す（外さないと 8 が落ちる） -------------
if [ "$AF_PERSISTENCE" = retain ] && [ -n "$DB_ID" ]; then
  echo "==> 8pre. removing RDS deletion protection ($DB_ID)"
  af_run "${AWS[@]}" rds modify-db-instance --db-instance-identifier "$DB_ID" \
    --no-deletion-protection --apply-immediately >/dev/null 2>&1 || true
fi

# --- 8 前半) CFN 受け渡し用バケットを空にする（空でないと 20-platform が消せない） ----
#
# 🔥 CloudFormation は**中身のあるバケットを削除できない**。ここを飛ばすと 20-platform の
# 削除が DELETE_FAILED で止まり、撤収が途中で死ぬ。そして「撤収が途中で死ぬ」は、まさに
# 今回 51,200 バイトの壁を 09-01 から誰も踏まなかった原因（docs/log/73 §73.7.2）と同じ形
# —— **走らせていない経路は壊れていても分からない**。ライフサイクル（7 日で expire）も
# 効いているが、それは事故を減らすだけで、削除の前提を満たすものではない。
CFN_BUCKET="$(af_stack_output "$AF_STACK_PLATFORM" CfnTemplatesBucket)"
if [ -n "$CFN_BUCKET" ]; then
  echo "==> 8pre2. emptying s3://$CFN_BUCKET (CFN template staging)"
  # バージョニングは付けていないので `s3 rm --recursive` で足りる。
  af_run "${AWS[@]}" s3 rm "s3://$CFN_BUCKET" --recursive >/dev/null 2>&1 \
    || echo "    (skip: 空にできなかった。20-platform の削除が落ちたらここを見る)"
fi

# --- 8) スタックを逆順に 1 本ずつ -------------------------------------------
echo "==> 8. deleting stacks in reverse order, one at a time"
for st in "$AF_STACK_INGRESS" "$AF_STACK_POOL" "$AF_STACK_PLATFORM" "$AF_STACK_DATA" "$AF_STACK_NETWORK"; do
  [ -n "$st" ] || continue
  if ! af_stack_exists "$st"; then echo "    - $st: already gone"; continue; fi
  echo "    - $st: delete"
  af_run "${AWS[@]}" cloudformation delete-stack --stack-name "$st"
  if [ "$AF_DRY" != 1 ]; then
    # ⚠️ ここで待つのが本体。まとめて発行すると exporting stack の削除が無言で
    # キャンセルされ、「消したつもり」で次に進んでしまう。
    if ! "${AWS[@]}" cloudformation wait stack-delete-complete --stack-name "$st"; then
      echo "ERROR: $st の削除が完了しなかった。CloudFormation のイベントを読むこと" >&2
      echo "       （典型は「まだ importer が居る」か、残ったランタイム資源の依存）" >&2
      exit 1
    fi
    echo "      done"
  fi
done

# --- 9 後半) retain が残したもの --------------------------------------------
if [ "$AF_PERSISTENCE" = retain ]; then
  if [ "$PURGE_RETAINED" = 1 ]; then
    # ⚠️ **ここは失敗を握り潰してはいけない。** retain が残したものは「消えなかったこと」が
    # 見えにくい——スタックはもう無いので CloudFormation は何も言わず、費用だけが残る。
    # よくあるのは EFS がマウントターゲットを持ったままで `FileSystemInUse` になる形。
    # だからエラーを捕まえて出し、最後に**実物を引いて**消えたことを確かめる。
    SNAP_ID="$("${AWS[@]}" rds describe-db-snapshots --snapshot-type manual \
      --query "DBSnapshots[?starts_with(DBSnapshotIdentifier,'$AF_STACK_DATA')].DBSnapshotIdentifier" \
      --output text 2>/dev/null | txt || true)"
    for s in $SNAP_ID; do
      echo "==> 9. deleting the RDS final snapshot $s"
      if [ "$AF_DRY" = 1 ]; then
        echo "DRY: rds delete-db-snapshot --db-snapshot-identifier $s"
      else
        err="$("${AWS[@]}" rds delete-db-snapshot --db-snapshot-identifier "$s" 2>&1)" \
          || echo "    ⚠️ 消せていない: $(printf '%s' "$err" | tail -1)"
      fi
    done
    if [ -n "$EFS_ID" ]; then
      echo "==> 9b. deleting the retained EFS $EFS_ID"
      if [ "$AF_DRY" = 1 ]; then
        echo "DRY: efs delete-file-system --file-system-id $EFS_ID"
      else
        err="$("${AWS[@]}" efs delete-file-system --file-system-id "$EFS_ID" 2>&1)" \
          || echo "    ⚠️ 消せていない: $(printf '%s' "$err" | tail -1)"
      fi
    fi
  else
    echo "==> 9. persistence=retain: RDS の最終スナップショットと EFS $EFS_ID は残した（--purge-retained で消す）"
  fi
fi

# --- 10) タスク定義 ----------------------------------------------------------
# ⚠️ `--family-prefix af-ws` が 0 を返したのに ACTIVE が 9 件あった（実測）。prefix を
# 使わずに列挙して自分で絞る。⚠️ `--max-items` 付きの --output text は末尾に改ページ
# トークン（`None`）を 1 行混ぜるので、ARN の形をしている行だけ通す。
echo "==> 10. task definitions"
TDS="$("${AWS[@]}" ecs list-task-definitions --status ACTIVE --query 'taskDefinitionArns' --output text 2>/dev/null \
  | txt | grep -E '/(af-ws-|af-.*-cp)' || true)"
for td in $TDS; do
  af_run "${AWS[@]}" ecs deregister-task-definition --task-definition "$td" >/dev/null 2>&1 || true
done
[ -n "$TDS" ] && echo "    deregistered $(count "$TDS")"

# --- 11) ACM の検証 CNAME ----------------------------------------------------
# 証明書を消してもゾーンには `_<hash>.<fqdn>` が残る。DELETE は TTL と値の**完全一致**が
# 要るので、いま入っている中身をそのまま送り返す。
if [ -n "$HOSTED_ZONE" ]; then
  REC="$("${AWS[@]}" route53 list-resource-record-sets --hosted-zone-id "$HOSTED_ZONE" \
    --query "ResourceRecordSets[?Type=='CNAME'&&ends_with(Name,'.$AF_FQDN.')].[Name,TTL,ResourceRecords[0].Value]" \
    --output text 2>/dev/null || true)"
  if [ -n "${REC// /}" ]; then
    echo "==> 11. deleting ACM validation CNAMEs"
    printf '%s\n' "$REC" | while IFS=$'\t' read -r name ttl value; do
      [ -n "$name" ] || continue
      case "$name" in _*) ;; *) continue ;; esac
      echo "    - $name"
      # ⚠️ ここで af_run を使うと、その DRY 行まで >/dev/null に吸われて「何もしないように
      # 見える dry-run」になる。分岐を書き下す方が正直である。
      if [ "$AF_DRY" = 1 ]; then
        echo "      DRY: route53 DELETE $name CNAME $value (TTL $ttl)"
      else
        "${AWS[@]}" route53 change-resource-record-sets --hosted-zone-id "$HOSTED_ZONE" \
          --change-batch "{\"Changes\":[{\"Action\":\"DELETE\",\"ResourceRecordSet\":{\"Name\":\"$name\",\"Type\":\"CNAME\",\"TTL\":$ttl,\"ResourceRecords\":[{\"Value\":\"$value\"}]}}]}" >/dev/null 2>&1 \
          || echo "      (skip — 中身が変わっている。list-resource-record-sets で確かめること)"
      fi
    done
  fi
fi

# --- 12) スイープ（ゼロを確かめる） -----------------------------------------
# ⚠️ ここでもう一度**刈る**。数えるだけでは足りない——スタックを消し終えるまでの間に
# CP が起こしたスロットや、そのスロットが作った home が残っていることがある（上の 1b と
# 同じ理由で、こちらは「消し始めてから死ぬまで」の窓）。もう誰も動いていないので、
# ここに居るものは定義上すべて残骸である。
if [ "$AF_DRY" != 1 ]; then
  late_slots="$(list_slots)"
  if [ -n "${late_slots// /}" ]; then
    echo "==> late arrivals: terminating $late_slots"
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 terminate-instances --instance-ids $late_slots >/dev/null 2>&1 || true
    # shellcheck disable=SC2086
    "${AWS[@]}" ec2 wait instance-terminated --instance-ids $late_slots 2>/dev/null || true
  fi
  for v in $(list_homes); do
    echo "==> late arrival: deleting volume $v"
    "${AWS[@]}" ec2 delete-volume --volume-id "$v" >/dev/null 2>&1 || echo "    (skip $v)"
  done
  for sn in $(list_snaps); do
    echo "==> late arrival: deleting snapshot $sn"
    "${AWS[@]}" ec2 delete-snapshot --snapshot-id "$sn" >/dev/null 2>&1 || echo "    (skip $sn)"
  done
fi

echo ""
echo "==> sweep（0 でないものが残骸）"
left() { printf '    %-22s %s\n' "$1" "$(count "$2")"; }
left "cfn stacks" "$(for st in "$AF_STACK_INGRESS" "$AF_STACK_POOL" "$AF_STACK_PLATFORM" "$AF_STACK_DATA" "$AF_STACK_NETWORK"; do
  [ -n "$st" ] && af_stack_exists "$st" && echo "$st"; done || true)"
left "ec2 instances" "$("${AWS[@]}" ec2 describe-instances --filters "Name=tag:af-pool,Values=$CLUSTER" \
  "Name=instance-state-name,Values=pending,running,stopping,stopped" \
  --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null | txt || true)"
left "ebs volumes" "$("${AWS[@]}" ec2 describe-volumes --filters "Name=tag:af-pool,Values=$CLUSTER" \
  --query 'Volumes[].VolumeId' --output text 2>/dev/null | txt || true)"
left "snapshots" "$("${AWS[@]}" ec2 describe-snapshots --owner-ids self --filters "Name=tag:af-pool,Values=$CLUSTER" \
  --query 'Snapshots[].SnapshotId' --output text 2>/dev/null | txt || true)"
left "ecs clusters" "$("${AWS[@]}" ecs list-clusters --query 'clusterArns' --output text 2>/dev/null | txt | grep -F "/$CLUSTER" || true)"
left "task definitions" "$("${AWS[@]}" ecs list-task-definitions --status ACTIVE --query 'taskDefinitionArns' \
  --output text 2>/dev/null | txt | grep -E '/(af-ws-|af-.*-cp)' || true)"
left "log groups /af" "$("${AWS[@]}" logs describe-log-groups --log-group-name-prefix /af \
  --query 'logGroups[].logGroupName' --output text 2>/dev/null | txt || true)"
# ★ EFS と RDS はスタックの外で生き延びうる唯一の実体（Persistence=retain）なので、
# **数えるところまでやる**。retain を残したままなら残骸ではないので、そう言い分ける。
efs_left=""
[ -n "$EFS_ID" ] && efs_left="$("${AWS[@]}" efs describe-file-systems --file-system-id "$EFS_ID" \
  --query 'FileSystems[].FileSystemId' --output text 2>/dev/null | txt || true)"
rds_left="$("${AWS[@]}" rds describe-db-instances \
  --query "DBInstances[?starts_with(DBInstanceIdentifier,'$AF_STACK_DATA')].DBInstanceIdentifier" \
  --output text 2>/dev/null | txt || true)"
snap_left="$("${AWS[@]}" rds describe-db-snapshots --snapshot-type manual \
  --query "DBSnapshots[?starts_with(DBSnapshotIdentifier,'$AF_STACK_DATA')].DBSnapshotIdentifier" \
  --output text 2>/dev/null | txt || true)"
if [ "$AF_PERSISTENCE" = retain ] && [ "$PURGE_RETAINED" != 1 ]; then
  printf '    %-22s %s\n' "efs (retain で残す)" "$(count "$efs_left")"
  printf '    %-22s %s\n' "rds snap (retain)" "$(count "$snap_left")"
  left "rds instances" "$rds_left"
else
  left "efs filesystems" "$efs_left"
  left "rds instances" "$rds_left"
  left "rds snapshots" "$snap_left"
fi

cat <<EOF

==> torn down: ${AF_FQDN:-this deployment}
    立て直す: deploy/aws/ecs/standup.sh --profile $AF_PROFILE --region $AF_REGION
    ⚠️ ECR は空（スタックと一緒に消えた）ので、standup が GHCR から crane copy し直す。
    残したもの: ホストゾーン $HOSTED_ZONE / $SSM_PREFIX/* $([ "$PURGE_SECRETS" = 1 ] && echo '（削除済み）')
EOF
