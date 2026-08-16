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
| `cfn/40-ec2-pool.yaml` | **proven in a sandbox** (deployed as a stack and driven end to end, in a public subnet and behind a NAT — docs/64 §64.16, §64.17, §64.19; never at scale) | **Optional — only for `WsRuntime=ecs-ec2`.** Launch template for a workspace *slot* (ECS-optimized AMI, cluster-join user-data, `af-mount`/`af-umount`), slot instance role + profile, slot SG. Creates **no instances**: the CP runs them on demand |

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

## Workspace size (CPU / memory / working disk)

Three `30-ingress` parameters set what every workspace gets by default; per-user
overrides live in the Console (Settings → members → "Set limits") and are clamped by the
tenant cap. Design and measurements: `docs/63`, decisions in `docs/decisions/0044`.

| Parameter | Env | Default | Notes |
|---|---|---|---|
| `WsTaskCpu` | `AF_ECS_TASK_CPU` | `1024` | Fargate units; 1024 = 1 vCPU |
| `WsTaskMemory` | `AF_ECS_TASK_MEMORY` | `2048` | MiB |
| `WsDiskGiB` | `AF_ECS_WS_DISK_GB` | `50` | 21–200 sets ephemeral storage; 0 = Fargate's free 20 GiB (turns cache relocation off) |

**Only specific (cpu, memory) pairs exist**, and the steps are not uniform — 8 vCPU moves
in 4096 MiB steps and 16 vCPU in 8192, while 0.25 vCPU accepts only 512/1024/2048. The
full matrix was measured against the ECS API and is in `docs/63` §63.2. The CP snaps any
request onto a valid pair, so an odd value never reaches a task definition; the visible
effect is that **raising CPU can also raise memory** (4 vCPU cannot run below 8 GiB).

The working disk is **not persistent** — it is wiped when the workspace stops, like every
other task-local disk on Fargate. Only the EFS home survives. `WsDiskGiB` is also the
switch for moving small-file trees off EFS onto that disk: the workspace entrypoint
relocates the caches (Go build cache, `uv`, Go modules) and the Agent relocates build
artifacts (`node_modules`, `target`, `.venv`, `build`) as each working copy is created.
Both arm only when the disk is **30 GiB or more**, because the measured caches do not fit
alongside the image in 20 GiB. On EFS these trees are 8–30× slower to write (`docs/63`
§63.4).

**The default is 50 GiB, so relocation is on.** It used to be 0, which meant the feature
could never arm and every deployment kept building on EFS. Above the free 20 GiB you pay
about **$0.097/GiB-month, and only while the task runs** — roughly $2.9/month for a
workspace that never stops, well under a dollar for one that idles out. A deployment that
does not want it sets `WsDiskGiB=0` and behaves exactly as before. **Existing stacks keep
the value they were deployed with**: CloudFormation stores the parameter, so a stack
created with `0` stays at `0` until you update it — pass `WsDiskGiB=50` explicitly (or
accept the new default in the console) when you update.

Above 200 GiB the CP switches the working disk to an **ECS-managed EBS volume**, which
needs an ECS infrastructure role passed as `AF_ECS_INFRA_ROLE` (policy
`AmazonECSInfrastructureRolePolicyForVolumes`). The reference stacks do not create that
role — without it a >200 GiB request silently falls back to the free default. 🚧 This path
is untested on real infrastructure; ephemeral storage covers everything up to 200 GiB.

## Optional: EC2 slot pool (`WsRuntime=ecs-ec2`)

🚧 **Stood up and run in a sandbox, including a production-shaped VPC — but never at
scale or for real users.** `40-ec2-pool` has been deployed as a stack and real workspaces
have run on it end to end, first in a public subnet (docs/64 §64.16, §64.17) and then in a
**private subnet behind a NAT gateway** (§64.19), which is the shape a real deployment
has. The task-ENI trap below does not reproduce there — there is no public IPv4 to lose.
What is still untested is everything about scale: more than two slots, more than two
users, and any run longer than about fifteen minutes. Fargate (`WsRuntime=ecs`) stays the
default and is untouched by this profile.

