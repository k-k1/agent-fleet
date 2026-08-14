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
| **static substrate** | **CloudFormation** (this dir) | VPC/subnets/NAT, SGs, EFS **filesystem**, RDS, ECR, ECS **cluster**, Service Connect **namespace**, IAM **roles**, ALB(ACM, TLS-termination only), **CP+Console service** |
| **per-workspace** | **Control Plane at runtime** (`runtime_ecs.go`, stage 2) | ECS **Service**, **TaskDefinition**, EFS **access point**, **SSM SecureString** params |

So CFN is deployed **once** per environment; workspaces appear/disappear afterwards
with zero CFN churn. This keeps teardown clean and the template small.

## Stack decomposition (`cfn/`)

Layered so the cheap-but-slow-to-rebuild network can outlive fast-iterating
platform changes:

| File | Status | Contents |
|------|--------|----------|
| `cfn/00-network.yaml` | **proven** (deploy→verify→teardown in sandbox) | VPC, 2×AZ public+private subnets, IGW, NAT, S3 gateway endpoint, base SGs (`alb`/`cp`/`ws`) |
| `cfn/10-data.yaml` | **proven** (EFS 2 mount targets available, RDS pg18 available/private/encrypted) | EFS filesystem + mount targets, RDS(Postgres, single-AZ t4g.micro, RDS-managed master secret) |
| `cfn/20-platform.yaml` | **proven** (ECR×2, cluster ACTIVE w/ SC default, 3 IAM roles) | ECR (cp+workspace), ECS cluster, Service Connect namespace (`af.internal`), IAM roles (`cp-task`/`exec`/`ws-task`) |
| `cfn/30-ingress.yaml` | **proven** (CP boots on Fargate, `/healthz` 200, `/oauth2/login` → Google w/ correct redirect_uri) | ACM(DNS-validated), ALB (TLS-termination only — auth is CP-native `AUTH=oauth`, no ALB OIDC), CP/Console Fargate service (Service Connect client), Route53 alias |

> `00`/`10`/`20` are proven end-to-end (deploy→verify→`delete-stack`, no orphans).
> `30-ingress` is authored and template-validated; standing it up needs a domain +
> Google OAuth client + the CP image in ECR (see stand-up below). Each stack imports
> the earlier ones' exports, so deploy in order `00 → 10 → 20 → 30`.

### Prerequisites (once per account)

- **ECS service-linked role.** A fresh account has no `AWSServiceRoleForECS`, and
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

Auth is the **CP-native login** (`AUTH=oauth`) — Google and/or any OIDC IdP
(Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab); the ALB only terminates TLS.
The CP stores its state in RDS Postgres and workspaces mount EFS — both wired
from `10-data`'s exports, so that stack must already be up. Prerequisites:

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
   # one of these two, or both — only the IdPs you enable are wired in
   aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
     --name /af-cp/google-client-secret --value "<GOOGLE_OAUTH_CLIENT_SECRET>"   # your terminal, not shared
   aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
     --name /af-cp/oidc-client-secret   --value "<AF_OIDC_<ID>_CLIENT_SECRET>"
   ```
3. **The IdP client**: add `https://<your-fqdn>/oauth2/callback` to its Authorized
   redirect URIs — that single URI serves every provider you enable. Pass the
   fqdn/zone + client id + allowed/super-admin emails as parameters at deploy:
   ```bash
   aws cloudformation deploy --stack-name af-ecs-ingress \
     --template-file cfn/30-ingress.yaml \
     --parameter-overrides GoogleClientId=<id> \
       Fqdn=af.example.com HostedZoneId=<your-zone-id> \
       AllowedEmails=you@example.com SuperAdminEmails=you@example.com \
     --profile af-sandbox --region $RG
   ```
   For a Microsoft Entra ID (or Okta / Keycloak / …) deployment, swap
   `GoogleClientId` for the `Oidc*` parameters (docs/61) — leaving `GoogleClientId`
   empty is fine, Google is then simply not offered:
   ```bash
     --parameter-overrides OidcProviderId=ENTRA \
       OidcIssuer=https://login.microsoftonline.com/<tenant-guid>/v2.0 \
       OidcClientId=<application-client-id> OidcTrust=issuer \
       OidcLabelJa="Microsoft でサインイン" OidcLabelEn="Sign in with Microsoft" \
       Fqdn=af.example.com HostedZoneId=<your-zone-id> \
       AllowedDomains=example.co.jp SuperAdminEmails=you@example.co.jp
   ```
   ★ Pin `OidcIssuer` to your tenant guid. With the `/common/` or `/organizations/`
   endpoint every Microsoft account in the world reaches the login, and personal
   accounts can rewrite their own email — the CP refuses to start on those unless
   `OidcAllowedTids` is set.
   ACM DNS validation and the Route53 alias are automated (the hosted zone must be
   in this account); cert issuance adds a few minutes to the first deploy.
   Versioned (release) images add `ImageTag=<v>` to the overrides — the default
   `dev` matches the sandbox push above.

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
- **S3** — deploy `30-ingress.yaml`; the CP/Console service boots, CP-native
  Google OAuth login works, RDS reachable.
