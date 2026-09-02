#!/usr/bin/env bash
# Stub end-to-end test for the deployment lifecycle scripts
# (standup.sh / teardown.sh / pause.sh — docs/log/73).
#
# 実 AWS も docker も使わない。PATH に置いた偽 `aws` / `crane` / `curl` が呼び出しを
# 記録し、**順序**を固定する。ここで守っているのは「消えること」ではなく
# **消える順序**である——実測で踏んだ壊れ方はすべて順序のものだった:
#
#   - CP を止める前に片付け始めると、**動いている CP が作り直す**
#   - スロットを terminate しても **home の EBS は残る**（別に消す必要がある）
#   - スタックをまとめて delete すると、importer が居る間 exporting stack の削除が
#     **無言でキャンセル**され、待ちループだけが回って「消えたつもり」になる
#   - 立てる側は 20（ECR を作る）の**後**にイメージを入れ、30 の**前**にプールを作って
#     その launch template の**新しい物理 ID**を 30 に渡さないと、スロットが二度と起きない
#
# そして最後の 1 件は「何もしないこと」のテストである: `--yes` を付けない撤収は
# **1 つも書き込みを出してはならない**。
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
ECS="$ROOT/deploy/aws/ecs"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STUB="$WORK/bin"; LOG="$WORK/calls.log"
mkdir -p "$STUB"

# --- 偽の控え（capture-env.sh が書いたもののかわり）。名前は <profile>.<region>。 ----
export AF_DEPLOY_STATE_DIR="$WORK/state"
STATE="$AF_DEPLOY_STATE_DIR/p.ap-northeast-1.t-ingress"
mkdir -p "$STATE/params"
cat > "$STATE/env" <<'EOF'
AF_FQDN=af.example.test
AF_STACK_NETWORK=t-network
AF_STACK_DATA=t-data
AF_STACK_PLATFORM=t-platform
AF_STACK_POOL=t-pool
AF_STACK_INGRESS=t-ingress
AF_WS_RUNTIME=ecs-ec2
AF_PERSISTENCE=delete
AF_IMAGE_TAG=9.9.9-dev-test
AF_DEV_DEPLOY=1
EOF
# 2 つめ: Persistence=retain の配備（プロファイル p2）。retain の経路はここでしか踏めない。
STATE2="$AF_DEPLOY_STATE_DIR/p2.ap-northeast-1.t-ingress"
mkdir -p "$STATE2/params"
sed 's/^AF_PERSISTENCE=delete$/AF_PERSISTENCE=retain/' "$STATE/env" > "$STATE2/env" 2>/dev/null || true

echo "VpcCidr=10.20.0.0/16"       > "$STATE/params/00-network"
echo "Persistence=delete"         > "$STATE/params/10-data"
echo "NetworkStackName=t-network" > "$STATE/params/20-platform"
echo "SlotRootVolumeGiB=100"      > "$STATE/params/40-ec2-pool"
cat > "$STATE/params/30-ingress" <<'EOF'
Fqdn=af.example.test
HostedZoneId=ZTEST
SsmPrefix=/af-cp
GoogleClientId=gid
ImageTag=0.0.0-old
Ec2SlotLaunchTemplate=lt-OLD
Ec2SlotAmiArm64=
CpArch=x86_64
BitbucketOauthKey=must-not-be-printed
EOF
cp -a "$STATE/params/." "$STATE2/params/"
sed -i 's/^AF_PERSISTENCE=delete$/AF_PERSISTENCE=retain/' "$STATE2/env"

