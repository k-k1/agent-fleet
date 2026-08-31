#!/bin/bash
# 第3ラウンド(b): EC2 プール型アダプタ（ecs-ec2）を実 AWS で通すための最小基盤。
# 方針: **repo の 40-ec2-pool.yaml をそのまま立てる**（テンプレート自体を検証したいので、
# 手書きの launch template は作らない）。同スタックは 00-network / 20-platform の
# export を参照するので、その 2 つだけダミースタックで供給する。ALB / RDS は作らない。
#
# ネットワークは 2 通り:
#
#   既定            デフォルト VPC のパブリックサブネット 1 本。安くて速いが、
#                   **本番と形が違う**——スロットは自動割当パブリック IPv4 で外に出るので、
#                   §64.5 (3) の「タスク ENI 残留 → パブリック IPv4 消失 → 戻ってこない」を
#                   踏む（実際に踏んだ・§64.17.5）。
#   AF_HARNESS_NAT=1  専用 VPC ＋ プライベートサブネット ＋ NAT ゲートウェイ。**本番相当**。
#                   スロットもタスクもパブリック IP を持たず、egress は NAT 経由になる。
#                   NAT は時間課金（約 $0.062/h ＋ 転送量）なので、必ず teardown まで閉じる。
set -euo pipefail
export AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1
N=af-ec2c
NAT=${AF_HARNESS_NAT:-0}
# Where the checkout is. ⚠️ NOT a hardcoded path: this script is meant to be COPIED to
# the work dir (see README), so it cannot always find the repo relative to itself, and a
# path baked in from whoever ran it last points at a worktree that no longer exists —
# which is exactly how it was found (a dead `@wip-…` path, months later). Resolve it, and
# say so instead of failing three stack-creations later.
if [ -z "${AF_HARNESS_REPO_DIR:-}" ] && [ -f "$(dirname "$0")/../cfn/20-platform.yaml" ]; then
  AF_HARNESS_REPO_DIR=$(cd "$(dirname "$0")/../../../.." && pwd)   # running in place
fi
REPO_DIR=${AF_HARNESS_REPO_DIR:-}
if [ ! -f "$REPO_DIR/deploy/aws/ecs/cfn/20-platform.yaml" ]; then
  echo "set AF_HARNESS_REPO_DIR to the agent-fleet checkout (this script needs cfn/20-platform.yaml and cfn/40-ec2-pool.yaml)" >&2
  exit 2
fi
cd ~/af-ec2c
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)

