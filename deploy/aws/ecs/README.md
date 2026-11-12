# Deploy on ECS with CloudFormation

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
| `cfn/00-network.yaml` | **proven** (deploy→verify→teardown in sandbox) | VPC, 2×AZ public+private subnets, IGW, NAT, base SGs (`alb`/`cp`/`ws`) |
| `cfn/10-data.yaml` | **proven** (EFS 2 mount targets available, RDS pg18 available/private/encrypted) | EFS filesystem + mount targets, RDS(Postgres, single-AZ t4g.micro, RDS-managed master secret), optionally the KMS key of the CP's key custodian (`CustodianKmsKey=create`) |
| `cfn/20-platform.yaml` | **proven** (ECR×2, cluster ACTIVE w/ SC default, 3 IAM roles) | ECR (cp+workspace, plus an empty `af-voicevox` for the optional speech engine and `af-llamacpp` / `af-comfyui` / `af-engine-tools` for the optional inference engines — the last two self-built, by `comfyui-image.yml` and `engine-tools-image.yml`), ECS cluster, Service Connect namespace (`af.internal`), IAM roles (`cp-task`/`exec`/`ws-task`) |
| `cfn/30-ingress.yaml` | **proven** (CP boots on Fargate, `/healthz` 200, `/oauth2/login` → Google w/ correct redirect_uri) | ACM(DNS-validated), ALB (TLS-termination only — auth is CP-native `AUTH=oauth`, no ALB OIDC), CP/Console Fargate service (Service Connect client), Route53 alias |
| `cfn/40-ec2-pool.yaml` | **proven in a sandbox** (deployed as a stack and driven end to end, in a public subnet and behind a NAT — docs/log/64 §64.16, §64.17, §64.19; never at scale) | **Optional — only for `WsRuntime=ecs-ec2`.** Launch template for a workspace *slot* (ECS-optimized AMI, cluster-join user-data, `af-mount`/`af-umount`), slot instance role + profile, slot SG. Creates **no instances**: the CP runs them on demand. One template covers both architectures — `SlotAmiIdArm64` is passed through as an ImageId override (docs/log/70 §70.8) |
| `cfn/60-engines.yaml` | **new** (ADR 0071 P0+P1, ADR 0072 P0/P1/P4/P2, ADR 0083 P2) | **Optional — only for self-hosted inference.** The fleet's own llama.cpp (`llm`) engine and an `image` role that runs this deployment's own ComfyUI, switching checkpoints per request (ADR 0072 decision 4; the earlier `sdcpp` choice, stable-diffusion.cpp with one checkpoint, was retired by ADR 0083, and the `ImageEngine` parameter that chose between them with it), on GPUs, as ECS services on the **EC2** launch type that are normally scaled to zero: one launch template per role for the box the Control Plane buys with EC2 Fleet, on Spot or on demand as the offer says (never one instance for both roles — VRAM; ADR 0075, ADR 0077), the IAM roles they need, an S3 bucket for the model catalogue, a Fargate ingest task (Hugging Face → sha256 → S3), each engine's task definition, service and Cloud Map name, one SG (8080 from the CP only), and the SSM parameter holding the engine table. Each role is created only when its `<Role>Enabled` is `true` (ADR 0072 P6). Imports 00-network and 20-platform (`af-engine-tools`, which holds the fetch sidecar and the ingest steps, plus whichever of `af-llamacpp` / `af-comfyui` it needs — all of which must already hold their image); hands 30-ingress its `EnginesSsmParam` output. ⚠️ It owns the cluster's capacity-provider associations |
| `cfn/50-tts.yaml` | **new, unproven** (ADR 0070 P0) | **Optional — only for Japanese speech.** The VOICEVOX (Zundamon) engine as a Fargate service that is normally scaled to zero, its Cloud Map DNS name, and a dedicated SG (50021 from the CP only). Imports 00-network and 20-platform (including the `af-voicevox` repository, which must already hold the image before this stack is created); hands 30-ingress its `TtsEcsService` / `VoicevoxUrl` outputs |

> The first five are proven end-to-end **including teardown**: two real deployments in two
> separate AWS accounts (`WsRuntime=ecs-ec2`, one `Persistence=delete` and one
> `=retain`) ran for days and were deleted on 2026-08-22 leaving zero orphans in
> either account — the sweep in §Teardown came back empty on every counter. Standing
> one up needs a domain + Google OAuth client + the CP image in ECR (see stand-up
> below). Each stack imports the earlier ones' exports, so deploy in order
> `00 → 10 → 20 → (40, ecs-ec2 only) → (50, speech only) → (60, self-hosted inference
> only) → 30`. `50-tts` and `60-engines` come before `30-ingress` because `30-ingress` is
> handed their outputs.

`30-ingress.yaml` carries most of the knobs a deployment actually tunes. Its
`Description:` fields are deliberately one or two lines each; the measurements, the
trade-offs and the traps behind them are in **[`cfn/PARAMETERS.md`](cfn/PARAMETERS.md)**
(see [the 51,200-byte wall](#the-51200-byte-wall-why-a-deploy-can-stop-working-without-anyone-touching-it)
for why they live outside the template).

### Prerequisites (once per account)

- **Service-linked roles.** A fresh account has no `AWSServiceRoleForECS`, and creating a
  cluster with a Service Connect default namespace fails with *"ECS Service Linked Role is
  not ready"*. A deployment that runs the inference engines (`60-engines`) should also have
  `AWSServiceRoleForEC2Fleet`, which is what AWS asks for before `CreateFleet` — though ADR
  0077's P0 run put three calls through an account that did not have it, so treat it as cheap
  insurance rather than a hard prerequisite. `standup.sh` creates both; by hand, once,
  idempotent (ignore the "has been taken" error on re-run):
  ```bash
  aws iam create-service-linked-role --aws-service-name ecs.amazonaws.com || true
  aws iam create-service-linked-role --aws-service-name ec2fleet.amazonaws.com || true
  # and, for a deployment that declares a `spot` offer:
  aws iam create-service-linked-role --aws-service-name spot.amazonaws.com || true
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
- **`10-data` needs `--capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM`** — it
  declares `Transform: AWS::LanguageExtensions`, and a template with a transform is
  rejected without the first (`Requires capabilities : [CAPABILITY_AUTO_EXPAND]`); under
  `Persistence=retain` it also creates the (unnamed) IAM role AWS Backup uses for the EFS
  backups, which needs the second:
  ```bash
  aws cloudformation deploy --stack-name af-ecs-data \
    --template-file cfn/10-data.yaml --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM \
    --parameter-overrides Persistence=retain \
    --profile af-sandbox --region ap-northeast-1
  ```
- **`20-platform` needs `--capabilities CAPABILITY_NAMED_IAM`** (it creates named IAM roles):
  ```bash
  aws cloudformation deploy --stack-name af-ecs-platform \
    --template-file cfn/20-platform.yaml --capabilities CAPABILITY_NAMED_IAM \
    --profile af-sandbox --region ap-northeast-1
  ```
  So do `40-ec2-pool`, `60-engines` and `30-ingress`: each attaches a named managed policy to
  the CP task role (`30-ingress`'s is the home-ops `RunTask` grant). Without it the change set
  is refused with `InsufficientCapabilitiesException … Requires capabilities : [CAPABILITY_IAM]`.
- **The CP task role's policy budget.** IAM caps a role's inline policies at 10,240
  characters together (whitespace not counted) and each managed policy at 6,144; how many
  managed policies a role may carry is an adjustable quota. 20-platform's inline `cp-runtime`
  is about 7,000 of those; every grant another stack adds to the role (`40-ec2-pool`'s slot
  launch, `60-engines`' ingest, `30-ingress`'s home ops) is an `AWS::IAM::ManagedPolicy` of
  its own, because inline they came to ~10,500 and `30-ingress` rolled back with
  `ServiceLimitExceeded` (#1576). Managed policy names are account-wide, so these carry the
  region (`af-<stack>-cp-ingest-<region>`, …). `deploy/local/cfn-iam-policy-size-test.py`
  renders every template, each stack name as long as its physical names allow, and fails CI
  past 9,500 inline characters per role, 5,800 per managed policy or 8 managed policies per
  role (held under the older default quota of 10); `--upgrade-from <git-ref>` replays
  `update.sh`'s order from a deployed release and prints the inline total at every step.

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
   To encrypt `master-key` with your own KMS key instead of `aws/ssm`, add `--key-id <key ARN>`
   to its `put-parameter` and set 20-platform's `MasterKeyKmsKeyArn` to the same ARN **first**
   (the execution role's `kms:Decrypt` grant must exist before the CP task starts). Order, key
   policy and back-out: `cfn/PARAMETERS.md`, "`MasterKeyKmsKeyArn`".
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
   CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text --profile <p> --region <r>)
   aws cloudformation deploy --stack-name af-ecs-ingress \
     --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn --capabilities CAPABILITY_NAMED_IAM \
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

### Preview subdomains (optional, off by default)

`30-ingress` can issue a per-start preview URL for the services a member runs inside
their workspace (docs/log/81):

```bash
--parameter-overrides PreviewDomain=pv.example.com ...
```

Every workspace start mints a random label, and the workspace's allow-listed ports
(default `3000, 8080`) become reachable at
`https://{slug}-{port}.pv.example.com/` — **at the root**, which is what a React /
Next.js / Spring Boot pair needs and what the existing `/preview/{port}` sub-path
cannot give. Empty (the default) creates none of it and leaves the sub-path preview as
the only mode.

What the parameter adds: a `*.{PreviewDomain}` ACM certificate, a
`ListenerCertificate` that hangs it on the existing 443 listener (SNI), a wildcard
Route53 alias, and `AF_PREVIEW_DOMAIN` in the CP's environment. **The existing
certificate is not touched** — adding a SAN to it would replace it, and there is no
reason to re-issue the Console's TLS to add a preview.

- ⚠️ The zone used for the preview must **contain `PreviewDomain`**; otherwise ACM's DNS
  validation never completes and the stack sits there — a stall, not a failure.
- ⚠️ Prefer a **sibling** of the Console FQDN (`pv.example.com` next to
  `af.example.com`), not a child (`*.af.example.com`): a previewed app is the member's
  own code, and as a child it could write a `.af.example.com` domain cookie. (Neither is
  airtight while both share one registrable domain — an app on `pv.example.com` can still
  write `.example.com`. Full separation needs a different registered domain.)
- **`PreviewHostedZoneId` when the sibling is not in the Console's zone.** If
  `HostedZoneId` is a *delegated* zone for the Console's own name (`af.example.com` is
  itself the zone), then `pv.example.com` lives outside it, and the sibling needs its own
  hosted zone plus an `NS` delegation from the parent — pass that zone's id here. Leave it
  empty whenever `PreviewDomain` sits inside the Console's zone (e.g.
  `pv.af.example.com`), which needs no DNS work at all:

  ```bash
  # sibling, delegated zone
  --parameter-overrides PreviewDomain=pv.example.com PreviewHostedZoneId=<pv zone id> ...
  # child of the Console's zone — nothing to delegate
  --parameter-overrides PreviewDomain=pv.af.example.com ...
  ```