- **S4** — with `runtime_ecs.go` (stage 2) implemented, the CP **dynamically provisions a
  workspace** (exercises the real adapter) → the E2E gate below.

CFN authoring (S1–S3) and the stage-2 Go code are **parallel tracks**; they meet at S4.

## E2E completion gate (frozen spec §20b.7.14)

1. login (CP-native Google OAuth) → tenant → workspace Start (Service desired 1, RUNNING, healthz).
2. session create → terminal attach (CP→Service Connect→Agent) → I/O.
3. Stop (desired 0) → re-Start resumes home/claude state (EFS persists).
4. P3-9 reaper: idle → stage-1 halt → stage-2 desired 0; no fire while attached.
5. **no plaintext secret**: `describe-task-definition` shows the DEK only as an SSM
   `valueFrom` ARN, never as an `environment` value.

## ECR push (before S3)

Scripted (P3 — `release-ecr.sh` wraps the command sequence below; the manual
sequence stays the source of truth, the script is its transcription):

```bash
# images already local (built by deploy/release/build.sh):
VERSION=<v> ./release-ecr.sh --profile af-sandbox --region <region>
# or from the air-gap images tar (B) when the build host is a different machine:
VERSION=<v> ./release-ecr.sh --profile af-sandbox --region <region> \
    --images-tar agent-fleet-images-<v>.tar.gz
```

The script only *verifies* the ECR repositories exist — they are owned by
`20-platform.yaml`, so deploy that stack first (creating them out of band would
make the later CFN deploy fail with AlreadyExists).

Manual equivalent:

```bash
aws ecr get-login-password --region <region> | docker login --username AWS \
    --password-stdin <acct>.dkr.ecr.<region>.amazonaws.com
# tag + push the images already built for local (no heavy rebuild here):
docker tag agent-fleet/control-plane:<tag> <acct>.dkr.ecr.<region>.amazonaws.com/af-control-plane:<tag>
docker tag agent-fleet/workspace:<tag>     <acct>.dkr.ecr.<region>.amazonaws.com/af-workspace:<tag>
docker push <acct>.dkr.ecr.<region>.amazonaws.com/af-control-plane:<tag>
docker push <acct>.dkr.ecr.<region>.amazonaws.com/af-workspace:<tag>
```

## Upgrade (runbook)

Both images are versioned together — `30-ingress.yaml` takes a single `ImageTag`
parameter (default `dev`) used for the CP image *and* the `AF_ECS_WORKSPACE_IMAGE`
the adapter launches. To move a deployment to a new release:

1. **Back up first** (production): EFS is on `Persistence=retain` so it survives
   stacks, but take an RDS snapshot / AWS Backup point before upgrading anyway.