if [ "$NAT" = 1 ]; then
  # --- 本番相当: 専用 VPC / パブリック（NAT 用）＋ プライベート（スロットとタスク）---
  CIDR=10.90.0.0/16
  VPC=$(aws ec2 create-vpc --cidr-block $CIDR --query Vpc.VpcId --output text)
  aws ec2 create-tags --resources "$VPC" --tags Key=Name,Value=$N
  aws ec2 modify-vpc-attribute --vpc-id "$VPC" --enable-dns-hostnames
  IGW=$(aws ec2 create-internet-gateway --query InternetGateway.InternetGatewayId --output text)
  aws ec2 attach-internet-gateway --internet-gateway-id "$IGW" --vpc-id "$VPC"
  PUB=$(aws ec2 create-subnet --vpc-id "$VPC" --cidr-block 10.90.0.0/24 \
    --availability-zone ap-northeast-1a --query Subnet.SubnetId --output text)
  SUBNET=$(aws ec2 create-subnet --vpc-id "$VPC" --cidr-block 10.90.1.0/24 \
    --availability-zone ap-northeast-1a --query Subnet.SubnetId --output text)
  # 2 本目の AZ。EBS は AZ に固定されるので、「別 AZ に home を持つ人にその AZ のスロットが
  # 立つか」は 1 本の subnet では一度も試されない（§64.20）。NAT は 1a 側のまま共有する
  # （AZ 跨ぎの転送料はかかるが、検証したいのは経路ではなく配置）。
  SUBNET2=$(aws ec2 create-subnet --vpc-id "$VPC" --cidr-block 10.90.2.0/24 \
    --availability-zone ap-northeast-1c --query Subnet.SubnetId --output text)
  aws ec2 create-tags --resources "$PUB" --tags Key=Name,Value=$N-public
  aws ec2 create-tags --resources "$SUBNET" --tags Key=Name,Value=$N-private
  aws ec2 create-tags --resources "$SUBNET2" --tags Key=Name,Value=$N-private-c
  # NAT 自身はパブリック側に置くので、そのサブネットだけ公開経路を持つ。
  aws ec2 modify-subnet-attribute --subnet-id "$PUB" --map-public-ip-on-launch
  PUBRT=$(aws ec2 create-route-table --vpc-id "$VPC" --query RouteTable.RouteTableId --output text)
  aws ec2 create-route --route-table-id "$PUBRT" --destination-cidr-block 0.0.0.0/0 --gateway-id "$IGW" >/dev/null
  aws ec2 associate-route-table --route-table-id "$PUBRT" --subnet-id "$PUB" >/dev/null
  EIP=$(aws ec2 allocate-address --domain vpc --query AllocationId --output text)
  NATGW=$(aws ec2 create-nat-gateway --subnet-id "$PUB" --allocation-id "$EIP" \
    --query NatGateway.NatGatewayId --output text)
  aws ec2 create-tags --resources "$NATGW" --tags Key=Name,Value=$N
  echo "waiting for the NAT gateway (2–3 min)"
  aws ec2 wait nat-gateway-available --nat-gateway-ids "$NATGW"
  PRIVRT=$(aws ec2 create-route-table --vpc-id "$VPC" --query RouteTable.RouteTableId --output text)
  aws ec2 create-route --route-table-id "$PRIVRT" --destination-cidr-block 0.0.0.0/0 --nat-gateway-id "$NATGW" >/dev/null
  aws ec2 associate-route-table --route-table-id "$PRIVRT" --subnet-id "$SUBNET" >/dev/null
  aws ec2 associate-route-table --route-table-id "$PRIVRT" --subnet-id "$SUBNET2" >/dev/null
  aws ec2 create-tags --resources "$PUBRT" "$PRIVRT" "$IGW" --tags Key=Name,Value=$N
  echo "VPC=$VPC PUB=$PUB PRIVATE=$SUBNET,$SUBNET2 NAT=$NATGW EIP=$EIP"
else
  VPC=$(aws ec2 describe-vpcs --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text)
  CIDR=$(aws ec2 describe-vpcs --vpc-ids "$VPC" --query 'Vpcs[0].CidrBlock' --output text)
  SUBNET=$(aws ec2 describe-subnets --filters Name=vpc-id,Values="$VPC" Name=availability-zone,Values=ap-northeast-1a \
    --query 'Subnets[0].SubnetId' --output text)
  SUBNET2=$(aws ec2 describe-subnets --filters Name=vpc-id,Values="$VPC" Name=availability-zone,Values=ap-northeast-1c \
    --query 'Subnets[0].SubnetId' --output text)
fi
echo "ACCOUNT=$ACCOUNT VPC=$VPC CIDR=$CIDR SUBNET=$SUBNET SUBNET2=$SUBNET2 NAT=$NAT"

# --- SG: タスク ENI 用（自己参照で全許可）＋ EFS は VPC 内から 2049 ---
SG=$(aws ec2 create-security-group --group-name $N-ws --description "af-ec2c ws task eni" --vpc-id "$VPC" --query GroupId --output text)
aws ec2 authorize-security-group-ingress --group-id "$SG" --protocol -1 --source-group "$SG" >/dev/null
EFSSG=$(aws ec2 create-security-group --group-name $N-efs --description "af-ec2c efs" --vpc-id "$VPC" --query GroupId --output text)
# EC2 起動タイプ＋awsvpc では EFS のマウントを誰の経路で張るかが自明でないので、
# タスク ENI とホストの両方を許すために VPC CIDR から開ける（sandbox 限定）。
aws ec2 authorize-security-group-ingress --group-id "$EFSSG" --protocol tcp --port 2049 --cidr "$CIDR" >/dev/null
echo "SG=$SG EFSSG=$EFSSG"

