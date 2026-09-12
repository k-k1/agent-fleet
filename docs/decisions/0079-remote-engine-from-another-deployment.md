# 0079. Borrowing another deployment's engines — a `remote` row in the engine table, with the waiting and the box-buying left to the far Control Plane

English | [日本語](0079-remote-engine-from-another-deployment.ja.md)

- Status: **proposed** (2026-09-12). Nothing is implemented.
- **Nothing was measured for this document.** Every claim says where it comes from —
  (a) measurements in ADR 0071, 0072, 0074, 0075 and 0077, (b) facts read out of this
  repository's code on 2026-09-12 (listed file:line under "Sources checked"), (c) things known
  only as the published behaviour of Caddy, an ALB or AWS and **not confirmed on any deployment
  of ours**. Decisions that rest on (c) are named under "Open questions".
- The user's request is one sentence — **a workspace in an Agent Fleet running on their own
  host (native / docker) should be able to use the llm and image engines of an Agent Fleet
  running on AWS.**

## Background

### What is actually bound to one deployment (read on 2026-09-12)

The path from a session to an engine is three stages, and **only the middle one knows which
deployment it is in**.

1. **The Workspace does not know what a runtime is.** Both providers dial
   `AF_CP_BASE_URL` plus the `base_url` the CP handed back (`workspace/agent/engines.go:185`),
   with the engine session token as the only credential. No AWS SDK, no bucket, no service
   name. Whether the far end is ECS, a LAN box or another fleet **cannot be told apart from
   here, and does not need to be**.
2. **The gateway assumes one thing: that there is an upstream URL.** `dial` composes
   `def.URL` + a provider-dependent prefix + the path (`control-plane/engine_gateway.go:766`),
   presents the engine's own key, and relays. The only place ECS is touched is `ensureStarted`
   (`engine_gateway.go:845`), and ADR 0076 already taught that function to answer "nothing here
   owns this engine".
3. **The registry decides what a row is.** `newEngineRegistry` (`control-plane/engines.go:635`)
   reads the table and gives each row an ECS adapter, a controller, an active set, a pending
   reader and a GPU ladder — except a row whose `lifecycle` is `external`, which gets none of
   them (`engines.go:753-772`, ADR 0076 decision 1).

So the question this ADR answers is not "can the bytes get there". It is **what a row means
when the engine belongs to another Agent Fleet that will start it for us.**

### The far side is already a token-authenticated public door

Nothing has to be opened on the deployment that owns the engines:

- `/engine/*` and `/internal/engine/*` are session-exempt and registered on **every** flavour
  (`engine_gateway.go:174-177`, called unconditionally from `control-plane/routes.go:33`).
- The ecs-ec2 ingress ALB has no path rules and its WAF is off unless an operator asks for it
  (`deploy/aws/ecs/cfn/30-ingress.yaml`); the compose flavour's Caddy is a bare
  `reverse_proxy 127.0.0.1:8099` (`deploy/compose/Caddyfile`). Both pass the whole surface.
- The credential model is already two-tier: a per-membership **issuing** token buys a
  per-session, per-engine **session** token (`control-plane/engine_token.go:47,85`), and the
  membership is resolved live on every request, so a removed member is refused inside the
  token's lifetime (`engine_gateway.go:340`).

What is missing is the **borrowing side**: a row in the local engine table that says "this
engine is another deployment's, and that deployment will start it".

### Three shapes of "somebody else's engine"

They are not the same problem, and conflating them is how one design ends up wrong for two of
them.