- Nothing is written to DNS per workspace — one wildcard record and one certificate
  serve every workspace and port, so issuing a URL costs no API call and no
  propagation wait.
- Members choose which ports are exposed, whether the URL stays stable across starts,
  and whether it is readable without signing in (off by default, and it returns to off
  on every start).

### Keys at rest on KMS (optional, off by default)

```bash
# 10-data: make the key (it follows Persistence, like RDS)
--parameter-overrides CustodianKmsKey=create ...
# 30-ingress: hand the CP the key from 10-data's CustodianKmsKeyArn output
--parameter-overrides CustodianKmsKeyArn=arn:aws:kms:<region>:<account>:key/<key-id> ...
```

The CP's key custodian (ADR 0005) then seals with data keys from KMS instead of a key derived
from `AF_MASTER_KEY`. Nothing stored before is re-encrypted and `AF_MASTER_KEY` stays
required; KMS errors fail closed. The trade-offs are in
[`cfn/PARAMETERS.md`](cfn/PARAMETERS.md#custodiankmskeyarn) and the operator's view in
`guide/operate/12-keys.md`.

#### A key of its own for each home (optional, kms only)

```bash
# 30-ingress, together with CustodianKmsKeyArn above
--parameter-overrides CustodianKmsKeyArn=arn:aws:kms:<region>:<account>:key/<key-id> WorkspaceDekRandom=on ...
```

Sets `AF_WORKSPACE_DEK=random` on the CP task (#1646); with no `CustodianKmsKeyArn`
the template's Rule fails the stack operation when it runs and rolls it back (a change set can
still be created; the failure appears in the stack events), and the flag alone sets no env var. Check before turning it
on: kept homes keep the derived key until their first start with the flag; turning it off only
stops keys for new homes (existing ones keep theirs, no reconnecting), while going back to a CP that
does not know the home's key, or losing `home_dek`, can need credentials reconnected; unmigrated homes and old snapshots are
not shredded (#1956). The per-workspace SSM parameter `secret-key-next` sits under `/af-ws/*`,
which the CP role (`SsmWorkspaceParams`) and the workspace execution role already cover. Details:
[`cfn/PARAMETERS.md`](cfn/PARAMETERS.md#workspacedekrandom) and `guide/operate/12-keys.md`.

#### Running `af-cp` commands against the Control Plane's database

`af-cp rewrap-keys` (re-seal values stored before the KMS switch) and `af-cp home-dek-status` (count, list
or remove per-home keys) need the Control Plane's own environment: the same `AF_MASTER_KEY`,
`AF_KMS_KEY_ID`, database settings and task role. On `ecs` / `ecs-ec2`, run them as a one-off task of
the Control Plane's task definition with the command overridden, in the Control Plane's subnets and
security group. The output is in the Control Plane's log group. Start with `--dry-run`:

```bash
aws ecs run-task --cluster <cluster> --launch-type FARGATE \
  --task-definition <the Control Plane task definition> \
  --network-configuration 'awsvpcConfiguration={subnets=[<private subnet>],securityGroups=[<Control Plane security group>]}' \
  --overrides '{"containerOverrides":[{"name":"cp","command":["rewrap-keys","--dry-run"]}]}'
```

What each command does and when to run it: `guide/operate/12-keys.md`.

### Remote MCP and MCP OAuth (optional, off by default)

```bash
# 30-ingress: serve /mcp, then (optionally) let hosted MCP clients sign in with OAuth
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides McpEnabled=on McpOAuth=on
```

`McpEnabled=on` sets `AF_MCP_ENABLED=true`, `McpOAuth=on` sets `AF_MCP_OAUTH=1` (it needs
`McpEnabled=on`; the Rule fails the operation otherwise) and `McpOAuthTrustedHosts` sets the
fallback host list `AF_MCP_OAUTH_TRUSTED_HOSTS`. All three default to off / empty and then
add no environment entry, so an existing stack is unchanged until you set them. **`update.sh`
overrides only `ImageTag` (plus the facts it reads from the other stacks: `EfsBackup`,
`WorkspaceSubnets`, `EgressProxyAddr`, `EgressTokenSecretArn`, `EgressArchiveBucket`)**, every other parameter
keeps its previous value and a parameter the stack has never had takes the template default, so a release
that adds these parameters turns nothing on; run the targeted deploy above to turn them on.
Details: [`cfn/PARAMETERS.md`](cfn/PARAMETERS.md#mcp-mcp-and-mcp-oauth) and
`guide/operate/04-secure.md`, "Sign-in for remote MCP clients".

### Provided routes (optional, off by default)

```bash
# 30-ingress: Bedrock the deployment pays for (ADR 0129 Q1, acceptance only)
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides ProvidedRoutes=on
```

`ProvidedRoutes=on` does four things:
- It sets `AF_PROVIDED_ROUTES=1` on the CP.
- It creates the route role, which can only invoke Bedrock models and only for the CP task role with the member's session tags.
- It gives the CP task role the right to assume that role.
- It opens the internal path the Agent's model calls need: Service Connect `af-cp-internal:8098`, with timeouts longer than the 15-minute deadline, never the 60-second ALB.

Then:
- Register the stack output `DeploymentRouteRoleArn` with `PUT /api/admin/provided-routes/deployment`.
- Grant tenants and set budgets over REST.
- Stop → Start running Workspaces.

Off adds nothing. Details: [`cfn/PARAMETERS.md`](cfn/PARAMETERS.md#provided-routes).

### Timers, GitHub sign-in extras and LAN / borrowed engines (optional, empty by default)

```bash
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides WsIdleTimeout=4h ComfyUrl=http://10.0.0.5:8188 ComfyApiKey=on
```

Control Plane variables that the operator guide calls configurable have a parameter each:
`SessionIdleTimeout`, `InteractionIdleTimeout`, `WsIdleTimeout`, `PresenceIdleTimeout`,
`BackgroundHoldMax`, `WorkspaceStartDeadline`, `UsageSampleInterval`, `AgentSilentStopAfter`; `GithubLoginClientId`,
`GithubLabelJa`, `GithubLabelEn`, `GithubMembershipTtl`, `GithubMembershipGrace`; `LlmUrl`,
`ComfyUrl`, `RemoteEngineUrl`, `RemoteEngineKeys`. Empty (the default) adds no environment entry,
so an existing stack is unchanged and `update.sh` (which overrides only `ImageTag`,
`EfsBackup`, `WorkspaceSubnets` and the egress parameters) neither sets nor clears them. The three secrets (`AF_GITHUB_LOGIN_CLIENT_SECRET`,
`AF_COMFY_API_KEY`, `AF_REMOTE_ENGINE_TOKEN`) are read from SSM SecureString parameters
`<SsmPrefix>/github-login-client-secret`, `/comfy-api-key`, `/remote-engine-token` when their own
switch (`GithubLoginSecret`, `ComfyApiKey`, `RemoteEngineToken` = `on`) is set; create the SSM
parameter first. `WS_ENV` is deliberately not exposed. A LAN engine needs a VPC route to it.
Details and IAM: [`cfn/PARAMETERS.md`](cfn/PARAMETERS.md#control-plane-timers-github-sign-in-extras-and-lan--borrowed-engines).

### Alarms — the Control Plane cannot reach its database (set this one)

```bash
--parameter-overrides CpAlarmEmail=oncall@example.com ...
```

`30-ingress` always turns the CP's `DB_UNAVAILABLE` log line into the CloudWatch metric
`AgentFleet/<stack>/DbUnavailable`. `CpAlarmEmail` is what makes it reach a person:
empty (the default) means the metric exists and nobody is told. **Confirm the SNS
subscription email** — an unconfirmed subscription is the same as no alarm.

⚠️ **Why this is not an optional nicety.** On 2026-09-01 a production deployment answered
500 to every `GET /api/*` for fifteen minutes. RDS had rotated its managed master
password; ECS resolves a task definition's `secrets` **once, at task start**, so the
running CP was still presenting the old one. `/healthz` returned `ok` throughout — it
does not touch the database — so the ALB target was healthy, the service was at steady
state and CloudFormation showed no drift. **Every instrument agreed the deployment was
fine.** The CP now re-reads the secret when Postgres refuses it
([ADR 0065](../../../docs/decisions/0065-db-credential-rotation.md)), and this alarm is
what tells you when even that did not work.

⚠️ **This needs `20-platform` redeployed**, not just `30-ingress`: the refresh runs as
`CpTaskRole`, which gained `secretsmanager:GetSecretValue`. The execution role's existing
copy only injects the variable at start. Without the task-role statement the CP falls
back to the injected password and works perfectly — until the next rotation. Check with:

```bash
aws logs filter-log-events --log-group-name "/af/<ingress-stack>/cp" \
  --filter-pattern DB_SECRET_REFRESH_FAILED
```

Recovery of last resort, four minutes and no interruption (blue/green):

```bash
aws ecs update-service --cluster <cluster> --service <cp-service> --force-new-deployment
```

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
make the later CFN deploy fail with AlreadyExists). `update.sh` calls it *after*
deploying 20-platform for that reason.

A deployment that runs its own engines gets a third image here: `af-engine-tools`,
under the tag 60-engines asks for. It does not travel the same way — it is baked to
GHCR by `engine-tools-image.yml` and never built locally or shipped in a
distribution — so it is a registry-to-registry `crane copy` rather than a
tag-and-push. If neither ECR nor GHCR has that tag the script stops: run
`engine-tools-image.yml` with it first.

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

1. **Back up first** (production): under `Persistence=retain` RDS has daily automated
   backups, and the EFS has them **only if the data stack carries the 0.26.0 backup vault
   and plan** (step 1a checks). The newest can be almost a day old. Take an RDS snapshot
   and an on-demand EFS recovery point right before upgrading (§EFS backup and restore,
   "On demand" — it needs the vault, so do 1a first on an older deployment).
   1a. **Check the EFS backup exists** (`Persistence=retain` only; `update.sh` runs this
   too and warns):
   ```bash
   ./efs-backup-check.sh --profile <p> --region <r> --stack af-ecs-ingress   # read-only
   aws cloudformation describe-stacks --stack-name af-ecs-data \
     --query "Stacks[0].Outputs[?OutputKey=='EfsBackupVaultName'].OutputValue" --output text
   ```
   No output / `None` for `EfsBackupVaultName` means the data stack predates 0.26.0 (or
   was never updated since): **there is no EFS backup**, whatever `Persistence` says.
   `update.sh` does not touch `10-data`, so turn it on by hand, with a change set and
   not a direct deploy — §EFS backup and restore, "Turning it on for a deployment that
   already runs" (`deploy --no-execute-changeset`, `describe-change-set`, then
   `execute-change-set`). Execute only if it adds the four `EfsBackup*` resources and
   nothing else; a `Modify` or `Replacement` on `FileSystem` or `Db` (both `Retain` /
   `Snapshot` under `retain`, but a replacement is still a new, empty resource) means
   stop and do not execute. Parameters keep their stored values when none are given.
2. **Push the new tag**: `VERSION=<v> ./release-ecr.sh --profile <p> --region <r>`.
3. **Re-deploy ingress with only the tag overridden** (all other parameters keep
   their previous values):
   ```bash
   CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text --profile <p> --region <r>)
   aws cloudformation deploy --stack-name af-ecs-ingress \
     --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn --capabilities CAPABILITY_NAMED_IAM \
     --parameter-overrides ImageTag=<v> \
     --profile <p> --region <r>
   ```
4. What happens: the **CP/Console service rolls** to the new image (brief
   blue/green replacement behind the ALB). **Workspaces are not touched** — the
   adapter builds task definitions statelessly from `AF_ECS_WORKSPACE_IMAGE`, so
   each workspace picks the new image up **on its next Start**; a running workspace
   keeps its task until it is stopped and started. No EFS/RDS change; state carries over.

Rollback is the same operation with the previous tag. Convention: never re-push a
different image under an already-released version tag (the repos stay `MUTABLE`
so the `:dev` sandbox flow can overwrite itself — release tags are write-once by
discipline, not enforcement).

**Upgrading across v0.26.0.** The EFS backup vault and plan arrived in `10-data` in
v0.26.0, and no later release redeploys that stack. A `Persistence=retain` deployment
stood up before it has had no EFS backup since, and nothing else says so. Run step 1a
once after upgrading past 0.26.0; `EfsBackupVaultName` appearing in the data stack's
outputs and a daily recovery point the next morning (`aws backup list-recovery-points-by-backup-vault`)
close it.

**`00-network` and `10-data` are never part of this runbook or of `update.sh`.** A release that
needs either says so in its release notes, and the block below is the one that does.

**Clearing a parameter: do not rely on `deploy`.** In the #2763 rehearsal `aws cloudformation
deploy --parameter-overrides Key=` did not clear `Key` on a live stack ("No changes"). The cause
is not known: the CLI does send the empty value. Where a value has to become empty, `update.sh`
and `standup.sh` do it themselves and read it back; by hand, use
`./clear-params.sh --profile <p> --region <r> --stack <stack> <Key>...`: a change set on the
stack's current template that leaves the keys out, so their Default applies once it is checked
to be exactly `""` there, with every other parameter at its previous value. It is gated on that
change set: with no flag it builds it, prints the resource changes and which keys go to empty,
keeps it, prints the command that executes it and stops. Read it; if it is what you want, run
that command (`--execute <change-set-name> <Key>...`, which refuses a change set that is gone,
not `AVAILABLE`, or not exactly the clear of those keys (it deletes such a change set unless it is gone or already running), then reads each value back). A masked (`NoEcho`) parameter passes only as `UsePreviousValue`, and an SSM-typed one only if it resolves to the same value; anything else it cannot prove unchanged is refused. `--yes`
builds, prints and executes in one go; `--dry-run` builds, prints and deletes the change set. A
change set that fails to build (for example "didn't contain changes") is reported and deleted.

### Upgrading: slot AMIs follow Amazon's SSM parameters (#3318)

With `WsRuntime=ecs-ec2` the Control Plane now reads Amazon's public ECS-optimized AL2023 AMI parameters
(x86_64 and arm64) itself, about hourly, and launches every slot with the result, so slots follow
Amazon's updates **without a deploy**. `SlotAmiId`, `SlotAmiIdArm64` and `Ec2SlotAmiArm64` stay as the
initial and fallback values. A slot is outdated when its AMI differs from the current one for its
architecture, or the launch template changed in more than its ImageId; the version number alone no
longer decides. The CP role needs `ssm:GetParameter` on
`arn:aws:ssm:<region>::parameter/aws/service/ecs/optimized-ami/amazon-linux-2023/*` and
`ec2:DescribeLaunchTemplateVersions`, both added to `20-platform`: deploy it **before or with** the new CP.
Until then the CP falls back quietly (one log line) and calls no slot outdated on its AMI. The first
new AMI after the upgrade makes every slot outdated at once; they are replaced at their next Start.

### Upgrading from v0.34.x

v0.34.x's `00-network` has an S3 gateway endpoint without a policy and no workspace subnets.
This release's gives workspaces subnets of their own and the endpoint a policy that refuses
(AccessDenied, with no NAT fallback) every S3 request it does not list, a workspace's own S3
included while that workspace still runs in a private subnet (the NAT bullet under "Cost &
ephemerality" has the reasons). `update.sh` does not deploy `00-network`, so this is done by hand,
in a maintenance window in which every running workspace is restarted.

⚠️ **Do not deploy the new `00-network` with its defaults.** `S3EndpointEnabled` defaults to
`true`: the change set is *Add* of the six workspace resources plus *Modify* `S3Endpoint` (its
policy), and executing it refuses the S3 requests of every workspace still in a private subnet
at once.

1. **Back up** (step 1 of the runbook above).
2. **Deploy `00-network` with `S3EndpointEnabled=false`, through a change set.** The stack now
   needs `CAPABILITY_IAM` (it declares the egress gateway's instance role, built only with
   `EgressRoute=gateway`):
   ```bash
   aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
     --capabilities CAPABILITY_IAM --parameter-overrides S3EndpointEnabled=false \
     --no-execute-changeset --profile <p> --region <r>
   aws cloudformation describe-change-set --change-set-name <the ARN it printed> \
     --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output text
   ```
   Expect *Add* of six resources, `WorkspaceSubnet0`, `WorkspaceSubnet1`, `WorkspaceRt`,
   `WorkspaceDefaultRoute`, `WorkspaceAssoc0` and `WorkspaceAssoc1`, and *Remove* `S3Endpoint`.
   The removal is expected: until step 5 the private subnets' S3 (image pulls on ecs-ec2, model
   fetches) goes through the NAT gateway and pays its data processing. Anything else added,
   modified or replaced: stop. Otherwise `execute-change-set --change-set-name <ARN>` and
   `wait stack-update-complete --stack-name af-ecs-network`.
3. **Run `update.sh`** (`VERSION=<v> ./update.sh --profile <p> --region <r>`). It passes the
   network stack's `WorkspaceSubnets` to 30-ingress, so every Start from then on lands in a
   workspace subnet, and it ends by listing the running workspaces still in a private subnet
   (`WARNING:` when there are any, an explicit "none" when the check ran and found none).
4. **Stop and start every running workspace** (that list, or `pause.sh --status`). A running task
   keeps its ENI. Check each one:
   `aws ecs describe-tasks --cluster <c> --tasks <t> --query "tasks[].attachments[].details[?name=='subnetId'].value"`
   names a workspace subnet; `update.sh --dry-run` repeats the list and changes nothing.
5. **Deploy `00-network` with `S3EndpointEnabled=true`**, again through a change set: one *Add*,
   `S3Endpoint`. Private-subnet S3 moves from the NAT route to the endpoint and open TCP
   connections do not survive that, so do it when no large model fetch or image pull is running.

**Rollback.** Before step 5 the private subnets' S3 already goes through the NAT. After it, `S3EndpointEnabled=false`
is *Remove* `S3Endpoint` only. Taking `00-network` back to v0.34.x's template removes the six
resources and modifies `S3Endpoint` back to no policy, so first stop every workspace that runs
in a workspace subnet; then `update.sh` clears 30-ingress's `WorkspaceSubnets` (the ids are gone).
That downgrade also drops the parameters v0.34.x does not know, `GatewayEipAllocationId` and
`GatewayEipAddress` among them: the next switch to `EgressRoute=gateway` must pass both again
("Egress gateway", "Handing back a retained address").

### One command: `update.sh`

`./update.sh` is the runbook above as a script — the ECS counterpart of compose's
`docker compose pull && docker compose up -d`. Use it unless you have a reason to
drive the steps by hand.

```bash
VERSION=<v> ./update.sh --profile <p> --region <r>          # deploy an already-pushed tag
VERSION=<v> ./update.sh --profile <p> --region <r> --push   # push to ECR first
VERSION=<v> ./update.sh --profile <p> --region <r> --dry-run
```

It does the things the hand-typed sequence gets wrong:

- **Warns about a missing EFS backup** (`Persistence=retain` only): `efs-backup-check.sh`
  reads the data stack's `EfsBackupVaultName` output and the backup plan's selection
  (read-only calls) and, when the file system is not covered, prints the change-set
  command that fixes it, before the deploy and again at the end. It never blocks the
  upgrade and never updates `10-data` itself. Only a plain selection naming the file system's exact ARN counts; a read it cannot make (missing
  `backup:List*`/`Get*`, an unreadable ingress stack) or a selection with `NotResources`,
  `Conditions` or tags is reported as unconfirmed, not as protected.
- **Refuses a tag that is not in ECR.** CloudFormation only stores a string, so a
  forgotten (or wrong-region) push *succeeds* and the CP task then dies with
  `CannotPullContainerError`. The script checks both repositories first.
- **Notices the empty change set.** Re-pushing the *same* tag (the `:dev` sandbox
  flow) leaves the template byte-identical, so `cloudformation deploy` reports
  "No changes to deploy" and the CP keeps running the old image forever. The
  script falls back to `ecs update-service --force-new-deployment` in that case
  (`--force` does it unconditionally), then waits for the service to stabilise.
- **Redeploys the slot pool (`40-ec2-pool`) on ecs-ec2.** Its user data carries
  security settings, and a pool left on an old template launches every future slot
  without them. It only adds a launch template version; running and retained slots keep
  the user data they were launched with (§Moving retained slots onto new user data). On
  `WsRuntime=ecs-ec2` it **stops** when it cannot find the pool stack (the lookup goes
  through the `<stack>-SlotLaunchTemplateId` export, so it needs
  `cloudformation:ListExports`); name it with `--pool-stack <stack>`, which is checked to
  own the deployment's launch template.
- **Carries on past a failed side stack, then fails.** `50-tts` and `60-engines` are optional and
  nothing on the release route imports them (30-ingress gets their URLs as plain parameters), so
  when one of their deploys fails (and CloudFormation rolls it back) the script records it, still
  deploys 30-ingress so the CP gets the release, and ends with a `FAILED:` summary naming the
  stack and a **non-zero exit**. Fix the cause (the stack events say which resource) and re-run.
  Failures before that point (image checks, 20-platform, the slot pool) still stop the run.
- **Lists the workspaces that are still on the old image**, because nothing moves
  them automatically. It never stops one: stopping kills that user's sessions, and
  when to take that is their call.
- **Lists the running workspaces still in a private subnet** once the network stack has
  workspace subnets, as a warning when `S3EndpointEnabled` is already `true` (their own S3 is
  refused until they restart). It never deploys `00-network` itself.
- **Clears what went empty.** A value the network or data stack no longer has
  (`WorkspaceSubnets` after a downgrade, the egress gateway's after a switch back, the archive
  bucket) is cleared on 30-ingress and 20-platform with a change set and read back, because an
  empty `--parameter-overrides` value did not clear one in the #2763 rehearsal. A failed read
  of the network stack stops the run rather than clear anything.
- **Keeps the ECR repository, the image and the stack in that order.** A release can
  need a repository that did not exist and an image nothing has copied in: 0.19.0 moved
  the engine's fetch and ingest steps into `af-engine-tools`. So `update.sh` deploys
  **20-platform** first (which owns the ECR repositories), through a change set it prints
  and only executes when nothing is replaced — and only on the round where the template
  or a parameter actually moved; then carries `af-engine-tools:<EngineToolsImageTag>`
  from GHCR into ECR when it is not already there; then deploys 60-engines. Reversed,
  none of it fails at the time: the stack deploys perfectly and both roles' fetch
  containers sit in `CannotPullContainerError` while the service reports a steady state
  (measured on a real deployment, 2026-09-11). `--dry-run` prints the plan in that order.
  The one thing it cannot do is *bake* the image — nothing on a release route may — so if
  GHCR has not got the tag either it stops and says to run `engine-tools-image.yml` first.
- **Points out a golden snapshot left behind** (`ecs-ec2` only). A golden baked from
  the previous image is not used at all — the CP builds new users' homes empty
  instead (ADR 0045 decision 9), which is not a failure, just a slow first start that
  nothing but the CP log mentions. With auto-bake on (the default) the CP replaces it
  within a few minutes of the deploy, as long as two slots are free; the pool panel
  says so while it is happening. Otherwise re-bake with `bake-golden.sh`.

### A development deployment: `dev-deploy.sh`

`update.sh` deploys a **released** tag. A deployment kept for development wants the
opposite: whatever is on `main` right now, without cutting a version. That is
`dev-deploy.sh` (docs/log/73), and it ends by calling `update.sh` — the pre-flight checks
above are the reason, and writing a second copy of them for the dev path would mean the
checks are what drifts.

```bash
./dev-deploy.sh --profile <p> --region <r>            # origin/main → the dev deployment
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
- **Whether the engine tools image has to be carried over.** `af-engine-tools` holds the
  fetch sidecar and the ingest steps (ADR 0072), it is not part of `dev-image.yml`, and
  only `standup.sh` used to copy it — so a dev deploy left the engines' fetch containers
  with nothing to pull. Now: the tag the engines stack asks for is carried into ECR when
  it is not there, and a per-commit tag is baked when `deploy/aws/ecs/engine-tools/` has
  changed since the deployed commit. 🔴 It cannot set `EngineToolsImageTag` itself (it
  only runs `update.sh` on the ingress stack), so in that second case it prints the tag
  to set in `params/60-engines` — otherwise the deployment looks current and runs the
  previous release's scripts.

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

## The 51,200-byte wall (why a deploy can stop working without anyone touching it)

CloudFormation refuses a template body over **51,200 bytes**; anything larger has to be
handed over through S3. `cfn/30-ingress.yaml` crossed that line on **2026-09-01** (PR #277,
RDS managed-password rotation: 50,191 → 54,681 bytes), and at that moment **every path that
deploys it stopped working** — `standup.sh` (build it) and `update.sh` (every release,
production included), because both call the same `cloudformation deploy --template-file`.

It was found on 2026-09-02, by the first stand-up since. Three things kept it hidden:

1. **The AWS CLI refuses on file size before it calls the API.** The failure is a CLI error,
   not a CloudFormation one, so there is nothing in the stack events to find.
2. **`update.sh` looked healthy.** It only ever overrides `ImageTag`, so people think of it
   as a parameter change — but it passes the whole template on every run, so it was equally
   broken.
3. **The stand-up path had never been exercised end to end.** Creating `30-ingress` is the
   only operation that had not been run since the growth.

★ The general rule this is an instance of: **a deployment script is only verified along the
paths somebody actually runs.** Green unit tests do not touch it, and a path that is skipped
because "it obviously still works" is exactly the one that rots. That is why the fix is not
just "big templates go to S3":

- `env.sh`'s **`af_cfn_deploy` is the only entry point** to `cloudformation deploy`, and it
  **measures the file on every call**. Remembering that "30-ingress is the big one" would
  reproduce the incident on whichever template grows next.
- The staging bucket lives in **20-platform** (created before 30-ingress, outlives it;
  putting it in 30-ingress would need the bucket to deploy the stack that creates it).
  Objects expire after 7 days.
- `teardown.sh` **empties the bucket before deleting 20-platform** — CloudFormation cannot
  delete a non-empty bucket, and a teardown that dies half-way is how "the create path is
  never exercised" gets reproduced.
- `deploy/local/ecs-lifecycle-stub-test.sh` cases **3b / 3c** run both of those on every CI
  run, against a deliberately padded template. A fix nobody executes is the original bug.

### Back inside the wall (2026-09-04)

The S3 route is the safety net, not the resting state — it needs a bucket that only exists
once 20-platform is up, it constrains the teardown order, and its failures surface as CLI
errors with nothing in the stack events. So `30-ingress.yaml` was put back under the limit:
**54,681 → 40,493 bytes**, with no parameter, default or resource changed.

What moved was prose. Parameter `Description:` bodies were 21,630 bytes — **40% of the
template** — and they are the one part that does not have to reach the API to do its job;
they now live in [`cfn/PARAMETERS.md`](cfn/PARAMETERS.md), in full, with the template
keeping a line or two per parameter.

⚠️ **A YAML comment costs exactly what a `Description:` costs.** The limit is on the
template *body*, so `#` is not a cheaper place to put a paragraph — moving one there saves
zero bytes. Long explanations have to leave the file, not change syntax inside it.

`ecs-lifecycle-stub-test.sh` case **3b-2** now fails the build when any `cfn/*.yaml` exceeds
51,200 bytes. It is a hard failure on purpose: the growth in 2026-09-01 was silent for three
months, and a warning would have been too. Crossing the wall again should be a deliberate
edit to that constant, visible in the diff — while `af_cfn_deploy` keeps measuring at run
time, so a template that does cross it still deploys instead of dying.

### 30-ingress may cross the wall again (2026-11-11, #3550)

Staying inside the wall had run out of road: `30-ingress.yaml` sat 200 bytes under its
50,176-byte test budget, the prose had already left (above), and every new parameter —
a `Parameters` entry, a condition, an `Environment` line — costs 300–500 bytes, so each PR
that added one failed CI and was trimmed by hand. Only nested stacks or a second template
would make room without S3, and moving resources between stacks replaces them; the ALB and
its listeners must not be replaced, so that is out.

So the S3 route stopped being only a safety net **for this one template**: 30-ingress is
always deployed after 20-platform's bucket exists (stand-up order 00 → 10 → 20 → images →
40 → 30), which is the property the pre-bucket templates lack. What changed:

- `cfn-cp-settings-env-test.py` budget for 30-ingress: 51,200 − 1,024 → **65,536** (about
  14 KiB, thirty-odd parameters of room). Raising it again stays a deliberate edit.
- case **3b-2** exempts 30-ingress from the 51,200 limit (the others keep it);
  `cfn-template-size-test.py` keeps the pre-bucket templates 4 KiB under it.
- Every hand-typed `aws cloudformation deploy` of 30-ingress (`--template-file`) in this
  README, `PARAMETERS.md` and `release-ecr.sh`'s hint now carries
  `--s3-bucket "$CFN_BUCKET" --s3-prefix cfn` (the AWS CLI refuses a direct upload over
  51,200 bytes before calling the API), and `cfn-template-size-test.py` fails on one that
  does not. `CFN_BUCKET` is the 20-platform output `CfnTemplatesBucket`:
  `CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text)`.
- The template itself did not change: no logical ID, parameter or resource moved, so an
  existing stack updates in place.

### Round trip, measured (2026-09-02)

Teardown → stand-up had never been done end to end. It has now, on the dev deployment, at
`0.14.3-dev-ecbbcc59`:

| Step | Result |
|---|---|
| `dev-image.yml`, `image=both`, 2 architectures | ~11 min; both images are `[amd64, arm64]` indexes |
| `standup.sh` 00 → 10 → 20 → images → 40 → 30 | `30-ingress` went through S3, `CREATE_COMPLETE` |
| CP boot | `metadata store: postgres`, `/readyz` 200 |
| Agent, both architectures | golden bake booted real workspaces: `Agent healthy` 99 s (arm64) / 101 s (x86_64) |
| CP → Agent | the Cloud Map fallback fired and succeeded: `did not resolve through the Service Connect alias -> using 10.20.x.x:7700 from Cloud Map` |
| `teardown.sh` | 5 stacks gone; sweep and an independent count both show 0 stacks / 0 NAT / 0 RDS / 0 ALB / 0 EC2 / 0 EFS / 0 ECR |

⚠️ **Check `/readyz`, not `/healthz`.** `/healthz` answers 200 with a dead database — that is
how the RDS rotation outage stayed silent for so long. `/readyz` is the one that touches it.

Two rough edges found on the way, **both since closed**:

- **`standup.sh`'s preflight compares the capture against the templates** (parameters with
  no `Default` must be present). It previously checked only that the capture *files existed*,
  so a capture older than the templates would fail **after** 00–20 were already built — the
  most expensive moment to find out. It no longer counts those parameters by a fixed
  indentation either: it takes the depth of the first key under `Parameters:`, and **says so
  when it reads none at all**. A parser that quietly reports "0 required" is the same shape
  as the wall above — a check that is not running, with nothing to see.
- **`teardown.sh`'s bucket-emptying step prints why it failed.** It used to swallow stderr
  (`>/dev/null 2>&1 || echo "(skip: …)"`), so "the bucket was already gone" and "it could
  not be emptied" looked identical — and the next thing to fail is the 20-platform delete,
  by which point the reason has been thrown away.

## Egress IP (the address a customer allow-lists)

Moved to the guide: [Pin the outbound IP address](../../../guide/operate/aws-ecs/egress-ip.md).

## Egress gateway (`EgressRoute=gateway`, ADR 0122)

Moved to the guide: [Control outbound traffic with a gateway](../../../guide/operate/aws-ecs/egress-gateway.md).

## Minimal IAM (deploying principal)

What the human/CI principal running `release-ecr.sh` + `cloudformation deploy`
needs, by service (the runtime roles the *tasks* use are created by `20-platform`
and are not listed here). Scope resources by the `af-` name prefix where the
service supports it:

| Service | Why | Actions (summary) |
|---|---|---|
| cloudformation | stack CRUD | Create/Update/Delete/Describe stacks + change sets |
| ec2 | 00-network (VPC/subnets/NAT/SG/S3 gateway endpoint; the egress gateway with `EgressRoute=gateway`) | Create/Delete/Describe VPC, subnets, IGW, NAT, EIP, route tables and routes, security groups, VPC endpoints (the S3 gateway endpoint with its policy: `ec2:CreateVpcEndpoint`/`ModifyVpcEndpoint`/`DeleteVpcEndpoints`); for the gateway also network interfaces, `AssociateAddress`/`DisassociateAddress` (its Elastic IP, `DescribeAddresses` for a handed-in one), launch templates (`CreateLaunchTemplate`/`CreateLaunchTemplateVersion`/`DeleteLaunchTemplate`), and `RunInstances`/`CreateTags` (checked when the Auto Scaling group is created from the launch template) |
| autoscaling | 00-network's egress gateway (`EgressRoute=gateway`; `pause.sh` scales it through the stack) | Create/Update/Delete/Describe AutoScalingGroups, plus `iam:CreateServiceLinkedRole` for `autoscaling.amazonaws.com` once per account |
| secretsmanager | 00-network's egress agent token (`EgressRoute=gateway`) | CreateSecret/DeleteSecret/DescribeSecret, GetRandomPassword (the stack generates the value) |
| s3 | 10-data's egress event archive bucket (`EgressArchive=create`, off by default) | CreateBucket/DeleteBucket, Put/GetBucketPolicy, PutBucketEncryption, PutLifecycleConfiguration, PutBucketPublicAccessBlock, PutBucketTagging; and on 20-platform's `CfnTemplatesBucket` `s3:PutObject` / `s3:GetObject` (the CLI uploads any template over 51,200 bytes, 30-ingress included) |
| ecr | 20-platform repos + image push | CreateRepository/DeleteRepository/Describe*, GetAuthorizationToken, BatchCheckLayerAvailability, InitiateLayerUpload, UploadLayerPart, CompleteLayerUpload, PutImage |
| ecs | 20-platform cluster + 30-ingress service | Create/Delete/Describe cluster & service, RegisterTaskDefinition, plus `iam:CreateServiceLinkedRole` once per account |
| servicediscovery | Service Connect namespace | Create/Delete/Get namespace |
| efs | 10-data filesystem + mount targets | CreateFileSystem/DeleteFileSystem/CreateMountTarget/DeleteMountTarget/Describe* |
| backup | 10-data EFS backup vault + plan (`Persistence=retain`), the runbook's backup/restore, teardown | CreateBackupVault/DescribeBackupVault/DeleteBackupVault/ListBackupVaults, Create/Get/Update/Delete BackupPlan, Create/Get/Delete BackupSelection, ListBackupPlans; `backup-storage:MountCapsule` and `kms:CreateGrant`/`DescribeKey` on the default `aws/backup` key; StartBackupJob/StartRestoreJob/DescribeRestoreJob, ListRecoveryPointsByBackupVault/DeleteRecoveryPoint, and `iam:PassRole` on the 10-data backup role |
| kms | 10-data custodian key (`CustodianKmsKey=create` only) | CreateKey, PutKeyPolicy, EnableKeyRotation, DescribeKey, TagResource, CreateAlias/DeleteAlias, ScheduleKeyDeletion (stack deletion with `Persistence=delete`) |
| rds | 10-data instance | CreateDBInstance/DeleteDBInstance/CreateDBSubnetGroup/Describe* (ManageMasterUserPassword also needs `secretsmanager:*` on the RDS-managed secret + `kms:DescribeKey`) |
| elasticloadbalancing | 30-ingress ALB/TG/listeners | Create/Delete/Describe/Modify load balancers, target groups, listeners |
| acm | 30-ingress cert | RequestCertificate/DeleteCertificate/DescribeCertificate |
| route53 | DNS validation + alias | ChangeResourceRecordSets/GetHostedZone/ListResourceRecordSets (on the zone) |
| logs | log groups | CreateLogGroup/DeleteLogGroup/PutRetentionPolicy/Describe* |
| iam | 20-platform named roles, 10-data's EFS backup role, 00-network's egress gateway role and instance profile, the managed policies 30/40/60 attach to the CP task role | CreateRole/DeleteRole/Get/PassRole, Put/Delete/Attach/DetachRolePolicy, CreatePolicy/DeletePolicy/GetPolicy, Create/Delete/ListPolicyVersions, Create/Delete InstanceProfile and Add/RemoveRoleFromInstanceProfile (→ `CAPABILITY_NAMED_IAM`; `CAPABILITY_IAM` for 10-data and 00-network) |
| ssm | CP secrets (out-of-band); the egress gateway's readiness and drain checks (`pause.sh`, `standup.sh`, `EgressRoute=gateway` only) | PutParameter/DeleteParameter under `/af-cp/*`; `SendCommand` on `arn:aws:ssm:<region>::document/AWS-RunShellScript` and on instances tagged `af-egress-gateway=<network stack>` (`ssm:resourceTag/af-egress-gateway`), and `GetCommandInvocation` |
| sts | account resolution in release-ecr.sh | GetCallerIdentity |

## Workspace size (CPU / memory / working disk)

Moved to the guide: [Set the default workspace size](../../../guide/operate/aws-ecs/workspace-size.md).

## Scheduled execution (timed wakes)

Moved to the guide: [Turn on timed wakes](../../../guide/operate/aws-ecs/schedules.md).

## Optional: Japanese speech (the VOICEVOX engine, `cfn/50-tts.yaml`)

Moved to the guide: [Add Japanese read-aloud](../../../guide/operate/aws-ecs/tts.md).

## Optional: the fleet's own inference engines (`cfn/60-engines.yaml`)

Moved to the guide: [Add inference engines](../../../guide/operate/aws-ecs/engines.md).

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
   does not take out everybody (ADR 0045 decision 16). It costs slots: a home in one AZ can only
   use a slot in that AZ, so free slots elsewhere are no use to it and the pool grows
   instead of reusing. Reusing a free slot still wins over balancing. If an AZ runs out of
   the slot type, a **new** home falls back to another; an **existing** one fails, because
   it cannot move (decision 15).
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
back. **One slot serves one user at a time** (`ADR 0045` decision 8).

| | Fargate (`ecs`) | EC2 pool (`ecs-ec2`) |
|---|---|---|
| Warm Start | ~105s | **84–110s** — *not* an improvement worth switching for (docs/log/64 §64.17.5, §64.19.2) |
| Home | EFS — small files are 8–30× slower | **EBS gp3** — 2,000 small files in 0.04s vs 30.7s |
| Size | 74 discrete (cpu, memory) pairs, ≤16 vCPU / 120 GiB | instance types; the task reserves nothing and gets the instance |
| Resources per workspace | 2 (service + EFS access points) | 6 (also instance, volume, container-instance registration, task def) |
| Idle cost | EFS (what you use) | EBS (what you **provision**) + any hot slots |

**Stand-up**

```bash
aws cloudformation deploy --stack-name af-ecs-ec2-pool \
  --template-file cfn/40-ec2-pool.yaml --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides NetworkStackName=af-ecs-network PlatformStackName=af-ecs-platform
# then point the CP at it (this is the whole switch, and the whole rollback):
aws cloudformation deploy --stack-name af-ecs-ingress --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides WsRuntime=ecs-ec2 Ec2SlotLaunchTemplate=lt-0123456789abcdef0 ...
```

| Parameter | Env | Default | Notes |
|---|---|---|---|
| `WsRuntime` | `AF_RUNTIME` | `ecs` | `ecs-ec2` switches the adapter. Rolling back is this value |
| `Ec2SlotLaunchTemplate` | `AF_ECS_EC2_LAUNCH_TEMPLATE` | — | `SlotLaunchTemplateId` output of `40-ec2-pool`. The CP refuses to boot without it on this profile |
| `Ec2SlotTypes` | `AF_ECS_EC2_SLOT_TYPES` | `m7i.large:8192:2,…` | `instanceType:memoryMiB[:vcpu]`, ascending — or several named **classes**, see below. The vCPU field is optional and display-only (the Console shows which instance a memory number lands on) |
| `Ec2SlotAmiArm64` | `AF_ECS_EC2_AMI_ARM64` | `""` | `SlotAmiIdArm64` output of `40-ec2-pool`. Required only when a class declares `arm64` — the CP refuses to boot otherwise. Initial/fallback only: the CP follows Amazon's SSM parameter itself (#3318) |
| `Ec2DefaultSlotClass` | `AF_ECS_EC2_DEFAULT_SLOT_CLASS` | `""` (the first class) | Where a member with no per-user and no per-tenant choice lands |
| `Ec2MaxSlots` | `AF_ECS_EC2_MAX_SLOTS` | `8` | Hard cap **across all classes**. Start fails at the cap rather than growing the bill |
| `Ec2SlotSleepSec` | `AF_ECS_EC2_SLOT_SLEEP_SEC` | `900` | How long an instance may sit with no task before it is **stopped**. Ends the compute charge; the root volume keeps billing. `0` = never |
| `Ec2SlotTerminateAfterSec` | `AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` | `0` (off) | The next step on the same clock: past it the instance is **terminated** and its root volume goes too. `0` means instances are kept forever, so retained roots grow to `Ec2MaxSlots` — see below. `14400` (4h) recommended |
| `Ec2HomeGiB` | `AF_ECS_EC2_HOME_GB` | `50` | Per-user home volume (gp3) |
| `Ec2HibernateAfterSec` | `AF_ECS_EC2_HIBERNATE_AFTER_SEC` | `0` (off) | **Default** for how long a home may sit unopened before it is snapshotted and its volume deleted. A tenant overrides it from the Console. Nothing is destroyed — the snapshot completes before the volume goes, and the next start restores it. See below |
| `Ec2HostReserveMb` | `AF_ECS_EC2_HOST_RESERVE_MB` | `auto` | How much of a slot is held back from the workspace so the instance's own daemons cannot be starved by it. `auto` = a flat 2 GiB at every rung (an 8 GiB slot's workspace gets 6 GiB; a fifth of the rung left too little and a box thrashed). `off` = uncapped (what every deployment did before 0.12.5, and what melted one). A number = that many MiB; `0` reserves nothing and caps the workspace at the full rung (not the same as `off`, and not auto). See below |
| — | `AF_ECS_EC2_SLOT_LOST_AFTER_SEC` | `300` | How long a slot may be EC2-`running` while the cluster cannot reach its agent before the CP gives up on it and rebuilds the workspace elsewhere. `0` = wait as long as the caller allows |
| — | `AF_ECS_EC2_DARK_SLOT_ACTION` | `detect` | What the sweeper does about a slot that holds a home while its ECS and SSM agents are both silent (ADR 0121). `detect` tags the instance `af-dark-since` and writes one `pool.slot_dark_detected` audit row per episode; `off` disables the scan. `rescue` / `quarantine` are not implemented yet and run as `detect` |
| — | `AF_ECS_EC2_DARK_AFTER_SEC` | `600` | How long the slot must stay dark before it is reported. Raised to at least the boot grace and `AF_ECS_EC2_SLOT_LOST_AFTER_SEC` + `AF_ECS_EC2_SWEEP_SEC`. `0` = off |
| — | `AF_ECS_EC2_DARK_BOOT_GRACE_SEC` | `300` | A slot launched or started more recently than this is never called dark (boot and wake look dark for a while) |
| — | `AF_ECS_EC2_GOLDEN_AUTOBAKE` | `1` (on) | Keep the golden snapshot in step with the workspace image without anyone re-baking by hand (ADR 0045 decision 9-1). Set `0` and it becomes your job on every release |
| — | `AF_ECS_EC2_GOLDEN_BAKE_SEC` | `60` | How often the baker looks. It advances one step per look, so this is also how fast a bake progresses |

**What a workspace actually gets (`Ec2HostReserveMb`).** A slot's memory is not all the
workspace's: dockerd, containerd, the ECS agent, SSM and the EFS stunnel live on the same
instance, and the rung overstates what the machine has (an "8192 MiB" m7i.large reports 7784).
So the container is capped at the rung less a reserve, and the Console prints the number
the workspace can actually spend with the instance beside it — `6 GiB (of 8 GiB)`.

⚠️ **Turning it off is not free.** Uncapped, one workspace taking several GB of anonymous
memory can push the instance into refault thrash: with no swap the kernel throws away page
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

⚠️ **The cheapest instance per hour is not the cheapest instance.** m6g bills 24% less per hour
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
   This value is only the initial and fallback AMI: the CP reads the same SSM parameter itself and launches every slot with it, so patching needs no deploy (#3318).
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
(ADR 0045 decision 4).

- **It hibernates; it never destroys.** This is the only automatic path in the product that
  moves someone's home, so it stays reversible — and off by default.
- It is a **third** timer after the two above: the person goes idle → the workspace stops →
  the slot sleeps → (days later) the home becomes a snapshot. Set it in days, not minutes.
- **Per tenant, with this env as the deployment default** (ADR 0045 decision 14).
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
- **Clean home does not delete them**, and nothing else does as a side effect. A tenant
  administrator deletes a member's copies on purpose from the member detail (*Delete backups*,
  audited as `workspace.delete_backups`) — the offboarding step for a home that must not
  survive anywhere, taken after Clean home: while the home exists, the schedule keeps
  taking copies of it. Destroying the workspace deletes them too. Clean home itself deletes the
  home volume and its hibernation snapshots; the member's logins and connections are on EFS
  and stay.

**Baking the workspace image into the slot AMI: tried, measured, removed.** A slot's root
volume IS the image cache, so baking the image in does remove the pull (31.8s → **0.185s**,
measured) — and makes the slot **slower overall**, because a private AMI's root is lazily
loaded from a fresh snapshot: the instance took ~56s longer to join the cluster and a new user's
first start measured **179–192s against 144s** on the stock ECS-optimized AMI. The script
and the CP-side reporting were removed rather than left as a not-recommended option; the
measurement and the reasoning are in docs/log/64 §64.24 / ADR 0045 decision 19. **`SlotAmiId` stays
at its default** (the ECS-optimized AMI's SSM parameter; since #3318 the CP also reads that parameter
itself at launch, so slots follow it without a deploy — decision 7).

**A slot that cannot mount a home is quarantined** (`af-role=quarantined`, ADR 0045 decision 20):
it leaves the pool so nobody else lands on it, its home is detached and freed for another
slot, and the instance is stopped. It stays on the Slots tab with the reason, because it still
holds its root volume — **remove it with "Terminate" at the end of its row** once you have
taken what you need from it (ADR 0045 decision 30; nothing collects one by itself, since both
sweeper walks filter on `af-role=slot`). The failure that made this necessary was a
wedged kernel holding a deleted volume's NVMe namespace, which no amount of retrying fixes.

The button is `DELETE /api/admin/ec2-pool/slots/{id}` (super_admin), and it is the only route
in the product that deletes a machine on request. It accepts a box of this pool tagged
`af-role=quarantined` with no home attached and no live claim, and refuses everything else —
a working slot included, which leaves through `Ec2SlotTerminateAfterSec` instead. The
quarantine reason goes into the audit log before the instance (and its tags) disappear.

**A wedged slot** (running, status checks `ok`, `agentConnected=false`, SSM silent, a home still
attached) is stopped with **Stop (force)** on its row
(`POST /api/admin/ec2-pool/slots/{id}/force-stop`, super_admin, audit `pool.slot_force_stop`; #2449). It
refuses a slot whose ECS agent is connected and SSM online unless the body says
`{"override_healthy": true}`, and refuses a stopped or quarantined slot; ends the occupant's Start (the
runtime's own Stop); cancels the CP's own pending SSM commands (those commented
`agent-fleet slot volume`); and calls `StopInstances` gracefully, repeating it with `Force` after 3
minutes (only on an instance still `stopping` from the same launch) or at once on a second press while
`stopping`. Graceful first because a wedged box may still have the home mounted and Force is a power
cut. The slot is **not** quarantined and the home stays attached: the member's next Start wakes the
same stopped slot, the reboot clears the thrash, and its ECS agent reports a task that was still
STOPPING (#2447). Evidence-keeping quarantine for dark slots is #2450. Needs `ssm:ListCommands`,
`ssm:CancelCommand` and `ssm:DescribeInstanceInformation` on the CP task role (`20-platform`,
`SsmSlotForceStop`; apply that stack before using the button). A task still STOPPING on a slot whose
agent is disconnected is shown on the row. Not verified against a live AWS account.

**Golden snapshot: skip boot-install for new users.** A brand-new home pays boot-install
(4 CLIs 41s + rtk 1s + agy 6s = 48s) and a cold npm cache. Bake one home that has already
paid it and every later user starts from that copy (ADR 0045 decision 9).

**The CP does this by itself** (decision 9-1, `AF_ECS_EC2_GOLDEN_AUTOBAKE=0` to switch off).
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
  it the instance is terminated and its root volume goes with it. ⚠️ Leave it off and the
  number of retained roots only ever grows, with `Ec2MaxSlots` as its ceiling — so raising
  `Ec2MaxSlots` to serve more people also signs you up for that many root volumes,
  permanently and whether or not anyone is working (30 × 40 GiB ≈ **$115/month**). That was
  measured on a live deployment, where the only cure was terminating instances by hand
  (docs/log/64 §64.32). **14400 (4h) is the recommended value**, and it costs your users 25
  seconds: come back the same day and you wake the instance (~110s), come back tomorrow and one
  is built for you (~135s).
- **Two different idle timers, in series.** `AF_WS_IDLE_TIMEOUT` / the per-tenant
  `ws_idle_timeout` is the product's existing idle-stop: it watches the person and stops
  their *workspace* (every runtime has it). `Ec2SlotSleepSec` only starts counting after
  that, and it stops the *slot*. Someone who walks away is therefore idle-stopped on the
  tenant's timeout and their instance sleeps 15 minutes later.
- **Slots are reclaimed only at the cap.** Below `Ec2MaxSlots` a new user gets a new
  slot; at the cap the longest-dormant occupant **of the same instance type** is evicted
  (a workspace with a running task is never touched). So `Ec2MaxSlots` bounds how many
  people work *at once*, `Ec2SlotSleepSec` bounds how many instances are *running*, and
  `Ec2SlotTerminateAfterSec` bounds how many *exist* — without the third, "how many
  exist" is `Ec2MaxSlots` too.
- **At the cap, an instance of the wrong size is removed rather than handed over.** An instance
  type is not something a running instance can change, so if the cap is held entirely by sizes
  a member cannot run on, eviction has nothing to offer. Rather than fail their start, the
  CP terminates one dormant instance of a size they cannot use — an empty one first, otherwise
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
  §64.31 only *occupied* slots were ever stopped, so a released instance ran until an operator
  noticed: measured on a live deployment, three empty `m*.large` up for over 24h with zero
  tasks, at ~$95/month each.
- **No hot spare is kept, and no warm floor either.** The first person of the morning
  wakes a stopped slot (~110s) or, if the pool has none, pays the full ~135s to build one.
  Keeping an empty slot *hot* would save them ~92s (43s vs 135s) for a full instance-hour,
  every hour — `Ec2SlotSleepSec=0` buys that if you want it. Keeping stopped instances around
  instead ("keep at least N warm") buys much less than it looks: a dormant instance is only
  reusable **by its owner** (its home is still attached, so `freeSlots` never offers it to
  anyone else), and making one generic means the next user pays the wake plus the attach
  plus the mount SSM round trip — 123–143s against 135s for a fresh instance. So there is no
  floor parameter; `Ec2SlotTerminateAfterSec` alone decides how long an instance is kept.
- **AZ is destiny.** An EBS volume cannot leave its AZ, so a user is pinned to the AZ
  their home was created in. If no slot can be run there, that user cannot start.
- **A slot's root volume is shared with whoever had it before.** `/tmp` is a tmpfs
  (nothing lands on disk, capped size; mounted `exec,nosuid,nodev`, `AF_ECS_EC2_TMP_OPTS`) and the ECS task-cleanup window is shortened to
  5m, but the image cache and container write layers are genuinely shared.
- **Patching slots = updating this stack** (the AMI parameter resolves at update time)
  and letting the old slots go. That is the operational cost the EC2 launch type adds.
  The same holds for the user data ([Move slots onto new user data](../../../guide/operate/aws-ecs/slot-maintenance.md#move-slots-onto-new-user-data)).
  **Kernel patch cadence:** a slot runs the kernel of the AMI it was launched from and is
  never patched in place. Refresh the AMI (update this stack) on the cadence your security
  policy sets for the kernel, then replace the old slots as below. The task adds
  `SYS_ADMIN` (below), so a kernel fix for a namespace or netfilter bug matters here more
  than on a stricter profile.
- **Credentials still live on EFS.** The auth/identity set (`homeKeep`: `.config`,
  `.ssh`, `.git-credentials`, `.gitconfig`, `.claude`, `.claude.json`, `.codex` — under
  100 MiB) is kept on an EFS access point and symlinked into home by the entrypoint, so
  losing one single-AZ volume does not take the user's logins with it.
- **The working disk (`WsDiskGiB`) does not apply.** `AF_WS_SCRATCH` is not injected on
  this profile: home is already local EBS, so there is nothing to relocate off EFS.

### Moving retained slots onto new user data

Moved to the guide: [Replace and stop slots](../../../guide/operate/aws-ecs/slot-maintenance.md#move-slots-onto-new-user-data).

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

Moved to the guide: [Keep costs down](../../../guide/operate/aws-ecs/cost.md).

## EFS backup and restore

Moved to the guide: [Back up and restore EFS](../../../guide/operate/aws-ecs/backup.md).

## Lifecycle scripts (stand up / pause / tear down)

Moved to the guide: [Stand up, pause and tear down with the scripts](../../../guide/operate/aws-ecs/lifecycle.md).

## Teardown

Moved to the guide: [Tear a deployment down](../../../guide/operate/aws-ecs/teardown.md).

## Notes

- Deploying principal (sandbox): broad create rights for VPC/ECS/EFS/RDS/ALB/**IAM**/
  ECR/SSM are fine in a throwaway account; for least privilege see
  §Minimal IAM above.
- The Google OAuth client used for the CP-native OAuth login is throwaway — delete it after testing.
- **`AuthMode=dev`** (30-ingress parameter) no longer exists: the CP refuses `AUTH=dev`
  behind the https URL this stack sets (#3485). A sandbox stack that used it must switch to
  `AuthMode=oauth` (see PARAMETERS.md, `AuthMode`).
- Naming prefix `af-` on every resource so a sandbox shared with other work stays
  greppable and safe to sweep.