# --- EFS（資格情報ハイブリッド: claude-config と keep のアクセスポイントが載る） ---
EFS=$(aws efs create-file-system --performance-mode generalPurpose --throughput-mode bursting \
  --tags Key=Name,Value=$N --query FileSystemId --output text)
while [ "$(aws efs describe-file-systems --file-system-id "$EFS" --query 'FileSystems[0].LifeCycleState' --output text)" != available ]; do sleep 3; done
# マウントターゲットは AZ ごとに要る。2 本目の AZ にスロットを立てたとき、ここが無いと
# タスクは資格情報の EFS をマウントできずに落ちる（本番の複数 AZ 構成でも同じ）。
aws efs create-mount-target --file-system-id "$EFS" --subnet-id "$SUBNET" --security-groups "$EFSSG" >/dev/null
aws efs create-mount-target --file-system-id "$EFS" --subnet-id "$SUBNET2" --security-groups "$EFSSG" >/dev/null
echo "EFS=$EFS"

# --- ECR ＋ 本番と同じイメージを複製（docker 不要・crane） ---
aws ecr create-repository --repository-name $N-ws >/dev/null 2>&1 || true
ECR="$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com/$N-ws"
aws ecr get-login-password | crane auth login "$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com" -u AWS --password-stdin
crane copy ghcr.io/k-k1/agent-fleet/workspace:0.8.0 "$ECR:dev"
echo "ECR=$ECR:dev"

# --- ECS クラスタ ＋ Service Connect 名前空間 ---
NSOP=$(aws servicediscovery create-private-dns-namespace --name $N.internal --vpc "$VPC" --query OperationId --output text)
for _ in $(seq 60); do
  st=$(aws servicediscovery get-operation --operation-id "$NSOP" --query 'Operation.Status' --output text)
  [ "$st" = SUCCESS ] && break; sleep 5
done
NSID=$(aws servicediscovery list-namespaces --query "Namespaces[?Name=='$N.internal'].Id | [0]" --output text)
NSARN=$(aws servicediscovery get-namespace --id "$NSID" --query 'Namespace.Arn' --output text)
aws ecs create-cluster --cluster-name $N --service-connect-defaults namespace="$NSARN" >/dev/null
echo "NSARN=$NSARN"

# --- IAM: 実行ロール（ECR pull / logs / SSM SecureString）とタスクロール ---
TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam create-role --role-name $N-exec --assume-role-policy-document "$TRUST" >/dev/null
aws iam attach-role-policy --role-name $N-exec --policy-arn arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy
aws iam put-role-policy --role-name $N-exec --policy-name ssm-read --policy-document \
  "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"ssm:GetParameters\"],\"Resource\":\"arn:aws:ssm:$AWS_REGION:$ACCOUNT:parameter/af-ws/*\"}]}"
aws iam create-role --role-name $N-ws-task --assume-role-policy-document "$TRUST" >/dev/null
echo "roles ok"

# --- CP タスクロールの複製。E2E を**本番の権限で**回すためのもの（docs/log/64 §64.23） ---
# ⚠️ **手で書き写さない。** 20-platform.yaml の CpTaskRole のポリシーをそのまま取り出す。
# 書き写した瞬間、ここは「テンプレートが与えている権限」ではなく「与えていると思っている
# 権限」になり、E2E は本物の穴を緑で通す——それが決定 18-1 で起きたことである
# （snapshot 権限が 1 つも無いまま 5 ラウンド緑だった）。
# 解決できない組み込み関数に当たったら**黙って落とさず落ちる**: statement が 1 本欠けた
# ロールは、欠けたぶんだけ本番より緩い（あるいは厳しい）別物になる。
DEPLOYER=$(aws sts get-caller-identity --query Arn --output text)
python3 - "$REPO_DIR/deploy/aws/ecs/cfn/20-platform.yaml" "$ACCOUNT" "$AWS_REGION" \
  "arn:aws:iam::$ACCOUNT:role/$N-exec" "arn:aws:iam::$ACCOUNT:role/$N-ws-task" \
  "arn:aws:ecr:$AWS_REGION:$ACCOUNT:repository/$N-ws" > cp-policy.json <<'PY'