**What you get, and what you do NOT.** Measured through the adapter on real AWS, a warm
Start is 43–110s against Fargate's ~105s — **the start latency is not the reason to switch**
(the earlier 22–27s figure was measured without Service Connect, which this product needs).
What the pool actually buys is **I/O and persistence** (small-file writes 8–30× faster than
EFS, and a home that really survives) and **sizes above Fargate's 16 vCPU / 120 GiB /
200 GiB ephemeral ceiling**.

**What changes.** A workspace stops being "a Fargate task with an EFS home" and becomes
"a task on a general-purpose EC2 *slot*, with the user's own EBS volume attached to it".
Slots are not owned by anyone: on Start the CP picks a free one, attaches that user's
volume at `/dev/sdf`, mounts it over SSM and pins the task there with an
`ec2InstanceId ==` placement constraint; on Stop it unmounts, detaches and hands the slot
back. **One slot serves one user at a time** (`ADR 0045` 決定 8).

| | Fargate (`ecs`) | EC2 pool (`ecs-ec2`) |
|---|---|---|
| Warm Start | ~105s | **84–110s** — *not* an improvement worth switching for (docs/64 §64.17.5, §64.19.2) |
| Home | EFS — small files are 8–30× slower | **EBS gp3** — 2,000 small files in 0.04s vs 30.7s |
| Size | 74 discrete (cpu, memory) pairs, ≤16 vCPU / 120 GiB | instance types; the task reserves nothing and gets the box |
| Resources per workspace | 2 (service + EFS access points) | 6 (also instance, volume, container-instance registration, task def) |
| Idle cost | EFS (what you use) | EBS (what you **provision**) + any hot slots |

**Stand-up**

```bash
aws cloudformation deploy --stack-name af-ecs-ec2-pool \
  --template-file cfn/40-ec2-pool.yaml --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides NetworkStackName=af-ecs-network PlatformStackName=af-ecs-platform
# then point the CP at it (this is the whole switch, and the whole rollback):
aws cloudformation deploy --stack-name af-ecs-ingress --template-file cfn/30-ingress.yaml \
  --parameter-overrides WsRuntime=ecs-ec2 Ec2SlotLaunchTemplate=lt-0123456789abcdef0 ...
```

| Parameter | Env | Default | Notes |
|---|---|---|---|
| `WsRuntime` | `AF_RUNTIME` | `ecs` | `ecs-ec2` switches the adapter. Rolling back is this value |
| `Ec2SlotLaunchTemplate` | `AF_ECS_EC2_LAUNCH_TEMPLATE` | — | `SlotLaunchTemplateId` output of `40-ec2-pool`. The CP refuses to boot without it on this profile |
| `Ec2SlotTypes` | `AF_ECS_EC2_SLOT_TYPES` | `m7i.large:8192,…` | `instanceType:memoryMiB`, ascending |
| `Ec2MaxSlots` | `AF_ECS_EC2_MAX_SLOTS` | `8` | Hard cap. Start fails at the cap rather than growing the bill |
| `Ec2HomeGiB` | `AF_ECS_EC2_HOME_GB` | `50` | Per-user home volume (gp3) |
| — | `AF_ECS_EC2_HIBERNATE_AFTER_SEC` | `0` (off) | Snapshot a home that has been dormant this long and delete the volume. See below |

**Hibernating long-unused homes (opt-in).** An EBS home bills for what it is *provisioned*
at, whether or not anyone opens it. With `AF_ECS_EC2_HIBERNATE_AFTER_SEC` set, the sweeper
takes a snapshot of a home that has been dormant that long and deletes the volume;
the owner's next Start rebuilds it from the snapshot. For a 20 GiB-used / 50 GiB-provisioned
home that is **$4.80 → $1.00 a month**, against ~122s on the return and a slightly slower
first day (ADR 0045 決定 4).

