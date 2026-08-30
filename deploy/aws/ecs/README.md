# Agent Fleet on ECS (CloudFormation) — P3-7 native AWS adapter

The **native AWS adapter** (`AF_RUNTIME=ecs`): each Workspace is one ECS Service
(`desiredCount` 0/1 = scale-to-zero) with an EFS access point for its home, reached
over Service Connect. This is **not** the single-VM Compose host — for "self-host on
AWS the easy way" use [`../ec2-single`](../ec2-single/) instead.

Frozen implementation spec: [`docs/log/p3-7-aws-adapter.md` §20b.7](../../../docs/log/p3-7-aws-adapter.md#20b7-段2-実装仕様凍結).
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
| `cfn/40-ec2-pool.yaml` | **proven in a sandbox** (deployed as a stack and driven end to end, in a public subnet and behind a NAT — docs/log/64 §64.16, §64.17, §64.19; never at scale) | **Optional — only for `WsRuntime=ecs-ec2`.** Launch template for a workspace *slot* (ECS-optimized AMI, cluster-join user-data, `af-mount`/`af-umount`), slot instance role + profile, slot SG. Creates **no instances**: the CP runs them on demand. One template covers both architectures — `SlotAmiIdArm64` is passed through as an ImageId override (docs/log/70 §70.8) |

> All five are proven end-to-end **including teardown**: two real deployments in two
> separate AWS accounts (`WsRuntime=ecs-ec2`, one `Persistence=delete` and one
> `=retain`) ran for days and were deleted on 2026-08-22 leaving zero orphans in
> either account — the sweep in §Teardown came back empty on every counter. Standing
> one up needs a domain + Google OAuth client + the CP image in ECR (see stand-up
> below). Each stack imports the earlier ones' exports, so deploy in order
> `00 → 10 → 20 → (40, ecs-ec2 only) → 30`.

### Prerequisites (once per account)

- **ECS service-linked role.** A fresh account has no `AWSServiceRoleForECS`, and
  creating a cluster with a Service Connect default namespace fails with
  *"ECS Service Linked Role is not ready"*. Create it once (idempotent — ignore the
  "has been taken" error on re-run):
  ```bash
  aws iam create-service-linked-role --aws-service-name ecs.amazonaws.com || true
  ```
- **Cloud cost (optional, but it cannot be backfilled — see docs/log/67).** Two account-level
  switches the templates cannot set, both in the Billing console of the **payer** account.
  ⚠️ **None of this arrives with the stacks.** The templates carry only the IAM permission
  (`ce:GetCostAndUsage` on `CpTaskRole`); the tags themselves are written by the *running*
  Control Plane, and there is no CloudFormation resource type for activating a cost
  allocation tag at all (`AWS::CE::CostAllocationTag` does not exist). So on a brand-new
  deployment: deploy → **start one workspace so the CP stamps the tags** → wait up to 24h
  for AWS to discover the keys → activate. The spend in that gap is lost for good, so start
  a workspace on day one even if nobody is using it yet.
  1. **IAM user and role access to Billing Information** must be ON, or `CpTaskRole`'s
     `ce:GetCostAndUsage` fails for every call even though the policy is attached.
  2. **Activate the cost allocation tags** — **normally the Control Plane does this for
     you.** It retries every poller tick until AWS has discovered each key, skips
     `af-workspace` (email-derived), and leaves alone any tag a human switched off. Do it
     by hand only if you removed the `CostAllocationTagActivation` statement from
     `CpTaskRole`, or if the Console reports it could not (`af-membership`, `af-tenant`,
     `af-role`, `af-pool`, `af-slot-size`):
     ```bash
     aws ce update-cost-allocation-tags-status --region us-east-1 \
       --cost-allocation-tags-status '[{"TagKey":"af-membership","Status":"Active"},
         {"TagKey":"af-tenant","Status":"Active"},{"TagKey":"af-role","Status":"Active"},
         {"TagKey":"af-pool","Status":"Active"},{"TagKey":"af-slot-size","Status":"Active"}]'
     ```
     ⚠️ **Do this on day one.** Activation is not retroactive: every day it is left off is a
     day of spend that can never be attributed to anyone.
     **Each key once per AWS account, by you — never again per tenant or per member.**
     Activation is keyed on the tag KEY alone (the API has no value dimension), so the one
     `af-membership` entry covers every member who will ever exist here and the one
     `af-tenant` entry covers every tenant: somebody joining next month needs nothing. What
     that does NOT mean is that a key can be skipped — every key in the list above has to be
     activated once. Under AWS Organizations only the management (payer) account can do it.
     ⚠️ **A tag key AWS has never seen on a real resource cannot be activated**
     (`ValidationException: Tag keys not found`). So the order is: deploy → start one
     workspace (the CP stamps the tags) → wait for AWS to discover the keys (up to 24h) →
     activate. The CP walks that sequence on its own; **your part is starting one workspace
     on day one**, even if nobody is using it yet, because the discovery gap is spend that
     is lost for good. `list-cost-allocation-tags`
     shows what AWS has found: a key already listed as `Inactive` flips to `Active`
     instantly, and a key missing from the list is one nothing has stamped yet. The wait is
     per KEY and only the first time it appears — not per tenant, per member, or per
     deployment of a key that is already listed.
     ⚠️ **Do not activate `af-workspace`.** Its value is derived from the member's email
     address, and activating it copies that into the billing data (CUR / Cost Explorer /
     invoice CSVs). `af-membership` is an opaque random id and is the join key the Control
     Plane uses.
- **`10-data` needs `--capabilities CAPABILITY_AUTO_EXPAND`** — it declares
  `Transform: AWS::LanguageExtensions`, and a template with a transform is rejected
  without that capability (`Requires capabilities : [CAPABILITY_AUTO_EXPAND]`). It
  creates no IAM, so this is the only capability it needs:
  ```bash
  aws cloudformation deploy --stack-name af-ecs-data \
    --template-file cfn/10-data.yaml --capabilities CAPABILITY_AUTO_EXPAND \
    --parameter-overrides Persistence=retain \
    --profile af-sandbox --region ap-northeast-1
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
   ⚠️ The **git providers' OAuth apps are not deployment configuration** (docs/log/71). A
   tenant administrator registers GitHub and Bitbucket in the Console under **Tenant
   settings → Integrations → Git provider OAuth**; there is no CFN parameter and no SSM
   entry for them. The Bitbucket consumer's **Callback URL must be exactly**
   `https://<your-fqdn>/api/oauth/bitbucket/callback` — Bitbucket matches it in full, and
   the CP derives it from `PUBLIC_BASE_URL`, so it is not separately configurable. The
   consumer needs a secret (authorization-code grant): the workspace credential helper
   refreshes the access token, and a public consumer issues none.
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
   `GoogleClientId` for the `Oidc*` parameters (docs/log/61) — leaving `GoogleClientId`
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

### WAF (optional, off by default)

`30-ingress` can put an AWS WAF web ACL in front of the ALB. Two knobs, and only two on
purpose:

```bash
--parameter-overrides WafRateLimitPer5Min=3000 WafIpReputation=on ...
```

- `WafRateLimitPer5Min` — requests per 5 minutes from one IP before WAF blocks it. `0`
  (default) creates no web ACL at all. It counts every request including Console polling,
  so keep it far above what a whole office behind one NAT produces. Verified end to end:
  with the limit at 100, a 150-request burst got `403` within ~45s; raising it released
  the IP on the next evaluation.
- `WafIpReputation` — AWS's managed IP reputation list. It matches on the SOURCE, not on
  the body. Measured on a real deployment: it blocked the scanners probing `/.env` and
  friends within minutes of being switched on.

⚠️ **The signature rule sets (Core rule set, SQLi, XSS, LFI) are deliberately not
offered.** This product carries source code and shell commands in ordinary request bodies
— chat messages, file writes, terminal input — so `'; DROP TABLE`, `../../etc/passwd` and
`<script>` are legitimate traffic here. Those rules would 403 real work at random, and it
would look like a product bug long before anyone suspects the WAF. (WAF also inspects only
the first 8 KB of a body by default, so a signature set buys less than it appears to.)

For a private deployment the cheaper and stronger control is upstream: set
`00-network`'s `AlbIngressCidr` to your own range and the traffic never reaches the ALB.
Cost, if you do turn WAF on: about $5/month for the web ACL plus $1/month per rule, plus
$0.60 per million requests.

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

### One command: `update.sh`

`./update.sh` is the runbook above as a script — the ECS counterpart of compose's
`docker compose pull && docker compose up -d`. Use it unless you have a reason to
drive the steps by hand.

```bash
VERSION=<v> ./update.sh --profile <p> --region <r>          # deploy an already-pushed tag
VERSION=<v> ./update.sh --profile <p> --region <r> --push   # push to ECR first
VERSION=<v> ./update.sh --profile <p> --region <r> --dry-run
```

It does the three things the hand-typed sequence gets wrong:

- **Refuses a tag that is not in ECR.** CloudFormation only stores a string, so a
  forgotten (or wrong-region) push *succeeds* and the CP task then dies with
  `CannotPullContainerError`. The script checks both repositories first.
- **Notices the empty change set.** Re-pushing the *same* tag (the `:dev` sandbox
  flow) leaves the template byte-identical, so `cloudformation deploy` reports
  "No changes to deploy" and the CP keeps running the old image forever. The
  script falls back to `ecs update-service --force-new-deployment` in that case
  (`--force` does it unconditionally), then waits for the service to stabilise.
- **Lists the workspaces that are still on the old image**, because nothing moves
  them automatically. It never stops one: stopping kills that user's sessions, and
  when to take that is their call.
- **Points out a golden snapshot left behind** (`ecs-ec2` only). A golden baked from
  the previous image is not used at all — the CP builds new users' homes empty
  instead (ADR 0045 決定 9), which is not a failure, just a slow first start that
  nothing but the CP log mentions. With auto-bake on (the default) the CP replaces it
  within a few minutes of the deploy, as long as two slots are free; the pool panel
  says so while it is happening. Otherwise re-bake with `bake-golden.sh`.

### A development deployment: `dev-deploy.sh`

`update.sh` deploys a **released** tag. A deployment kept for development wants the
opposite: whatever is on `develop` right now, without cutting a version. That is
`dev-deploy.sh` (docs/log/73), and it ends by calling `update.sh` — the pre-flight checks
above are the reason, and writing a second copy of them for the dev path would mean the
checks are what drifts.

```bash
./dev-deploy.sh --profile <p> --region <r>            # origin/develop → the dev deployment
./dev-deploy.sh --profile <p> --region <r> --dry-run  # just the plan
```

It names the tag (`<next patch>-dev-<sha8>`), bakes it through `dev-image.yml` (GHCR,
never a release tag), copies it into ECR **index and all**, and moves `ImageTag`.
Three things it decides for you:

- **Whether the workspace image needs baking at all.** Only when `workspace/` changed
  since the deployed tag; otherwise the workspace image is re-tagged inside ECR, which
  copies no bytes. Baking it costs ~10 minutes of QEMU.
- **That the golden snapshot does not have to be re-baked** when those bytes are
  identical (docs/log/72 §72.6.4 paid 10 minutes and two slots for exactly that).
- **That this is not somebody's live deployment.** It refuses any `Fqdn` but the dev
  one unless `--allow-fqdn` says otherwise: moving `ImageTag` puts a "restart required"
  badge in front of everyone running a workspace there.

⚠️ It bakes **origin's** ref. Local commits that are not pushed are silently not in it.

### What the users see

Two different signals, and they mean different things:

| Signal | Trigger | What it costs |
|---|---|---|
| Console toast "New version available" | the new CP is serving a new `version.json` | a reload; **sessions keep running** |
| WS bar "Restart needed" badge | this workspace is running an older image than a Start would use now | a Stop→Start; **sessions stop** |

The badge is CP-side detection (`control-plane/workspace_stale.go` →
`runtime_ecs_stale.go`): at every Start the adapter stamps the fingerprint of the
image content the tag resolved to into the task definition it registers, and the
`/api/workspace` poll compares that stamp with what the tag resolves to *now*
(`ecr:BatchGetImage`, cached 60s). Never a version comparison, and attestation-only
re-pushes are silent — the fingerprint is the set of per-platform manifest digests,
not the index digest.

⚠️ **Existing deployments must re-deploy `20-platform` once** for the badge to work:
the probe needs `ecr:BatchGetImage` / `ecr:DescribeImages` on the workspace
repository, which was added to `CpTaskRole` for it. Without that permission the
probe fails, the CP reports "unknown", and the badge simply never appears — a
silent loss, not an error. A deployment whose `AF_ECS_WORKSPACE_IMAGE` is not an
ECR reference (mirroring GHCR directly, say) gets no badge either, by design.

## Egress IP (the address a customer allow-lists)

A deployment egresses from **exactly one address**: the Elastic IP on `00-network`'s
NAT gateway. Everything outbound goes through it — every workspace's git, model API
and package traffic, and the CP's own calls — because the slots and the CP task sit
in private subnets with **no public IPv4** (`AssignPublicIp: DISABLED`; the slot
launch template deliberately assigns none, ADR 0045 決定 3-3) and the only VPC
endpoint is the free S3 gateway. So this is the value to hand to a corporate
firewall or a SaaS IP allow-list.

```bash
alloc=$(aws cloudformation describe-stacks --stack-name af-ecs-network \
  --query "Stacks[0].Outputs[?OutputKey=='NatEipAllocationId'].OutputValue" --output text)
aws ec2 describe-addresses --allocation-ids "$alloc" --query "Addresses[0].PublicIp" --output text
```

⚠️ **`describe-addresses` also shows the ALB's addresses** (`ServiceManaged: alb`).
Those are inbound, AWS-owned, and not contractually stable — never publish them.
Inbound is the FQDN's job.

**Keeping the address across a rebuild.** `NatEip` carries `DeletionPolicy: Retain`
and `UpdateReplacePolicy: Retain`, so no stack operation — including a full
`teardown.sh` — can release it. Capture the allocation id (`capture-env.sh` stores
it as a `00-network` parameter) and pass it back at the next stand-up:

```bash
aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
  --parameter-overrides NatEipAllocationId=eipalloc-...
```

🔴 **Do NOT set that parameter on a running deployment — not even to the address the
stack already created.** Measured on a live deployment (2026-08-26, via
`--no-execute-changeset`): CFN sees the `AllocationId` property's source change from
`!GetAtt NatEip.AllocationId` to a parameter, evaluates it `Static` /
`RequiresRecreation: Always`, and plans `ReplaceAndDelete` on the **NAT gateway** —
with the same allocation still attached to the old one. Egress drops and the create
cannot succeed. The parameter is for a *fresh* stand-up, where it is an ordinary
create. On a live stack, retention alone already guarantees the address.

⚠️ **One NAT means one AZ.** `00-network` puts a single NAT gateway in the first AZ
for cost; if that AZ is lost, *every* workspace loses egress, including those in the
second AZ. Adding a NAT per AZ is the fix — and it makes the deployment egress from
**two** addresses. Decide before the allow-list is published: an unattached EIP costs
~$3.6/month, so allocating the second address up front and registering both is the
cheap way to keep that door open.

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
tenant cap. Design and measurements: `docs/log/63`, decisions in `docs/decisions/0044`.

| Parameter | Env | Default | Notes |
|---|---|---|---|
| `WsTaskCpu` | `AF_ECS_TASK_CPU` | `1024` | Fargate units; 1024 = 1 vCPU |
| `WsTaskMemory` | `AF_ECS_TASK_MEMORY` | `2048` | MiB |
| `WsDiskGiB` | `AF_ECS_WS_DISK_GB` | `50` | 21–200 sets ephemeral storage; 0 = Fargate's free 20 GiB (turns cache relocation off) |

**Only specific (cpu, memory) pairs exist**, and the steps are not uniform — 8 vCPU moves
in 4096 MiB steps and 16 vCPU in 8192, while 0.25 vCPU accepts only 512/1024/2048. The
full matrix was measured against the ECS API and is in `docs/log/63` §63.2. The CP snaps any
request onto a valid pair, so an odd value never reaches a task definition; the visible
effect is that **raising CPU can also raise memory** (4 vCPU cannot run below 8 GiB).

The working disk is **not persistent** — it is wiped when the workspace stops, like every
other task-local disk on Fargate. Only the EFS home survives. `WsDiskGiB` is also the
switch for moving small-file trees off EFS onto that disk: the workspace entrypoint
relocates the caches (Go build cache, `uv`, Go modules) and the Agent relocates build
artifacts (`node_modules`, `target`, `.venv`, `build`) as each working copy is created.
Both arm only when the disk is **30 GiB or more**, because the measured caches do not fit
alongside the image in 20 GiB. On EFS these trees are 8–30× slower to write (`docs/log/63`
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

🚧 **Stood up and run in a sandbox, including a production-shaped VPC — but never with
real users.** `40-ec2-pool` has been deployed as a stack and real workspaces have run on it
end to end, first in a public subnet (docs/log/64 §64.16, §64.17), then in a **private subnet
behind a NAT gateway** (§64.19), and then with **four slots across two AZs, three users
starting at once, and the sweep loop left running for eighteen minutes** (§64.20). The
task-ENI trap below does not reproduce behind a NAT — there is no public IPv4 to lose.
Fargate (`WsRuntime=ecs`) stays the default and is untouched by this profile.

⚠️ **Multiple AZs: three things to know.** An EBS home cannot leave its AZ, so the CP
starts a slot in the AZ that user's home is already in.

1. **Every AZ you list in `AF_ECS_SUBNETS` needs its own EFS mount target**, or a task
   landing there cannot mount the credentials filesystem and never comes up.
2. **New homes are spread across the AZs you list**, fewest-homes-first, so losing one AZ
   does not take out everybody (ADR 0045 決定 16). It costs slots: a home in one AZ can only
   use a slot in that AZ, so free slots elsewhere are no use to it and the pool grows
   instead of reusing. Reusing a free slot still wins over balancing. If an AZ runs out of
   the slot type, a **new** home falls back to another; an **existing** one fails, because
   it cannot move (決定 15).
3. **To move someone to another AZ there is no "move".** Hibernate their home and start it
   again with free capacity only where you want them — the snapshot has no AZ. docs/log/64
   §64.20.7 has the runbook, including the AWS CLI form for moving one person to a
   named AZ.

**Losing an AZ is a different question from moving.** A home cannot be evacuated, so the
answer has to be in place before the bad day: turn on **home backups** (below). docs/log/64
§64.21 walks through what the adapter actually does during an AZ outage, what the operator
should do first (drop the failed subnet from `AF_ECS_SUBNETS` and restart the CP), and how
to rebuild a home from a backup afterwards.

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
| Warm Start | ~105s | **84–110s** — *not* an improvement worth switching for (docs/log/64 §64.17.5, §64.19.2) |
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
| `Ec2SlotTypes` | `AF_ECS_EC2_SLOT_TYPES` | `m7i.large:8192:2,…` | `instanceType:memoryMiB[:vcpu]`, ascending — or several named **classes**, see below. The vCPU field is optional and display-only (the Console shows which box a memory number lands on) |
| `Ec2SlotAmiArm64` | `AF_ECS_EC2_AMI_ARM64` | `""` | `SlotAmiIdArm64` output of `40-ec2-pool`. Required only when a class declares `arm64` — the CP refuses to boot otherwise |
| `Ec2DefaultSlotClass` | `AF_ECS_EC2_DEFAULT_SLOT_CLASS` | `""` (the first class) | Where a member with no per-user and no per-tenant choice lands |
| `Ec2MaxSlots` | `AF_ECS_EC2_MAX_SLOTS` | `8` | Hard cap **across all classes**. Start fails at the cap rather than growing the bill |
| `Ec2SlotSleepSec` | `AF_ECS_EC2_SLOT_SLEEP_SEC` | `900` | How long a box may sit with no task before it is **stopped**. Ends the compute charge; the root volume keeps billing. `0` = never |
| `Ec2SlotTerminateAfterSec` | `AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` | `0` (off) | The next step on the same clock: past it the box is **terminated** and its root volume goes too. `0` means boxes are kept forever, so retained roots grow to `Ec2MaxSlots` — see below. `14400` (4h) recommended |
| `Ec2HomeGiB` | `AF_ECS_EC2_HOME_GB` | `50` | Per-user home volume (gp3) |
| `Ec2HibernateAfterSec` | `AF_ECS_EC2_HIBERNATE_AFTER_SEC` | `0` (off) | **Default** for how long a home may sit unopened before it is snapshotted and its volume deleted. A tenant overrides it from the Console. Nothing is destroyed — the snapshot completes before the volume goes, and the next start restores it. See below |
| `Ec2HostReserveMb` | `AF_ECS_EC2_HOST_RESERVE_MB` | `auto` | How much of a slot is held back from the workspace so the box's own daemons cannot be starved by it. `auto` = a fifth of the rung, clamped to 1–2 GiB, which puts an 8 GiB slot's workspace at 6.4 GiB. `off` = uncapped (what every deployment did before 0.12.5, and what melted one). See below |
| — | `AF_ECS_EC2_SLOT_LOST_AFTER_SEC` | `300` | How long a slot may be EC2-`running` while the cluster cannot reach its agent before the CP gives up on it and rebuilds the workspace elsewhere. `0` = wait as long as the caller allows |
| — | `AF_ECS_EC2_GOLDEN_AUTOBAKE` | `1` (on) | Keep the golden snapshot in step with the workspace image without anyone re-baking by hand (ADR 0045 決定 9-1). Set `0` and it becomes your job on every release |
| — | `AF_ECS_EC2_GOLDEN_BAKE_SEC` | `60` | How often the baker looks. It advances one step per look, so this is also how fast a bake progresses |

**What a workspace actually gets (`Ec2HostReserveMb`).** A slot's memory is not all the
workspace's: dockerd, containerd, the ECS agent, SSM and the EFS stunnel live on the same
box, and the rung overstates what the machine has (an "8192 MiB" m7i.large reports 7784).
So the container is capped at the rung less a reserve, and the Console prints the number
the workspace can actually spend with the box beside it — `6.4 GiB (of 8 GiB)`.

⚠️ **Turning it off is not free.** Uncapped, one workspace taking several GB of anonymous
memory can push the box into refault thrash: with no swap the kernel throws away page
cache, every daemon spends its time re-reading its own executable off disk, and the slot
stops answering the cluster entirely while still looking healthy to EC2. That happened on
a live deployment (docs/log/64 §64.40) — nobody could start that workspace again for hours.

⚠️ **Changing the reserve replaces running tasks.** It is part of the task definition, so
the first start after the change registers a new revision — the same as an image change.

**Alarms (`PoolAlarmEmail` on `40-ec2-pool`).** Off by default. When set, the stack creates
an SNS topic and two CloudWatch alarms on the pool's I/O — a deep EBS queue sustained for
15 minutes, and an instance that has spent a fifth of its EBS byte credits. Both were
unmistakable during that incident and nobody was looking. ⚠️ **They cannot be scoped to
this stack**: the metrics are dimensioned by volume and instance id, the pool creates those
at runtime, and Metrics Insights filters on dimension values rather than tags — so the
queries are account-wide within the region. Turn them on where the account is the
deployment's own.

**Offering a choice of machine (docs/log/70).** `Ec2SlotTypes` takes several named ladders,
`id|label|arch|<ladder>` separated by `;`, and a tenant administrator then picks one per
tenant and per member. Memory still picks the rung *within* the class, so "8 GB" keeps
meaning the same thing. **The shipped default already declares two**, because "cost over
speed" is a legitimate choice and one version of it costs nothing to offer:

```
standard|Standard (Intel)|x86_64|m7i.large:8192:2,…;saver|Lower cost (Intel, previous gen)|x86_64|m6i.large:8192:2,…
```

Measured in ap-northeast-1 on 2026-08-22 — the price from the Pricing API, the speed
from this repository's own build on each family (`harness/bench-instance-classes.sh`,
docs/log/70 §70.3):

| against m7i | $/hour | build time | **cost per build** |
|---|---|---|---|
| m6i | −4.8% | +13% | +7.3% |
| **m8g** (Graviton4) | **−11.0%** | **−29%** | **−37.1%** |
| m7g (Graviton3) | −19.0% | −10% | −27.4% |
| m6g (Graviton2) | **−24.0%** | **+32%** | ±0 |

⚠️ **The cheapest box per hour is not the cheapest box.** m6g bills 24% less per hour
and takes 32% longer, which nets out to no saving at all on work that keeps the CPU
busy — and m8g is *both* cheaper and faster than m7i, so there is no reason to run m7g.

⚠️ **Which column matters depends on the member.** A slot bills for the wall-clock time
it is RUNNING, and a workspace spends most of that idle (reading, thinking, waiting on a
model), so a mostly-reading member really does pay 24% less on m6g. A member who builds
all day pays in time instead. That is the whole reason this is a per-member setting —
and the reason a class's LABEL should carry the trade-off ("Cheapest — older, slower")
rather than just the discount.

**m6i is the one that needs nothing.** Same architecture as m7i, so: the same workspace
image, the same AMI, the same golden snapshot, and a member's home keeps working when they
move between the two. −4.8% for no prerequisites and no migration. Graviton is where the
real money is, and it is also where every prerequisite below lives.

⚠️ Adding a class moves nobody. `Ec2DefaultSlotClass` decides where members with no choice
of their own land, and it defaults to the FIRST class. A running stack also keeps its own
`Ec2SlotTypes` on a redeploy unless you pass the parameter, so upgrading the CP does not
put a picker in front of anybody.

Before declaring an `arm64` class, in this order:

1. **The workspace image needs an arm64 manifest.** Without it the slot launches fine and
   the task cannot pull anything that runs on it. (`crane manifest <image>` — a single
   `manifest.v2+json` is one architecture; a manifest list / OCI index is what you want.)
2. **Set `Ec2SlotAmiArm64`.** A launch template pins one AMI and Graviton cannot boot the
   x86_64 ECS-optimized one; the CP passes this as a `RunInstances` ImageId override.
   `aws ssm get-parameter --name /aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id --query Parameter.Value --output text`.
   Like the x86_64 AMI, patching it means redeploying `40-ec2-pool`.
3. **Expect a second golden bake.** A golden is a home full of binaries, so there is one
   per architecture; until the arm64 one is baked, new members on an arm64 class start
   from an empty home (slower, not broken). The pool screen lists them per architecture.

Adding a class moves nobody by itself. Moving a member *across architectures* is safe but
not free: their home is kept and its architecture-dependent contents (`~/.local` CLIs, nvm
node, Chromium) are reinstalled on the next start, and anything they built under `~/repos`
(`node_modules`, `target`, `.venv`) has to be reinstalled by them. The Console says so
before saving.

**Hibernating long-unused homes (opt-in).** An EBS home bills for what it is *provisioned*
at, whether or not anyone opens it. With this enabled, a home that nobody has opened for
that long is captured as a snapshot and its volume deleted; the owner's next Start rebuilds
it from the snapshot. For a 20 GiB-used / 50 GiB-provisioned home that is
**$4.80 → $1.00 a month**, against ~122s on the return and a slightly slower first day
(ADR 0045 決定 4).

- **It hibernates; it never destroys.** This is the only automatic path in the product that
  moves someone's home, so it stays reversible — and off by default.
- It is a **third** timer after the two above: the person goes idle → the workspace stops →
  the slot sleeps → (days later) the home becomes a snapshot. Set it in days, not minutes.
- **Per tenant, with this env as the deployment default** (ADR 0045 決定 14).
  Settings → Admin → a tenant → *Hibernate unused homes* takes a duration string
  (`720h`); empty follows this env and `0` means never for that tenant. The trigger lives
  in the idle-stop reaper, so **`AF_IDLE_SWEEP_INTERVAL=0` disables hibernation too**.
- **Two ways to turn it on, and the tenant one needs no deploy.** A retention window is
  usually the tenant's answer, so setting it in the Console is both quicker and more
  correct. Use `Ec2HibernateAfterSec` when you want a floor for tenants that never choose.
  ⚠️ Until that parameter existed the deployment default could not be set on ECS at all —
  there was no CFN parameter, and a hand-edited task definition is overwritten by the next
  deploy — so a deployment reading "default 0 (off)" was reading the *only* value it had.
- A snapshot of a 45 GiB home takes 30–40 minutes; the sweeper advances one step per pass
  and the state lives entirely in AWS tags, so a CP restart mid-way resumes rather than
  strands. If the owner comes back first, the hibernation is abandoned and the volume is
  simply reattached.

**Spare copies of each home (opt-in).** An EBS home lives in exactly ONE Availability
Zone and cannot be evacuated, so losing that zone loses the home with it. Backups are the
only copy that is not in the zone — snapshots are regional.

| Env | Default | Notes |
|---|---|---|
| `AF_ECS_EC2_BACKUP_EVERY_SEC` | `0` (off) | **Default** interval; a tenant overrides it in Settings → Admin → the tenant → *Keep a spare copy of each home* |
| `AF_ECS_EC2_BACKUP_KEEP` | `3` | How many completed copies to keep per home. Snapshots are incremental, so copy 2 costs only what changed |

- Taken **while the home is in use** — a backup is crash-consistent, the same picture a
  power cut leaves. Quiescing would mean taking a working person's home away on a timer.
- **Never restored automatically.** A backup is older than the home by definition; handing
  somebody a silently older home is worse than telling an operator to decide. The restore
  runbook is docs/log/64 §64.21.4.
- The trigger is the idle-stop reaper, so `AF_IDLE_SWEEP_INTERVAL=0` turns backups off too.
- The Slots tab shows, per home, how old its newest spare copy is — and says so loudly when
  there is none.

**Baking the workspace image into the slot AMI: tried, measured, removed.** A slot's root
volume IS the image cache, so baking the image in does remove the pull (31.8s → **0.185s**,
measured) — and makes the slot **slower overall**, because a private AMI's root is lazily
loaded from a fresh snapshot: the box took ~56s longer to join the cluster and a new user's
first start measured **179–192s against 144s** on the stock ECS-optimized AMI. The script
and the CP-side reporting were removed rather than left as a not-recommended option; the
measurement and the reasoning are in docs/log/64 §64.24 / ADR 0045 決定 19. **`SlotAmiId` stays
at its default** (the ECS-optimized AMI's SSM parameter — re-deploying this stack is how
slots get patched, 決定 7).

**A slot that cannot mount a home is quarantined** (`af-role=quarantined`, ADR 0045 決定 20):
it leaves the pool so nobody else lands on it, its home is detached and freed for another
slot, and the box is stopped. It stays on the Slots tab with the reason, because it still
holds its root volume — **terminate it yourself** once you have taken what you need from
it (this adapter never terminates instances). The failure that made this necessary was a
wedged kernel holding a deleted volume's NVMe namespace, which no amount of retrying fixes.

**Golden snapshot: skip boot-install for new users.** A brand-new home pays boot-install
(4 CLIs 41s + rtk 1s + agy 6s = 48s) and a cold npm cache. Bake one home that has already
paid it and every later user starts from that copy (ADR 0045 決定 9).

**The CP does this by itself** (決定 9-1, `AF_ECS_EC2_GOLDEN_AUTOBAKE=0` to switch off).
When the image it runs has no golden, it boots a reserved seed through the ordinary Start
path, captures its home as a *candidate*, boots a second reserved member from that
candidate, and only publishes it once that one comes up. It will not start while the pool
has fewer than two free slots, and it gives up on an image after two failed candidates —
the pool panel says which of those is happening. Everything below is the manual path, for
a deployment that has turned it off (or for baking one on the spot):

```bash
# 1. create a seed member, start their workspace from the Console, let it finish booting
# 2. stop it and wait for the sweeper to STOP THE SLOT (see below — the home stays attached)
./bake-golden.sh --workspace af-ws-<tenant>-<seed> --image <the exact AF_ECS_WORKSPACE_IMAGE>
# 3. destroy the seed workspace:  DELETE /api/admin/workspaces {tenant_slug,user_key}
```

- **A seed member has to be somebody who can sign in.** There is no admin "start
  this member's workspace" — `/api/workspace/start` always resolves to the caller's
  own identity — so an invite-only address nobody holds gives you a membership whose
  workspace can never be created. Adding an account you DO hold to a throwaway tenant
  is the cheap way to get the fresh membership a seed needs.
- **Step 3 is a membership of your own, and that is allowed.** The roster refuses only
  your *last* membership (docs/log/61 §61.10.6), so you take yourself off the throwaway
  tenant from the Console — *Remove member* with *destroy the workspace and home*
  ticked, or remove then destroy. Until that rule was narrowed, every self-removal was
  refused and a deployment with a single administrator could not free the seed's slot at
  all — which then blocks the automatic bake, because it will not start with fewer than
  two free slots.
- **Step 2 waits for the slot to stop, not for the volume to detach.** A Stop keeps the
  home attached on purpose (the attachment *is* that user's slot), and the sweeper only
  stops the instance — it logs `stopping slot <id> (home stays attached)`. Nothing in the
  normal lifecycle ever detaches it, so waiting for `available` never ends. Baking off a
  **stopped** slot is correct: that shutdown unmounted the filesystem, which is the same
  reasoning `releaseSlot` uses when it skips the umount on a stopped slot.
- **Do not clone repositories into the seed.** `~/repos` lives on the home volume, so
  anything cloned there is handed to every new user. Bake boot-install and nothing else.
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
  re-mounting. After `Ec2SlotSleepSec` (default 15m) the sweeper **stops** that slot, so
  the image cache survives on its root volume. A stopped slot costs only that volume
  ($3.84/month at 40 GiB) instead of ~$95 for a running one.
- **Stopping ends the compute charge; only terminating ends the ROOT VOLUME charge.**
  `Ec2SlotTerminateAfterSec` (default `0` = off) is the next step on the same clock: past
  it the box is terminated and its root volume goes with it. ⚠️ Leave it off and the
  number of retained roots only ever grows, with `Ec2MaxSlots` as its ceiling — so raising
  `Ec2MaxSlots` to serve more people also signs you up for that many root volumes,
  permanently and whether or not anyone is working (30 × 40 GiB ≈ **$115/month**). That was
  measured on a live deployment, where the only cure was terminating boxes by hand
  (docs/log/64 §64.32). **14400 (4h) is the recommended value**, and it costs your users 25
  seconds: come back the same day and you wake the box (~110s), come back tomorrow and one
  is built for you (~135s).
- **Two different idle timers, in series.** `AF_WS_IDLE_TIMEOUT` / the per-tenant
  `ws_idle_timeout` is the product's existing idle-stop: it watches the person and stops
  their *workspace* (every runtime has it). `Ec2SlotSleepSec` only starts counting after
  that, and it stops the *slot*. Someone who walks away is therefore idle-stopped on the
  tenant's timeout and their box sleeps 15 minutes later.
- **Slots are reclaimed only at the cap.** Below `Ec2MaxSlots` a new user gets a new
  slot; at the cap the longest-dormant occupant **of the same instance type** is evicted
  (a workspace with a running task is never touched). So `Ec2MaxSlots` bounds how many
  people work *at once*, `Ec2SlotSleepSec` bounds how many boxes are *running*, and
  `Ec2SlotTerminateAfterSec` bounds how many *exist* — without the third, "how many
  exist" is `Ec2MaxSlots` too.
- **At the cap, a box of the wrong size is removed rather than handed over.** An instance
  type is not something a running box can change, so if the cap is held entirely by sizes
  a member cannot run on, eviction has nothing to offer. Rather than fail their start, the
  CP terminates one dormant box of a size they cannot use — an empty one first, otherwise
  the longest-dormant — and builds theirs in its place. What that costs its former owner is
  the image cache (~110s becomes ~135s on their return), never their home: it is detached
  first and never deleted. ⚠️ This is **not** gated on `Ec2SlotTerminateAfterSec`: that
  parameter is about idle cost, this only ever runs when the alternative is a member who
  cannot start at all — and its default of `0` is exactly the deployment that sits at the
  cap. If nothing can be freed (everything running, or claimed by a start in flight) the
  start still fails, and says the pool is full.
- **An EMPTY slot sleeps on the same timer.** A slot whose home has been released — by an
  eviction, a size/class change, a `Destroy`, or the golden bake finishing with its seed
  and probe — belongs to nobody, and `Ec2SlotSleepSec` stops it too. ⚠️ Before docs/log/64
  §64.31 only *occupied* slots were ever stopped, so a released box ran until an operator
  noticed: measured on a live deployment, three empty `m*.large` up for over 24h with zero
  tasks, at ~$95/month each.
- **No hot spare is kept, and no warm floor either.** The first person of the morning
  wakes a stopped slot (~110s) or, if the pool has none, pays the full ~135s to build one.
  Keeping an empty slot *hot* would save them ~92s (43s vs 135s) for a full instance-hour,
  every hour — `Ec2SlotSleepSec=0` buys that if you want it. Keeping stopped boxes around
  instead ("keep at least N warm") buys much less than it looks: a dormant box is only
  reusable **by its owner** (its home is still attached, so `freeSlots` never offers it to
  anyone else), and making one generic means the next user pays the wake plus the attach
  plus the mount SSM round trip — 123–143s against 135s for a fresh box. So there is no
  floor parameter; `Ec2SlotTerminateAfterSec` alone decides how long a box is kept.
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
`docs/log/62` §62.9): **126s on a fresh home, 128–218s on a warm one** — the earlier
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
[`docs/log/62-ecs-start-latency.md`](../../../docs/log/62-ecs-start-latency.md) §62.9–62.10),
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
[`docs/log/62-ecs-start-latency.md`](../../../docs/log/62-ecs-start-latency.md) §62.7
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