import json, sys, yaml

tpl, account, region, exec_arn, ws_arn, ws_ecr_arn = sys.argv[1:7]

class CFN(yaml.SafeLoader):
    pass

def short(loader, suffix, node):
    if isinstance(node, yaml.ScalarNode):
        v = loader.construct_scalar(node)
    elif isinstance(node, yaml.SequenceNode):
        v = loader.construct_sequence(node)
    else:
        v = loader.construct_mapping(node)
    return {suffix if suffix == "Ref" else "Fn::" + suffix: v}

CFN.add_multi_constructor("!", short)
doc = yaml.load(open(tpl), Loader=CFN)
pols = doc["Resources"]["CpTaskRole"]["Properties"]["Policies"]
if len(pols) != 1:
    sys.exit("CpTaskRole now carries %d policies; teach this extractor which ones to copy" % len(pols))

# The harness stands up its own equivalents of the template's named resources; the
# workspace ECR repo is $N-ws (created above) and is what AF_ECS_WORKSPACE_IMAGE points
# at, so the drift probe (runtime_ecs_stale.go) must be scoped to it here too.
getatt = {"ExecRole.Arn": exec_arn, "WsTaskRole.Arn": ws_arn, "EcrWorkspace.Arn": ws_ecr_arn}

def resolve(x, path):
    if isinstance(x, dict):
        if list(x) == ["Fn::Sub"] and isinstance(x["Fn::Sub"], str):
            s = x["Fn::Sub"].replace("${AWS::AccountId}", account).replace("${AWS::Region}", region)
            if "${" in s:
                sys.exit("unresolved !Sub at %s: %r — teach the harness this substitution" % (path, s))
            return s
        if list(x) == ["Fn::GetAtt"]:
            k = x["Fn::GetAtt"]
            k = ".".join(k) if isinstance(k, list) else k
            if k not in getatt:
                sys.exit("!GetAtt %s at %s has no harness equivalent — map it or the copy is not the real role" % (k, path))
            return getatt[k]
        for key in x:
            if key == "Ref" or key.startswith("Fn::"):
                sys.exit("unsupported intrinsic %s at %s" % (key, path))
        return {k: resolve(v, path + "." + k) for k, v in x.items()}
    if isinstance(x, list):
        return [resolve(v, "%s[%d]" % (path, i)) for i, v in enumerate(x)]
    return x

print(json.dumps(resolve(pols[0]["PolicyDocument"], "PolicyDocument"), indent=1))
PY
echo "CP policy statements: $(python3 -c 'import json,sys;d=json.load(open("cp-policy.json"));print(" ".join(s.get("Sid","?") for s in d["Statement"]))')"
aws iam create-role --role-name $N-cp --max-session-duration 43200 --assume-role-policy-document \
  "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"$DEPLOYER\"},\"Action\":\"sts:AssumeRole\"}]}" >/dev/null
aws iam put-role-policy --role-name $N-cp --policy-name cp-runtime --policy-document file://cp-policy.json
# 資格情報はプロファイル経由で渡す。静的な STS 資格情報だと 1 時間で切れて、
# 80 分の E2E が途中から資格情報エラーで落ちる（SDK は role_arn を自分で取り直す）。
CPROLE=arn:aws:iam::$ACCOUNT:role/$N-cp
cat > aws-config <<CFG
[profile $N-cp]
role_arn = $CPROLE
source_profile = af-sandbox
region = $AWS_REGION
CFG
echo "waiting for the CP role to become assumable (IAM is eventually consistent)"
for _ in $(seq 30); do
  AWS_CONFIG_FILE=$PWD/aws-config AWS_PROFILE=$N-cp aws sts get-caller-identity >/dev/null 2>&1 && break
  sleep 5
done
AWS_CONFIG_FILE=$PWD/aws-config AWS_PROFILE=$N-cp aws sts get-caller-identity --query Arn --output text

aws logs create-log-group --log-group-name /$N >/dev/null 2>&1 || true

