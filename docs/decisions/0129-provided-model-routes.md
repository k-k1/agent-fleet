# 0129. Provided model routes: Bedrock from the tenant or the deployment, with member budgets and cost attribution

English | [日本語](0129-provided-model-routes.ja.md)

- Status: **proposed** (2026-11-06). Design only; nothing is built. Every `file:line` was read on `9434dae72`.
  **What is measured and what is not.** Claude Code's Bedrock wire (the variables, the Unix socket, the retry
  behaviour) was measured by ADR 0128's P2b against a stand-in gateway, never against real Bedrock. Nothing else in
  this record is measured: what Amazon Bedrock accepts, how it bills and attributes cost, and its quotas come from AWS's
  documentation as fetched on 2026-11-06 (listed under Sources). What opencode and codex send comes from their
  documentation and `--help` (opencode v2.2.5, codex-cli 0.167.0). No AWS call was made. Each statement of that kind is
  marked *(docs)* or *(unmeasured)* where it carries a decision.
- Related: [0128](0128-harness-route-maker.md) (harness × route, the A2 gateway, the switch; decisions 3, 4, 5, 7, 10,
  11, 12, 14) / [0084](0084-engine-indicator-and-tenant-gate.md) (the engine's tenant gate and its four places) /
  [0048](0048-member-cloud-cost.md) (actual AWS cost from cost allocation tags, no apportionment) /
  [0029](0029-usage-accounting.md) (the token ledger and the per-session spend budget) /
  [0069](0069-image-generation-providers.md) (credential tiers, the tenant key) /
  [0126](0126-aws-runs-in-the-mirror-and-confirm-before-use.md) (confirm before use) /
  [0106](0106-kubernetes-runtime.md) decision 8 (the CP's internal address, `AF_CP_INTERNAL_URL`)

## Context

ADR 0128 made Bedrock reachable for one kind of credential: a member's own AWS profile, marked as a model route
(decision 11, C2) and held by the Agent's route gateway (`workspace/agent/internal/routegw`, PRs #3349, #3389, #3400;
the acceptance switch #3417). The member signs in to SSO, marks a profile that carries only `bedrock:InvokeModel*`, and
picks it in the launch dialog. That is the right path for a member who owns an AWS account, and the wrong one for
everyone else.

The user wants three more things:

1. **Model access someone else provides.** (a) A **tenant connection**: an administrator registers it once, and the
   members the tenant allows use it. (b) A **deployment-provided credential**: on ECS-EC2, Fargate or EKS, the Control
   Plane's own task role or IRSA identity in the deployment's AWS account, so nobody stores a key.
2. **Budgets.** The tenant gives each member a budget (for example USD per month), enforced before each call and visible
   to the member. The motivating use: a member's subscription window runs out, and they continue briefly on the
   tenant's Bedrock.
3. **Cost per member**, both in the token ledger (ADR 0029: an estimate, in real time) and as cloud cost (ADR 0048:
   actual AWS spend from cost allocation tags, about a day late).

…for more than one harness: claude, opencode (ADR 0128's P2c) and codex, all on the same provided routes, through
ADR 0128 decision 4's allow-list.

What the tree already says, and what constrains the design:

- **The workspace has no AWS identity on purpose.** `awsx.IsolateWorkloadChain` (`workspace/agent/internal/awsx/workload.go:51`)
  removes the ECS container credential variables from the Agent's environment before it starts anything, so a build
  tool with no member credentials fails instead of acting as the workspace task; `WsTaskRole` carries no policies
  (`opencode/catalog.go:153-155`). The Control Plane passes the opt-out `AF_WS_WORKLOAD_AWS=1` only when the operator
  sets it (`control-plane/workspace_lifecycle.go:563`). A deployment-provided credential therefore cannot be "the
  workspace's role": that role is withheld from sessions for a reason.
- **Same-UID processes can act through the owner's process tree.** #3389 bound the route token to the session's
  launch-pane process tree over a Unix socket with `SO_PEERCRED`, and its review accepted what remains: another
  process of the same user can type into the owner's pane, inject where ptrace allows, or be handed a connected socket
  (ADR 0128, note of 2026-11-06, "Still possible after this"). That is acceptable when the credential is the member's
  own, because the only party who can abuse it is the member. It is not acceptable for a credential the tenant or the
  deployment pays for: the whole workspace, Agent included, runs as the member, so a budget enforced inside it is
  enforced by code the member can step around.
- **The Control Plane already proxies model traffic.** The engine gateway (ADR 0071) serves `/engine/{key}/v1/*`,
  checks a session token, the live membership and the tenant gate on every request
  (`control-plane/engine_gateway.go:472-511`), tracks the connection so a member removal cuts a running stream
  (`:497`), and posts each call's usage to the Agent's ledger (`POST /engine/usage`, `control-plane/engine_usage.go:365`,
  `workspace/agent/engines.go:1439`). Its token is the precedent not to copy as it is (30 days, not revoked;
  ADR 0128 decision 7).
- **Per-member limits today are counts, not money.** guide/admin/02-limits.md: super_admin sets tenant-wide limits
  and the engine-use switches; tenant_admin sets per-member session limits, workspace size and per-role engine access.
  The only money limit is the per-session spend budget (#1054, ADR 0029 addendum of 2026-10-04), priced from the
  session's own transcript at list price.
- **ADR 0048 does not apportion shared cost** (decision 4): a figure that is divided per head stops being actual spend.

What AWS documents *(docs, fetched 2026-11-06; not measured)*:

- **API support by model.** On Bedrock, the Anthropic Claude models support Invoke and Converse, and most of the recent
  ones the Anthropic Messages API as well; none supports Chat Completions or Responses. The OpenAI GPT models support
  Chat Completions and Responses, most of them Converse, some Invoke; `gpt-oss` supports all four, but Responses only on
  the `bedrock-mantle` endpoint, not on `bedrock-runtime`. (Bedrock "API compatibility" page.)
- **Responses on `bedrock-runtime`** is served at `https://bedrock-runtime.<region>.amazonaws.com/openai/v1`, requires a
  system inference profile id (`us.openai.…`, `global.openai.…`) for the closed-weight GPT models, stores responses for
  30 days unless `store` is false, and **rejects application inference profiles with a 400**.
- **Cost attribution mechanisms.** *IAM principal attribution* (caller identity, plus principal and session tags,
  in Cost Explorer and CUR 2.0) works on every inference API on both endpoints. *Application inference profiles* work
  with InvokeModel and Converse on `bedrock-runtime` only; Responses rejects them with a 400. (The pages disagree about
  Chat Completions: the comparison table lists it as supported, the profile page says it rejects them too.) *Projects*
  cover Responses and Chat Completions on `bedrock-mantle` only. *Per-request metadata* reaches invocation logs only.
  The cost-tracking page has a section for exactly this case: behind an LLM gateway the
  gateway's role is the caller, and "for per-user dollars … have the gateway assume its Amazon Bedrock role per user
  or tenant, using a per-user `RoleSessionName` or session tags", caching the credentials.
- **Application inference profiles** are one per model; the quota "Inference profiles per account" is 1,000 per Region,
  adjustable; and AWS's own scaling note recommends tagging at team level rather than per user, and IAM principal
  attribution for per-user cost.
- Cost allocation tags are activated from the payer account, are not retroactive, appear only after the principal has
  made a call, and take up to 24 hours to appear; cost data takes up to 24 hours as well.

## Decision

### 1. Words: credential source, and provided routes

A route (ADR 0128 decision 1) now has a **credential source**, which says who registered the credential and who pays:

| Source | Registered by | Pays |
|---|---|---|
| `member` | the member (Settings > AWS profiles/SSM, marked as a model route) | the member's AWS account |
| `tenant` | a tenant_admin (Tenant settings > Connections) | the tenant's AWS account |
| `deployment` | the operator (stack parameter), granted per tenant by a super_admin | the deployment's AWS account |

A **provided route** is one whose source is `tenant` or `deployment`. The catalogue (ADR 0128 decision 3) records one
credential tier per route id, and the ledger's rollup key carries `Route` (decision 8), so the sources become **route
ids of their own**: `bedrock` stays the member's, and `bedrock-tenant` and `bedrock-deployment` are added. That keeps
"who pays" in every rollup without a new key field, and keeps decision 3's rule that a session never moves between
connections without a visible stop.

### D1. The three credential sources

- **D1a. Store a secret for every source** (an access key or a Bedrock long-term API key in the CP's encrypted store).
  Simple, and works on any runtime. A long-term key is an IAM user's, so billing cannot tell members apart, and the CP
  becomes the custodian of a standing secret the tenant has to rotate.
- **D1b. Store no secret where an identity exists** *(recommended)*:
  - `deployment`: the CP's own identity (the ECS task role `CpTaskRole`, `deploy/aws/ecs/cfn/20-platform.yaml:213`; IRSA
    on EKS) assumes a **route role** in the deployment's account that the stack defines with
    `bedrock:InvokeModel` and `bedrock:InvokeModelWithResponseStream` only, and whose trust policy allows the CP role
    `sts:AssumeRole` and `sts:TagSession`. Nothing is stored. The operator turns it on with a stack parameter (the role
    ARN and the allowed Regions); the runtime declares whether the source exists, as it declares a cost surface in ADR
    0048 decision 9. docker, native and a Kubernetes cluster whose CP has no AWS identity have no deployment source.
  - `tenant`: a **cross-account role** in the tenant's AWS account, trusting the deployment's CP role with an
    **external id that AF generates per tenant** (AWS's standard shape for third-party access). The CP stores the role ARN,
    the Region and the external id; none is a secret. The CP role needs `sts:AssumeRole` on such roles, which widens its
    IAM; the stack limits it to a role path (for example `role/af-model-route/*`) and the operator enables the whole
    feature, so a deployment that does not want CP-side cross-account access never grants it.
  - `member`: unchanged (ADR 0128 C2): the member's marked profile, held by the Agent in the workspace.
  - A tenant whose deployment's CP has no AWS identity could register a Bedrock long-term API key instead (D1a's shape,
    tenant source only). That is open question 1, not part of the recommendation.
- **D1c. Let the workspace hold the provided credential** (the deployment role through `AF_WS_WORKLOAD_AWS=1`, or a
  tenant key in the Agent). Rejected: it undoes `IsolateWorkloadChain`'s reason and puts a credential someone else pays
  for into a container the member controls (D2).

Each source also carries a **model allow-list** set by whoever registered it (the operator for `deployment`, a
tenant_admin for `tenant`), enforced by the gateway on the model id each call names. Opus and Haiku differ in price by
an order of magnitude, and the budget alone does not say which one the tenant meant to pay for.

### D2. Where the gateway runs for each source

- **G1. The Agent's route gateway for every source** (today's `routegw`, handed the provided credential). Rejected for
  provided sources. The member can read what runs as the member: the Agent's memory and environment are the same UID's,
  and the budget check would be code inside the member's container. The #3389 limits are about the member's own
  sessions; here they would expose the tenant's or the deployment's account.
- **G2. A CP gateway the harness calls directly**, with a CP-minted bearer in the pane. Rejected: the bearer is back in
  a process environment with no caller binding (what #3389 removed), and each harness needs a second configuration
  shape (a remote HTTPS base URL instead of the socket) that differs by source.
- **G3. A CP gateway, with the Agent forwarding** *(recommended)*:

  ```
  harness ──Unix socket, route token (0128 P2b binding)──▶ Agent routegw
          ──AF_CP_INTERNAL_URL (else the public base), CP route token──▶ CP route gateway
          ──AssumeRole(source, membership) + SigV4──▶ bedrock-runtime.<region>.amazonaws.com
  ```

  - **One harness contract for all three sources.** The harness talks to the same socket with the same bearer shape; only
    the Agent's upstream differs (`member`: sign locally; `tenant`/`deployment`: forward). ADR 0128's caller binding,
    eager revoke and 10-minute renewable token are kept as they are.
  - **What the CP route token is.** Minted by the CP for the Agent, never for the session, on the Agent's own CP
    credential (the engine's `/internal/engine/token` pattern, `engine_gateway.go:227`), bound to the membership, the
    workspace, the session's name and incarnation, the route and the source; short-lived (10 minutes, renewed by the
    Agent's sweep while the session's launch pane lives, as in P2b); revoked by the Agent's `EndRouteTokens` path at every
    session exit and by the CP on the events below. The member can, in principle, extract it from the Agent. That is
    accepted, because everything that protects the payer is enforced at the CP: the source's grant, the model
    allow-list, the budget and the attribution are all checked there and filed under that member. A member who
    extracts the token can spend only their own budget, which they could spend through a session anyway.
  - **Every call, at the CP, in order:** the token; the membership is live; the tenant's grant for the source (D3); the
    member's grant and budget (D4); the model is on the source's allow-list; the source is still registered and
    unchanged. Then the call is admitted against the budget (D4), credentials are taken from the cache, the request is
    signed and the response is streamed with a flush per read, as the engine gateway already does.
  - **Revocation** is at the CP and takes effect on the next call: a tenant gate turned off, a grant or budget removed,
    a source deleted or edited. A member removal also cuts streams in flight, through the same connection tracking the
    engine gateway uses (`g.mgr.memberConns.track`, `engine_gateway.go:497`).
  - **Latency** *(unmeasured)*: one more hop than the member route (Agent → CP → Bedrock instead of Agent → Bedrock).
    Where the operator sets `AF_CP_INTERNAL_URL` (ADR 0106 decision 8), the CP passes it to every workspace start
    (`control-plane/workspace_lifecycle.go:647`) and the hop stays inside the deployment's network; otherwise it goes
    through the public base. The gate measures the added time to first token.
  - **Streaming.** The public load balancer's idle timeout is 60 seconds (`deploy/aws/ecs/cfn/30-ingress.yaml:501`). A
    stream that sends nothing for 60 seconds through it is cut. Whether Bedrock streams can be silent that long, during
    long extended thinking for example, is *unmeasured*. The Agent therefore uses the internal address wherever one is
    injected, and a deployment that only has the public path either raises that timeout or is documented as such.
  - **CP capacity** *(unmeasured)*: each in-flight call holds one goroutine and two connections in the CP; the bytes are
    tokens, so bandwidth is small. A CP restart or deploy cuts every in-flight stream; the route gateway therefore stops
    admitting calls on shutdown and lets admitted ones finish up to a deadline, and a cut stream settles at its
    reservation (D4). A per-member cap on concurrent calls (D4) also bounds the CP's load.
  - **Fargate, ECS-EC2, EKS:** the workspace needs no AWS identity at all, so the route works on every workspace runtime
    the moment the CP has one. The egress to Bedrock is the CP's, which is outside the workspace egress restriction
    (ADR 0069's tenant tier).
  - **Usage.** The CP keeps the authoritative per-member spend (D4), because the ledger lives in the member's workspace.
    The Agent's `routegw` writes the ledger row for the forwarded call, as it does for a member call, from the same
    response, and the two carry the same call id.
- **G4. A gateway service of its own** (a separate ECS service or Deployment). Deferred: it adds a deploy unit and a
  credential for the CP to hand over, for a capacity problem that has not been measured. Revisit if G3's gate shows the
  CP cannot carry the streams.
- **G5. Bedrock short-term API keys minted per member and handed to the session.** Rejected: a bearer that reaches
  Bedrock directly, valid for hours, with no budget check on its way.

### D3. Who may use what

The gates follow ADR 0084: the super_admin decides per tenant, the tenant_admin per member, the member chooses.

| Who | `deployment` | `tenant` |
|---|---|---|
| operator | enables the source for the deployment (stack parameter: role, Regions, model allow-list) | enables tenant connections for the deployment (the CP's cross-account permission) |
| super_admin | grants it per tenant, with a **monthly ceiling** for that tenant | — |
| tenant_admin | sets the per-member budget (a tenant default and per-member overrides) and "Every member" / "Granted members only", as for engine access | registers the connection and its model allow-list; then the same budget and grant as for `deployment` |
| member | picks it at launch or in the switch offer (D6) | the same |

- **The grant is a new capability that spends money, so it is off by default:** a plain `bool` with zero = false, the
  `AllowEngineIngest` precedent, not ADR 0084 decision 7's `*bool` whose nil means "allowed" (that one guards a capability
  that already existed). An upgrade enables nothing.
- **No budget, no use.** A member may use a provided route only when the tenant's grant is on and their effective budget
  is above zero. A ceiling that is empty while the grant is on is refused at save time, the same rule as ADR 0128 B3's
  cap that cannot be empty.
- **The four places of ADR 0084 decision 8 apply**, with the CP as the gate: (1) the Agent's live kind catalog lists a
  provided route only when the CP's grant answer says the member may use it, so the launch dialog never offers what will
  be refused; (2) the CP refuses to mint a CP route token; (3) the CP gateway refuses each call; (4) the Console is told on
  the events stream when a grant or a budget changes. 1 is the gate; 2 and 3 cover the window in which a stale answer is
  held.
- Grants, ceilings, budgets and connection changes write audit rows (ADR 0084 found the limits endpoint wrote none).
- ADR 0128 decision 12 already says permission is use, not ownership, for a tenant connection, and leaves open whether a
  tenant administrator may allow or deny routes (its open question 2). This answers it for provided routes: yes, through
  the grant and the budget. Routes on the member's own credential stay outside tenant control, as today.

### D4. Budget

- **Unit: USD at Bedrock's list price**, priced the way the ledger prices a `route.gateway` row (the `amazon-bedrock`
  table). It is an estimate, not the bill: the actual cost arrives a day late (D5), and a budget has to answer before
  the call. The Console writes it with "≈" as the session spend budget already does. This answers ADR 0128's open
  question 5 for provided routes: the budget counts the real-time estimate, and the actual cost is shown beside it, never
  folded into it.
- **Period: the calendar month in UTC**, the day boundary Cost Explorer reports in. The Console shows the reset in the
  viewer's local time.
- **Levels:** the tenant's monthly ceiling (super_admin, `deployment` only) and the member's monthly budget (tenant_admin).
  Both are checked; the tighter wins. The sum of member budgets may exceed the ceiling, which then acts as the pool.
- **Pre-call admission, post-call settlement** *(recommended)*. Post-call accounting alone lets every call that starts
  under budget run, so the overshoot grows with concurrency and with the size of each call. Instead:
  - At admission the CP **reserves** a conservative price for the call: the input estimated from the request's size
    (an upper bound, not a tokenizer), plus the request's own `max_tokens` (or the model's maximum when the request names
    none) at the output price. A call is admitted only while `spent + reserved < budget` at both levels.
  - At the end the reservation is replaced by the price of the usage Bedrock reported (the headers for `invoke`, the
    `metadata` event or the invocation metrics for streams, as `routegw/usage.go` reads them). A call that ends with no
    usage (cut, failed mid-stream) settles **at its reservation, never at 0**.
  - **Overshoot bound with streaming:** a call is never cut for budget reasons once admitted, because cutting a stream
    loses tokens that are billed anyway and breaks the harness's turn. So a member's final spend is at most the budget
    plus the reservation of the last call admitted (the reservation is an upper bound of that call's price, so the
    calls admitted before it cannot overshoot). A per-member cap on concurrent calls (a deployment default, *value
    unmeasured*) bounds how much is reserved at once. If a measurement shows the input estimate can fall below the real
    count, the bound gains that error and the estimate is changed.
  - A reservation can be large (a request that allows a long output on an Opus-class model). Near the end of a budget the
    member is refused before the remaining amount reaches zero; the message says so ("the next call could cost up to
    $X").
- **When it runs out:** the CP answers with a refusal of the gateway's own — **503 with `x-should-retry: false`**, the
  status P2b part 2 measured Claude Code to show once with the gateway's message instead of retrying (ADR 0128 note of
  2026-11-06) — naming the budget, the amount used, the level that ran out and the reset date. The Agent records a limit
  episode on that route (a spend limit: waiting clears it only at the period boundary), the session stops after the
  failed turn, and the member is notified. **No fallback, silent or otherwise** (ADR 0128 decision 3): the Console may
  show the switch offer (D6) with the routes still available, and nothing moves until the member chooses.
- **Visible to the member** before and during use: the launch dialog and the switch offer show the remaining budget for
  each provided route; the session's mirror shows the spend of the current period while the session is on one; the
  member's usage page has a line per provided route. A tenant_admin sees each member's spend against their budget on the
  member detail, beside the other limits.
- **The other caps stay as they are and apply independently.** The per-session spend budget (#1054) still prices the
  session's transcript and stops it after a turn at its cap; the route budget refuses calls at the CP. Whichever is hit
  first acts. The per-member session limit and the tenant's limits are counts and are unchanged. The admin guide's
  "Two kinds of limits" section gains the budget as a third kind, money per member per month.

### D5. Cost: the ledger estimate and the actual cloud cost

- **The ledger (estimate, real time).** Each forwarded call writes one `feature=route.gateway` row in the workspace's
  ledger with `Route=bedrock-tenant` or `bedrock-deployment` and `Billing=metered`, as member calls already do. The CP's
  spend table holds the same call, by the same call id, for budgets and for the admin views; the ledger is the member's
  copy, the CP's is the one that enforces.
- **The actual cost: IAM principal attribution with session tags** *(recommended)*. The CP assumes the route role once
  per (source, membership), with `RoleSessionName` = the membership id and session tags `af-membership` (the membership
  id) and `af-tenant` (the slug), and caches the credentials until five minutes before they expire (one STS call per
  member per hour, not per request, as the AWS page warns). Every call is then billed to a principal that carries the
  member's tags, so the cost is **tied directly** to the member. ADR 0048's rule is respected, not revisited: nothing is
  divided per head; a call that somehow runs without the tags falls to "shared".
  - The values are the ones ADR 0048 decision 3 already chose: the opaque membership id (never an email or its hash) and
    the slug (omitted, never empty, when unknown).
  - *(Unmeasured)* whether the existing activation of `af-membership` and `af-tenant` as resource tags also covers them as
    **IAM principal** tags (the billing console filters cost allocation tags by type "IAM principal", which suggests a
    separate entry), and whether 0048's `ce:GetCostAndUsage` grouping by tag returns principal-tag cost under the same
    key. AWS documents CUR 2.0 columns with an `iamPrincipal/` prefix and Cost Explorer grouping by principal tags. The
    phase that shows cloud cost starts by measuring this, and activates the keys as early as possible, because ADR
    0048 measured that activation has no backfill.
  - **Where it shows.** The `deployment` source's cost is in the deployment's account, where 0048's CP already reads Cost
    Explorer: the member detail gets a line "model calls on the deployment's Bedrock", beside, never inside, "cost tied
    directly to your workspace". The `tenant` source's cost is in the tenant's own account, which AF does not read: AF
    shows the ledger estimate only, and documents the tags so the tenant can group their own bill the same way.
  - **Linking the two.** For a closed day or month the Console shows the estimate and the actual side by side, labelled
    as such. Neither corrects the other: the budget keeps counting the estimate (0048 decision 2's reason for not mixing
    them).
- **Application inference profiles, evaluated.**
  - *Per member:* rejected. One profile per model per member per Region, against a quota of 1,000 per Region
    (adjustable) *(docs)*; created and deleted with memberships and with every new model; the CP needs
    `bedrock:CreateInferenceProfile`, `TagResource` and `DeleteInferenceProfile`; each harness must send the profile ARN
    as its model id; Responses rejects them, so codex could not use them at all *(docs)*; and AWS's own scaling note
    recommends principal attribution for per-user cost instead.
  - *Per tenant:* not needed for attribution, since the `af-tenant` session tag already cuts by tenant. It stays an
    option for a tenant that wants profile-level CloudWatch metrics (open question 6).
  - *Per-request metadata:* reaches invocation logs only *(docs)*; the ledger already records each call. Not used.

### H. Harnesses, wires and models on a provided route

A provided route is reached through the same harness rows as the member route (ADR 0128 decisions 4 and 5); the source
does not change what a harness speaks. Each row still ships only after decision 14's live gate.

| Harness | What it speaks to the gateway | Upstream Bedrock API | Models that make sense | Translation |
|---|---|---|---|---|
| claude | Bedrock InvokeModel (`invoke-with-response-stream`) on `ANTHROPIC_BEDROCK_BASE_URL` over `ANTHROPIC_UNIX_SOCKET`, bearer from `AWS_BEARER_TOKEN_BEDROCK` (measured against a stand-in, ADR 0128 P2b part 2) | `bedrock-runtime` invoke, as today | Anthropic models only (the CLI's own) | none |
| opencode | its own `amazon-bedrock` provider: `AWS_BEARER_TOKEN_BEDROCK` takes precedence over every AWS credential method, `endpoint` (an alias of `baseURL`) points it elsewhere, and a model id may be an inference profile ARN *(docs)*. Whether it calls Converse or Invoke, and whether it can use a Unix socket, is *unmeasured* | Converse / ConverseStream (already on the gateway's allow-list), if that is what it sends | anything Converse serves: Claude, the GPT models with Converse, `gpt-oss`, others | none |
| codex | a custom `model_providers.<id>` with `base_url` at the gateway, `wire_api = "responses"` (documented as "the only supported value"), and the bearer from `env_key` or `auth.command` (a command that prints a bearer, refreshed every 5 minutes by default) *(docs)*. Unix socket support is *unmeasured* | Responses on `bedrock-runtime` (`/openai/v1/responses`); the gateway's allow-list gains that one `POST` | the OpenAI GPT models with Responses on `bedrock-runtime` (named by system profile, `us.openai.…` / `global.openai.…`); **not Claude**, which has no Responses API on Bedrock; **not `gpt-oss`** there, whose Responses API is on `bedrock-mantle` only *(docs)* | none (below) |

- **No protocol translation in the gateway** *(recommended)*. Translating codex's Responses into Converse would make
  codex drive Claude, but the surface is large (tool calls, reasoning items, streaming event shapes, stored state),
  every codex release can move it, and codex's prompts and tools are tuned for OpenAI models; a member who wants Claude
  on Bedrock has claude and opencode. Each harness gets the models its own wire reaches, and the launch dialog offers
  only those.
- **codex's built-in `amazon-bedrock` provider** (`model_providers.amazon-bedrock.aws.profile` / `.region`, *docs*)
  resolves AWS credentials inside the session, so it is not used: on a provided route there is no AWS credential in the
  workspace to give it.
- **Responses stores state by default.** On Bedrock a response is kept for 30 days unless `store` is false *(docs)*.
  Whether codex sends `store: false` is *unmeasured*; until it is, the gateway allows only `POST /openai/v1/responses`,
  and whether the gateway should force `store: false` for provided routes is open question 7.
- **Caller binding for a harness on TCP.** If opencode or codex cannot use a Unix socket, the binding is the one ADR
  0128's P2b note names: the loopback peer resolved through `/proc/net/tcp` to a socket inode and the process holding it.
- **Managed execution.** ADR 0128 decision 5's rule stands: a shared opencode `serve` or codex app-server carries one
  credential, and its process is not in any session's launch-pane tree. A provided route under Managed needs a daemon per
  session (or a binding rooted at the daemon plus per-session attribution the daemon cannot give). Terminal comes first;
  the Managed cost is measured before it is offered.

### D6. The switch (ADR 0128 decision 10, P4) with provided routes

- **The target becomes a list.** The harness declares its switch targets (`agents.RouteSwitcher`; claude declares
  `anthropic-api` today), and the offer lists each one the member can use **now**: the member's Anthropic API key, the
  member's own marked profile (`bedrock`), and each provided route with budget left. Each entry says who pays and, for a
  provided route, the remaining budget. Nothing is preselected.
- **Switching back is new.** P4 is one-way. A session switched away from `anthropic-subscription` by this offer records
  that in its switch boundary (`Meta.RouteSwitches`). Options:
  - **R1, a manual offer** "return to the subscription" once the limit episode's reset time has passed;
  - **R2, opt-in automatic return**, chosen in the switch confirmation, run at the first idle point after the reset;
  - **R3, none**.
  - **Recommended: R1, and R2 as an opt-in that is preticked when the target is a provided route.** Returning moves
    spending off the payer who lent the route and is what the motivating use case asks for ("briefly"). R2 runs at most
    once per episode: if the first turn back hits the window again, the session stops with the offer, and it never
    alternates by itself.
- **The same transition, in both directions.** The return uses P4a's exclusive transition unchanged (the lifecycle
  gate, the step record, the booking cancellation, stop, store, resume, rollback, crash recovery); only the target is
  the subscription. The ledger files each turn under the route in force at its own timestamp (`Meta.RouteAt`).
- **Caveats the offer states:**
  - *The prompt cache does not carry over.* Bedrock and Anthropic caches are separate, so the first turn after each switch
    pays cache creation on the whole context: on the payer's route going out, and against the subscription's window
    coming back. The ledger records the real figures; the cost is stated as "may cost more", not as a number, until it is
    measured per route pair (ADR 0128 decision 10).
  - *Features differ.* The model id changes (`--model opus` became `us.anthropic.claude-opus-5-5` on Bedrock, measured in
    P2b part 2), a model may not be offered in the source's Regions or on its allow-list, and features of Anthropic's own
    API (server-side tools, beta headers, the context window offered) may differ on Bedrock *(unmeasured; the list is
    not written until measured)*.
- **codex is not offered a switch yet.** Its limit evidence is an account-wide snapshot re-sent with every event (ADR
  0128 note of 2026-11-03, P0b), so "this session's route is at its limit" cannot be proven.

### D7. Phases

Each row needs the decision 14 gate for every harness × route row it opens, on real Bedrock, plus the gate listed.

| Phase | Content | Gate |
|---|---|---|
| Q0 | Catalogue ids `bedrock-tenant` / `bedrock-deployment`; the CP's spend table and grant/budget model; no behaviour change | Existing `bedrock` rows and the rollup unchanged; a provided row and a member row of one model stay apart in the rollup |
| Q1 | CP route gateway (G3) with the `deployment` source; Agent forwarding; super_admin grant and ceiling; tenant_admin budgets; admission and settlement; session-tagged AssumeRole; claude, Terminal | A live claude turn through the CP on real Bedrock; added time to first token measured; budget 0 → visible 503 and no fallback; grant turned off → next call refused; member removed → stream cut; N concurrent calls at the budget edge stay within the stated bound; CP restart drains or fails visibly |
| Q2 | Budget visibility (launch dialog, mirror, member detail, usage page); cloud cost line from principal tags; estimate beside actual | Principal tags activated and visible in Cost Explorer (≥ 24 h after the first call); one test member's day compared, estimate against actual, with the difference stated, not tuned away |
| Q3 | D6: the target list, the return (R1, R2) | A live window stop → switch to a provided route → reset → return, automatic and manual; a budget exhausted on the borrowed route stops visibly; a forced failure on the return rolls back |
| Q4 | The `tenant` source: cross-account role with external id; Tenant settings > Connections; its model allow-list | AssumeRole into a test account; a wrong external id and a deleted role both refuse visibly; an edited connection refuses the bound session instead of moving it |
| Q5 | opencode on provided routes (after ADR 0128 P2c): its wire measured, TCP binding if needed; Terminal, then Managed | Decision 14 per execution method; the Converse usage read; a negative control per source |
| Q6 | codex on provided routes: Responses wire, OpenAI-family models only; `store` behaviour measured | Decision 14 per execution method; a Claude model id refused at launch, not on the first message |

Q1 depends on ADR 0128's claude `bedrock` row passing its own live gate, since it uses the same adapter and socket.

## Rejected options

- **The workspace's own AWS identity as the deployment source** (`AF_WS_WORKLOAD_AWS=1` plus a Bedrock policy on
  `WsTaskRole`). Every process in every workspace could call Bedrock on the deployment's account with no budget and no
  attribution beyond the workspace task; `IsolateWorkloadChain` exists to prevent exactly that.
- **A provided credential in the Agent** (G1) and **a CP bearer in the pane** (G2): D2.
- **Per-member application inference profiles** as the attribution mechanism: D5.
- **Splitting the cost of a shared role per head from the ledger**: an estimate presented as actual spend (ADR 0048
  decision 4).
- **Protocol translation in the gateway** to let codex drive Claude: H.
- **Cutting a stream when the budget runs out mid-call**: billed tokens are lost and the turn breaks; the reservation
  bounds the overshoot instead (D4).
- **Falling back to a provided route when a member's own credential fails, or to another provided route when one runs
  out**: a silent move of the bill (ADR 0128 decision 3).

## Open questions

1. Whether a tenant on a deployment whose CP has no AWS identity may register a Bedrock long-term API key instead of a
   role (D1), accepting that billing cannot tell its members apart.
2. The default per-member budget when a tenant is first granted the source: zero (nobody until a tenant_admin acts) or
   a tenant default chosen at grant time.
3. The per-member concurrent-call cap and the input estimate's ratio (D4), both to be set from Q1's measurements.
4. Whether a deployment with only the public load balancer path raises its idle timeout or does not offer provided
   routes (D2).
5. Whether the existing tag activations cover IAM principal tags, and whether Cost Explorer groups them under the same
   key (D5); measured at the start of Q2.
6. Per-tenant application inference profiles for tenants that want profile-level metrics (D5).
7. Whether the gateway forces `store: false` on Responses for provided routes (H).
8. Currencies other than USD: ADR 0048 decision 6 shows what AWS returns; budgets are in USD until a deployment needs
   otherwise.

## Consequences

- A member with no AWS account can use Bedrock that the tenant or the deployment lends, within a budget they can see;
  the member route is unchanged.
- The CP carries model traffic for provided routes, as it already does for the engine, and becomes the place where
  provided-route budgets and grants are enforced.
- No secret is stored for the deployment source or for a role-based tenant connection; the CP's IAM gains `sts:AssumeRole`
  on the route role (and, when tenant connections are enabled, on a role path in other accounts).
- Per-member cloud cost for provided Bedrock becomes actual spend, a day late, through principal tags; the token ledger
  keeps the real-time estimate; the two are shown side by side and never merged.
- codex on Bedrock is limited to OpenAI-family models; Claude on Bedrock is for claude and opencode.
- Each provided harness × route row costs one live gate per execution method, as ADR 0128 decision 14 requires.

## Sources checked (2026-11-06)

AWS documentation, Amazon Bedrock User Guide: "API compatibility"; "Responses API"; "Track usage and costs in Amazon
Bedrock"; "IAM principal attribution"; "Application inference profiles" (cost management); "Set up a model invocation
resource using inference profiles"; "Create an application inference profile". AWS General Reference: "Amazon Bedrock
endpoints and quotas" (the "Inference profiles per account" row). opencode documentation: "Providers", Amazon Bedrock
section. Codex documentation: "Configuration reference" (`model_providers`, `wire_api`, `auth`,
`model_providers.amazon-bedrock.aws`). Local CLIs: `codex --help`, `codex exec --help` (0.167.0); `opencode --version`
(v2.2.5); `claude --version` (2.1.318). None of these was exercised against AWS.