## Lifecycle scripts (stand up / pause / tear down)

The runbooks in this file are the source of truth; these four are their executable
form (docs/log/73). A deployment is addressed the way `update.sh` already addresses one —
by **AWS profile** — and everything else is read from the live deployment, which
matters because two deployments built from these same templates can differ in ways a
hardcoded default gets wrong (one names its pool stack `af-ecs-pool`, another
`af-ecs-ec2-pool`).

```bash
./capture-env.sh --profile <p> --region <r>            # record a live deployment
./pause.sh       --profile <p> --region <r> [--up]     # stop / resume (data stays)
./teardown.sh    --profile <p> --region <r> [--yes]    # delete everything
./standup.sh     --profile <p> --region <r> [--yes]    # build it back
```

- **`capture-env.sh` first, always.** It writes the parameters each stack was deployed
  with to `~/.config/agent-fleet/deploy/<profile>.<region>/` (outside the repository —
  the values are account-specific). The templates live here; **what was passed to them
  lives only inside the deployment**, and `delete-stack` takes it away. `teardown.sh`
  refuses to run without it, and `standup.sh` has nothing to deploy with.
- **Nothing happens without `--yes`.** They print the plan (and, for teardown, an
  inventory of what would go) and exit. With `--yes` on a terminal they also make you
  type the FQDN.
