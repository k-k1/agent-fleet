---
audience: "Deployment administrators on AWS ECS (`ecs` / `ecs-ec2`) who want a feature that is off by default"
source_of_truth: "`deploy/aws/ecs/cfn/30-ingress.yaml` and `10-data.yaml` for the parameters, `deploy/aws/ecs/cfn/PARAMETERS.md` for the details"
---

# Turn on optional features with stack parameters

English | [日本語](options.ja.md)

Turn on, through stack parameters, the features an ECS deployment leaves off by default: preview subdomains, keys at rest on KMS, remote MCP, provided routes, timers and outside engines, and WAF.
Left at its default, every parameter here adds nothing and leaves an existing stack unchanged.

## How it works

| What you do | What happens |
|---|---|
| Deploy `30-ingress` naming only that parameter | Only the parameters you name change; every other one keeps its previous value. The Control Plane's service is replaced |
| Move to a new release with `update.sh` | It does not touch these parameters. What you turned on stays on, what is off stays off |

| Parameter | Stack | What it turns on |
|---|---|---|
| `PreviewDomain` | `30-ingress` | Serve the services in a workspace at the root of a per-start subdomain |
| `CustodianKmsKey` / `CustodianKmsKeyArn` | `10-data` / `30-ingress` | Protect the secrets the Control Plane stores with a KMS key |
| `WorkspaceDekRandom` | `30-ingress` | Give each home a key of its own (KMS only) |
| `McpEnabled` / `McpOAuth` | `30-ingress` | Serve `/mcp` and let outside clients sign in with OAuth |
| `ProvidedRoutes` | `30-ingress` | Let members use Bedrock the deployment pays for, within budgets (acceptance only) |
| Timers, GitHub sign-in, outside engines | `30-ingress` | The Control Plane's timers, extra settings for GitHub sign-in, engines on your LAN or in another deployment |
| `WafRateLimitPer5Min` / `WafIpReputation` | `30-ingress` | Put AWS WAF in front of the ALB |

## Change a parameter

`update.sh` overrides only `ImageTag` and the facts it reads from the other stacks (`EfsBackup`, `WorkspaceSubnets`, `EgressProxyAddr`, `EgressTokenSecretArn`, `EgressArchiveBucket`).

- Every other parameter keeps its previous value
- A parameter the stack has never had takes the template default. A release that adds a parameter turns nothing on

So turn these features on with a targeted deploy of `30-ingress`.

```bash
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides McpEnabled=on McpOAuth=on
```

