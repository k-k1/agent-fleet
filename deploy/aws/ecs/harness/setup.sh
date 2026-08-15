#!/bin/bash
# 第3ラウンド(b): EC2 プール型アダプタ（ecs-ec2）を実 AWS で通すための最小基盤。
# 方針: **repo の 40-ec2-pool.yaml をそのまま立てる**（テンプレート自体を検証したいので、
# 手書きの launch template は作らない）。同スタックは 00-network / 20-platform の
# export を参照するので、その 2 つだけダミースタックで供給する。
# NAT / ALB / RDS は作らない（デフォルト VPC のパブリックサブネット 1 本）。
set -euo pipefail
export AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1
N=af-ec2c
REPO_DIR=/home/dev/repos/agent-fleet@wip-sdcg4ag
cd ~/af-ec2c
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
VPC=$(aws ec2 describe-vpcs --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text)
CIDR=$(aws ec2 describe-vpcs --vpc-ids "$VPC" --query 'Vpcs[0].CidrBlock' --output text)
SUBNET=$(aws ec2 describe-subnets --filters Name=vpc-id,Values=$VPC Name=availability-zone,Values=ap-northeast-1a \
  --query 'Subnets[0].SubnetId' --output text)
echo "ACCOUNT=$ACCOUNT VPC=$VPC CIDR=$CIDR SUBNET=$SUBNET"

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
aws efs create-mount-target --file-system-id "$EFS" --subnet-id "$SUBNET" --security-groups "$EFSSG" >/dev/null
echo "EFS=$EFS"

# --- ECR ＋ 本番と同じイメージを複製（docker 不要・crane） ---
aws ecr create-repository --repository-name $N-ws >/dev/null 2>&1 || true
ECR="$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com/$N-ws"
aws ecr get-login-password | crane auth login "$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com" -u AWS --password-stdin
crane copy ghcr.io/k-k1/agent-fleet/workspace:0.8.0 "$ECR:dev"
echo "ECR=$ECR:dev"

# --- ECS クラスタ ＋ Service Connect 名前空間 ---
NSOP=$(aws servicediscovery create-private-dns-namespace --name $N.internal --vpc "$VPC" --query OperationId --output text)
for i in $(seq 60); do
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
export AF_ECS_SUBNETS=$SUBNET
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
export AF_ECS_EC2_MAX_SLOTS=2
export AF_ECS_EC2_HOME_GB=8
export AF_ECS_EC2_SWEEP_SEC=3600
export AF_ECS_EC2_TMP_MB=512
export AF_HARNESS_VPC=$VPC
export AF_HARNESS_EFSSG=$EFSSG
export AF_HARNESS_ACCOUNT=$ACCOUNT
ENV
echo "=== SETUP DONE ==="
cat state.env