# --- 偽 aws。問い合わせには「実物と同じ形」を返す ---------------------------
cat > "$STUB/aws" <<'FAKE'
#!/usr/bin/env bash
echo "aws $*" >> "$STUB_LOG"
args="$*"
case "$args" in
  *"sts get-caller-identity"*) echo "123456789012" ;;
  *"cloudformation list-exports"*"SlotLaunchTemplateId"*) echo "t-pool-SlotLaunchTemplateId" ;;
  *"cloudformation list-exports"*) echo "t-cluster" ;;
  *"--profile p2"*"describe-stack-resource"*) echo "t-db" ;;
  *"describe-stack-resource"*) echo "None" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='EfsId']"*) echo "fs-1" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='SlotLaunchTemplateId']"*) echo "lt-NEW" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='CfnTemplatesBucket']"*) echo "t-cfn-bucket" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='SlotAmiIdArm64']"*) echo "None" ;;
  *"cloudformation describe-stacks"*"Outputs[?OutputKey=='Url']"*) echo "https://af.example.test" ;;
  # capture-env.sh 用: 引数と Output の一覧（join 形式）。★ NatEipAllocationId は
  # **引数では空・Output には実体**という、今回踏んだ形そのものを再現する。
  # ⚠️ スタック名は --query より前に出る。グロブの順序を取り違えると全部この下の
  # 総称枝に落ちて、テストが「何も起きない」形で嘘をつく。
  *"t-network"*"Parameters[].join"*) printf 'VpcCidr=10.20.0.0/16\nNatEipAllocationId=\n' ;;
  *"t-network"*"Outputs[].join"*)    printf 'NatEipAllocationId=eipalloc-REAL\nVpcId=vpc-1\n' ;;
  *"Parameters[].join"*)             printf 'Fqdn=af.example.test\n' ;;
  *"Outputs[].join"*)                printf '\n' ;;
  *"ParameterKey=='Fqdn'"*) echo "af.example.test" ;;
  *"ParameterKey=='NetworkStackName'"*) echo "t-network" ;;
  *"ParameterKey=='DataStackName'"*) echo "t-data" ;;
  *"ParameterKey=='PlatformStackName'"*) echo "t-platform" ;;
  *"ParameterKey=='WsRuntime'"*) echo "ecs-ec2" ;;
  *"ParameterKey=='ImageTag'"*) echo "9.9.9-dev-test" ;;
  *"--profile p2"*"ParameterKey=='Persistence'"*) echo "retain" ;;
  *"ParameterKey=='Persistence'"*) echo "delete" ;;
  *"ParameterKey=='CpArch'"*) echo "x86_64" ;;
  *"ParameterKey=='Ec2SlotLaunchTemplate'"*) echo "lt-OLD" ;;
  *"ParameterKey=='SsmPrefix'"*) echo "/af-cp" ;;
  *"ParameterKey=='HostedZoneId'"*) echo "ZTEST" ;;
  *"ParameterKey=='Ec2SlotSleepSec'"*) echo "900" ;;
  *"cloudformation describe-stacks"*) echo "STACK" ;;
  *"ecs list-services"*) echo "arn:aws:ecs:x:1:service/t-cluster/af-ws-alice" ;;
  # runningCount の問い合わせ（CP が止まったかの待ち）と、desired>0 のサービス一覧を
  # 取り違えないこと。前者に名前を返すと、待ちループが 5 分回る。
  *"ecs describe-services"*"runningCount"*) echo "0" ;;
  *"ecs describe-services"*) echo "af-ws-alice" ;;
  *"ecs list-container-instances"*) echo "arn:aws:ecs:x:1:container-instance/ci-1" ;;
  *"ecs list-task-definitions"*) echo "arn:aws:ecs:x:1:task-definition/af-ws-alice:1" ;;
  *"ec2 describe-instances"*)
    if [ "${STUB_NO_SLOTS:-0}" = 1 ]; then echo ""; else echo "i-1"; fi ;;
  *"ec2 describe-volumes"*) echo "vol-1" ;;
  *"ec2 describe-snapshots"*) echo "snap-1" ;;
  *"efs describe-access-points"*) echo "fsap-1" ;;
  *"ssm describe-parameters"*) echo "/af-ws/alice" ;;
  *"ecr describe-images"*)
    # 撤収したあとの ECR は空。standup が crane copy する経路を通させる。
    [ "${STUB_ECR_HAS:-0}" = 1 ] || exit 1 ;;
  *"ssm get-parameter"*) echo "ok" ;;
  *"iam get-role"*) echo "ok" ;;
  *"route53 get-hosted-zone"*) echo "ok" ;;
  *"route53 list-resource-record-sets"*) printf '_abc.af.example.test.\t300\tval.acm-validations.aws.\n' ;;
  *"logs describe-log-groups"*) echo "" ;;
  *"ecs list-clusters"*) echo "" ;;
  *"rds describe-db-snapshots"*) echo "t-data-snapshot-db-xyz" ;;
  *"rds describe-db-instances"*) echo "" ;;
  *"efs describe-file-systems"*) echo "" ;;   # 消えたあとの確認＝空
esac
FAKE
cat > "$STUB/crane" <<'FAKE'
#!/usr/bin/env bash
echo "crane $*" >> "$STUB_LOG"
case "$1" in
  # 2 アーキの index を返す（リリース版の CP イメージと同じ形）。片方しか返さないと
  # --cp-arch arm64 の経路が前検査で落ちて、その先を試せない。
  manifest) echo '{"manifests":[{"platform":{"architecture":"amd64","os":"linux"}},{"platform":{"architecture":"arm64","os":"linux"}}]}' ;;
  auth) cat >/dev/null ;;