# --- 40-ec2-pool.yaml が参照する export をダミースタックで供給する ---
cat > exports.yaml <<'YAML'
AWSTemplateFormatVersion: "2010-09-09"
Description: af-ec2c harness — supplies only the two exports 40-ec2-pool.yaml imports.
Parameters:
  VpcId: { Type: String }
  ClusterName: { Type: String }
Resources:
  Noop: { Type: AWS::CloudFormation::WaitConditionHandle }
Outputs:
  VpcId:
    Value: !Ref VpcId
    Export: { Name: !Sub "${AWS::StackName}-VpcId" }
  ClusterName:
    Value: !Ref ClusterName
    Export: { Name: !Sub "${AWS::StackName}-ClusterName" }
YAML
aws cloudformation deploy --stack-name $N-net --template-file exports.yaml \
  --parameter-overrides VpcId="$VPC" ClusterName=$N
aws cloudformation deploy --stack-name $N-plat --template-file exports.yaml \
  --parameter-overrides VpcId="$VPC" ClusterName=$N

# --- ここが本番: repo の 40-ec2-pool.yaml をそのまま立てる ---
aws cloudformation deploy --stack-name $N-pool \
  --template-file "$REPO_DIR/deploy/aws/ecs/cfn/40-ec2-pool.yaml" \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides NetworkStackName=$N-net PlatformStackName=$N-plat SlotRootVolumeGiB=60
LT=$(aws cloudformation describe-stacks --stack-name $N-pool \
  --query "Stacks[0].Outputs[?OutputKey=='SlotLaunchTemplateId'].OutputValue | [0]" --output text)
echo "LT=$LT"

cat > state.env <<ENV
export AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1
export AF_ECS_EC2_LIVE=1
export AF_ECS_REGION=ap-northeast-1
export AF_ECS_CLUSTER=$N
export AF_ECS_SUBNETS=$SUBNET,$SUBNET2
export AF_ECS_SECURITY_GROUP=$SG
export AF_ECS_EFS_ID=$EFS
export AF_ECS_NAMESPACE_ARN=$NSARN
export AF_ECS_EXEC_ROLE=arn:aws:iam::$ACCOUNT:role/$N-exec
export AF_ECS_TASK_ROLE=arn:aws:iam::$ACCOUNT:role/$N-ws-task
export AF_ECS_LOG_GROUP=/$N
export AF_ECS_WORKSPACE_IMAGE=$ECR:dev
export AF_ECS_EC2_LAUNCH_TEMPLATE=$LT
export AF_ECS_EC2_POOL=$N
export AF_ECS_EC2_SLOT_TYPES=m7i.large:8192
export AF_ECS_EC2_MAX_SLOTS=4
export AF_ECS_EC2_SLOT_SLEEP_SEC=60
# 就寝の次の段（ADR 0045 決定 23）。本番の推奨は 4h だが、ここは就寝 60 秒に合わせて
# 300 秒に縮める——実機で確かめたいのは「時間の長さ」ではなく **home を外してから
# terminate し、ECS の登録も残さない**という順序の方なので。
export AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC=300
export AF_ECS_EC2_RELEASE_GRACE_SEC=60
export AF_ECS_EC2_HOME_GB=8
export AF_ECS_EC2_SWEEP_SEC=3600
export AF_ECS_EC2_TMP_MB=512
export AF_HARNESS_VPC=$VPC
export AF_HARNESS_SUBNET_B=$SUBNET2
export AF_HARNESS_EFSSG=$EFSSG
export AF_HARNESS_ACCOUNT=$ACCOUNT
export AF_HARNESS_NAT=$NAT
# 実機 E2E は**製品側だけ**をこのロールで走らせる（テスト自身の確認と後始末は
# デプロイヤのまま）。これが無いと go test は「デプロイヤで走った」と明示ログを出す。
export AF_HARNESS_CP_ROLE=$CPROLE
export AF_HARNESS_CP_PROFILE=$N-cp
export AF_HARNESS_CP_CONFIG=$HOME/af-ec2c/aws-config
ENV
echo "=== SETUP DONE ==="
cat state.env
