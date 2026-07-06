# Agent Fleet on ECS (CloudFormation) — P3-7 native AWS adapter

The **native AWS adapter** (`AF_RUNTIME=ecs`): each Workspace is one ECS Service
(`desiredCount` 0/1 = scale-to-zero) with an EFS access point for its home, reached
over Service Connect. This is **not** the single-VM Compose host — for "self-host on
AWS the easy way" use [`../ec2-single`](../ec2-single/) instead.

Frozen implementation spec: [`docs/history/p3-7-aws-adapter.md` §20b.7](../../../docs/history/p3-7-aws-adapter.md#20b7-段2-実装仕様凍結).
Only the AWS CLI is required (no Terraform / CDK). Tear everything down with
`delete-stack`.

## What CloudFormation builds vs what the Control Plane builds

The single most important boundary (frozen spec §20b.7.1: the ECS adapter is
**stateless** — every per-workspace resource is addressed by a deterministic
name/tag and created on demand):

| Layer | Owner | Resources |
|-------|-------|-----------|
| **static substrate** | **CloudFormation** (this dir) | VPC/subnets/NAT, SGs, EFS **filesystem**, RDS, ECR, ECS **cluster**, Service Connect **namespace**, IAM **roles**, ALB(ACM/OIDC), **CP+Console service** |
| **per-workspace** | **Control Plane at runtime** (`runtime_ecs.go`, 段2) | ECS **Service**, **TaskDefinition**, EFS **access point**, **SSM SecureString** params |

So CFN is deployed **once** per environment; workspaces appear/disappear afterwards
with zero CFN churn. This keeps teardown clean and the template small.

## Stack decomposition (`cfn/`)

Layered so the cheap-but-slow-to-rebuild network can outlive fast-iterating
platform changes:

| File | Status | Contents |
|------|--------|----------|
| `cfn/00-network.yaml` | **proven** (deploy→verify→teardown in sandbox) | VPC, 2×AZ public+private subnets, IGW, NAT, base SGs (`alb`/`cp`/`ws`) |
| `cfn/10-data.yaml` | **proven** (EFS 2 mount targets available, RDS pg18 available/private/encrypted) | EFS filesystem + mount targets, RDS(Postgres, single-AZ t4g.micro, RDS-managed master secret) |
| `cfn/20-platform.yaml` | **proven** (ECR×2, cluster ACTIVE w/ SC default, 3 IAM roles) | ECR (cp+workspace), ECS cluster, Service Connect namespace (`af.internal`), IAM roles (`cp-task`/`exec`/`ws-task`) |
| `cfn/30-ingress.yaml` | **authored + validated** (deploy pending OAuth secrets) | ACM(DNS-validated), ALB (TLS-termination only — auth is CP-native `AUTH=oauth`, no ALB OIDC), CP/Console Fargate service (Service Connect client), Route53 alias |

> `00`/`10`/`20` are proven end-to-end (deploy→verify→`delete-stack`, no orphans).
> `30-ingress` is authored and template-validated; standing it up needs a domain +
> Google OAuth client + the CP image in ECR (see stand-up below). Each stack imports
> the earlier ones' exports, so deploy in order `00 → 20 → 30` (10-data is only
> needed once the store moves to RDS / workspaces mount EFS).

### Prerequisites (once per account)

- **ECS service-linked role.** A fresh account has no `AWSServiceRoleFor ECS`, and
  creating a cluster with a Service Connect default namespace fails with
  *"ECS Service Linked Role is not ready"*. Create it once (idempotent — ignore the
  "has been taken" error on re-run):
  ```bash
  aws iam create-service-linked-role --aws-service-name ecs.amazonaws.com || true
  ```
- **`20-platform` needs `--capabilities CAPABILITY_NAMED_IAM`** (it creates named IAM roles):
  ```bash
  aws cloudformation deploy --stack-name af-ecs-platform \
    --template-file cfn/20-platform.yaml --capabilities CAPABILITY_NAMED_IAM \
    --profile af-sandbox --region ap-northeast-1
  ```

### 30-ingress stand-up (milestone: CP boots + Google login)

Auth is **CP-native Google OAuth** (`AUTH=oauth`); the ALB only terminates TLS.
SQLite is ephemeral in this milestone (no EFS/RDS). Prerequisites:

1. **Push the CP image to ECR** (image already built locally — no rebuild):
   ```bash
   AWS=<acct>; RG=ap-northeast-1
   aws ecr get-login-password --region $RG --profile af-sandbox | \
     docker login --username AWS --password-stdin $AWS.dkr.ecr.$RG.amazonaws.com
   docker tag agent-fleet/control-plane:dev $AWS.dkr.ecr.$RG.amazonaws.com/af-control-plane:dev
   docker push $AWS.dkr.ecr.$RG.amazonaws.com/af-control-plane:dev
   ```
2. **CP secrets into SSM SecureString** (read by the exec role; never in plaintext env):
   ```bash
   aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
     --name /af-cp/cookie-secret       --value "$(openssl rand -hex 32)"
   aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
     --name /af-cp/master-key          --value "$(openssl rand -hex 32)"
   aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
     --name /af-cp/google-client-secret --value "<GOOGLE_OAUTH_CLIENT_SECRET>"   # your terminal, not shared
   ```
