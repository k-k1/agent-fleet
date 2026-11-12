---
audience: "Account administrators who give the people or CI deploying an AWS ECS (`ecs` / `ecs-ec2`) deployment the least IAM permissions"
source_of_truth: "The resources `deploy/aws/ecs/cfn/*.yaml` create, and the APIs `release-ecr.sh`, `standup.sh`, `update.sh`, `pause.sh` and `teardown.sh` call"
---

# IAM permissions for deploying

English | [日本語](iam.ja.md)

Decide which permissions to give the person (or CI) who runs `release-ecr.sh` and `cloudformation deploy`.
The runtime roles the tasks use are created by `20-platform` and are not listed here.

## How it works

| What you do | What happens |
|---|---|
| Try it in a throwaway sandbox | Broad create rights for VPC, ECS, EFS, RDS, ALB, IAM, ECR and SSM are fine |
| Deploy in a production account | Narrow it to the per-service permissions below. Scope resources by the `af-` name prefix where the service supports it |

## Permissions by service

| Service | Why | Actions (summary) |
|---|---|---|
| cloudformation | Stack create, update and delete | Create/Update/Delete/Describe stacks and change sets |
| ec2 | `00-network` (VPC, subnets, NAT, SG, S3 gateway endpoint; the egress gateway with `EgressRoute=gateway`) | Create/Delete/Describe VPC, subnets, IGW, NAT, EIP, route tables and routes, security groups, VPC endpoints (the S3 gateway endpoint with its policy: `ec2:CreateVpcEndpoint`/`ModifyVpcEndpoint`/`DeleteVpcEndpoints`). For the gateway also network interfaces, `AssociateAddress`/`DisassociateAddress` (its Elastic IP, `DescribeAddresses` for a handed-in one), launch templates (`CreateLaunchTemplate`/`CreateLaunchTemplateVersion`/`DeleteLaunchTemplate`), and `RunInstances`/`CreateTags` (checked when the Auto Scaling group is created from the launch template) |
| autoscaling | `00-network`'s egress gateway (`EgressRoute=gateway`; `pause.sh` scales it through the stack) | Create/Update/Delete/Describe AutoScalingGroups, plus `iam:CreateServiceLinkedRole` for `autoscaling.amazonaws.com` once per account |
| secretsmanager | `00-network`'s egress agent token (`EgressRoute=gateway`) | CreateSecret/DeleteSecret/DescribeSecret, GetRandomPassword (the stack generates the value) |
| s3 | `10-data`'s egress event archive bucket (`EgressArchive=create`, off by default) | CreateBucket/DeleteBucket, Put/GetBucketPolicy, PutBucketEncryption, PutLifecycleConfiguration, PutBucketPublicAccessBlock, PutBucketTagging. On `20-platform`'s `CfnTemplatesBucket`, `s3:PutObject` / `s3:GetObject` (the CLI uploads the template on a deploy with `--s3-bucket`, which `30-ingress` always uses) |
| ecr | `20-platform` repositories and image push | CreateRepository/DeleteRepository/Describe*, GetAuthorizationToken, BatchCheckLayerAvailability, InitiateLayerUpload, UploadLayerPart, CompleteLayerUpload, PutImage |
| ecs | `20-platform` cluster and `30-ingress` service | Create/Delete/Describe cluster and service, RegisterTaskDefinition, plus `iam:CreateServiceLinkedRole` once per account |
| servicediscovery | Service Connect namespace | Create/Delete/Get namespace |
| efs | `10-data` file system and mount targets | CreateFileSystem/DeleteFileSystem/CreateMountTarget/DeleteMountTarget/Describe* |
| backup | `10-data` EFS backup vault and plan (`Persistence=retain`), backup and restore, teardown | CreateBackupVault/DescribeBackupVault/DeleteBackupVault/ListBackupVaults, Create/Get/Update/Delete BackupPlan, Create/Get/Delete BackupSelection, ListBackupPlans; `backup-storage:MountCapsule` and `kms:CreateGrant`/`DescribeKey` on the default `aws/backup` key; StartBackupJob/StartRestoreJob/DescribeRestoreJob, ListRecoveryPointsByBackupVault/DeleteRecoveryPoint, and `iam:PassRole` on the `10-data` backup role |
| kms | `10-data` Control Plane key (`CustodianKmsKey=create` only) | CreateKey, PutKeyPolicy, EnableKeyRotation, DescribeKey, TagResource, CreateAlias/DeleteAlias, ScheduleKeyDeletion (stack deletion with `Persistence=delete`) |
| rds | `10-data` instance | CreateDBInstance/DeleteDBInstance/CreateDBSubnetGroup/Describe* (ManageMasterUserPassword also needs `secretsmanager:*` on the RDS-managed secret and `kms:DescribeKey`) |
| elasticloadbalancing | `30-ingress` ALB, target group, listeners | Create/Delete/Describe/Modify load balancers, target groups, listeners |
| acm | `30-ingress` certificate | RequestCertificate/DeleteCertificate/DescribeCertificate |
| route53 | DNS validation and alias | ChangeResourceRecordSets/GetHostedZone/ListResourceRecordSets (on the zone) |
| logs | Log groups | CreateLogGroup/DeleteLogGroup/PutRetentionPolicy/Describe* |
| iam | `20-platform` named roles, `10-data`'s EFS backup role, `00-network`'s egress gateway role and instance profile, the managed policies `30`, `40` and `60` attach to the Control Plane's task role | CreateRole/DeleteRole/Get/PassRole, Put/Delete/Attach/DetachRolePolicy, CreatePolicy/DeletePolicy/GetPolicy, Create/Delete/ListPolicyVersions, Create/Delete InstanceProfile and Add/RemoveRoleFromInstanceProfile (→ `CAPABILITY_NAMED_IAM`; `CAPABILITY_IAM` for `10-data` and `00-network`) |
| ssm | Control Plane secrets (created outside the stacks); the egress gateway's readiness and drain checks (`pause.sh`, `standup.sh`, `EgressRoute=gateway` only) | PutParameter/DeleteParameter under `/af-cp/*`; `SendCommand` on `arn:aws:ssm:<region>::document/AWS-RunShellScript` and on instances tagged `af-egress-gateway=<network stack>` (`ssm:resourceTag/af-egress-gateway`), and `GetCommandInvocation` |
| sts | Account resolution in `release-ecr.sh` | GetCallerIdentity |

## Policy limits on the Control Plane's task role

What to know before you add permissions to the Control Plane's task role (`CpTaskRole`).

| Limit | Details |
|---|---|
| Inline policies | A role's inline policies may total 10,240 characters (whitespace not counted). `20-platform`'s inline `cp-runtime` uses about 7,000 of them |
| Managed policies | 6,144 characters each. How many a role may carry is an adjustable quota |

- The grants other stacks add (`40-ec2-pool`'s slot launch, `60-engines`' ingest, `30-ingress`'s home operations) are each an `AWS::IAM::ManagedPolicy` of their own. Inline they go over the limit and `30-ingress` rolls back with `ServiceLimitExceeded`
- Managed policy names are account-wide, so they carry the region (`af-<stack>-cp-ingest-<region>` and so on)

## Related

- [Build on ECS](install.md)
- [Understand the ECS layout](overview.md)
- [Keep templates under the size limit](template-size.md)