- `CFN_BUCKET` is `20-platform`'s output `CfnTemplatesBucket` ([Build on ECS](install.md#3-deploy-30-ingress))
- Name only the parameters you want to change in `--parameter-overrides`
- `--parameter-overrides Key=` does not make a value empty again. Use `clear-params.sh` ([Clear a parameter](upgrade.md#clear-a-parameter))

## Use preview subdomains

Serve the services a member runs in their workspace at a per-start URL.

```bash
--parameter-overrides PreviewDomain=pv.example.com ...
```

- Every start mints a random label, and the allow-listed ports (default `3000, 8080`) become reachable at `https://{slug}-{port}.pv.example.com/`
- **At the root**, so apps that do not work under the `/preview/{port}` sub-path, such as a React, Next.js or Spring Boot pair, can be previewed
- Empty (the default) creates none of it, and the sub-path preview is the only mode

| What the parameter adds | Details |
|---|---|
| ACM certificate | `*.{PreviewDomain}` |
| `ListenerCertificate` | Attaches that certificate to the existing 443 listener (SNI) |
| Route 53 | One wildcard alias |
| Control Plane environment | The preview domain |

The existing certificate is not touched. Adding a SAN to it would replace it, so the preview gets a certificate of its own.
Nothing is written to DNS per workspace. One wildcard record and one certificate serve every workspace and port, so issuing a URL costs no API call and no propagation wait.

### Choose the domain and the zone

| Where it sits | Example | `PreviewHostedZoneId` |
|---|---|---|
| Inside the Console's zone | `pv.af.example.com` | Leave it empty. No DNS work |
| A sibling of the Console, in the same zone | `pv.example.com` (zone `example.com`) | Leave it empty |
| A sibling of the Console, whose zone is delegated for the Console's name alone | `pv.example.com` (zone `af.example.com`) | Create a separate hosted zone for the sibling, delegate it from the parent with `NS`, and pass that zone's id |

```bash
# sibling, delegated zone
--parameter-overrides PreviewDomain=pv.example.com PreviewHostedZoneId=<pv zone id> ...
# child of the Console's zone — nothing to delegate
--parameter-overrides PreviewDomain=pv.af.example.com ...
```

- The zone you use must **contain** `PreviewDomain`. Otherwise ACM's DNS validation never completes and the stack sits there: a stall, not a failure
- Prefer a **sibling** of the Console FQDN (`pv.example.com` next to `af.example.com`) over a child (`*.af.example.com`). A previewed app is the member's own code, and as a child it could write a `.af.example.com` cookie
- Even a sibling can write `.example.com` while both share one registrable domain. Full separation needs a different registered domain ([Other operational controls](../secure/controls.md#put-the-preview-domain-on-a-separate-domain))

Members choose which ports are exposed, whether the URL stays the same across starts, and whether it is readable without signing in (off by default, and back to off on every start).

## Keep keys at rest on KMS

Protect the secrets the Control Plane seals with a KMS key instead of a key derived from `AF_MASTER_KEY`. What changes and what to watch is in [Put the keys at rest on AWS KMS](../keys/kms.md); this section has the parameters only.

```bash
# 10-data: make the key (it follows Persistence, like RDS)
--parameter-overrides CustodianKmsKey=create ...
# 30-ingress: hand the CP the key from 10-data's CustodianKmsKeyArn output
--parameter-overrides CustodianKmsKeyArn=arn:aws:kms:<region>:<account>:key/<key-id> ...
```

1. Deploy `10-data` with `CustodianKmsKey=create`. The key follows `Persistence`, like RDS
2. Pass `10-data`'s output `CustodianKmsKeyArn` to `30-ingress`'s `CustodianKmsKeyArn`

### Give each home its own key

```bash
# 30-ingress, together with CustodianKmsKeyArn above
--parameter-overrides CustodianKmsKeyArn=arn:aws:kms:<region>:<account>:key/<key-id> WorkspaceDekRandom=on ...
```

- `WorkspaceDekRandom=on` without `CustodianKmsKeyArn` makes the template's Rule fail the stack operation and roll it back. A change set can still be created; the failure shows in the stack events
- What to check before turning it on, and what happens when you turn it off, is in [Give each home its own key](../keys/home-keys.md)
- The per-workspace SSM parameter `secret-key-next` sits under `/af-ws/*`, which the Control Plane's role and the workspace execution role already cover

### Run `af-cp` commands against the Control Plane's database

`af-cp rewrap-keys` (re-seal values stored before the KMS switch) and `af-cp home-dek-status` (count, list or remove per-home keys) run in the Control Plane's own environment: the same `AF_MASTER_KEY`, `AF_KMS_KEY_ID`, database settings and task role.

On ECS, run them as a one-off task of the Control Plane's task definition with the command overridden, in the Control Plane's subnets and security group. Start with `--dry-run`.

```bash
aws ecs run-task --cluster <cluster> --launch-type FARGATE \
  --task-definition <the Control Plane task definition> \
  --network-configuration 'awsvpcConfiguration={subnets=[<private subnet>],securityGroups=[<Control Plane security group>]}' \
  --overrides '{"containerOverrides":[{"name":"cp","command":["rewrap-keys","--dry-run"]}]}'
```

The output is in the Control Plane's log group. What each command does and when to run it is in [Put the keys at rest on AWS KMS](../keys/kms.md#re-seal-values-stored-before-the-switch) and [Give each home its own key](../keys/home-keys.md).

## Serve remote MCP

```bash
# 30-ingress: serve /mcp, then (optionally) let hosted MCP clients sign in with OAuth
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides McpEnabled=on McpOAuth=on
```

| Parameter | Details |
|---|---|
| `McpEnabled=on` | Serve `/mcp` |
| `McpOAuth=on` | Let hosted MCP clients sign in with OAuth. Needs `McpEnabled=on`; otherwise the template's Rule fails the operation |
| `McpOAuthTrustedHosts` | The fallback list of trusted client hosts (comma-separated). Empty means the Control Plane's default |

All three default to off (empty). What to expose and which hosts to trust is in [Operate MCP and external-connection sign-in](../secure/mcp.md).

## Use provided routes

Let members use Bedrock the deployment pays for, within budgets the administrators set. This is the switch for the acceptance run.

```bash
# 30-ingress: Bedrock the deployment pays for (acceptance only)
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides ProvidedRoutes=on
```

`ProvidedRoutes=on` does four things.

| What it does | Details |
|---|---|
| The Control Plane's switch | Turns provided routes on |
| Creates the route role | It can only invoke Bedrock models, and only the Control Plane's task role with the member's session tags can assume it |
| Grants the Control Plane | The task role may assume that role |
| Opens the internal path | Service Connect `af-cp-internal:8098`, which the Agent's model calls take, with timeouts longer than the 15-minute deadline, never the 60-second ALB |

Once it is on:

1. Register the stack output `DeploymentRouteRoleArn` with `PUT /api/admin/provided-routes/deployment`
2. Grant tenants and set budgets over REST. Until a tenant is granted with a ceiling and its members have a budget, nobody can use the route
3. Stop and start the running workspaces. They keep the environment they started with, so they cannot use the route until then

Off adds nothing. How to size budgets and the registration body are in [`cfn/PARAMETERS.md`, "Provided routes"](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md#provided-routes).

## Set timers, GitHub sign-in and outside engines

```bash
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides WsIdleTimeout=4h ComfyUrl=http://10.0.0.5:8188 ComfyApiKey=on
```

Each Control Plane variable the operator guide calls configurable has a parameter. Empty (the default) adds no environment entry, and `update.sh` neither sets nor clears them.

| Kind | Parameters |
|---|---|
| Timers | `SessionIdleTimeout`, `InteractionIdleTimeout`, `WsIdleTimeout`, `PresenceIdleTimeout`, `BackgroundHoldMax`, `WorkspaceStartDeadline`, `UsageSampleInterval`, `AgentSilentStopAfter` |
| GitHub sign-in | `GithubLoginClientId`, `GithubLabelJa`, `GithubLabelEn`, `GithubMembershipTtl`, `GithubMembershipGrace` |
| Outside engines | `LlmUrl`, `ComfyUrl`, `RemoteEngineUrl`, `RemoteEngineKeys` |

What the timers mean and their defaults are in [Use idle stop and force-stop](../run/idle-stop.md) and [Limits and defaults](../../ref/limits.md).

### Secrets are read behind a switch

The three secrets are read from SSM SecureString parameters only when their own switch is `on`. **Create the SSM parameter first**, then turn the switch on: a switch pointing at a parameter that does not exist stops the task from starting.

| Switch (default `off`) | SSM parameter read |
|---|---|
| `GithubLoginSecret` | `<SsmPrefix>/github-login-client-secret` |
| `ComfyApiKey` | `<SsmPrefix>/comfy-api-key` |
| `RemoteEngineToken` | `<SsmPrefix>/remote-engine-token` |

- `SsmPrefix` defaults to `/af-cp`, which the execution role already reads. If you move it outside `/af-cp/` and `/af-ws/`, widen the execution role's `ssm-read` policy to that prefix
- An engine on your LAN needs a route from the VPC (VPN, peering, Transit Gateway). The stacks do not create one
- `WS_ENV` is deliberately not exposed, so that no secret ends up in a task definition in plain text

Details are in [`cfn/PARAMETERS.md`, "Control Plane timers, GitHub sign-in extras and LAN / borrowed engines"](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md#control-plane-timers-github-sign-in-extras-and-lan--borrowed-engines).

## Put WAF in front

`30-ingress` can put an AWS WAF web ACL in front of the ALB. Two knobs, and only two on purpose.

```bash
--parameter-overrides WafRateLimitPer5Min=3000 WafIpReputation=on ...
```

| Parameter | Details |
|---|---|
| `WafRateLimitPer5Min` | Requests per 5 minutes from one IP before WAF blocks it. `0` (the default) creates no web ACL. It counts every request including Console polling, so keep it far above what a whole office behind one NAT produces |
| `WafIpReputation` | AWS's managed IP reputation list. It matches on the source, not on the body. Usable only when `WafRateLimitPer5Min` is above 0 |

- With the limit at 100, a 150-request burst got `403` within about 45 seconds. Raising the limit releases the IP on the next evaluation
- Within minutes of being switched on, the IP reputation list blocked the scanners probing `/.env` and the like

### Signature rule sets are not offered

The Core rule set, SQLi, XSS and LFI rule sets are deliberately not offered.

- This product carries source code and shell commands in ordinary request bodies: chat messages, file writes, terminal input. `'; DROP TABLE`, `../../etc/passwd` and `<script>` are legitimate traffic here
- Those rules would 403 real work at random, and it would look like a product bug long before anyone suspects the WAF
- WAF inspects only the first 8 KB of a body by default, so a signature set buys less than it appears to

For a private deployment the cheaper and stronger control is upstream: set `00-network`'s `AlbIngressCidr` to your own range and the traffic never reaches the ALB.

WAF costs about $5 a month for the web ACL, $1 a month per rule, and $0.60 per million requests.

## Related

- [Build on ECS](install.md)
- [Set up the alarm for an unreachable database](alarms.md)
- [Upgrade on ECS](upgrade.md)
- [`cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md)
