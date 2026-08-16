#!/bin/bash
# 全部消して 0 件を確認する。落ちても続ける（消し残しを作らないため set -e にしない）。
export AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1
N=af-ec2c
say(){ echo "=== $* ==="; }

say services
for s in $(aws ecs list-services --cluster $N --query 'serviceArns[]' --output text 2>/dev/null); do
  aws ecs update-service --cluster $N --service "$s" --desired-count 0 >/dev/null 2>&1
  aws ecs delete-service --cluster $N --service "$s" --force >/dev/null 2>&1 && echo "deleted $s"
done

say slots
IDS=$(aws ec2 describe-instances --filters Name=tag:af-pool,Values=$N Name=instance-state-name,Values=pending,running,stopping,stopped \
  --query 'Reservations[].Instances[].InstanceId' --output text)
[ -n "$IDS" ] && aws ec2 terminate-instances --instance-ids $IDS >/dev/null && aws ec2 wait instance-terminated --instance-ids $IDS && echo "terminated $IDS"

say container-instances
for ci in $(aws ecs list-container-instances --cluster $N --query 'containerInstanceArns[]' --output text 2>/dev/null); do
  aws ecs deregister-container-instance --cluster $N --container-instance "$ci" --force >/dev/null 2>&1 && echo "deregistered $ci"
done

say volumes
for v in $(aws ec2 describe-volumes --filters Name=tag:af-pool,Values=$N --query 'Volumes[].VolumeId' --output text); do
  aws ec2 delete-volume --volume-id "$v" >/dev/null 2>&1 && echo "deleted $v"
done

say task-definitions
for td in $(aws ecs list-task-definitions --family-prefix $N --query 'taskDefinitionArns[]' --output text 2>/dev/null); do
  aws ecs deregister-task-definition --task-definition "$td" >/dev/null 2>&1
done
for td in $(aws ecs list-task-definitions --family-prefix $N --status INACTIVE --query 'taskDefinitionArns[]' --output text 2>/dev/null); do
  aws ecs delete-task-definitions --task-definitions "$td" >/dev/null 2>&1
done

say cluster
aws ecs delete-cluster --cluster $N >/dev/null 2>&1 && echo "cluster deleted"

say efs
EFS=$(aws efs describe-file-systems --query "FileSystems[?Name=='$N'].FileSystemId | [0]" --output text)
if [ -n "$EFS" ] && [ "$EFS" != None ]; then
  for ap in $(aws efs describe-access-points --file-system-id "$EFS" --query 'AccessPoints[].AccessPointId' --output text); do
    aws efs delete-access-point --access-point-id "$ap" >/dev/null 2>&1 && echo "ap $ap"
  done
  for mt in $(aws efs describe-mount-targets --file-system-id "$EFS" --query 'MountTargets[].MountTargetId' --output text); do
    aws efs delete-mount-target --mount-target-id "$mt" >/dev/null 2>&1 && echo "mt $mt"
  done
  for _ in $(seq 40); do
    n=$(aws efs describe-mount-targets --file-system-id "$EFS" --query 'length(MountTargets)' --output text 2>/dev/null || echo 0)
    [ "$n" = 0 ] && break; sleep 5
  done
  aws efs delete-file-system --file-system-id "$EFS" >/dev/null 2>&1 && echo "efs $EFS deleted"
fi

say ssm-params
for p in $(aws ssm describe-parameters --query "Parameters[?starts_with(Name,'/af-ws/$N')].Name" --output text); do
  aws ssm delete-parameter --name "$p" >/dev/null 2>&1 && echo "param $p"
done

# ORDER, not a list. -plat / -net exist only to publish the two exports -pool imports,
# and CloudFormation CANCELS the delete of an exporting stack while an importer is still
# there ("Cannot delete export ... as it is in use by af-ec2c-pool" — measured; the three
# deletes were issued together and the last two silently did nothing, leaving both stacks
# behind while the wait loop spun on a delete that had already been cancelled).
say cfn-stacks
aws cloudformation delete-stack --stack-name $N-pool >/dev/null 2>&1
aws cloudformation wait stack-delete-complete --stack-name $N-pool 2>/dev/null && echo "stack $N-pool deleted"
for s in $N-plat $N-net; do
  aws cloudformation delete-stack --stack-name $s >/dev/null 2>&1
done
for s in $N-plat $N-net; do
  aws cloudformation wait stack-delete-complete --stack-name $s 2>/dev/null && echo "stack $s deleted"
done

say iam
for r in $N-exec $N-ws-task; do
  for p in $(aws iam list-attached-role-policies --role-name $r --query 'AttachedPolicies[].PolicyArn' --output text 2>/dev/null); do
    aws iam detach-role-policy --role-name $r --policy-arn "$p" >/dev/null 2>&1
  done
  for p in $(aws iam list-role-policies --role-name $r --query 'PolicyNames[]' --output text 2>/dev/null); do
    aws iam delete-role-policy --role-name $r --policy-name "$p" >/dev/null 2>&1
  done
  aws iam delete-role --role-name $r >/dev/null 2>&1 && echo "role $r deleted"
done

say namespace
NSID=$(aws servicediscovery list-namespaces --query "Namespaces[?Name=='$N.internal'].Id | [0]" --output text)
if [ -n "$NSID" ] && [ "$NSID" != None ]; then
  for sv in $(aws servicediscovery list-services --query "Services[].Id" --output text); do
    aws servicediscovery delete-service --id "$sv" >/dev/null 2>&1
  done
  aws servicediscovery delete-namespace --id "$NSID" >/dev/null 2>&1 && echo "namespace $NSID"
fi

say ecr-logs
aws ecr delete-repository --repository-name $N-ws --force >/dev/null 2>&1 && echo "ecr deleted"
aws logs delete-log-group --log-group-name /$N >/dev/null 2>&1 && echo "log group deleted"