| Shape | The far side | What the local CP must do |
|---|---|---|
| **1. A fleet that buys boxes** (ecs-ec2) | a CP that wakes a GPU on demand and stops it again | forward and **let the far side hold the request** |
| **2. A fleet pointing at an always-on engine** (compose / native / ec2-single with ADR 0076's `external` row) | a CP that only forwards | forward; there is nothing to wait for |
| **3. No fleet in the path** | the engine itself, on a reachable URL | ADR 0076 as it stands — an `external` row |

This ADR is about shapes 1 and 2, which are the same row with the same code; shape 2 simply
never exercises the waiting. **Shape 3 is not rejected — it is the right answer whenever the
engine is always on**, and ADR 0076's `external` row already does it: nothing in that decision
requires the URL to be on a LAN, and its decision 7 already describes putting a reverse proxy
in front and giving the CP the bearer.

### What happens today with no code change at all

Worth writing down, because it is the baseline any measurement will be compared against, and
because it is nearly usable:

Setting `AF_COMFY_URL=https://<far fleet>/engine/image/v1` with
`AF_COMFY_API_KEY=<an afe_… session token>` produces a correct upstream — the comfy prefix is
`/` (`engine_gateway.go:811`), so the request lands on the far route as
`/engine/image/v1/prompt`. It then fails for three separate reasons, and each one is a decision
below:

- **It only works while the far engine is already warm.** `engineHealthy` gives the probe five
  seconds (`engine_gateway.go:920`); a sleeping engine makes the far gateway hold and then
  answer 503, so the local row — being `external` — fails at once (`engine_gateway.go:845`).
  🔥 Worse than useless: that failed five-second probe still records demand on the far side and
  **buys a GPU box** that nothing local is waiting for.
- **The llm role cannot be wired at all.** An inline row via `AF_ENGINES_JSON` is accepted
  (`engines.go:498`) but there is no way to give it a bearer: `apiKey` is filled only for the
  synthesised comfy row (`engines.go:768`) or from SSM, which the external lane deliberately
  never reads.
- **`X-AF-Model` is dropped.** `dial` forwards Content-Type and Accept and nothing else, so the
  far side loses the model declaration its usage accounting and its pending guard both read.

## Decisions

### 1. A borrowed engine is a table row, declared `lifecycle: "remote"`

`engineDef.Lifecycle` gains a third value. Empty is this deployment's ECS service; `external`
is ADR 0076's "a URL and nothing else, and nobody starts it"; **`remote` is "another Agent
Fleet's gateway, and that fleet starts it"**. Declared, never inferred from the URL's shape
(ADR 0053) — a URL that happens to contain `/engine/` must not silently change what a row means.

A `remote` row gets exactly what an `external` row gets: no ECS adapter, no controller, no
active set, no pending reader, no GPU ladder, no uptime sampler. It differs from `external` in
three places only — decisions 5, 6 and 7 — and every one of those differences exists because
**there is somebody on the other end who can start the engine**.

The predicate the code branches on today is `engineDef.external()` (`engines.go:119`). It
becomes two: "not managed here" (external or remote — the admin row, the missing ECS adapter,
the refusal of `ondemand`) and "nobody will start it" (external only — the immediate failure).
Reusing one predicate for both is how a remote row would inherit ADR 0076 decision 4 and fail
every cold start.

### 2. The operator declares WHERE and WITH WHAT. The far catalogue declares WHICH

Two environment variables, shaped like `AF_COMFY_URL` before them, plus one optional filter:

| Variable | Meaning |
|---|---|
| `AF_REMOTE_ENGINE_URL` | the far fleet's base URL, no path: `https://af.example.com` |
| `AF_REMOTE_ENGINE_TOKEN` | the `afei_…` issuing token (decision 3) |
| `AF_REMOTE_ENGINE_KEYS` | optional. `image` to borrow only that role; empty = every role the far fleet offers |

**Which engines exist, and what `api` and `provider` each answers to, is read from the far
deployment's own catalogue** (`GET /internal/engine/catalog`, `engine_gateway.go:224`) and not
declared locally. This is not derivation: the catalogue is the far administrator's declaration,
and it is the only party entitled to make it. Guessing would be actively unsafe — an image role
is `comfy` on one deployment and `sdcpp` on another, the Workspace composes a completely
different request for each, and a wrong guess produces a ComfyUI graph posted at an
OpenAI-compatible endpoint.

The consequence is that **rows appear when the first catalogue fetch succeeds, not at boot**.
That path already exists: ADR 0077 P1 made the registry adopt rows that show up after start,
because a table can legitimately be empty for a while. A CP whose far fleet is unreachable at
boot therefore has no engines, which is the truth.

### 3. The credential is a purpose-made membership's issuing token; revocation is deleting that membership

The far deployment mints `afei_<membership>` deterministically from its signing master
(`engine_token.go:47`) and injects it into every workspace of that membership
(`control-plane/workspace_lifecycle.go:401`). That token, and only that token, opens
`/internal/engine/token` and `/internal/engine/catalog`. It opens no git, no MCP, no memos and
no API.

- 🔴 **Never a person's issuing token.** It is deterministic, so the only way to invalidate one
  is to rotate the signing master — which is shared with the git, memo and schedule tokens
  (`control-plane/git_http.go:88`). Revoking one borrower would log out the fleet.
- **A membership used for nothing else.** Invite one, read `AF_ENGINE_ISSUE_TOKEN` once from its
  workspace, stop that workspace. Revoking the borrower is then **removing that membership**:
  `liveMembership` resolves it on every single request and answers 401 the moment it is gone
  (`engine_gateway.go:340`). No new credential store, no new expiry, no new format.
- It also fixes decision 9 for free: a membership with no running workspace is one the far
  side's usage post-back cannot deliver to, which is exactly what we want.

P1 adds the far side's super-admin a button that mints and shows this token, so that borrowing
does not require booting a workspace to read an environment variable. It is ~30 lines and it is
deliberately **not** in P0: P0 must be provable without deploying new code to the deployment
that owns the engines.

### 4. The path is passed through verbatim, and `X-AF-Model` goes with it

For a remote row the upstream is `<AF_REMOTE_ENGINE_URL>/engine/<key>/v1/` + the path after the
local `/engine/<key>/v1/`, with the query string. No provider-dependent prefix
(`engineUpstreamPrefix`, `engine_gateway.go:811`) — **the far gateway owns that rewrite**, and
applying it twice is how `/v1/v1/chat/completions` happens.

`dial` must also forward `X-AF-Model`. It is how the far side knows which catalogue row a
request is for: its usage accounting reads it, and so does the pending guard that answers
`engine_waking` while a model is still being synced onto the instance (ADR 0072 P2 欠落 7).
Dropping it does not fail a request; it silently degrades the far deployment's accounting and
its most useful retryable refusal, which is the worst shape a bug can have.

### 5. A remote row is never health-probed and never started here

`ensureReady` returns immediately for a remote row. No health probe, no start, no wait.

🔥 **The health probe is not merely useless here, it is expensive.** `engineHealthy` bounds
itself at five seconds (`engine_gateway.go:920`) while a cold GPU takes 527 s measured (ADR
0071) — so the probe can only ever fail. But the probe is an ordinary request to the far
gateway, which records demand and buys a box (`engine_gateway.go:384` onward). A five-second
check that always fails and starts a $1.26/hour instance is the single worst outcome available
in this design, and it is what a naive reuse of the `external` lane produces.

There is no health path to probe in any case: the far fleet publishes none that does not reach
the engine itself.

### 6. 🔥 The local hold outlives the far one, and its expiry is `engine_waking`

This is the trap that decides whether a cold start works at all.

- The far non-streaming path holds for 45 s and then answers `503 engine_waking` with a
  `Retry-After` (`engine_gateway.go:100`, bounded below the ingress ALB's 60 s idle timeout).
- The local non-streaming path defaults to the same 45 s. Whichever expires first decides the
  answer, and the local timer starts first.
- 🔴 A local expiry is **not** `engine_waking` today: with no `ensureReady` in the way, the
  deadline fires inside `engineClient.Do`, which `plain` turns into `engine_unavailable`
  (`engine_gateway.go:716-740`) — and the image provider retries `engine_waking` for sixteen
  minutes but **does not retry `engine_unavailable` at all**
  (`workspace/agent/internal/imagegen/sdcpp.go:505`). A cold start would be a permanent
  failure, reported as a start that failed.

So, for remote rows: the effective hold is `AF_ENGINE_PLAIN_HOLD` with a **higher default (75 s)**,
and a deadline reached while relaying to a remote row is mapped to `engine_waking` with a
`Retry-After`, because that is what it means. The streaming path needs nothing: the far side
answers 200 and a heartbeat every 10 s immediately, `dial` returns on that first byte, and the
bytes are relayed as they are.

A local deployment sitting behind its own proxy is the one thing this cannot interrogate —
hence a knob, and hence open question 2.

### 7. The catalogue is mirrored from the far deployment, read-only

`engineCatalog` is a thin cache over a `store.EngineModelStore` (`control-plane/engine_catalog.go:58`).
A remote row is given an implementation of that interface whose `ListEngineModels` reads the
mirrored far catalogue and whose writes are refused.

Hand-typing the catalogue locally — ADR 0076's answer for a LAN ComfyUI — is **rejected here**:

- The Workspace composes a ComfyUI graph from **its own** deployment's catalogue rows (`files`,
  `params`, `base_model`). The far engine only has the files the far active set staged. Any
  drift between the two is a `Value not in list` at generation time, and ADR 0072's P2 notes
  record how hard that failure is to read.
- `negative_always`, the per-model window, the declared sizes and `warm` are all in the far
  catalogue already and all of them are wrong if guessed.

Refresh on the Agent's existing 10-minute cadence; a failed refresh keeps the previous answer,
for the same reason the local catalogue does (a transient error must not read as "no models",
which means "do not start this engine").

**A role the far fleet switches off disappears from its catalogue** (`engine_gateway.go:224`
skips `engineModeOff`). The mirror then holds no models for that row, and `serve` already
refuses that with `engine_unavailable` and "this engine has no enabled model". Rows are
therefore added but never removed while the process runs — removing a live row is not something
the registry supports, and it does not need to.

### 8. The local session name is stated to the far side

The local gateway verified a local session token, so it knows the session
(`engineSessionClaims.Session`). It states that name when it exchanges the issuing token for a
far session token. The far deployment's usage rows then carry the borrower's session name,
which is the only way an operator there can tell one borrower's spending from another's.

Tokens are cached per (engine key, session) and renewed before expiry, exactly as the Agent
already does; the TTL is 30 days (`engine_token.go:85`), so this is cheap.

### 9. Both sides count the usage, and the far side's post-back lands nowhere

The local CP counts as it does today — `recordUsage` reads the usage out of the relayed bytes
(`control-plane/engine_usage.go:198`) and posts the row to the workspace that asked. Nothing
changes.

The far CP also counts, against the borrowing membership. Its post-back resolves that
membership's workspace (`engine_usage.go:218-232`); with decision 3's purpose-made membership
there is none, the post-back is a no-op, and **no unrelated workspace's ledger is polluted**.
Two ledgers recording the same generation is correct: one deployment consumed it, the other
paid for it.

### 10. The admin row is `managed:false`, `lifecycle:"remote"`, and on/off

Same contract ADR 0076 decision 5 wrote for external rows (`control-plane/engine_admin.go:205-217`):
the row carries `url` and `warm` and **omits** `state`, `desired`, `box`, `stop_eta`,
`idle_secs` and `window_*` — omitted rather than zeroed, because none of them is this
deployment's to claim. `warm` works unchanged: it is what the local gateway last saw answer.

`ondemand` is refused with 400 for a remote row, as it already is for external
(`engine_admin.go:463`): `off` here closes the route, it does not stop somebody else's box. The
Console needs no change for P0 — it already branches on `managed` — and gets its own "another
fleet" label in P1, because "externally managed" is true but unhelpful when there is a fleet on
the other end with a panel of its own.

### 11. An external row needs a way to declare a bearer without SSM

Small, independent, and it belongs in this ADR because shape 2 cannot be reached without it:
`apiKey` is populated only for the comfy row synthesised from `AF_COMFY_URL`
(`engines.go:768`). An inline `AF_ENGINES_JSON` row — the only way to declare an external
**llm** engine — has no way to carry one, so it is refused 401 by anything that checks.

Add `AF_ENGINE_API_KEY_<KEY>`, read at registry build for any external row. This unlocks an
external llm engine for native, docker and ec2-single deployments, which today can have an
external image engine and no external chat engine at all. It is P2 because nothing in P0
depends on it: a remote row mints its own credential (decision 3).

## Rejected alternatives

| Rejected | Why |
|---|---|
| **The Workspace dials the far CP directly** (a second base URL + issuing token in the container) | Fewest lines by far, and wrong in three ways: the far CP's usage post-back resolves **its own** store and would deliver rows to an unrelated workspace (`engine_usage.go:218-232`); the borrowing credential would sit in every workspace container's environment; and the workspace container gains an egress destination, which ADR 0071 decision 4(a) exists to avoid |
| **An `external` row with a long-lived static token** | Works only while the far engine is warm, and its health probe buys a GPU box each time it fails (decision 5). Making it work would mean teaching the far gateway to accept a non-expiring credential on the data path — weakening the far side to save the near side |
| **A tunnel or VPN to the engine boxes** (SSM port forward, Tailscale into the VPC) | The boxes are short-lived, addressed through Cloud Map, and their SG admits the CP only. Worse, **nothing but the far CP can buy or wake one** — a tunnel reaches an address that is usually not there. And llama-server and ComfyUI have no authentication of their own: reachability is their access control |
| **Publishing the engines on the far ALB** | Same authentication problem with a bigger audience, and it discards the wake, the hold, the catalogue check and the accounting the gateway exists to do |
| **Doing ec2-single's engine stack first** (make a single-VM deployment drive the ECS engine roles, then borrow from it) | Not rejected — **reordered**. It is a cost optimisation of the far side, not a capability: the borrower still faces shape 1, so this ADR is still needed. It touches CloudFormation, IAM, VPC placement and the ec2-single runbook, needs a live stand-up to prove, and its economics turn on whether a NAT gateway can be avoided. Doing it after this ADR means its completion test is "the same workspace gets the same image, more cheaply" |

## Decisions this overrides, and the ones it keeps

- **ADR 0071 decision 4 (the gateway is the only way a Workspace reaches an engine): kept**, and
  this is what makes the borrowing invisible to the Workspace — no Agent change is proposed
  anywhere in this document.
- **ADR 0071 decision 5 (hold the first request, heartbeat at 10 s): kept and inherited.** The
  far side does the holding; the local side relays its heartbeats.
- **ADR 0076 decision 1 (the lifecycle is declared, external rows get no machinery): extended**
  with a third value. The list of what a row does not get is unchanged.
- **ADR 0076 decision 4 (an external engine fails immediately, because nothing can start it):
  not applicable to a remote row, and the reason is the decision's own reason.** Something can
  start it.
- **ADR 0053 (declare, do not derive): kept** — both by decision 1 (the lifecycle is a field)
  and by decision 2 (which engines exist is read from the far administrator's declaration, not
  inferred from a probe).
- **ADR 0072 decision 1 (the catalogue is the whole declaration): kept**, with the far
  deployment's catalogue as the declaration for a borrowed row (decision 7).

## Open questions (decide after measuring)

1. **Is 75 s the right local hold?** It has to exceed the far side's 45 s and stay under
   whatever cuts the local connection. 45 s was chosen against a 60 s ALB
   (`30-ingress.yaml`); a native CP has no proxy at all and compose has Caddy, whose
   `reverse_proxy` is understood to impose no timeout of its own — **(c), not confirmed here**.
2. **Does the local front end pass SSE through unbuffered?** The heartbeat only works if
   nothing buffers it. The CP already sends `X-Accel-Buffering: no` for nginx
   (`engine_gateway.go:610` onward); Caddy is believed to stream by default — **(c)**.
3. **What does a borrowed cold start actually cost in wall time?** Two holds, two retry loops
   and an internet round trip between them. ADR 0071 measured 527 s cold and 165 s for the
   image role; the borrowed number is unknown and is P1's first measurement.
4. **Does a far deployment with WAF enabled rate-limit the comfy polling?** `/history` is
   polled per generation and the whole borrowing fleet arrives from one NAT address.
   `WafRateLimitPer5Min` defaults to 0 (`30-ingress.yaml:166`), so this only bites a deployment
   that turned it on.
5. **Is "the row appears when the first catalogue fetch succeeds" acceptable operationally?**
   It means a far fleet that is down at boot leaves the local launch menu without those models,
   and the operator's only signal is a log line. The alternative — declaring `api` and
   `provider` locally — is what decision 2 rejects.

## Phases

**P0 — the borrowing lane, provable without touching the far deployment.**

1. `lifecycle: "remote"`, and the split of `external()` into "not managed here" / "nobody starts it".
2. `AF_REMOTE_ENGINE_URL` / `AF_REMOTE_ENGINE_TOKEN` / `AF_REMOTE_ENGINE_KEYS`, and the
   registry gate that lets them build a registry with no AWS at all (the `engineTableNeedsAWS`
   lane, `engines.go:622`).
3. The token client: exchange, cache per (key, session), renew early.
4. The catalogue mirror, and the rows it adopts.
5. `dial` for remote rows: verbatim path, `X-AF-Model`, no prefix.
6. `ensureReady` returns at once; the hold ordering and the `engine_waking` mapping of decision 6.
7. The admin row of decision 10.
8. Tests, on `engine_external_test.go`'s pattern: an `httptest` far gateway exercising cold
   (`engine_waking` relayed, `Retry-After` intact), warm, a streamed answer with heartbeats, a
   token renewal, a mirrored catalogue, and a far fleet that is down at boot.

**Done when**: with a fake far gateway, a cold borrowed engine produces a retryable refusal and
a warm one produces the answer; with a real far deployment, `generate_image` in a local
workspace returns one image and the far side's usage row carries the local session's name.

**P1 — the live run and the operator's route in.** One image through a real ecs-ec2 deployment,
timed (open question 3). The far side's super-admin button that mints the borrowing credential
(decision 3). The Console's "another fleet" label (decision 10). The operator chapter, which is
a new section of `guide/operate/07-image-engine.*` plus the `.env` / native runbook entries.

**P2 — `AF_ENGINE_API_KEY_<KEY>`** (decision 11), which is what makes shape 2 reachable for an
llm engine.

**Explicitly not in scope.** TTS: VOICEVOX is a direct CP→engine URL with no token-authenticated
gateway in front of it (ADR 0013, 0070), so borrowing speech is a different design. Borrowing in
the other direction (an AWS fleet using an engine at home) is the same row pointed the other
way, but the far side would be behind a residential network and nothing here assumes it is
reachable.

## Sources checked (2026-09-12, this repository's code)

| Claim | Where |
|---|---|
| The Workspace only ever dials `AF_CP_BASE_URL` | `workspace/agent/engines.go:185` |
| The engine routes are registered on every flavour, session-exempt | `control-plane/routes.go:33`, `control-plane/engine_gateway.go:174-177` |
| The issuing token buys a session token; the catalogue needs the issuing token | `engine_gateway.go:187`, `:224` |
| The membership is resolved live, so removal revokes at once | `engine_gateway.go:340` |
| Session token TTL is 30 days | `control-plane/engine_token.go:85` |
| The issuing token is deterministic, and its signing master is shared with git/memo/schedule | `engine_token.go:47`, `control-plane/git_http.go:88` |
| The issuing token is injected at workspace start | `control-plane/workspace_lifecycle.go:401` |
| `dial` composes URL + provider prefix + path, forwards only Content-Type and Accept | `engine_gateway.go:754-800`, `:811` |
| The non-streaming hold is 45 s, bounded below the ALB's 60 | `engine_gateway.go:100`, `deploy/aws/ecs/cfn/30-ingress.yaml` |
| `engine_unavailable` is not retried by the image provider; `engine_waking` is | `workspace/agent/internal/imagegen/sdcpp.go:505` |
| The health probe is 5 s and any non-200 is "not ready" | `engine_gateway.go:920` |
| An external row fails immediately because nothing owns it | `engine_gateway.go:845` |
| An external row gets no adapter, controller, active set, pending reader or ladder | `control-plane/engines.go:753-772` |
| `apiKey` is filled only for the synthesised comfy row | `engines.go:768` |
| An inline table row needs only key + url when it is external | `engines.go:498-514` |
| The catalogue is a cache over `store.EngineModelStore` | `control-plane/engine_catalog.go:58`, `control-plane/internal/store/store.go:363` |
| An engine switched off is omitted from the catalogue | `engine_gateway.go:224` |
| An engine with no enabled model is refused with `engine_unavailable` | `engine_gateway.go:384` onward |
| The admin row omits ECS-derived fields for an external row; `ondemand` is refused | `control-plane/engine_admin.go:205-217`, `:463` |
| Usage is posted to the workspace resolved from the CP's own store | `control-plane/engine_usage.go:198`, `:218-232` |
| The compose flavour's Caddy proxies every path | `deploy/compose/Caddyfile` |
| WAF is off unless asked for | `deploy/aws/ecs/cfn/30-ingress.yaml:166` |