- **Secrets are neither captured nor restored.** The SSM SecureStrings are not CFN
  parameters; `standup.sh` only checks that they *exist*. Teardown keeps `/af-cp/*` by
  default so the same account can be redeployed into.
- **Which deployment is the development one is not in this repository.** `dev-deploy.sh`
  (docs/log/73) only touches a deployment whose local capture says `AF_DEV_DEPLOY=1`.
- The order these run in is what the stub test in `deploy/local/ecs-lifecycle-stub-test.sh`
  pins — see the next section for why each step is where it is.

## Teardown

> The executable version is `./teardown.sh --profile <p> --region <r>` (above). Read this section
> anyway: the script does exactly these steps, and when one fails you are back here.

Most of what a live deployment owns is **not** in the stacks: the Control Plane
creates workspace services, EFS access points, SSM parameters and — under
`WsRuntime=ecs-ec2` — slot instances, home EBS volumes and golden snapshots at
runtime. Deleting the stacks first leaves those behind and then **stalls on the
dependencies they hold**. The order below was measured end-to-end on 2026-08-22
against two real `ecs-ec2` deployments (one `Persistence=delete`, one `=retain`)
and ended with every sweep counter at zero in both accounts.

**1. Stop the Control Plane first.** Everything in steps 2–7 is *its* bookkeeping,
and it launches slots on demand — a running CP can recreate what you just deleted.