say security-groups
sleep 20
for g in $N-ws $N-efs; do
  ID=$(aws ec2 describe-security-groups --filters Name=group-name,Values=$g --query 'SecurityGroups[0].GroupId' --output text 2>/dev/null)
  [ -n "$ID" ] && [ "$ID" != None ] && aws ec2 delete-security-group --group-id "$ID" >/dev/null 2>&1 && echo "sg $g deleted"
done

# AF_HARNESS_NAT=1 で作った専用 VPC。順序が全て——NAT を消して EIP を解放するまで
# サブネットは消えず、ENI が残っているうちはサブネットも消えない。NAT は削除要求から
# 実際に消えるまで数分あるので待つ（待たずに VPC を消しに行くと DependencyViolation）。
say vpc
VPC=$(aws ec2 describe-vpcs --filters Name=tag:Name,Values=$N --query 'Vpcs[0].VpcId' --output text 2>/dev/null)
if [ -n "$VPC" ] && [ "$VPC" != None ]; then
  for ng in $(aws ec2 describe-nat-gateways --filter Name=vpc-id,Values="$VPC" \
      --query 'NatGateways[?State!=`deleted`].NatGatewayId' --output text); do
    aws ec2 delete-nat-gateway --nat-gateway-id "$ng" >/dev/null 2>&1 && echo "nat $ng"
    aws ec2 wait nat-gateway-deleted --nat-gateway-ids "$ng" 2>/dev/null
  done
  for a in $(aws ec2 describe-addresses --query 'Addresses[].AllocationId' --output text); do
    aws ec2 release-address --allocation-id "$a" >/dev/null 2>&1 && echo "eip $a"
  done
  # ECS / EFS が残した ENI は上で消えているはずだが、遅れて外れるものがある。
  for _ in $(seq 20); do
    LEFT=$(aws ec2 describe-network-interfaces --filters Name=vpc-id,Values="$VPC" \
      --query 'NetworkInterfaces[].NetworkInterfaceId' --output text)
    [ -z "$LEFT" ] && break
    for e in $LEFT; do aws ec2 delete-network-interface --network-interface-id "$e" >/dev/null 2>&1; done
    sleep 10
  done
  for g in $(aws ec2 describe-security-groups --filters Name=vpc-id,Values="$VPC" \
      --query "SecurityGroups[?GroupName!='default'].GroupId" --output text); do
    aws ec2 delete-security-group --group-id "$g" >/dev/null 2>&1 && echo "sg $g"
  done
  for s in $(aws ec2 describe-subnets --filters Name=vpc-id,Values="$VPC" --query 'Subnets[].SubnetId' --output text); do
    aws ec2 delete-subnet --subnet-id "$s" >/dev/null 2>&1 && echo "subnet $s"
  done
  for rt in $(aws ec2 describe-route-tables --filters Name=vpc-id,Values="$VPC" \
      --query 'RouteTables[?length(Associations[?Main==`true`])==`0`].RouteTableId' --output text); do
    aws ec2 delete-route-table --route-table-id "$rt" >/dev/null 2>&1 && echo "rt $rt"
  done
  for igw in $(aws ec2 describe-internet-gateways --filters Name=attachment.vpc-id,Values="$VPC" \
      --query 'InternetGateways[].InternetGatewayId' --output text); do
    aws ec2 detach-internet-gateway --internet-gateway-id "$igw" --vpc-id "$VPC" >/dev/null 2>&1
    aws ec2 delete-internet-gateway --internet-gateway-id "$igw" >/dev/null 2>&1 && echo "igw $igw"
  done
  aws ec2 delete-vpc --vpc-id "$VPC" >/dev/null 2>&1 && echo "vpc $VPC deleted"
fi

say "残存確認（すべて空であること）"
echo -n "instances: "; aws ec2 describe-instances --filters Name=instance-state-name,Values=pending,running,stopping,stopped --query 'Reservations[].Instances[].InstanceId' --output json
echo -n "volumes:   "; aws ec2 describe-volumes --query 'Volumes[].VolumeId' --output json
echo -n "snapshots: "; aws ec2 describe-snapshots --owner-ids self --query 'Snapshots[].SnapshotId' --output json
echo -n "efs:       "; aws efs describe-file-systems --query 'FileSystems[].FileSystemId' --output json
echo -n "clusters:  "; aws ecs list-clusters --query 'clusterArns' --output json
echo -n "namespaces:"; aws servicediscovery list-namespaces --query 'Namespaces[].Name' --output json
echo -n "ecr:       "; aws ecr describe-repositories --query 'repositories[].repositoryName' --output json
echo -n "stacks:    "; aws cloudformation describe-stacks --query "Stacks[?StackStatus!='DELETE_COMPLETE'].StackName" --output json
echo -n "roles:     "; aws iam list-roles --query "Roles[?starts_with(RoleName,'af-')].RoleName" --output json
echo -n "eni:       "; aws ec2 describe-network-interfaces --query 'NetworkInterfaces[].NetworkInterfaceId' --output json
echo -n "sg:        "; aws ec2 describe-security-groups --query "SecurityGroups[?GroupName!='default'].GroupName" --output json
echo -n "vpc:       "; aws ec2 describe-vpcs --query "Vpcs[?!(IsDefault)].VpcId" --output json
echo -n "nat:       "; aws ec2 describe-nat-gateways --query "NatGateways[?State!='deleted'].NatGatewayId" --output json
echo -n "eip:       "; aws ec2 describe-addresses --query 'Addresses[].AllocationId' --output json
echo -n "logs:      "; aws logs describe-log-groups --query 'logGroups[].logGroupName' --output json
echo TEARDOWN_DONE
