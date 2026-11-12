---
audience: "Deployment administrators who build and run a deployment on AWS ECS (`ecs` / `ecs-ec2`)"
source_of_truth: "`deploy/aws/ecs/cfn/*.yaml` for the templates, `deploy/aws/ecs/cfn/PARAMETERS.md` for the parameters in detail"
---

# Understand the ECS layout

English | [日本語](overview.ja.md)

Learn which stacks an AWS ECS deployment (`ecs` / `ecs-ec2`) is made of, and in which order to build them.
Each workspace is one ECS service, and stopping it sets its `desiredCount` to 0 (scale to zero). The Control Plane reaches it over Service Connect.

If you only want an easy self-hosted deployment on AWS, pick compose on a single EC2 instance instead. The difference is in [Choose where to deploy](../choose.md).

## How it works

| What you do | What happens |
|---|---|
| Deploy the CloudFormation stacks in order | The VPC, EFS, RDS, ECR, the ECS cluster, the ALB and the Control Plane's service are built, once per deployment |
| A member starts a workspace | The Control Plane creates that workspace's ECS service, task definition, EFS access point and SSM SecureString parameters under deterministic names. The stacks do not change |
| Move to a new release | Redeploy only `30-ingress` with a new `ImageTag` ([Upgrade](upgrade.md)) |
| Tear it down | Delete the stacks with `delete-stack` |

All you need is the AWS CLI. No Terraform, no CDK. Run the commands from `deploy/aws/ecs/` in the repository.

## What CloudFormation builds and what the Control Plane builds

| Layer | Built by | Resources |
|---|---|---|
| Static substrate | CloudFormation (`deploy/aws/ecs/cfn/`) | VPC, subnets, NAT, security groups, the EFS file system, RDS, ECR, the ECS cluster, the Service Connect namespace, IAM roles, the ALB (ACM, TLS termination only), the Control Plane and Console service |
| Per workspace | The running Control Plane | ECS service, task definition, EFS access point, SSM SecureString parameters |

- The Control Plane finds a workspace's resources by deterministic name and tag. It keeps no state of its own about them
- The stacks are deployed once per deployment. Workspaces come and go without any change to the stacks

## How the stacks are split

The stacks are layered so that the network, which is slow to rebuild, can outlive the parts you change often.

| Template | When you need it | Contents |
|---|---|---|
| `cfn/00-network.yaml` | Always | VPC, public and private subnets in two AZs, internet gateway, NAT, the base security groups (`alb` / `cp` / `ws`) |
| `cfn/10-data.yaml` | Always | The EFS file system and its mount targets, RDS (Postgres, single-AZ, `db.t4g.micro` by default, master password managed by RDS). With `CustodianKmsKey=create`, also the KMS key for the Control Plane's keys |
| `cfn/20-platform.yaml` | Always | ECR (Control Plane and workspace, plus `af-voicevox`, `af-llamacpp`, `af-comfyui` and `af-engine-tools` for the optional engines), the ECS cluster, the Service Connect namespace (`af.internal`), IAM roles (`cp-task` / `exec` / `ws-task`) |
| `cfn/40-ec2-pool.yaml` | Only with `WsRuntime=ecs-ec2` | The launch template of a workspace slot, the slot's instance role and security group. It creates no instances: the Control Plane launches them on demand |
| `cfn/50-tts.yaml` | Only for Japanese speech | The VOICEVOX engine (a Fargate service normally scaled to zero), its Cloud Map name, a dedicated security group |
| `cfn/60-engines.yaml` | Only for self-hosted inference | The GPU services of llama.cpp (`llm`) and ComfyUI (`image`), the launch templates of the instances the Control Plane launches with EC2 Fleet, the S3 bucket that holds the models, the ingest task |
| `cfn/30-ingress.yaml` | Always | The ACM certificate (DNS-validated), the ALB, the Control Plane and Console Fargate service, the Route 53 alias |

### Deploy order

Each stack reads the earlier stacks' exports. Deploy them in this order.

```
00 → 10 → 20 → (40: ecs-ec2 only) → (50: speech only) → (60: self-hosted inference only) → 30
```

`50-tts` and `60-engines` come before `30-ingress` because `30-ingress` is handed their outputs.

### Parameters in detail

Most of the parameters a deployment tunes are in `30-ingress.yaml`. Their `Description:` fields are one or two lines; the measurements, trade-offs and traps are in [`cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md). The parameters of `60-engines` are in [`cfn/PARAMETERS-60-engines.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS-60-engines.md).
Why the descriptions live outside the templates is in [Keep templates under the size limit](template-size.md).

## Naming

Every resource name starts with `af-`. In an account shared with other work, searching for `af-` finds the deployment's resources and nothing else, so they are safe to sweep.

## Related

- [Build on ECS](install.md)
- [Turn on optional features with stack parameters](options.md)
- [IAM permissions for deploying](iam.md)
- [Troubleshoot ECS](troubleshooting.md)
- [Deployment targets — what exists where](../../ref/deploy-targets.md)