2. **Push the new tag**: `VERSION=<v> ./release-ecr.sh --profile <p> --region <r>`.
3. **Re-deploy ingress with only the tag overridden** (all other parameters keep
   their previous values):
   ```bash
   aws cloudformation deploy --stack-name af-ecs-ingress \
     --template-file cfn/30-ingress.yaml \
     --parameter-overrides ImageTag=<v> \
     --profile <p> --region <r>
   ```
4. What happens: the **CP/Console service rolls** to the new image (brief
   blue/green replacement behind the ALB). **Workspaces are not touched** — the
   adapter builds task definitions statelessly from `AF_ECS_WORKSPACE_IMAGE`, so
   each workspace picks the new image up **on its next Start**; running workspaces
   keep their current image until stopped. No EFS/RDS change; state carries over.

Rollback is the same operation with the previous tag. Convention: never re-push a
different image under an already-released version tag (the repos stay `MUTABLE`
so the `:dev` sandbox flow can overwrite itself — release tags are write-once by
discipline, not enforcement).

## Minimal IAM (deploying principal)

What the human/CI principal running `release-ecr.sh` + `cloudformation deploy`
needs, by service (the runtime roles the *tasks* use are created by `20-platform`
and are not listed here). Scope resources by the `af-` name prefix where the
service supports it:

| Service | Why | Actions (summary) |
|---|---|---|
| cloudformation | stack CRUD | Create/Update/Delete/Describe stacks + change sets |
| ec2 | 00-network (VPC/subnets/NAT/SG/endpoint) | Create/Delete/Describe VPC, subnets, IGW, NAT, EIP, routes, security groups, VPC endpoints |
| ecr | 20-platform repos + image push | CreateRepository/DeleteRepository/Describe*, GetAuthorizationToken, BatchCheckLayerAvailability, InitiateLayerUpload, UploadLayerPart, CompleteLayerUpload, PutImage |
| ecs | 20-platform cluster + 30-ingress service | Create/Delete/Describe cluster & service, RegisterTaskDefinition, plus `iam:CreateServiceLinkedRole` once per account |
| servicediscovery | Service Connect namespace | Create/Delete/Get namespace |
| efs | 10-data filesystem + mount targets | CreateFileSystem/DeleteFileSystem/CreateMountTarget/DeleteMountTarget/Describe* |
| rds | 10-data instance | CreateDBInstance/DeleteDBInstance/CreateDBSubnetGroup/Describe* (ManageMasterUserPassword also needs `secretsmanager:*` on the RDS-managed secret + `kms:DescribeKey`) |
| elasticloadbalancing | 30-ingress ALB/TG/listeners | Create/Delete/Describe/Modify load balancers, target groups, listeners |
| acm | 30-ingress cert | RequestCertificate/DeleteCertificate/DescribeCertificate |
| route53 | DNS validation + alias | ChangeResourceRecordSets/GetHostedZone/ListResourceRecordSets (on the zone) |
| logs | log groups | CreateLogGroup/DeleteLogGroup/PutRetentionPolicy/Describe* |
| iam | 20-platform named roles | CreateRole/DeleteRole/Get/PassRole, Put/Delete/AttachRolePolicy (→ `CAPABILITY_NAMED_IAM`) |
| ssm | CP secrets (out-of-band) | PutParameter/DeleteParameter under `/af-cp/*` |
| sts | account resolution in release-ecr.sh | GetCallerIdentity |

## Known behavior: a cold Start answers `starting`, not `running`

A cold workspace Start pays a full image pull on every launch (Fargate keeps no
image cache between tasks) and converges in **~100s measured**. `POST
/api/workspace/start` does **not** wait for that: it returns as soon as the ECS
service sits at `desiredCount 1`, with the live state — normally `starting`.
Poll `GET /api/workspace` for the transition to `running`; the Console already
does (4s, and it walks the cold start to `running` without a reload).

Older builds blocked the request on the Agent readiness wait
(`AF_ECS_START_TIMEOUT_SEC`, then 90s) and so outlived the ALB's **60s** idle
timeout — the *HTTP request* died with a **504** while the workspace converged
fine behind it. Fixed in `control-plane/runtime_ecs.go` (`watchReady`): the
readiness poll now runs in the background for a log line only
(`ecs start: service <name> Agent healthy <n>s after Start`), on a budget
deliberately longer (default **300s**) than any request may take. `30-ingress.yaml`
now spells the ALB's `idle_timeout` out at its default 60s so the ceiling is
visible. 🚧 Verified by unit tests only — not re-run against a live ECS stack.