- **It hibernates; it never destroys.** This is the only automatic path in the product that
  moves someone's home, so it stays reversible — and off by default.
- It is a **third** timer after the two above: the person goes idle → the workspace stops →
  the slot sleeps → (days later) the home becomes a snapshot. Set it in days, not minutes.
- **Deployment-wide, not per tenant.** The sweeper works from EC2 tags and has no view of
  tenants (ADR 0012).
- A snapshot of a 45 GiB home takes 30–40 minutes; the sweeper advances one step per pass
  and the state lives entirely in AWS tags, so a CP restart mid-way resumes rather than
  strands. If the owner comes back first, the hibernation is abandoned and the volume is
  simply reattached.

**Golden snapshot: skip boot-install for new users.** A brand-new home pays boot-install
(4 CLIs 41s + rtk 1s + agy 6s = 48s) and a cold npm cache. Bake one home that has already
paid it and every later user starts from that copy (ADR 0045 決定 9):

```bash
# 1. create a seed member, start their workspace from the Console, let it finish booting
# 2. stop it and wait for the sweeper to release the slot
./bake-golden.sh --workspace af-ws-<tenant>-<seed> --image <the exact AF_ECS_WORKSPACE_IMAGE>
# 3. destroy the seed workspace:  DELETE /api/admin/workspaces {tenant_slug,user_key}
```

- **Re-bake on every release that moves the image or a CLI pin.** The CP compares the
  `af-image` tag against the image it runs and **refuses a stale golden**, falling back to
  an empty home — new users just get the slow first start, and the CP logs why. Forgetting
  to re-bake cannot silently hand anyone old CLIs.
- One golden per pool, shared. It carries no `af-membership` tag, so destroying a
  workspace never touches it.

**Destroying a workspace (irreversible).** `DELETE /api/admin/workspaces
{tenant_slug,user_key}` deletes the home and every per-membership resource — on this
profile the EBS volume, any hibernation snapshot, the slot claim, the ECS service, both
EFS access points and both SSM parameters. It only accepts a membership that has already
been removed, and it **overrides the deletion locks of ADR 0028** (those live inside the
home, which is unreadable while the workspace is stopped). Removing a membership on its
own still keeps everything, as before; `{"purge": true}` on that call does both at once.
⚠️ On Fargate the same call cannot delete the EFS *directories* behind the access points —
they survive, and keep billing. The response and the audit entry list what was left.

**Operational facts worth knowing before you turn it on**

- **A workspace keeps its slot while it is stopped, and the slot goes to sleep with it.**
  Stopping a workspace does not detach its home ("lazy release"): the attachment IS the
  affinity, so the same person comes back to the same slot without re-attaching or
  re-mounting. After `Ec2SlotSleepSec` (default 15m) the sweeper **stops** that slot —
  never terminates it, so the image cache survives on its root volume. A stopped slot
  costs only that volume (~$9.6/month at 100 GiB) instead of ~$95 for a running one.
- **Two different idle timers, in series.** `AF_WS_IDLE_TIMEOUT` / the per-tenant
  `ws_idle_timeout` is the product's existing idle-stop: it watches the person and stops
  their *workspace* (every runtime has it). `Ec2SlotSleepSec` only starts counting after
  that, and it stops the *slot*. Someone who walks away is therefore idle-stopped on the
  tenant's timeout and their box sleeps 15 minutes later.
- **Slots are reclaimed only at the cap.** Below `Ec2MaxSlots` a new user gets a new
  slot; at the cap the longest-dormant occupant is evicted (a workspace with a running
  task is never touched). So `Ec2MaxSlots` bounds the number of *provisioned* slots, and
  `Ec2SlotSleepSec` bounds how many of them are *running*.
- **No hot spare is kept.** The first person of the morning wakes a stopped slot (~90s,
  estimated) or, if the pool has none, pays the full ~135s to build one.
- **AZ is destiny.** An EBS volume cannot leave its AZ, so a user is pinned to the AZ
  their home was created in. If no slot can be run there, that user cannot start.