3. **Google OAuth client**: add `https://af-dev.lazmix.jp/oauth2/callback` to the
   client's Authorized redirect URIs. Pass its client id + allowed/super-admin emails
   as parameters at deploy:
   ```bash
   aws cloudformation deploy --stack-name af-ecs-ingress \
     --template-file cfn/30-ingress.yaml \
     --parameter-overrides GoogleClientId=<id> \
       AllowedEmails=you@example.com SuperAdminEmails=you@example.com \
     --profile af-sandbox --region $RG
   ```
   ACM DNS validation and the Route53 alias are automated (the `lazmix.jp` zone is
   in-account); cert issuance adds a few minutes to the first deploy.

## Prove-out sequence

Runs from a host (or an Agent Fleet workspace — dogfooding) with **dedicated
sandbox** credentials. AWS API calls are just HTTPS; the heavy compute (ECS tasks,
RDS) lives in AWS, so this host's build/OOM limits don't gate it.

- **S0** — `aws sts get-caller-identity` in the sandbox; pick a region.
- **S1** — deploy `00-network.yaml`; confirm the deploy → `delete-stack` loop and
  the standing cost (NAT is the main one, see below). **proven.**
- **S1.5** — deploy `10-data.yaml` (EFS+RDS) and `20-platform.yaml` (ECR/cluster/SC
  namespace/IAM); confirm and tear down. **proven.**
- **S2** — with ECR up (from `20`), push the CP + Workspace images (§ECR push below).
  ECS can't pull until they're in ECR.
- **S3** — deploy `30-ingress.yaml`; the CP/Console service boots, ALB OIDC login
  works, RDS reachable.
- **S4** — with `runtime_ecs.go` (段2) implemented, the CP **dynamically provisions a
  workspace** (exercises the real adapter) → the E2E gate below.

CFN authoring (S1–S3) and the 段2 Go code are **parallel tracks**; they meet at S4.

## E2E completion gate (frozen spec §20b.7.14)

1. login (ALB OIDC) → tenant → workspace Start (Service desired 1, RUNNING, healthz).
2. session create → terminal attach (CP→Service Connect→Agent) → I/O.
3. Stop (desired 0) → re-Start resumes home/claude state (EFS persists).
4. P3-9 reaper: idle → stage-1 halt → stage-2 desired 0; no fire while attached.
5. **no plaintext secret**: `describe-task-definition` shows the DEK only as an SSM
   `valueFrom` ARN, never as an `environment` value.

## ECR push (before S3)

```bash
aws ecr get-login-password --region <region> | docker login --username AWS \
    --password-stdin <acct>.dkr.ecr.<region>.amazonaws.com
# tag + push the images already built for local (no heavy rebuild here):
docker tag agent-fleet/control-plane:<tag> <acct>.dkr.ecr.<region>.amazonaws.com/af-control-plane:<tag>
docker tag agent-fleet/workspace:<tag>     <acct>.dkr.ecr.<region>.amazonaws.com/af-workspace:<tag>
docker push <acct>.dkr.ecr.<region>.amazonaws.com/af-control-plane:<tag>
docker push <acct>.dkr.ecr.<region>.amazonaws.com/af-workspace:<tag>
```

## Cost & ephemerality

Standing costs while the substrate is up: **NAT (~$32/mo)**, ALB (~$20/mo), RDS
t4g.micro (~$15–30/mo), EFS (usage). For iteration, **deploy → E2E → `delete-stack`**
and keep stacks short-lived.

- NAT is needed because workspaces egress to **git / Anthropic** (public internet).
  VPC endpoints (ECR api+dkr, S3 gw, Logs, SSM) cut AWS-service traffic off NAT but
  not the public git/Anthropic egress — for a dev loop, a **NAT instance** (t4g.nano)
  or just accepting the NAT Gateway for the hours a stack lives is simpler.
- `DeletionPolicy` on EFS/RDS: default **Delete** in the sandbox (ephemeral). Flip to
  `Retain` + `Snapshot` only for a persistent environment.

## Teardown

```bash
aws cloudformation delete-stack --stack-name af-ecs-platform
aws cloudformation delete-stack --stack-name af-ecs-data
aws cloudformation delete-stack --stack-name af-ecs-network   # last (others depend on it)
```

Per-workspace resources the CP created at runtime (ECS Services, task defs, EFS
access points, SSM params) are **not** in these stacks — deregister/delete them via
the CP's own workspace-delete path, or sweep by the `af-ws*` name/tag prefix, before
deleting `af-ecs-network`.

## Notes

- Deploying principal (sandbox): broad create rights for VPC/ECS/EFS/RDS/ALB/**IAM**/
  ECR/SSM. Fine in a throwaway account; least-priv doc for customers comes with P3-10.
- The Google OAuth client used for ALB OIDC is throwaway — delete it after testing.
- Naming prefix `af-` on every resource so a sandbox shared with other work stays
  greppable and safe to sweep.