esac
FAKE
cat > "$STUB/curl" <<'FAKE'
#!/usr/bin/env bash
echo "curl $*" >> "$STUB_LOG"
echo 200
FAKE
chmod +x "$STUB/aws" "$STUB/crane" "$STUB/curl"
export PATH="$STUB:$PATH" STUB_LOG="$LOG"

fail() { echo "NG: $1"; echo "--- log ---"; cat "$LOG"; exit 1; }
# ⚠️ `set -o pipefail` の下では「見つからない grep」がパイプライン全体を失敗にし、
# set -e が**アサーションを報告する前に**スクリプトを殺す。見つからないことは
# ここでは正常な入力なので、明示的に握り潰す。
lineno() { local n; n="$(grep -nF -- "$1" "$LOG" | head -1 | cut -d: -f1)" || true; echo "$n"; }
lineno_last() { local n; n="$(grep -nF -- "$1" "$LOG" | tail -1 | cut -d: -f1)" || true; echo "$n"; }
has() { grep -qF -- "$1" "$LOG" || fail "missing: $1"; }
hasnt() { ! grep -qF -- "$1" "$LOG" || fail "must not happen: $1"; }
order() { # order <earlier> <later>
  local a b; a="$(lineno "$1")"; b="$(lineno "$2")"
  [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ] || fail "order: '$1' must precede '$2' (a=${a:-?} b=${b:-?})"
}
# 同じ呼び出しが 2 回出る（計画時の列挙と、CP が止まってからの再列挙）ときに使う。
# 「最後の 1 回が後に来ている」＝再列挙が確かに走っている、を見る。
order_again() { # order_again <earlier> <repeated-later>
  local a b; a="$(lineno "$1")"; b="$(lineno_last "$2")"
  [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ] || fail "order: '$2' must be re-read after '$1' (a=${a:-?} b=${b:-?})"
}

echo "== case 1: teardown without --yes touches nothing =="
: > "$LOG"
"$ECS/teardown.sh" --profile p --region ap-northeast-1 --stack t-ingress > "$WORK/out1" </dev/null
hasnt "delete-stack"
hasnt "delete-service"
hasnt "terminate-instances"
hasnt "delete-volume"
hasnt "update-service"
grep -q "取り返しがつかない" "$WORK/out1" || fail "the plan did not say what it would do"

echo "== case 2: teardown order =="
: > "$LOG"
"$ECS/teardown.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out2" </dev/null
# 1. CP を止めるのが最初。走っている CP は、消したそばから作り直す。
order "ecs update-service --cluster t-cluster --service af-t-ingress-cp --desired-count 0" \
      "ecs delete-service --cluster t-cluster --service arn:aws:ecs:x:1:service/t-cluster/af-ws-alice"
# 2. スロットを消してから home を消す（terminate では消えない）
order "ec2 terminate-instances" "ec2 delete-volume --volume-id vol-1"
# 3. EFS アクセスポイント → データ層スタック（先に消さないと 10-data が止まる）
order "efs delete-access-point" "cloudformation delete-stack --stack-name t-data"
# 4. スタックは逆順、しかも 1 本ずつ **待ってから** 次へ
order "cloudformation delete-stack --stack-name t-ingress" "cloudformation wait stack-delete-complete --stack-name t-ingress"
order "cloudformation wait stack-delete-complete --stack-name t-ingress" "cloudformation delete-stack --stack-name t-pool"
order "cloudformation wait stack-delete-complete --stack-name t-pool" "cloudformation delete-stack --stack-name t-platform"
order "cloudformation wait stack-delete-complete --stack-name t-platform" "cloudformation delete-stack --stack-name t-data"
order "cloudformation wait stack-delete-complete --stack-name t-data" "cloudformation delete-stack --stack-name t-network"
# 5. ★ CP が止まったことを確かめてから残置を数え直す（撤収の最中に CP が golden を
#    焼き直してスロットを起こすことがあり、走る前の一覧で terminate すると孤児が残る）
order_again "ecs update-service --cluster t-cluster --service af-t-ingress-cp --desired-count 0" \
            "ec2 describe-instances"
# 6. 既定では秘密を消さない（同じアカウントに立て直せる）
has "ssm delete-parameter --name /af-ws/alice"
hasnt "ssm delete-parameter --name /af-cp"
# 7. ACM の検証 CNAME は TTL と値を完全一致で送り返す
has '"TTL":300'
has "val.acm-validations.aws."

