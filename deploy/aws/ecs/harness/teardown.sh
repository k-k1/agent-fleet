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
  for i in $(seq 40); do
    n=$(aws efs describe-mount-targets --file-system-id "$EFS" --query 'length(MountTargets)' --output text 2>/dev/null || echo 0)
    [ "$n" = 0 ] && break; sleep 5
  done
  aws efs delete-file-system --file-system-id "$EFS" >/dev/null 2>&1 && echo "efs $EFS deleted"
fi

say ssm-params
for p in $(aws ssm describe-parameters --query "Parameters[?starts_with(Name,'/af-ws/$N')].Name" --output text); do
  aws ssm delete-parameter --name "$p" >/dev/null 2>&1 && echo "param $p"
done

say cfn-stacks
for s in $N-pool $N-plat $N-net; do
  aws cloudformation delete-stack --stack-name $s >/dev/null 2>&1
done
for s in $N-pool $N-plat $N-net; do
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
echo -n "logs:      "; aws logs describe-log-groups --query 'logGroups[].logGroupName' --output json
echo TEARDOWN_DONE