```bash
CL=af-af-ecs-platform      # cluster = af-<platform stack name>
aws ecs update-service --cluster $CL --service af-af-ecs-ingress-cp --desired-count 0
```

**2. Workspace services** — `af-ws-*`, one per member that ever started a
workspace. `delete-service --force` also removes the service's Cloud Map entry, so
do not delete that by hand (you get `ServiceNotFound`).

```bash
for s in $(aws ecs list-services --cluster $CL --query 'serviceArns[]' --output text | tr '\t' '\n' | grep /af-ws-); do
  aws ecs update-service --cluster $CL --service "$s" --desired-count 0 >/dev/null
  aws ecs delete-service --cluster $CL --service "$s" --force >/dev/null
done
```

**3. Slot instances and home volumes** (`ecs-ec2` only). Terminating a slot takes
its root volume but **not the home volume** — homes are detached and kept on
purpose (delayed return), so they survive and keep billing.

```bash
IDS=$(aws ec2 describe-instances --filters Name=tag:Name,Values='af-slot-*' \
  Name=instance-state-name,Values=pending,running,stopping,stopped \
  --query 'Reservations[].Instances[].InstanceId' --output text)
aws ec2 terminate-instances --instance-ids $IDS && aws ec2 wait instance-terminated --instance-ids $IDS
# then every leftover af-ws-*-home volume
aws ec2 delete-volume --volume-id <vol-id>
```