echo "== case 3: standup order and the launch template hand-off =="
: > "$LOG"
"$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3" </dev/null
order "cloudformation deploy --stack-name t-network" "cloudformation deploy --stack-name t-data"
order "cloudformation deploy --stack-name t-data" "cloudformation deploy --stack-name t-platform"
# ECR は 20 が作る。イメージはその後、30 の前。
order "cloudformation deploy --stack-name t-platform" "crane copy ghcr.io/k-k1/agent-fleet/control-plane:9.9.9-dev-test"
order "crane copy ghcr.io/k-k1/agent-fleet/workspace:9.9.9-dev-test" "cloudformation deploy --stack-name t-pool"
order "cloudformation deploy --stack-name t-pool" "cloudformation deploy --stack-name t-ingress"
# capability を取り違えると即座に拒否される
grep -q "deploy --stack-name t-data .*CAPABILITY_AUTO_EXPAND" "$LOG" || fail "10-data needs CAPABILITY_AUTO_EXPAND"
grep -q "deploy --stack-name t-platform .*CAPABILITY_NAMED_IAM" "$LOG" || fail "20-platform needs CAPABILITY_NAMED_IAM"
grep -q "deploy --stack-name t-pool .*CAPABILITY_NAMED_IAM" "$LOG" || fail "40-ec2-pool needs CAPABILITY_NAMED_IAM"
# ★ 立て直したプールの **新しい** launch template を 30 へ。古い値が残ると CFN も CP も
#   成功して、スロットだけが二度と起きない。
grep -q "deploy --stack-name t-ingress .*Ec2SlotLaunchTemplate=lt-NEW" "$LOG" || fail "30-ingress got a stale launch template"
hasnt "Ec2SlotLaunchTemplate=lt-OLD"
grep -q "deploy --stack-name t-ingress .*ImageTag=9.9.9-dev-test" "$LOG" || fail "30-ingress did not get the deployed tag"
# ★ フラグが**渡す値**まで届いているか。検査だけ通って既定値で立つ、が実際に起きた。
: > "$LOG"
"$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes --cp-arch arm64 > /dev/null </dev/null
grep -q "deploy --stack-name t-ingress .*CpArch=arm64" "$LOG" || fail "--cp-arch did not reach the CFN parameters"
if grep -q "deploy --stack-name t-ingress .*CpArch=x86_64" "$LOG"; then fail "the captured CpArch overrode the flag"; fi