- **A slot's root volume is shared with whoever had it before.** `/tmp` is a tmpfs
  (nothing lands on disk, capped size) and the ECS task-cleanup window is shortened to
  5m, but the image cache and container write layers are genuinely shared.
- **Patching slots = updating this stack** (the AMI parameter resolves at update time)
  and letting the old slots go. That is the operational cost the EC2 launch type adds.
- **Credentials still live on EFS.** The auth/identity set (`homeKeep`: `.config`,
  `.ssh`, `.git-credentials`, `.gitconfig`, `.claude`, `.claude.json`, `.codex` — under
  100 MiB) is kept on an EFS access point and symlinked into home by the entrypoint, so
  losing one single-AZ volume does not take the user's logins with it.
- **The working disk (`WsDiskGiB`) does not apply.** `AF_WS_SCRATCH` is not injected on
  this profile: home is already local EBS, so there is nothing to relocate off EFS.

## Known behavior: a cold Start answers `starting`, not `running`

A cold workspace Start pays a full image pull on every launch (Fargate keeps no
image cache between tasks). Measured end-to-end in the sandbox (2026-08-15,
`docs/62` §62.9): **126s on a fresh home, 128–218s on a warm one** — the earlier
"~100s" was optimistic. `POST
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

**The split has been measured** (2026-08-15,
[`docs/62-ecs-start-latency.md`](../../../docs/62-ecs-start-latency.md) §62.9–62.10),
and the answer was not the image. A warm-home Stop→Start costs **~101s**: **4–8s**
for ECS to create the task, **16s** provision (ENI attach), **35s** image pull
(918 MiB compressed, 34 layers), **~25s** EFS mount + container create, **21s**
entrypoint. Image pull is ~35% of that — under the 40% decision gate and under 40s
absolute — so SOCI lazy loading was **rejected**.

⚠️ **Benchmarking caveat, learned the hard way**: restarting a workspace *right
after* stopping it (within a minute or two) makes ECS take **40–143s** just to
create the task, because the previous task is still being torn down while the new
deployment lands on top of it. That is a property of the measurement loop, not of
production (a workspace is stopped by the reaper and started minutes-to-hours
later). **State the stop→start gap whenever you quote a restart number.**
Re-measure the same way if any of this changes:

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
not recur — `~/.local` persists on EFS. So measure **two** scenarios and judge
on the second: a first Start on an empty home (pull + boot-install) and a
Stop→Start on a warm home (pull only). The warm one is what scale-to-zero pays
every time.

To isolate the pull with no EFS/entrypoint noise, register a throwaway task
definition for the same image with `entryPoint` overridden (`run-task
--overrides` can override `command` but **not** `entryPoint`, and
`entrypoint.sh` ends in `exec "$@"`, so overriding the command alone still runs
the whole entrypoint). Every `run-task` is cold by construction — Fargate keeps
no image cache between tasks. The full recipe, the delta arithmetic and the
decision gate are in
[`docs/62-ecs-start-latency.md`](../../../docs/62-ecs-start-latency.md) §62.7
(conclusion on SOCI: conditional yes, gated on this measurement).

## Cost & ephemerality

**Standing cost with every workspace stopped and nobody logged in** — list prices
pulled from the AWS Pricing API for **ap-northeast-1**, 730 h/month, 2026-08-15
(scale by your region's rates; us-east-1 is roughly 30% lower):

| Always-on | Rate | $/month |
|---|---|---|
| NAT Gateway ×1 | $0.062/h | **45.3** |
| **CP/Console Fargate task ×1** (0.5 vCPU + 1 GB, `DesiredCount: 1`) | $0.05056/vCPU-h + $0.00553/GB-h | **22.5** |
| RDS db.t4g.micro Single-AZ | $0.025/h | **18.3** |
| ALB ×1 | $0.0243/h (+ $0.008/LCU-h, ~0 when idle) | **17.7** |
| RDS storage 20 GiB | $0.138/GB-mo | 2.8 |
| Secrets Manager (RDS-managed password) | $0.40/secret | 0.4 |
| Cloud Map private namespace (Route 53 private zone) | — | 0.5 |
| ECR (CP + WS ≈ 1.05 GB) | $0.10/GB-mo | 0.1 |
| EFS (every user's home persists) | $0.36/GB-mo | usage |
| **Total** | | **≈ $107/mo + EFS** |

Stopped workspaces cost **nothing** (that is scale-to-zero working); the S3 gateway
endpoint, ACM, the NAT's EIP and standard SSM parameters are free. A *running*
workspace adds Fargate at **$0.0616/h** (1 vCPU + 2 GB) — ~$11/mo at 8 h × 22 days,
$45/mo if left on around the clock.

So **the floor is four always-on pieces that do not care how many users you have**.
The lever is NAT: a **NAT instance** (t4g.nano + EBS + its public IPv4 ≈ $8/mo)
cuts ~$37/mo, at the price of running it yourself. Dropping the CP task to
256/512 saves another ~$11 (unverified that it fits). ALB and RDS are effectively
the floor — Aurora Serverless v2 costs more at its 0.5-ACU minimum.

**One or two users: [`../ec2-single`](../ec2-single/) is cheaper**, and that is what
it exists for. A t3.medium + 30 GB gp3 + EIP is ~$47/mo **with the workspaces
included** (t3.large, the default, ~$87/mo), versus ~$72/mo for a trimmed ECS
deployment that still bills workspace hours on top. ECS wins on cost only around
**8–10 concurrent users**, where the single VM has to be sized for everyone's peak
24/7 while Fargate bills only the hours each workspace actually runs — plus the
isolation ECS gives you regardless of price.

For iteration, **deploy → E2E → `delete-stack`** and keep stacks short-lived.

- NAT is needed because workspaces egress to **git / Anthropic** (public internet),
  and every private-subnet task also reaches ECR / CloudWatch Logs / SSM through it
  (there is no public IP on those tasks) — so the NAT is on the **workspace start
  path**, not just the user's internet access.
  - The **S3 gateway endpoint is in `00-network.yaml`** because it is free (no
    hourly charge, no ENI) and ECR layer blobs live in S3 — it takes the bulk of
    every cold image pull off the NAT's data processing charge ($0.062/GB in
    ap-northeast-1 — the same rate as its hourly charge). **Interface**
    endpoints (ecr.api, ecr.dkr, logs, ssm) are left out on purpose: $0.01/AZ/hour
    × 2 AZ each adds up past the NAT Gateway they would relieve.
  - What still crosses the NAT: git / Anthropic / npm / package registries (the real
    developer traffic), plus the small ECR auth+manifest, Logs and SSM calls.
  - Replacing the NAT Gateway with a **NAT instance** (t4g.nano $3.9 + 8 GB gp3
    $0.8 + its public IPv4 $3.7 ≈ **$8/mo**, vs **$45** for the managed gateway) is
    the biggest single lever, at the price of running it yourself (source/dest check
    off, iptables MASQUERADE, ASG for replacement + route rewrite).
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
deleting `af-ecs-network`. Order that works (measured 2026-08-15):

1. ECS **Service** `af-ws*` — `update-service --desired-count 0`, then `delete-service --force`.
2. **Task definitions** `af-ws*` — `deregister-task-definition` for every ACTIVE revision
   (the adapter registers a new one per Start, so expect several).
3. **EFS access points** — `describe-access-points --file-system-id <efs>` then delete each.
   ⚠️ **Miss these and `delete-stack af-ecs-data` stalls.** The filesystem id is the
   `EfsId` output of `af-ecs-data` (*not* `FileSystemId` — a sweep script keyed on the
   wrong name silently deletes nothing).
4. **SSM** `/af-ws/*` (per-workspace DEK/token) and `/af-cp/*` (the out-of-band CP
   secrets you created before `30-ingress`).
5. Then the four `delete-stack` calls above, in order.

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
