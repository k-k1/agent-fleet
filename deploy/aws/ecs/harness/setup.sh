#!/bin/bash
# Minimal infrastructure for exercising the EC2 pool adapter (ecs-ec2) against real AWS.
# The approach is to stand up the repository's own 40-ec2-pool.yaml as it is — the template
# itself is what is being verified, so no hand-written launch template. That stack imports
# exports from 00-network / 20-platform, so only those two are supplied by dummy stacks. No
# ALB and no RDS.
#
# Two network shapes:
#
#   default         one public subnet of the default VPC. Cheap and fast, but a different
#                   shape from production: slots reach the outside through an auto-assigned
#                   public IPv4 and therefore hit §64.5 (3), "task ENI lingers -> public
#                   IPv4 is lost -> it never comes back" (which did happen, §64.17.5).
#   AF_HARNESS_NAT=1  a dedicated VPC plus private subnets and a NAT gateway — equivalent to
#                   production. Neither slots nor tasks have a public IP and egress goes
#                   through the NAT. The NAT bills by the hour (about $0.062/h plus
#                   transfer), so always close it down through teardown.
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
  # --- production-equivalent: dedicated VPC, public (for the NAT) plus private (slots and
  # tasks) ---
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
  # A second AZ. EBS is pinned to an AZ, so with a single subnet "does a user whose home is
  # in another AZ get a slot in that AZ" is never exercised at all (§64.20). The NAT stays
  # shared on the 1a side (cross-AZ transfer costs something, but what is being verified is
  # placement, not the route).
  SUBNET2=$(aws ec2 create-subnet --vpc-id "$VPC" --cidr-block 10.90.2.0/24 \
    --availability-zone ap-northeast-1c --query Subnet.SubnetId --output text)
  aws ec2 create-tags --resources "$PUB" --tags Key=Name,Value=$N-public
  aws ec2 create-tags --resources "$SUBNET" --tags Key=Name,Value=$N-private
  aws ec2 create-tags --resources "$SUBNET2" --tags Key=Name,Value=$N-private-c
  # The NAT itself sits on the public side, so only that subnet has a public route.
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

# --- SGs: one for the task ENI (self-referencing, allow all) plus EFS on 2049 from inside
# the VPC ---
SG=$(aws ec2 create-security-group --group-name $N-ws --description "af-ec2c ws task eni" --vpc-id "$VPC" --query GroupId --output text)
aws ec2 authorize-security-group-ingress --group-id "$SG" --protocol -1 --source-group "$SG" >/dev/null
EFSSG=$(aws ec2 create-security-group --group-name $N-efs --description "af-ec2c efs" --vpc-id "$VPC" --query GroupId --output text)
# With the EC2 launch type plus awsvpc it is not obvious whose route carries the EFS mount,
# so this opens it to the whole VPC CIDR to allow both the task ENI and the host (sandbox
# only).
aws ec2 authorize-security-group-ingress --group-id "$EFSSG" --protocol tcp --port 2049 --cidr "$CIDR" >/dev/null
echo "SG=$SG EFSSG=$EFSSG"

# --- EFS (the credential hybrid: the claude-config and keep access points live on it) ---
EFS=$(aws efs create-file-system --performance-mode generalPurpose --throughput-mode bursting \
  --tags Key=Name,Value=$N --query FileSystemId --output text)
while [ "$(aws efs describe-file-systems --file-system-id "$EFS" --query 'FileSystems[0].LifeCycleState' --output text)" != available ]; do sleep 3; done
# A mount target is needed per AZ. Without one, a task on a slot in the second AZ cannot
# mount the credential EFS and dies — the same holds for a multi-AZ production setup.
aws efs create-mount-target --file-system-id "$EFS" --subnet-id "$SUBNET" --security-groups "$EFSSG" >/dev/null
aws efs create-mount-target --file-system-id "$EFS" --subnet-id "$SUBNET2" --security-groups "$EFSSG" >/dev/null
echo "EFS=$EFS"

# --- ECR plus a copy of the same image production uses (no docker needed — crane) ---
aws ecr create-repository --repository-name $N-ws >/dev/null 2>&1 || true
ECR="$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com/$N-ws"
aws ecr get-login-password | crane auth login "$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com" -u AWS --password-stdin
crane copy ghcr.io/k-k1/agent-fleet/workspace:0.8.0 "$ECR:dev"
echo "ECR=$ECR:dev"

# --- ECS cluster plus the Service Connect namespace ---
NSOP=$(aws servicediscovery create-private-dns-namespace --name $N.internal --vpc "$VPC" --query OperationId --output text)
for _ in $(seq 60); do
  st=$(aws servicediscovery get-operation --operation-id "$NSOP" --query 'Operation.Status' --output text)
  [ "$st" = SUCCESS ] && break; sleep 5
done
NSID=$(aws servicediscovery list-namespaces --query "Namespaces[?Name=='$N.internal'].Id | [0]" --output text)
NSARN=$(aws servicediscovery get-namespace --id "$NSID" --query 'Namespace.Arn' --output text)
aws ecs create-cluster --cluster-name $N --service-connect-defaults namespace="$NSARN" >/dev/null
echo "NSARN=$NSARN"

# --- IAM: the execution role (ECR pull / logs / SSM SecureString) and the task role ---
TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam create-role --role-name $N-exec --assume-role-policy-document "$TRUST" >/dev/null
aws iam attach-role-policy --role-name $N-exec --policy-arn arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy
aws iam put-role-policy --role-name $N-exec --policy-name ssm-read --policy-document \
  "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"ssm:GetParameters\"],\"Resource\":\"arn:aws:ssm:$AWS_REGION:$ACCOUNT:parameter/af-ws/*\"}]}"
aws iam create-role --role-name $N-ws-task --assume-role-policy-document "$TRUST" >/dev/null
echo "roles ok"

# --- A copy of the CP task role, so E2E runs with production's permissions
# (docs/log/64 §64.23) ---
# Never transcribe it by hand: extract 20-platform.yaml's CpTaskRole policy as it stands.
# The moment it is transcribed, this stops being "the permissions the template grants" and
# becomes "the permissions we think it grants", and E2E passes a real hole green — which is
# what happened around decision 18-1, five rounds green without a single snapshot
# permission.
# On an intrinsic function that cannot be resolved, fail loudly rather than dropping it: a
# role missing one statement is a different role, looser (or tighter) than production by
# exactly that much.
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
# Pass credentials through a profile. Static STS credentials expire after an hour, and an
# 80-minute E2E run then fails part way through with credential errors (with a profile the
# SDK re-assumes role_arn by itself).
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

# --- supply the exports 40-ec2-pool.yaml imports from dummy stacks ---
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

# --- the real thing: stand up the repository's 40-ec2-pool.yaml as it is ---
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
# The step after sleep (ADR 0045 decision 23). Production recommends 4h; here it is cut to
# 300 s to match the 60 s sleep, because what real hardware has to confirm is not the length
# of time but the order: detach home, then terminate, leaving no ECS registration behind.
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
# On real hardware, E2E runs only the product side under this role; the test's own
# assertions and cleanup stay on the deployer. Without this, go test logs explicitly that it
# ran as the deployer.
export AF_HARNESS_CP_ROLE=$CPROLE
export AF_HARNESS_CP_PROFILE=$N-cp
export AF_HARNESS_CP_CONFIG=$HOME/af-ec2c/aws-config
ENV
echo "=== SETUP DONE ==="
cat state.env