echo "== case 3b: 51,200 バイトを超えるテンプレートは S3 経由で渡す =="
#
# 🔥 これが無いと 2026-09-01 の再演になる。30-ingress.yaml が 51,200 バイトを超えた瞬間、
# **それを配備する経路が全部止まった**（standup も update も同じ `cloudformation deploy
# --template-file` を呼ぶ）。AWS CLI は API を叩く前にファイルサイズで断るので、症状は
# CFN ではなく CLI のエラーとして出る。しかも撤収→再構築を 1 往復していなかったため、
# ingress を「作る」経路は 3 か月近く 1 度も走っていなかった（docs/log/73 §73.7.2）。
#
# ★ 判定は**サイズ**であって名前ではない。テンプレートを 1 つ太らせて、そのときだけ
#   --s3-bucket が付くことを見る。
: > "$LOG"
FATCFN="$WORK/cfn"; mkdir -p "$FATCFN"; cp "$ECS"/cfn/*.yaml "$FATCFN"/
python3 - "$FATCFN/30-ingress.yaml" <<'PYEOF'
import sys
p = sys.argv[1]
with open(p, "a") as f:
    f.write("\n# pad " + "x" * 60000 + "\n")
PYEOF
[ "$(wc -c < "$FATCFN/30-ingress.yaml")" -gt 51200 ] || fail "パディングが効いていない"
[ "$(wc -c < "$FATCFN/00-network.yaml")" -le 51200 ] || fail "00-network まで大きい（前提が違う）"
AF_STANDUP_CFN_DIR="$FATCFN" "$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3b" </dev/null
grep -q "deploy --stack-name t-ingress .*--s3-bucket t-cfn-bucket" "$LOG" \
  || fail "大きい 30-ingress が --s3-bucket 無しで渡された（2026-09-01 の再演）"
# 小さいテンプレートは従来どおり（S3 を経由させると余計な権限と後片付けが要る）
if grep -q "deploy --stack-name t-network .*--s3-bucket" "$LOG"; then
  fail "小さいテンプレートまで S3 経由になっている"
fi

echo "== case 3c: 撤収は 20-platform を消す前にバケットを空にする =="
# CFN は**中身のあるバケットを削除できない**。ここを飛ばすと 20-platform が
# DELETE_FAILED で止まり、撤収が途中で死ぬ＝次の立て直しがまた実証されないまま残る。
: > "$LOG"
"$ECS/teardown.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out3c" </dev/null
order "s3 rm s3://t-cfn-bucket --recursive" "cloudformation delete-stack --stack-name t-platform"

echo "== case 3d: capture は Output にしか無い値を取り落とさない =="
#
# 🔴 空の引数は「自分で作る」分岐を選んだ印であることがあり、作られた実体の id は
# Output 側にしかない。引数だけ写すと、立て直しで同じ空値がもう一度「作る」を選び、
# **前の実体（Retain の EIP）が孤児になる**＝顧客が許可リストに載せた egress アドレスが
# 黙って変わる。2026-09-02 の実機一巡で実際に踏んだ形。
: > "$LOG"
CAPOUT="$WORK/capture"; rm -rf "$CAPOUT"
AF_DEPLOY_STATE_DIR="$CAPOUT" "$ECS/capture-env.sh" --profile p --region ap-northeast-1 --stack t-ingress > "$WORK/out3d" </dev/null
CAPFILE="$CAPOUT/p.ap-northeast-1.t-ingress/params/00-network"
[ -r "$CAPFILE" ] || fail "capture が 00-network を書いていない"
grep -q "^NatEipAllocationId=eipalloc-REAL$" "$CAPFILE" \
  || fail "空の引数が Output の実体で埋まっていない（立て直しで EIP が孤児になる）: $(grep NatEip "$CAPFILE" || echo '<行が無い>')"
# 引数に無い Output まで写してはいけない（CFN は知らない引数を拒む）
if grep -q "^VpcId=" "$CAPFILE"; then fail "引数ではない Output まで控えに写している"; fi

echo "== case 4: pause stops the control plane LAST =="
: > "$LOG"
"$ECS/pause.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes --fast > "$WORK/out4" </dev/null
order "ecs update-service --cluster t-cluster --service af-ws-alice --desired-count 0" "ec2 stop-instances"
order "ec2 stop-instances" "ecs update-service --cluster t-cluster --service af-t-ingress-cp --desired-count 0"

echo "== case 5: nothing prints a secret-looking parameter =="
: > "$LOG"
"$ECS/standup.sh" --profile p --region ap-northeast-1 --stack t-ingress --yes --dry-run > "$WORK/out5" </dev/null
if grep -q "must-not-be-printed" "$WORK/out5"; then fail "a secret-looking parameter value was printed"; fi
grep -q "BitbucketOauthKey=\*\*\*" "$WORK/out5" || fail "the masked form is missing"

echo "== case 6: retain — 削除保護は外すが、残したものは消さない =="
: > "$LOG"
"$ECS/teardown.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes > "$WORK/out6" </dev/null
# 削除保護を外すのは **スタック削除より前**（外さないと delete-stack がそこで落ちる）
order "rds modify-db-instance --db-instance-identifier t-db --no-deletion-protection" \
      "cloudformation delete-stack --stack-name t-data"
# retain が残したものには触らない
hasnt "rds delete-db-snapshot"
hasnt "efs delete-file-system"
grep -q "retain" "$WORK/out6" || fail "retain で残したことを言っていない"

echo "== case 7: retain + --purge-retained — 全部消し、消えたことを確かめる =="
: > "$LOG"
"$ECS/teardown.sh" --profile p2 --region ap-northeast-1 --stack t-ingress --yes --purge-retained > "$WORK/out7" </dev/null
# ★ 消すのは **スタックが全部消えたあと**（先に消すと delete-stack が最終スナップショットを
#   作り直す/掴んだままになる）
order "cloudformation wait stack-delete-complete --stack-name t-network" "rds delete-db-snapshot"
order "cloudformation wait stack-delete-complete --stack-name t-network" "efs delete-file-system"
has "rds delete-db-snapshot --db-snapshot-identifier t-data-snapshot-db-xyz"
has "efs delete-file-system --file-system-id fs-1"
# スイープで実物を引き直して数えている（黙って消え残らせない）
order "efs delete-file-system" "efs describe-file-systems --file-system-id fs-1"

echo "OK: deployment lifecycle stub test passed"