**4. Container instances that stay registered.** After the EC2 instances are gone,
the cluster can still list them (3 of 4 did, in one account) and the platform
stack's cluster delete fails on that. `aws ecs deregister-container-instance
--cluster $CL --container-instance <arn> --force` each one.

**5. EFS access points** — `describe-access-points --file-system-id <efs>`, delete
each. ⚠️ **Miss these and `delete-stack af-ecs-data` stalls.** The filesystem id is
the `EfsId` output of `af-ecs-data` (*not* `FileSystemId` — a sweep script keyed on
the wrong name silently deletes nothing).

**6. SSM `/af-ws/*`** (per-workspace DEK/token). `/af-cp/*` are the out-of-band CP
secrets you created *before* `30-ingress`: keep them to redeploy into the same
account, delete them to rehearse the prerequisites from scratch.

**7. Golden snapshots** — `describe-snapshots --owner-ids self`, delete each
(50 GiB per baked home, one per image tag).

**8. The stacks, in reverse order, one at a time.**

```bash
for s in af-ecs-ingress af-ecs-pool af-ecs-platform af-ecs-data af-ecs-network; do
  aws cloudformation delete-stack --stack-name $s
  aws cloudformation wait stack-delete-complete --stack-name $s || break   # stop on the first failure
done
```

⚠️ **Issue them one at a time and wait.** CloudFormation **cancels** the delete of
an exporting stack while an importer still exists (`Cannot delete export … as it is
in use by …`) — fire all five together and the last ones silently do nothing while
your wait loop spins on a delete that was already cancelled. `af-ecs-pool` exists
only under `ecs-ec2`; its stack name is whatever you deployed `40-ec2-pool.yaml` as.
Wall clock: ~10 min (`delete` persistence) to ~16 min (`retain`, RDS snapshotting).

**9. `Persistence=retain` needs three extra steps** — a plain `delete-stack` fails
on the first one:

```bash
aws rds modify-db-instance --db-instance-identifier <db> --no-deletion-protection --apply-immediately
# after af-ecs-data is deleted:
aws rds delete-db-snapshot --db-snapshot-identifier af-ecs-data-snapshot-db-<suffix>  # DeletionPolicy: Snapshot leaves one
aws efs delete-file-system --file-system-id <efs>                                     # DeletionPolicy: Retain keeps it
```

**10. Task definitions.** They cost nothing but they outlive every stack.
⚠️ **`list-task-definitions --family-prefix af-ws` reported 0 while 9 ACTIVE
revisions existed** — enumerate without the prefix and count. Deregister every
ACTIVE revision, then `delete-task-definitions` the INACTIVE ones in batches of 10.
⚠️ `--max-items` with `--output text` appends the pagination token (`None`) as a
line of its own; pass that through to `delete-task-definitions` and the whole batch
fails. Filter with `grep '^arn:'`.

**11. The ACM validation CNAME survives the certificate.** `30-ingress` deletes the
cert and the ALB alias record, but `_<hash>.<fqdn>` stays in the hosted zone
forever. Harmless — but leave it and the next deployment's certificate validates
off a record you never created, so an issuance bug goes unnoticed. Delete it with
`change-resource-record-sets` (the DELETE needs the **exact** TTL and value; TTL is
300).

**12. What deliberately survives** anything above: the hosted zone itself, the
`/af-cp/*` secrets if you kept them, **the NAT gateway's Elastic IP** — and nothing
else. ⚠️ The EIP is new to this list: it is retained on purpose so a rebuild can
egress from the same address (see §Egress IP), which also means an unattached EIP is
now billed (~$3.6/month) until you release it. Finishing with a deployment for good
is therefore one extra deliberate step: `aws ec2 release-address --allocation-id
eipalloc-...`. Sweep to confirm the rest: EC2
instances / volumes / snapshots / AMIs / EIPs, ECS clusters + task definitions,
EFS, RDS instances + manual snapshots, ALB, ACM, NAT, ECR, Cloud Map namespaces,
`/af` log groups, `af-*` IAM roles and instance profiles, non-default VPCs.

⚠️ **ECR cannot be preserved.** `af-control-plane` and `af-workspace` are resources
of `20-platform` with `EmptyOnDelete: true` — the stack delete takes the
repositories *and every image in them*. Redeploying starts from `release-ecr.sh` /
`crane copy` again.

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
