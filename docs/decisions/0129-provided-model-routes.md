# 0129. Provided model routes: Bedrock from the tenant or the deployment, with member budgets and cost attribution

English | [日本語](0129-provided-model-routes.ja.md)

- Status: **proposed** (2026-11-06). Design only; nothing is built. Every `file:line` was read on `9434dae72`.
  **What is measured and what is not.** Claude Code's Bedrock wire (the variables, the Unix socket, the retry
  behaviour) was measured by ADR 0128's P2b against a stand-in gateway, never against real Bedrock. Nothing else in
  this record is measured: what Amazon Bedrock accepts, how it bills and attributes cost, and its quotas come from AWS's
  documentation as fetched on 2026-11-06 (listed under Sources). What opencode and codex send comes from their
  documentation and `--help` (opencode v2.2.5, codex-cli 0.167.0). No AWS call was made. Each statement of that kind is
  marked *(docs)* or *(unmeasured)* where it carries a decision. Reviewed by a separate session (codex `gpt-6.1-sol`);
  round 1's ten findings were checked against the tree and the cited pages and are folded into this text.
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

What AWS documents *(docs, fetched 2026-11-06; not measured)*, linked at each claim:

- **API support by model.** On Bedrock, the Anthropic Claude models support Invoke and Converse, and most of the recent
  ones the Anthropic Messages API as well; none supports Chat Completions or Responses. The OpenAI GPT models support
  Chat Completions and Responses, most of them Converse, some Invoke; `gpt-oss` supports all four, but Responses only on
  the `bedrock-mantle` endpoint, not on `bedrock-runtime` ([API compatibility](https://docs.aws.amazon.com/bedrock/latest/userguide/models-api-compatibility.html)).
- **Responses on `bedrock-runtime`** ([Responses API](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-responses-api.html)) is served at `https://bedrock-runtime.<region>.amazonaws.com/openai/v1`, requires a
  system inference profile id (`us.openai.…`, `global.openai.…`) for the closed-weight GPT models, stores responses for
  30 days unless `store` is false, and **rejects application inference profiles with a 400**.
- **Cost attribution mechanisms** ([Track usage and costs](https://docs.aws.amazon.com/bedrock/latest/userguide/cost-management.html)). *IAM principal attribution*
  ([page](https://docs.aws.amazon.com/bedrock/latest/userguide/cost-mgmt-iam-principal-tracking.html); caller identity, plus principal and session tags,
  in Cost Explorer and CUR 2.0) works on every inference API on both endpoints. *Application inference profiles* work
  with InvokeModel and Converse on `bedrock-runtime` only; Responses rejects them with a 400. (The pages disagree about
  Chat Completions: the comparison table lists it as supported, the profile page says it rejects them too.) *Projects*
  cover Responses and Chat Completions on `bedrock-mantle` only. *Per-request metadata* reaches invocation logs only.
  The cost-tracking page has a section for exactly this case: behind an LLM gateway the
  gateway's role is the caller, and "for per-user dollars … have the gateway assume its Amazon Bedrock role per user
  or tenant, using a per-user `RoleSessionName` or session tags", caching the credentials.
- **Application inference profiles** are one per model
  ([cost management](https://docs.aws.amazon.com/bedrock/latest/userguide/cost-mgmt-application-inference-profiles.html),
  [creating one](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-profiles-create.html)); the quota "Inference profiles per account" is 1,000 per Region,
  adjustable ([endpoints and quotas](https://docs.aws.amazon.com/general/latest/gr/bedrock.html)); and AWS's own scaling note recommends tagging at team level rather than per user, and IAM principal
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

**A provided source is bound for life, as #3400 bound the member's profile.** Every source registration has a
persistent id and a **payer fingerprint** over what decides who pays and where the calls go: the role ARN, the
external id's version, and the Region set. A create binds the session's selection to `<source id>#<fingerprint>`
(the shape of `routes.BindAWSConnection`); the CP route token and the CP's credential cache key both carry it. An edit
that changes the fingerprint refuses every bound session at its next mint and its next call
(`route_connection_changed`) instead of moving its bill on a resume, fork, recreate or Agent restart, and drops the
cached credentials of the old fingerprint at the edit. A change of the model allow-list or the budget does not change
who pays, so it applies to bound sessions at their next call without changing the fingerprint.

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
    **external id that the CP generates per tenant** (AWS's
    [third-party access](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_common-scenarios_third-party.html)
    shape). The CP stores the role ARN, the Region and the external id; none is a secret. The CP role needs
    `sts:AssumeRole` on such roles, which widens its IAM; the stack limits it to a role path (for example
    `role/af-model-route/*`) and the operator enables the whole feature, so a deployment that does not want CP-side
    cross-account access never grants it.
    - **Generating an id does not prove the role requires it.** Tenant A could otherwise register tenant B's role, if
      B trusts the CP role without a condition, and make the CP spend B's money. So a connection is **activated only
      after three probes by the CP**: AssumeRole succeeds with this tenant's id, and fails both with no id and with a
      wrong id (a random one of the same shape, which also stands in for another tenant's). A connection that fails any
      probe stays inactive, with the failed probe named. The same probes run again whenever the ARN changes.
    - The external id is the CP's: no API, Console form or import accepts one from a caller. A tenant_admin can only ask
      the CP to rotate it, which issues a new id, bumps its version (and so the fingerprint, decision 1), and leaves the
      connection inactive until the probes pass against the role's updated trust policy.
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
    credential (the engine's `/internal/engine/token` pattern, `engine_gateway.go:227`), signed by the CP and bound to
    the membership, the session's name and incarnation, the route and the bound source (`<source id>#<fingerprint>`,
    decision 1); short-lived (10 minutes, renewed by the Agent's sweep while the session's launch pane lives, as in P2b).
    The member can, in principle, extract it, or the Agent's issuer credential, and call the CP directly. That is
    accepted only because **the CP is the boundary and trusts nothing the caller says** (next bullet); Agent-side checks
    protect nothing against such a caller.
  - **The CP's signing contract.** The CP gateway is a gateway for a few model operations, never a generic SigV4 or STS
    deputy; the tenant's role may be broader than it should be, and the CP's own rules are what narrow it.
    - **Identity comes from the CP's own records.** The membership comes from the verified token; the tenant and the
      workspace from the CP database for that membership; the budget keys, the `RoleSessionName` and the session tags from
      those. No header, query parameter or body field the caller sends is read as identity.
    - **The CP builds the upstream request.** The host and Region come from the source registration; the role ARN and the
      external id from the CP database. A caller-supplied URL, Region, role, external id or tag is refused, not ignored.
      Of the caller's headers only `content-type`, `accept` and a named list of Bedrock request headers pass (the list
      is written per operation when it is built); the caller's `Authorization` never does.
    - **Operations and models are checked at the CP, not only at the Agent.** Only `POST` on
      `/model/<id>/{invoke,invoke-with-response-stream,converse,converse-stream}` and, from Q7, `/openai/v1/responses`.
      The model id is taken from the path (URL-decoded once) or, for Responses, from the body, and must equal an entry of
      the source's model allow-list verbatim. Allow-list entries are foundation-model and system inference profile ids
      (`us.anthropic.…`, `global.openai.…`); any ARN is refused, so an application inference profile in some other
      account or a model outside the list cannot be reached by naming its ARN.
    - **A session policy narrows every assumed session.** AssumeRole passes an inline session policy that allows only
      `bedrock:InvokeModel` and `bedrock:InvokeModelWithResponseStream` on the allow-listed models' resources, so even a
      broad tenant role yields credentials that can do nothing else (the effective permission is the intersection).
    - **Tags attribute; they never authorize.** The session tags exist for billing (D5); no AF decision reads them back.
    - **The CP never returns credentials** or any upstream header that carries them, and logs none.
  - **Every call, at the CP, in order:** the token's signature and expiry; the membership is live; the tenant's grant
    for the source (D3); the member's grant; the bound source still exists with the same fingerprint and is active; the
    operation and model pass the contract above; then budget admission (D4). Only then are credentials taken from the
    cache (keyed by source id, fingerprint and membership), the request signed, and the response streamed with a flush
    per read, as the engine gateway already does.
  - **Two kinds of revocation.** *Trusted* revocation is the CP's own and holds at the next call whatever the Agent does:
    a tenant gate turned off, a grant or budget removed, a source deleted, deactivated or re-fingerprinted, a membership
    removed (which also cuts streams in flight, through the connection tracking the engine gateway uses,
    `g.mgr.memberConns.track`, `engine_gateway.go:497`). *Lifecycle* revocation (a session ended, its incarnation changed)
    is requested by the Agent, which the member controls: the session name and incarnation in the token are the Agent's
    assertions, as they are in the engine precedent, where the issuer proves the membership and not the session. So
    per-session attribution and the isolation of one member's sessions from each other are best effort, as in ADR 0128;
    **the CP boundary is the member's grant and budget, nothing finer.**
  - **Latency** *(unmeasured)*: one more hop than the member route (Agent → CP → Bedrock instead of Agent → Bedrock).
    Where the operator sets `AF_CP_INTERNAL_URL` (ADR 0106 decision 8), the CP passes it to every workspace start
    (`control-plane/workspace_lifecycle.go:647`) and the hop stays inside the deployment's network; otherwise it goes
    through the public base. The gate measures the added time to first token.
  - **Streaming.** The public load balancer's idle timeout is 60 seconds (`deploy/aws/ecs/cfn/30-ingress.yaml:501`). A
    stream that sends nothing for 60 seconds through it is cut. Whether Bedrock streams can be silent that long, during
    long extended thinking for example, is *unmeasured*. The Agent therefore uses the internal address wherever one is
    injected, and **a deployment whose only path to the CP is the public load balancer does not offer provided routes**
    until its own acceptance run has held a silent stream past that timeout (by raising it, or by measuring that
    Bedrock streams are never silent that long); it is not left to documentation.
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
| super_admin | grants it per tenant, with a **monthly ceiling** for that tenant | grants **"may register a tenant connection"** per tenant; sets no ceiling, because the tenant's own account pays |
| tenant_admin | sets the per-member budget (a tenant default and per-member overrides) and "Every member" / "Granted members only", as for engine access | registers and activates the connection (D1's probes) and its model allow-list; may set a tenant-wide monthly ceiling of its own (empty = none); then the same budgets and grant as for `deployment` |
| member | picks it at launch or in the switch offer (D6) | the same |

- **Each grant is a new capability, so it is off by default:** a plain `bool` with zero = false, the
  `AllowEngineIngest` precedent, not ADR 0084 decision 7's `*bool` whose nil means "allowed" (that one guards a capability
  that already existed). An upgrade enables nothing. The tenant source has a super_admin grant even though the tenant
  pays, because the CP assumes roles in another account on the tenant's behalf and carries its traffic.
- **No budget, no use.** A member may use a provided route only when the super_admin's grant for that source is on, the
  source is active, and the member's effective budget is above zero. A `deployment` ceiling that is empty while the
  grant is on is refused at save time, the same rule as ADR 0128 B3's cap that cannot be empty.
- **ADR 0128 decision 12 still comes first.** On every create path (the Console, REST, MCP `create_session`, and
  schedules for a new, a reused and a rotated session) the CP refuses a selection naming a provided route the member
  cannot use — grant off, source missing, inactive or re-fingerprinted, budget zero — **before waking the Workspace**,
  as it already refuses an unknown route. The checks below are later backstops, not replacements.
- **The four places of ADR 0084 decision 8 apply**, with the CP as the gate: (1) the Agent's live kind catalog lists a
  provided route only when the CP's grant answer says the member may use it, so the launch dialog never offers what will
  be refused; (2) the CP refuses to mint a CP route token; (3) the CP gateway refuses each call; (4) the Console is told on
  the events stream when a grant or a budget changes. 1 is the gate (with the pre-wake refusal above); 2 and 3 cover the
  window in which a stale answer is held.
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
  viewer's local time. **A call belongs to the month it was admitted in**: the admission month and the price table's
  version are frozen on the call's reservation, and a settlement that arrives after midnight on the 1st goes to that
  original month, never to the new month's bucket.
- **Levels:** the tenant's monthly ceiling (super_admin for `deployment`; the tenant_admin's optional one for `tenant`)
  and the member's monthly budget (tenant_admin). Both are checked; the tighter wins. The sum of member budgets may
  exceed the ceiling, which then acts as the pool.
- **Pre-call admission, post-call settlement** *(recommended)*. Post-call accounting alone lets every call that starts
  under budget run, so the overshoot grows with concurrency and with the size of each call. Instead:
  - **The reservation is a proven upper bound, not an estimate**, computed and enforced by the CP per model and API:
    - *Output:* the request's own cap — `max_tokens` for an Anthropic invoke body, `inferenceConfig.maxTokens` for
      Converse, `max_output_tokens` for Responses — which must be present and no larger than the model's maximum output
      in the CP's model-limits table. A request without a cap, or above the maximum, is refused (whether every harness
      always sends one is part of each harness's gate).
    - *Input:* the model's hard context window from the same table. The request's size is not a bound: images,
      documents and cached prefixes are not tokenized in proportion to bytes, and an arbitrary caller controls the JSON.
    - *Rates:* every billed category at the most expensive rate that can apply — input at the cache-write rate, which
      exceeds plain input (`usage_price.go:54-66` already prices it at 1.25× when the source states none), long-context
      tiers where a model has them, output at its highest tier.
    - A model without a price and limits row, or a request carrying a price-affecting option the table does not cover
      (a service tier, for example), is **refused, never priced at zero**. The model allow-lists (D1) can only name
      models that have both rows.
    - The consequence is stated, not hidden: on a large-context, high-price model one reservation is a large share of a
      small budget, so few calls run at once near the end of a month. A tighter input bound may replace the context
      window only when it is proven for that API (for example, a body whose every input is plain text the CP can count
      exactly); a sampled ratio never can.
  - **Full-fit admission.** A call is admitted only while `spent + reserved_in_flight + this_reservation ≤ budget` at
    the member level and at the tenant level, and the member's in-flight count is below its cap (a deployment default,
    *value unmeasured*). Because the candidate is counted and the reservation bounds its price, **no admitted call can
    take the spend past the budget**: the only overshoot left is a defect in the price or limits table, which is logged
    as one when a settlement exceeds its reservation (and is then settled as reported). Near the end of a budget the
    member is refused while some amount remains; the message says so ("the next call could cost up to $X").
  - **Admission is one transaction, durable before dispatch.** One database transaction in the CP's store updates the
    member's and the tenant's period rows and the member's in-flight count, and inserts the call's reservation row
    (call id, membership, tenant, bound source, model, API, admission month, price version, reserved amount, state
    `in_flight`, admission time, the CP instance). The condition is in the write itself (a conditional update, or a
    row lock on Postgres; SQLite's single writer on a one-CP deployment), so two requests, or two CPs during a rollout,
    that read the same sums cannot both pass. Nothing is sent to Bedrock until that transaction has committed.
  - **Settlement is exactly once.** One transaction moves the row from `in_flight` to `settled` and replaces the
    reservation in the admission month's buckets by the final amount; it is conditional on the row still being
    `in_flight`, so a duplicate settlement (a retry, a replay, a second CP) changes nothing.
  - **The final amount is the priced usage only when complete terminal totals are proven**: the whole non-stream body,
    the invocation metrics of a stream's closing event, Converse's `metadata` event. Anything less — an Anthropic stream
    cut after `message_start` and some `message_delta`s, a failed stream, no usage at all — settles **at the
    reservation**. `routegw.usageObserver` today returns `Known=true` from `message_start` / `message_delta` alone
    (`workspace/agent/internal/routegw/usage.go:236`); that may stand for the ledger's estimate row, and never for
    settlement.
  - **Orphans settle conservatively.** The CP bounds each upstream call with a deadline. A sweep settles at its
    reservation every `in_flight` row whose CP instance is gone or whose deadline has passed, because Bedrock may still
    be running a call whose CP has died. A reservation is never released to zero.
  - A call is never cut for budget reasons once admitted: cutting a stream loses tokens that are billed anyway and breaks
    the harness's turn, and full-fit admission makes it unnecessary.
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
    key. AWS documents CUR 2.0 columns with an `iamPrincipal/` prefix and Cost Explorer grouping by principal tags. Q1's
    acceptance calls are the first tagged ones, which lets AWS discover the keys, and the keys are activated before Q2's
    release, because ADR 0048 measured that a key must be seen before it can be activated and that activation has no
    backfill.
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
    option for a tenant that wants profile-level CloudWatch metrics (open question 5).
  - *Per-request metadata:* reaches invocation logs only *(docs)*; the ledger already records each call. Not used.

### H. Harnesses, wires and models on a provided route

A provided route is reached through the same harness rows as the member route (ADR 0128 decisions 4 and 5); the source
does not change what a harness speaks. Each row still ships only after decision 14's live gate.

| Harness | What it speaks to the gateway | Upstream Bedrock API | Models that make sense | Translation |
|---|---|---|---|---|
| claude | Bedrock InvokeModel (`invoke-with-response-stream`) on `ANTHROPIC_BEDROCK_BASE_URL` over `ANTHROPIC_UNIX_SOCKET`, bearer from `AWS_BEARER_TOKEN_BEDROCK` (measured against a stand-in, ADR 0128 P2b part 2) | `bedrock-runtime` invoke, as today | Anthropic models only (the CLI's own) | none |
| opencode | its own `amazon-bedrock` provider. Its [providers page](https://opencode.ai/docs/providers/) (fetched 2026-11-06, not pinned to v2.2.5) says `AWS_BEARER_TOKEN_BEDROCK` takes precedence over the other AWS credential methods, that an `endpoint` option is an alias of `baseURL`, and that a model id may be an inference profile ARN; none of that is measured on the installed version, and a second read of the current docs did not find the alias or the precedence. Whether it calls Converse or Invoke, and whether it can use a Unix socket, is *unmeasured* | Converse / ConverseStream (already on the gateway's allow-list), if that is what it sends | anything Converse serves: Claude, the GPT models with Converse, `gpt-oss`, others | none |
| codex | a custom `model_providers.<id>` with `base_url` at the gateway, `wire_api = "responses"` (documented as "the only supported value"), and the bearer from `env_key` or `auth.command` (a command that prints a bearer, refreshed every 5 minutes by default), per the [configuration reference](https://developers.openai.com/codex/config-reference) *(docs)*. Unix socket support is *unmeasured* | Responses on `bedrock-runtime` (`/openai/v1/responses`); the gateway's allow-list gains that one `POST` | the OpenAI GPT models with Responses on `bedrock-runtime` (named by system profile, `us.openai.…` / `global.openai.…`); **not Claude**, which has no Responses API on Bedrock; **not `gpt-oss`** there, whose Responses API is on `bedrock-mantle` only ([API compatibility](https://docs.aws.amazon.com/bedrock/latest/userguide/models-api-compatibility.html), *docs*) | none (below) |

- **No protocol translation in the gateway** *(recommended)*. Translating codex's Responses into Converse would make
  codex drive Claude, but the surface is large (tool calls, reasoning items, streaming event shapes, stored state),
  every codex release can move it, and codex's prompts and tools are tuned for OpenAI models; a member who wants Claude
  on Bedrock has claude and opencode. Each harness gets the models its own wire reaches, and the launch dialog offers
  only those.
- **codex's built-in `amazon-bedrock` provider** is documented with an AWS profile and a Region
  (`model_providers.amazon-bedrock.aws.profile` / `.region`, *docs*); it may also accept `AWS_BEARER_TOKEN_BEDROCK`.
  It is not rejected: what is unproven is whether its endpoint can be pointed at the gateway. The custom provider is the
  documented way to set a `base_url`, so it is the plan; the built-in one replaces it only if a measurement shows an
  endpoint override and a bearer that work with the gateway.
- **Stored Responses state is closed before Q7, at the CP.** On Bedrock a response is kept for 30 days unless `store` is
  false, and `previous_response_id` reads the stored context through an ordinary `POST`, scoped by the account's
  Project; `bedrock-runtime` has only the default Project
  ([Responses API](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-responses-api.html), *docs*). Under a
  shared route role, a response id from another member or tenant could therefore reuse their context, and a tiny body
  could pull in a huge hidden context and defeat D4's bound. So on provided routes the CP **forces `store: false`** on
  every Responses request and **refuses `previous_response_id`, `conversation` and any other reference to stored
  state**; the request parser fails closed on top-level fields it does not know, so a field added later is refused
  until it is reviewed. If codex turns out to need stored state, codex on provided routes waits for a CP-owned registry
  of which member and source owns each response id, with the whole chained context counted in the reservation; that
  is not designed here.
- **Responses streams are a different format.** `routegw`'s usage parser reads AWS event streams; Responses streams are
  server-sent events. Q7 builds and gates the parser for their terminal usage, and the refusal statuses codex retries or
  shows at once (measured, as P2b part 2 measured claude's).
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
  - *Cache sharing is not guaranteed.* Bedrock and Anthropic caches are separate, so the first turn after a switch may
    start from a cold prefix and pay cache creation on part or all of the context, which can cost more than plain
    input. Coming back, the original route's cache may still be warm or may have expired. The ledger records the real
    figures; the offer says "may cost more", not a number, until it is measured per route pair (ADR 0128 decision 10).
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
| Q0 | Catalogue ids `bedrock-tenant` / `bedrock-deployment`; the CP's spend, reservation and grant/budget model; source ids and fingerprints; the model price-and-limits table; no behaviour change | Existing `bedrock` rows and the rollup unchanged; a provided row and a member row of one model stay apart in the rollup |
| Q1 | **Acceptance only, behind a switch like `AF_WS_CLAUDE_BEDROCK_ROUTE`; not released.** CP route gateway (G3) with the `deployment` source and its signing contract; Agent forwarding; super_admin grant and ceiling; tenant_admin budgets; admission and settlement; session-tagged AssumeRole with its session policy; pre-wake refusal on every create path; claude, Terminal | A live claude turn through the CP on real Bedrock; added time to first token measured. **Budget:** two CPs racing at the budget edge never admit past it; a CP killed between admission and settlement leaves a reservation the sweep settles, never zero; a call admitted at 23:59:59 UTC settles into its own month; a duplicated settlement changes nothing; a cut stream settles at its reservation; an unpriced model and a missing output cap are refused. **Contract:** a forged or expired token, identity headers, caller tags, Region, URL or role, an ARN model id, a model outside the list (path and body), another operation — each refused, nothing signed. **Revocation:** budget 0 → visible 503, no fallback; grant off → next call refused; member removed → stream cut. These first tagged calls let AWS discover the principal tags |
| Q2 | **Release.** The minimum visibility (remaining budget in the launch dialog and the switch offer; the exhaustion message; the tenant_admin's member view); the principal tags activated; the CP path held | The tags activated in the payer account before release (ADR 0048: no backfill, and a key must be seen before it can be activated); a silent stream held past the load balancer's idle timeout, or the deployment's provided routes stay off (D2); CP capacity at the deployment's expected concurrent streams, with a deploy drained or failing visibly |
| Q3 | Richer reporting: the mirror, the usage page, the cloud cost line from principal tags, estimate beside actual | Principal-tag cost visible in Cost Explorer (≥ 24 h after a call); one test member's day compared, estimate against actual, with the difference stated, not tuned away |
| Q4 | D6: the target list, the return (R1, R2) | A live window stop → switch to a provided route → reset → return, automatic and manual; a budget exhausted on the borrowed route stops visibly; a forced failure on the return rolls back |
| Q5 | The `tenant` source: super_admin grant; cross-account role with the CP's external id and D1's activation probes; Tenant settings > Connections; its model allow-list and optional ceiling | Activation refused when AssumeRole succeeds without an id or with a wrong one (a role that trusts the CP without the condition); refused when it fails with the right one; an id supplied by a caller is not accepted; a deleted role refuses visibly; an edited ARN or rotated id refuses the bound session instead of moving it |
| Q6 | opencode on provided routes (after ADR 0128 P2c): its wire measured, TCP binding if needed; Terminal, then Managed | Decision 14 per execution method; the Converse usage read; a negative control per source |
| Q7 | codex on provided routes: Responses wire, OpenAI-family models only; `store: false` forced and stored references refused; Responses SSE usage parser | Decision 14 per execution method; a Claude model id refused at launch, not on the first message; another member's response id and a chained `previous_response_id` refused; terminal usage read from a real stream; codex's handling of the gateway's refusal statuses measured |

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
- **Cutting a stream when the budget runs out mid-call**: billed tokens are lost and the turn breaks; full-fit
  admission against a proven upper bound makes it unnecessary (D4).
- **A reservation from the request's size, or from a sampled ratio**: not an upper bound against a caller who controls
  the JSON, images, documents or cache writes (D4).
- **Falling back to a provided route when a member's own credential fails, or to another provided route when one runs
  out**: a silent move of the bill (ADR 0128 decision 3).

## Open questions

1. Whether a tenant on a deployment whose CP has no AWS identity may register a Bedrock long-term API key instead of a
   role (D1), accepting that billing cannot tell its members apart.
2. The default per-member budget when a tenant is first granted the source: zero (nobody until a tenant_admin acts) or
   a tenant default chosen at grant time.
3. The per-member concurrent-call cap (D4), set from Q1's measurements, and which APIs can prove an input bound
   tighter than the context window.
4. Whether the existing tag activations cover IAM principal tags, and whether Cost Explorer groups them under the same
   key (D5); the activation is checked before Q2's release, the grouping in Q3.
5. Per-tenant application inference profiles for tenants that want profile-level metrics (D5).
6. Currencies other than USD: ADR 0048 decision 6 shows what AWS returns; budgets are in USD until a deployment needs
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

None of these was exercised against AWS; each is also linked at the claim it supports.

- Amazon Bedrock User Guide: [API compatibility](https://docs.aws.amazon.com/bedrock/latest/userguide/models-api-compatibility.html);
  [Responses API](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-responses-api.html); [Track usage and costs](https://docs.aws.amazon.com/bedrock/latest/userguide/cost-management.html);
  [IAM principal attribution](https://docs.aws.amazon.com/bedrock/latest/userguide/cost-mgmt-iam-principal-tracking.html);
  [Application inference profiles (cost)](https://docs.aws.amazon.com/bedrock/latest/userguide/cost-mgmt-application-inference-profiles.html);
  [Inference profiles](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-profiles.html); [Create an application inference profile](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-profiles-create.html).
- AWS General Reference: [Amazon Bedrock endpoints and quotas](https://docs.aws.amazon.com/general/latest/gr/bedrock.html)
  (the "Inference profiles per account" row).
- IAM User Guide: [Access to AWS accounts owned by third parties](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_common-scenarios_third-party.html)
  (the external id).
- opencode: [Providers](https://opencode.ai/docs/providers/), Amazon Bedrock section (not pinned to the installed v2.2.5).
- Codex: [Configuration reference](https://developers.openai.com/codex/config-reference) (`model_providers`, `wire_api`,
  `auth`, `model_providers.amazon-bedrock.aws`).
- Local CLIs: `codex --help`, `codex exec --help` (0.167.0); `opencode --version` (v2.2.5); `claude --version` (2.1.318).