Note `running` here means the ECS **task** is RUNNING, which is a moment before
the entrypoint has finished and the Agent answers. A create issued in that window
still gets `502 workspace agent unreachable`; retry.

**Measure before optimizing.** Nobody has split the ~100s into its parts. One
Start gives you both numbers:

```bash
# image pull window (and the surrounding task lifecycle)
aws ecs describe-tasks --cluster <cluster> --tasks <task-arn> \
  --query 'tasks[0].{created:createdAt,pullStart:pullStartedAt,pullStop:pullStoppedAt,started:startedAt}'
# entrypoint cost (boot-install of the pinned CLIs on a fresh home is NOT a pull)
aws logs tail <ws-log-group> --since 10m
```

Note the workspace image pushed here is the **lean** variant
(`BAKE_AGENT_CLIS=0`), so a *fresh home* additionally pays a one-time
npm/GitHub boot-install inside the entrypoint (~60s for all CLIs, measured in
`workspace/entrypoint.sh`). That cost is network, not image pull, and it does
not recur — `~/.local` persists on EFS. Whether to adopt SOCI (Seekable OCI)
lazy loading for the pull itself is analysed in
[`docs/62-ecs-start-latency.md`](../../../docs/62-ecs-start-latency.md)
(conclusion: conditional yes, gated on the measurement above).

## Cost & ephemerality

Standing costs while the substrate is up: **NAT (~$32/mo)**, ALB (~$20/mo), RDS
t4g.micro (~$15–30/mo), EFS (usage). For iteration, **deploy → E2E → `delete-stack`**
and keep stacks short-lived.

- NAT is needed because workspaces egress to **git / Anthropic** (public internet),
  and every private-subnet task also reaches ECR / CloudWatch Logs / SSM through it
  (there is no public IP on those tasks) — so the NAT is on the **workspace start
  path**, not just the user's internet access.
  - The **S3 gateway endpoint is in `00-network.yaml`** because it is free (no
    hourly charge, no ENI) and ECR layer blobs live in S3 — it takes the bulk of
    every cold image pull off the NAT's $0.045/GB data processing. **Interface**
    endpoints (ecr.api, ecr.dkr, logs, ssm) are left out on purpose: $0.01/AZ/hour
    × 2 AZ each adds up past the NAT Gateway they would relieve.
  - What still crosses the NAT: git / Anthropic / npm / package registries (the real
    developer traffic), plus the small ECR auth+manifest, Logs and SSM calls.
  - Replacing the NAT Gateway with a **NAT instance** (t4g.nano/small, ~$7–12/mo vs
    $33) is the remaining lever for a dev loop, at the price of running it yourself
    (source/dest check off, iptables MASQUERADE, ASG for replacement + route rewrite).
- Persistence: `10-data.yaml` takes a **`Persistence` parameter** — `delete`
  (default, sandbox-ephemeral: EFS/RDS dropped with the stack, no backups) or
  `retain` (production: EFS `Retain`, RDS `Snapshot` + 7-day backups + deletion
  protection). **Deploy production with `Persistence=retain`.**

## Teardown

```bash
aws cloudformation delete-stack --stack-name af-ecs-ingress
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
  ECR/SSM are fine in a throwaway account; for least privilege see
  §Minimal IAM above.
- The Google OAuth client used for the CP-native OAuth login is throwaway — delete it after testing.
- **`AuthMode=dev`** (30-ingress parameter) skips login entirely for sandbox/E2E
  gates. Before deploying with it, restrict the ALB SG (80/443) to your own IP —
  dev auth hands an authenticated session to anyone who reaches the ALB.
- Naming prefix `af-` on every resource so a sandbox shared with other work stays
  greppable and safe to sweep.
