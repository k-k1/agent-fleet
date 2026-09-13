# 0079. Borrowing another deployment's engines — a `remote` row in the engine table, with the waiting and the box-buying left to the far Control Plane

English | [日本語](0079-remote-engine-from-another-deployment.ja.md)

- Status: **accepted** (2026-09-12). P0 is implemented behind a declaration nothing sets yet: a
  deployment that does not set `AF_REMOTE_ENGINE_URL` takes exactly the path it took before. The
  live half of P0's completion test, and therefore open questions 1, 3, 4 and 6, are still
  unmeasured — no deployment has borrowed anything.
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

The predicate the code branches on today is **two** predicates, not one, and the review counted
both: `engineDef.external()` at nine non-test sites, and `e.ecs == nil` / `e.ctrl != nil` at four
more. The first becomes "not managed here" (external or remote — the table's missing `service`,
the machinery a row is not given, the admin row, the refusal of `ondemand`) and "nobody will
start it" (external only — the immediate failure). Reusing one predicate for both is how a remote
row would inherit ADR 0076 decision 4 and fail every cold start.

The four `e.ecs`-shaped sites need nothing: `mode` (`engines.go:877`) and the admin row's
`managed` (`engine_admin.go:200`) already ask "is there a service here", which is the right
question for a remote row and answers it correctly. The exception is `ensureStarted`
(`engine_gateway.go:851`), which asks the same question and produces ADR 0076 decision 4's
refusal — that one is the "nobody will start it" half wearing a different spelling, and
decision 5 is what replaces it.

One site divides three ways rather than two, and it is the one the review found broken:
`warm` (`engines.go:928-944`). It needs "not managed here" to be reached at all, and its body is
an active health probe — which decision 5 forbids. Decision 10 says where a remote row's warmth
comes from instead.

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
ADR 0077 P1 built the machine for that — `engineRegistry.adopt` (`engines.go:374`) constructs an
adopted row through the same `build` closure boot uses — but 🔴 **it is wired only in the AWS
lane, and a borrowing-only CP does not reach it at all.** Four gates, each of which P0 has to
open (the review found all four; none is hard, and missing one leaves a deployment with no
engines and no route):

- `newEngineRegistry` returns nil when neither `AF_ENGINES_SSM_PARAM`, `AF_ENGINES_JSON` nor
  `AF_COMFY_URL` is set (`engines.go:639`). `AF_REMOTE_ENGINE_URL` joins that gate.
- It returns nil again when the boot table has no rows and there is no AWS (`engines.go:696`) —
  which is exactly a native or docker CP that borrows, since its rows arrive later.
- `reg.build` is assigned only `if haveAWS` (`engines.go:826-828`), and `adopt` answers false
  without it (`engines.go:375`). The builder has to be attached on the no-AWS lane too.
- The poller that calls `adopt` is **not reusable**: `newEngineTableReloader` refuses to exist
  without SSM (`engine_table_reload.go:61-65`) and re-reads an SSM parameter. The remote lane
  brings its own poll — the catalogue mirror of decision 7, on the same 10-minute cadence — and
  borrows only `adopt`.

And `registerEngineRoutes` returns before `exemptPrefix` and the three handlers when the registry
is nil (`engine_gateway.go:168-177`), so a CP that fell through any of the four would not even
route `/engine/…`. A CP whose far fleet is unreachable at boot has no engines, which is the
truth; a CP that borrows and has no registry object is a bug.

**A key a local row already holds is not borrowed**, and one line says so. The registry is a map
keyed by `key` (`adopt` refuses a key already present, `engines.go:383-388`), and `AF_COMFY_URL`
already has an explicit merge rule with a managed row winning (`engineTableWithEnvRow`,
`engines.go:601-617`). Remote rows follow the same direction rather than leaving the answer to
whichever goroutine got there first.

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
  (`engine_gateway.go:340-352` — `GetMembershipByID`, and `!ok` is a 401). No new credential
  store, no new expiry, no new format.
- It also fixes decision 9 for free: a membership with no running workspace is one the far
  side's usage post-back cannot deliver to, which is exactly what we want.

🔴 **Reading it once is harder than that sentence, and the review could not make it work
without a person who can sign in.** Three facts, all read from the code:

- A membership with nobody behind it *can* be created: `AddMembership` pre-creates the identity
  with `UpsertIdentity(ctx, email, key, "")` (`internal/tenantsrv/tenants.go:587`), so an invite
  naming only a `user_key` makes one with no address at all. `checkInviteDomain` only bites when
  the tenant set `allowed_domains`, and then it demands an address.
- But the token exists **only inside that membership's own workspace container**
  (`workspace_lifecycle.go:401`, itself inside `if m.publicBaseURL != ""` — so `PUBLIC_BASE_URL`
  is a precondition, which an AWS deployment meets). **There is no route that starts another
  member's workspace, and none that opens a session in one**: the admin surface offers stop,
  clean-home and destroy (`tenant_wiring.go:80-100`) and read-only session lists
  (`admin_sessions.go:39`). So the value is reachable only by signing in **as** that membership —
  and a `user_key`-only invite produces an identity the IdP will never authenticate.
- Minting it offline is possible in principle and not in practice: the derivation is public in
  this repository (`engineSignKey(SHA-256(AF_MASTER_KEY))`, `main.go:128-129`,
  `engine_token.go:39-49`), so an operator holding `AF_MASTER_KEY` can compute the token with ten
  lines and no deployment — but they need the membership **id**, which surfaces in exactly one
  super_admin response, the cloud-cost view (`cloudcost.go:734`), and only for a membership that
  has attributed spend. Not a route for a freshly invited one.

So P0's credential is **a dedicated account the far IdP will really log in** — a real address,
used for nothing else, signed in once to read one environment variable. That is a cost the
operator chapter has to state, and it is what open question 6 asks about.

**P1 built that route, and it is what closes this** (`POST /api/admin/engines/issue-token`,
super_admin only, `control-plane/engine_issue_token.go`): the far side's operator names a
membership and gets its issuing token back, so borrowing needs no account and no sign-in at all.
There is no `GET` — a credential must not land in a URL, a browser history or a proxy log — the
answer states what the token opens and how to revoke it, and a membership the gateway would refuse
is refused here too (409) rather than handed out as a string that answers 401. It was deliberately
**not** in P0: P0 had to be provable without deploying new code to the deployment that owns the
engines, and its fake-far-gateway half (phase P0 item 8) needs no credential of any kind.

### 4. The path is passed through verbatim, and `X-AF-Model` goes with it

For a remote row the upstream is `<AF_REMOTE_ENGINE_URL>` + the path the far side itself named +
the path after the local `/engine/<key>/v1/`, with the query string. No provider-dependent prefix
(`engineUpstreamPrefix`, `engine_gateway.go:811`) — **the far gateway owns that rewrite**, and
applying it twice is how `/v1/v1/chat/completions` happens.

The far side's own `/internal/engine/token` answer carries `base_url` (`/engine/<key>/v1`,
`engine_gateway.go:215`), so that middle segment is **read, not composed**. Composing it here
would be this document asserting the far side's route layout, which is the thing the Workspace
deliberately does not do either — it dials `AF_CP_BASE_URL` plus whatever `base_url` came back
(`workspace/agent/engines.go:185`). Same rule, one level up.

🔴 **The credential on a remote row is per request, so it cannot live where the others live.**
`dial` presents `eng.apiKey` (`engine_gateway.go:789-791`), one string fixed when the row is
built. Decision 8's token is per (engine key, **session**), and `dial` is not even given the
claims today (`dial(ctx, eng, r, body)`, `engine_gateway.go:754`). P0 threads the session through
and resolves the bearer per request; `apiKey` stays what it is for the other two lifecycles.

`dial` must also forward `X-AF-Model`. It is how the far side knows which catalogue row a
request is for: its usage accounting reads it, and so does the pending guard that answers
`engine_waking` while a model is still being synced onto the instance (ADR 0072 P2 欠落 7).
Dropping it does not fail a request; it silently degrades the far deployment's accounting and
its most useful retryable refusal, which is the worst shape a bug can have.

### 5. A remote row is never health-probed and never started here

`ensureReady` returns immediately for a remote row. No health probe, no start, no wait.

The plain reason is enough: there is no health path to probe. `engineHealthy` asks
`engineHealthURL(d)` — the row's URL plus `/health` (`engine_gateway.go:911-913`) — and the far
fleet publishes nothing there that says anything about an engine. It bounds itself at five seconds
(`engine_gateway.go:920`) against a measured 527-second cold start (ADR 0071), so on the one shape
where the probe *does* reach an engine it can only ever fail.

🔥 **And on one shape it is not merely useless but expensive.** When the row's URL already
contains the engine path — which is the no-code-change baseline above,
`AF_COMFY_URL=https://<far>/engine/image/v1` — the probe lands on the far gateway as
`/engine/image/v1/system_stats`, which records demand and buys a box (`engine_gateway.go:384`
onward). A five-second check that always fails and starts a $1.26/hour instance is the single
worst outcome available here, and it is what a naive reuse of the `external` lane produces.
Decision 4's URL is the bare base, so a probe there would only reach the far CP's root — but the
row that gets there by hand is the one an operator reaches for first.

⚠️ Decision 10's `warm` is a health probe too, on the admin panel's path rather than a
generation's. That is the other half of this decision, and it is where the review found the text
wrong.

### 6. 🔥 The local hold outlives the far one, and its expiry is `engine_waking`

This is the trap that decides whether a cold start works at all.

- The far non-streaming path holds for 45 s and then answers `503 engine_waking` with a
  `Retry-After` (`engine_gateway.go:100`, bounded below the ingress ALB's 60 s idle timeout —
  `idle_timeout.timeout_seconds: "60"`, `30-ingress.yaml:452`).
- 🟢 That refusal **does** survive the relay: `plain` copies the upstream's headers, status and
  body through unchanged (`engine_gateway.go:738-746`), so the far side's `engine_waking` and its
  `Retry-After` arrive at the provider intact. This is the mechanism the whole decision rests on,
  and it needs no code.
- The local non-streaming path defaults to the same 45 s. Whichever expires first decides the
  answer, and the local timer starts first.
- 🔴 A local expiry is **not** `engine_waking` today: with no `ensureReady` in the way, the
  deadline fires inside `engineClient.Do`, which `plain` turns into `engine_unavailable`
  (`engine_gateway.go:732-736`) — and the image providers retry `engine_waking` for sixteen
  minutes but **do not retry `engine_unavailable` at all**
  (`workspace/agent/internal/imagegen/sdcpp.go:505`, and `sdcppRetryable` is the *shared*
  predicate: comfy reaches it through `engineHTTPAttempt` as well, `comfy.go:670-681`, so this
  covers both image providers and not just sd.cpp). A cold start would be a permanent failure,
  reported as a start that failed.

So, for remote rows, two changes and the second is the load-bearing one:

- The hold is raised **per row**, not globally. `enginePlainHold()` takes no argument
  (`engine_gateway.go:100-107`) and 75 s as a new default would raise it for managed rows on
  every ecs-ec2 deployment, **above that ALB's 60 s** — reintroducing the exact 504 the 45 s was
  chosen to avoid. P0 makes it row-aware and leaves 45 s where it is. Nothing else caps the wait:
  `engineClient` sets no `Timeout` and `ResponseHeaderTimeout: 0` (`engine_gateway.go:135-144`),
  so the hold really is the only bound and a knob can do the job.
- A deadline reached while relaying to a remote row is mapped to `engine_waking` with a
  `Retry-After`, because that is what it means. 🔴 **This, and not the 75 s, is what makes a
  borrowed cold start work.** The far side's 45 s is `AF_ENGINE_PLAIN_HOLD` *on the far
  deployment* — a knob this side cannot read and has no right to assume. A far operator who
  raised it to 120 s puts the local expiry first again whatever number is chosen here. The number
  only decides how often the far side's own sentence arrives instead of ours; the mapping decides
  whether the request is retryable at all.

The streaming path needs nothing, though not quite for the reason it first appears: the far side
flushes its 200 immediately but the first *body* byte is its first heartbeat, ten seconds in
(`engine_gateway.go:620-628`), and `dial` blocks on that byte (`:797-800`). What covers the gap is
the local path writing heartbeats of its own on the same cadence, and the bound there is
`engineWakeTimeout()` (900 s), not the plain hold. Both sides then hold for 900 s and the local
one expires a round trip earlier, which costs the far side's wording and nothing else.

A local deployment sitting behind its own proxy is the one thing this cannot interrogate —
hence a knob, and hence open question 2.

### 7. The catalogue is mirrored from the far deployment, read-only

`engineCatalog` is a thin cache over a `store.EngineModelStore` (`control-plane/engine_catalog.go:58`).
**A remote row's catalogue gets a different SOURCE, not a different store.** One field and one
branch in `list` (`engine_catalog.go:74-88`), which is the only place that reader is used.

🔴 The draft said "an implementation of that interface whose writes are refused", and the review
found that such an implementation **refuses nothing**: no write ever travels through
`engineCatalog`. Every one of them goes to `a.mgr.store` or `g.models` directly, keyed by the
`key` in the path — `engine_admin.go:865,868,876,882,885,888` for the toggles,
`:1132` for register, `:1177` for delete, `engine_ingest.go:820,869` for the ingest. The interface
has ten methods (one reader, nine writers), so the stub would also have been the larger change for
no effect. The refusal belongs where the writes are:

- **The admin write routes refuse a remote role**, 400 with the reason — the same shape
  `ondemand` is refused with (decision 10). A borrowed catalogue is the far administrator's
  document and this panel is not where it is edited.
- **The mirror is an empty list, never an absent source.** `hasModels` answers **true** when it
  has no store at all (`engine_catalog.go:122-124`) — deliberately, since the false direction
  stops a GPU. A remote row whose source were merely missing would therefore pass `serve`'s
  no-models gate and then 404 `model_unknown` on every chat request that names a model
  (`engine_gateway.go:435-440`). Nothing, not even before the first fetch, may present as nil.

Hand-typing the catalogue locally — ADR 0076's answer for a LAN ComfyUI — is **rejected here**:

- The Workspace composes a ComfyUI graph from **its own** deployment's catalogue rows (`files`,
  `params`, `base_model`). The far engine only has the files the far active set staged. Any
  drift between the two is a `Value not in list` at generation time, and ADR 0072's P2 notes
  record how hard that failure is to read.
- `negative_always`, the per-model window, the declared sizes and `warm` are all in the far
  catalogue already and all of them are wrong if guessed.

🟢 The far catalogue really does carry all of it — `files` (flag + `s3_key`), `params`,
`base_model`, `sizes`, `negative`, `selected`, `default`, `warm` and the window
(`engine_catalog.go:556-604`) — and the one fact that makes a *mirrored* `s3_key` correct rather
than a leak of somebody else's bucket layout is that only its **basename** is ever used: the
Agent takes the last path segment as the file name to put in a loader node, because "the box
mirrors bucket keys onto disk verbatim" (`workspace/agent/engines.go:519-521` and `:544-556`).
The bucket is irrelevant; the file name travels.

⚠️ But the wire row is not a `store.EngineModel`, and two fields have to be put back by the
mirror rather than read:

- **`kind` is not on the wire at all.** LoRAs ride a separate `loras` array beside `model_rows`
  (`engine_gateway.go:270-286`), so the mirror sets `Kind:"lora"` from *which array a row arrived
  in*. Get it wrong and every borrowed LoRA becomes a model: it enters the launch menu, and a
  LoRA-only catalogue makes `hasModels` answer true, which is a box started to run nothing.
- **`enabled` is not on the wire either**, because only enabled rows are published. The mirror
  sets it true, which is also why "a role the far fleet switched off" collapses to an empty list.
- `bytes` is dropped, so the panel's "+N s on the next cold start" reads 0 for a borrowed row.
  Harmless — that estimate is about a box this deployment does not pay for.

Refresh on the Agent's existing 10-minute cadence; a failed refresh keeps the previous answer,
for the same reason the local catalogue does (a transient error must not read as "no models",
which means "do not start this engine"). This same poll is what adopts the rows in decision 2.

**A role the far fleet switches off disappears from its catalogue** (`engine_gateway.go:224`
skips `engineModeOff`). The mirror then holds no models for that row, and `serve` already
refuses that with `engine_unavailable` and "this engine has no enabled model". Rows are
therefore added but never removed while the process runs — removing a live row is not something
the registry supports, and it does not need to.

### 8. The local session name is stated to the far side

The local gateway verified a local session token, so it knows the session
(`engineSessionClaims.Session`). It states that name when it exchanges the issuing token for a
far session token — the far side takes it as stated and does not verify it, on purpose
(`engine_gateway.go:182-186`), and the far side's `recordUsage` builds a usage row carrying it.

🔴 **Corrected 2026-09-13: that row is then dropped, so this decision buys the far operator
nothing.** The draft called the name "the only way an operator there can tell one borrower's
spending from another's", for the llm role. It is not, because there is no row on the far side to
carry it. The ledger is a file inside a Workspace, not a table in the Control Plane
(`engine_usage.go:6-10` — the CP's own usage tables, `0008_usage.sql`, `0053_usage_hourly.sql`
and `0055_engine_hourly.sql`, hold occupancy seconds and have no feature column at all), so the
only way to record a row is to POST it to a RUNNING workspace, and decision 3's purpose-made
membership has none. Decision 9 states this two paragraphs later as a BENEFIT; the two were
never reconciled, and decision 9 is the one that matches the code.

What the name still does is real and smaller: it reaches the far side, it is what the far token
is minted for, and it is what any durable record there would have to key on (open question 7).

Tokens are cached per (engine key, session) and renewed before expiry, exactly as the Agent
already does; the TTL is 30 days (`engine_token.go:85`), so this is cheap.

### 9. Both sides count a conversation; neither side counts a picture

The local CP counts as it does today — `recordUsage` reads the usage out of the relayed bytes
(`control-plane/engine_usage.go:198`) and posts the row to the workspace that asked. Nothing
changes.

The far CP also counts, against the borrowing membership. Its post-back resolves that
membership's workspace (`engine_usage.go:218-232`); with decision 3's purpose-made membership
there is none, the post-back is a no-op, and **no unrelated workspace's ledger is polluted**.
Two ledgers recording the same conversation is correct: one deployment consumed it, the other
paid for it.

🔴 **The image role writes no ENGINE usage row on either side, and the draft said otherwise.**
`engineUsageRowFor` refuses any row whose `api` is not `chat` (`engine_usage.go:170-172`), so
`recordUsage` stops at `noteServed` — the in-memory warm-model note — and returns. This is
pre-existing and deliberate (an image answer carries no token counts), but it has consequences this
ADR has to own:

- The borrower's own record survives: the Agent writes the picture into its own ledger as
  `tool.imagegen`, with a count and a pixel figure rather than tokens
  (`workspace/agent/engines.go:403`, ADR 0069). So a borrowed generation is not invisible HERE.
- 🔴 The far operator, on the other hand, gets **no durable record of a borrowed generation at
  all**: a demand mark and a warm-model note in one process's memory. Attributing borrowed image
  spend is therefore an open question (7), not something decision 8 already solved.
- ⚠️ And for the image role there is no session name to pass on in the first place. The Agent asks
  the local CP for a WORKSPACE-scoped engine token there (an empty session, deliberately — that
  credential never leaves the Agent's process, and the usage row is written Agent-side anyway,
  `workspace/agent/engines.go:399-405`), so the claims the far token is bought with carry no
  session. Decision 8 is a statement about the llm role twice over.
- P0's completion test has to change. "The far side's usage row carries the local session's name"
  is provable through the borrowed **llm** role and not through `generate_image`.

### 10. The admin row is `managed:false`, `lifecycle:"remote"`, and on/off

Same contract ADR 0076 decision 5 wrote for external rows (`control-plane/engine_admin.go:205-221`):
the row carries `url` and `warm` and **omits** `state`, `desired`, `box`, `stop_eta`,
`idle_secs` and `window_*` — omitted rather than zeroed, because none of them is this
deployment's to claim. `managed:false` needs nothing: it is already `e.ecs != nil`
(`engine_admin.go:200`).

Two things the draft got wrong here, and the review found both:

- 🔴 **`lifecycle` is a hard-coded constant, not the row's own field.** The handler writes
  `row["lifecycle"] = engineLifecycleExternal` literally (`engine_admin.go:210`), so a remote row
  would announce itself as `external` and P1's "another fleet" label would have nothing to branch
  on. P0 emits `e.def.Lifecycle`. (The Console side is confirmed ready: `lifecycle?: string`
  already exists on the row type, `console/src/features/settings/admin/engineTypes.ts:311`, and no
  component reads it yet — so P0 really needs no Console change.)
- 🔴 **`warm` does not "work unchanged", twice over.** As written it returns false for a remote row
  — `warm` falls past `e.ctrl != nil` to `if !e.def.external() { return false }`
  (`engines.go:928-934`) — so the panel would report every borrowed engine cold. And putting a
  remote row into the external branch instead is worse: that branch is an active `engineHealthy`
  call behind a 10-second cache (`engines.go:893-896,935-944`), fired on every panel load, which
  is precisely the probe decision 5 refuses. Nor is it "what the gateway last saw answer" — no
  such record exists; the nearest thing is `noteServed`, which is a warm model *id*.
  **A remote row's `warm` is read off the mirror**: the far catalogue already marks the model the
  far CP last saw answer with (`"warm": true`, `engine_catalog.go:591-594`), which is the far
  deployment's own observation and the only honest one available here.

`ondemand` is refused with 400 for a remote row, as it already is for external
(`engine_admin.go:463`): `off` here closes the route, it does not stop somebody else's box. The
Console needs no change for P0 — it already branches on `managed` — and gets its own "another
fleet" label in P1, because "externally managed" is true but unhelpful when there is a fleet on
the other end with a panel of its own.

⚠️ One more line the operator will need: a remote row's failures log **without naming the row**.
`streamed` prints through `eng.ecs.logKey()` (`engine_gateway.go:658`, `:692`), which is nil-safe
but answers the bare string `"engine"` when there is no adapter (`engine_ecs.go:464-469`) — so two
borrowed roles are indistinguishable in the log that open question 5 makes the operator's only
signal. P0 logs through `eng.def.Key` on those two lines.

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
   (`30-ingress.yaml:452`); a native CP has no proxy at all and compose has Caddy, whose
   `reverse_proxy` is understood to impose no timeout of its own — **(c), not confirmed here**.
   The review narrowed what turns on the answer: the far side's 45 s is its own knob, so the
   number decides whose refusal message arrives, and decision 6's mapping — not this default —
   decides whether the request is retryable.
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
6. ~~**Does P0's credential really need a dedicated account at the far IdP?**~~ **Answered by
   building the route instead (P1).** The question was real — no admin route starts another
   member's workspace or opens a session in one, and the offline mint needs a membership id no API
   reliably exposes — so a borrower would have had to create an account at the far IdP and sign in
   once. `POST /api/admin/engines/issue-token` removes that, and the cost it was weighed against
   (P0 losing "no new code on the far deployment") was never paid: P0 shipped without it, and the
   route is P1. What remains is the ordinary consequence, which is that **the far deployment must be
   running a Control Plane new enough to have it** — a borrower facing an older one is back to the
   account and the sign-in, and chapter 08 says so.
7. **How is borrowed use attributed on the far side?** ~~a borrowed IMAGE generation~~ — **the
   question is wider than the draft put it, and its cheapest answer does not work (2026-09-13).**

   **Wider**: a borrowed CONVERSATION leaves no durable record over there either. The far CP's
   post-back resolves the borrowing membership's workspace and returns silently when there is
   none (`engine_usage.go:226-232`), which decision 3's purpose-made membership never has — and
   the issue-token route warns the far operator off lending a membership that *does* have one
   (`engine_issue_token.go:54-56, 68-70`). So the far operator sees a GPU that was bought and no
   record of who for, for BOTH roles. Decision 9 already says so; decision 8 said otherwise and
   has been corrected above.

   **The cheapest answer does not work.** A zero-token row from `engineUsageRowFor` for image
   engines would be a row with nowhere to go: the ledger is a file inside a Workspace
   (`engine_usage.go:6-10`), the far membership has no Workspace, and the row would be dropped by
   the same branch that drops the chat one. Two further reasons it was the wrong shape anyway —
   an image request's far token carries an EMPTY session, because the Agent asks the local CP for
   a WORKSPACE-scoped engine token (`workspace/agent/engines.go:399-418`), so the row would have
   no borrower to attribute to; and `engine.image` is a feature value ADR 0029 §2's frozen
   enumeration does not have, written on top of the `tool.imagegen` row the borrower already
   keeps. `TestOnlyChatEnginesAreCountedByTheGateway` (`engine_gateway_test.go:766-790`) exists
   to refuse exactly that, and its comment gives the same two reasons.

   **What is left is to build the receiving end**, on the far Control Plane, because that side is
   the only one holding both the membership and a durable store. Two shapes, and the split is the
   same one decision 9 draws — a conversation has rows, a picture has not:

   - **For the conversation: somewhere for the row to land.** The row already exists, fully
     formed, at the moment it is dropped: `engineUsageRowFor` built it with the borrower's session
     name on it, and `postUsage` throws it away for want of an endpoint. A table on the far CP
     that takes it when the membership has no running workspace needs no new counting and no new
     enumeration value. 🔴 **And it closes a hole that is not about borrowing at all**: the same
     branch silently drops an ordinary member's engine row whenever their workspace stopped
     between the answer and the bookkeeping, which `engine_usage.go:226-232` admits in its own
     comment. The cost to weigh is that ADR 0029's ledger is deliberately a file inside a
     Workspace, and this is a second place engine rows can live.
   - **For the picture: seconds and requests, not a row.** There are no tokens to record and no
     session to attribute (above), so the honest unit is the one `engine_hourly`
     (`migrations/0055_engine_hourly.sql`) already keeps — GPU seconds per engine key per hour —
     with the membership axis it lacks. The gateway has `mv.MembershipID` in hand where it
     records demand.

   Either way this is the far deployment's OWN bookkeeping, borrowing or not, and ADR 0048
   decision 2 and ADR 0071 decision 9 both bar putting a price on it. It is deliberately not
   decided here. No borrower is blocked by its absence; what the absence costs is the far
   operator's ability to answer "who was that box for", which today is answered once, at
   `engine.issue_token` in the audit log (`engine_issue_token.go:141`), and never again.

## Phases

**P0 — the borrowing lane, provable without touching the far deployment.**

1. `lifecycle: "remote"`, and the split of `external()` into "not managed here" / "nobody starts
   it" — all nine sites, with `warm` (decision 10) as the one that answers neither.
2. `AF_REMOTE_ENGINE_URL` / `AF_REMOTE_ENGINE_TOKEN` / `AF_REMOTE_ENGINE_KEYS`, and the four
   gates decision 2 names: the boot gate (`engines.go:639`), the empty-table return
   (`:696`), `reg.build` off the AWS lane (`:826-828`) and the remote lane's own poll. The
   `engineTableNeedsAWS` lane (`engines.go:622`) is what keeps AWS out of it.
3. The token client: exchange, cache per (key, session), renew early — and threaded into `dial`,
   which has neither the claims nor a per-request bearer today (decision 4).
4. The catalogue mirror: the source on `engineCatalog`, `kind` and `enabled` reconstructed, the
   admin write routes refusing a remote role, and the rows the poll adopts.
5. `dial` for remote rows: the far side's own `base_url`, verbatim path, `X-AF-Model`, no prefix.
6. `ensureReady` returns at once; the **per-row** hold and the `engine_waking` mapping of
   decision 6.
7. The admin row of decision 10: `lifecycle` from the row, `warm` from the mirror, and the two log
   lines that name the key.
8. Tests, on `engine_external_test.go`'s pattern: an `httptest` far gateway exercising cold
   (`engine_waking` relayed, `Retry-After` intact), the local hold expiring (`engine_waking`, not
   `engine_unavailable`), warm, a streamed answer with heartbeats, a token renewal, a mirrored
   catalogue with a LoRA in it, an admin write refused, and a far fleet that is down at boot —
   then reachable, and adopted. None of this needs a credential or a far deployment.

**Done when**: with a fake far gateway, a cold borrowed engine produces a retryable refusal and
a warm one produces the answer, and a borrowed row is adopted by a CP that started with nothing;
with a real far deployment, `generate_image` in a local workspace returns one image, and one chat
completion through the borrowed llm role leaves a usage row on the far side carrying the local
session's name (the image role leaves none — decision 9).

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

Every line re-drawn during the review below, against `61042381`, then re-checked after merging
`origin/develop` at `f71c7c4d`: **88 citations, 0 drifted** on a scripted pass. Seven were off by a
line or a range and are corrected here; the rest landed where the draft said.

| Claim | Where |
|---|---|
| The Workspace only ever dials `AF_CP_BASE_URL` | `workspace/agent/engines.go:185` |
| The engine routes are registered on every flavour, session-exempt — and not at all when the registry is nil | `control-plane/routes.go:33`, `control-plane/engine_gateway.go:168-177` |
| The issuing token buys a session token; the catalogue needs the issuing token | `engine_gateway.go:187`, `:224` |
| The session name is taken as stated, never verified | `engine_gateway.go:182-186` |
| The token answer states its own `base_url` | `engine_gateway.go:215` |
| The membership is resolved live, so removal revokes at once | `engine_gateway.go:340-352` |
| A membership can be created with no address (invite by `user_key`) | `control-plane/internal/tenantsrv/tenants.go:587` |
| No admin route starts another member's workspace, or opens a session in one | `control-plane/tenant_wiring.go:80-100`, `control-plane/admin_sessions.go:39` |
| A membership id surfaces in one super_admin response only | `control-plane/cloudcost.go:734` |
| Session token TTL is 30 days | `control-plane/engine_token.go:85` |
| The issuing token is deterministic, and its signing master is shared with git/memo/schedule | `engine_token.go:47`, `control-plane/git_http.go:88` |
| The signing master is `SHA-256(AF_MASTER_KEY)` | `control-plane/main.go:128-129`, `engine_token.go:39-49` |
| The issuing token is injected at workspace start, only when `PUBLIC_BASE_URL` is set | `control-plane/workspace_lifecycle.go:376,401` |
| `dial` composes URL + provider prefix + path, forwards only Content-Type and Accept, and presents one fixed `apiKey` | `engine_gateway.go:754-800`, `:789-791`, `:811` |
| `engineClient` sets no timeout of its own | `engine_gateway.go:135-144` |
| The non-streaming hold is 45 s, global, bounded below the ALB's 60 | `engine_gateway.go:100-107`, `deploy/aws/ecs/cfn/30-ingress.yaml:452` |
| `plain` relays the upstream's status, headers and body unchanged | `engine_gateway.go:738-746` |
| A local deadline becomes `engine_unavailable` | `engine_gateway.go:732-736` |
| `engine_unavailable` is not retried by EITHER image provider; `engine_waking` is | `workspace/agent/internal/imagegen/sdcpp.go:505`, `comfy.go:670-681` |
| The streaming path flushes 200 at once but its first body byte is a heartbeat | `engine_gateway.go:620-628`, `:797-800` |
| The health probe is 5 s against `<url>/health`, and any non-200 is "not ready" | `engine_gateway.go:911-913`, `:920` |
| An external row fails immediately because nothing owns it | `engine_gateway.go:845-851` |
| An external row gets no adapter, controller, active set, pending reader or ladder | `control-plane/engines.go:754-773` |
| `warm` answers false for anything that is neither controlled nor `external()`, and probes for the rest | `engines.go:893-896`, `:928-944` |
| `adopt` builds through boot's own closure, and needs `reg.build` | `engines.go:374-398`, `:826-828` |
| The registry is nil for an empty table with no AWS | `engines.go:639`, `:696` |
| The table reloader does not exist without SSM | `engine_table_reload.go:61-65` |
| `apiKey` is filled only for the synthesised comfy row | `engines.go:767-769` |
| A table row needs a `service` unless it is external | `engines.go:511-513` |
| The catalogue is a cache over `store.EngineModelStore` — one reader, nine writers | `control-plane/engine_catalog.go:58`, `:74-88`, `control-plane/internal/store/store.go:363-391` |
| `hasModels` answers true when there is no store at all | `engine_catalog.go:122-124` |
| Catalogue writes never travel through `engineCatalog` | `engine_admin.go:865,868,876,882,885,888`, `:1132`, `:1177`, `engine_ingest.go:820,869` |
| The catalogue row carries files, params, base_model, sizes, negative, warm and the window | `engine_catalog.go:556-604` |
| LoRAs ride their own array, and `kind` is not on the wire | `engine_gateway.go:270-286` |
| A file's on-disk name is the basename of its `s3_key` | `workspace/agent/engines.go:519-521`, `:544-556` |
| An engine switched off is omitted from the catalogue | `engine_gateway.go:224` |
| An engine with no enabled model is refused with `engine_unavailable`; a named model the catalogue lacks is a 404 | `engine_gateway.go:384` onward, `:454-460` |
| The admin row omits ECS-derived fields for an external row; `lifecycle` is a constant there; `ondemand` is refused | `control-plane/engine_admin.go:200`, `:205-221`, `:210`, `:463` |
| A row with no ECS adapter logs as the bare word "engine" | `control-plane/engine_ecs.go:464-469`, `engine_gateway.go:658`, `:692` |
| Usage is posted to the workspace resolved from the CP's own store | `control-plane/engine_usage.go:198`, `:218-232` |
| No usage row is written for a non-chat engine | `control-plane/engine_usage.go:170-172` |
| The Console carries `lifecycle` on the row type and no component reads it | `console/src/features/settings/admin/engineTypes.ts:311` |
| The compose flavour's Caddy proxies every path | `deploy/compose/Caddyfile` |
| The ingress ALB has no path rules | `deploy/aws/ecs/cfn/30-ingress.yaml` (no `ListenerRule`, no `PathPattern`) |
| WAF is off unless asked for | `deploy/aws/ecs/cfn/30-ingress.yaml:166-168` |

## Review (2026-09-12, before P0)

In the manner of ADR 0076's and 0077's reviews, the chain "decision → grounds → current state"
was checked against the code by a second session, with the drafting session's working copy left
alone. Verdict: **approved (P0 may start). The design holds — a borrowed engine really is one
table row and the Workspace really needs no change — but eleven statements of the current state
were wrong or incomplete, and four decisions stood on them. Decision 2's "that path already
exists" was the largest (R1), decision 7's refusal mechanism refuses nothing (R4), decision 10's
`warm` and `lifecycle` are both wrong (R3), decision 9 claimed an accounting that does not happen
for the image role and took P0's completion test down with it (R8), and decision 3 drops to an
open question (R9). The text above has been corrected accordingly** — this ADR is still
"proposed" with not one line implemented, so the correction is the "implementation corrects the
text" stage brought forward, with what changed and why kept here. **Nothing was measured for this
section and no money was spent**; reading the code was enough, which is what the draft predicted.
Every `file:line` in "Sources checked" was re-drawn against `61042381` and re-checked after
merging `origin/develop` at `f71c7c4d` (where only `engine_offer.go` moved, which this review does
not cite); **88 citations, 0 drifted** on a scripted pass. Seven were off before that pass and are
fixed.

### What the review checked

- **R1. Decision 2's adopt path exists but is wired only in the AWS lane.** `engineRegistry.adopt`
  (`engines.go:374`) is real and builds through boot's own closure, as the draft said. What the
  draft did not check is what a borrowing-only CP reaches: `newEngineRegistry` returns nil for a
  deployment that declares none of the three existing engine variables (`:639`), returns nil again
  for an empty table with no AWS (`:696`) — which *is* a native CP whose rows arrive later — never
  assigns `reg.build` off the AWS lane (`:826-828`), and the only caller of `adopt` is an
  **SSM** reloader that refuses to exist without a parameter name
  (`engine_table_reload.go:61-65`). On top of that `registerEngineRoutes` returns before
  `exemptPrefix` and the three handlers when the registry is nil (`engine_gateway.go:168-177`), so
  falling through any gate means `/engine/…` is not even routed. Decision 2 now names all four
  and states that only `adopt` is reusable — the poll is the catalogue mirror's.
- **R2. The predicate is two predicates, and the draft counted one.** Nine non-test `external()`
  sites, plus four that ask `e.ecs == nil` / `e.ctrl != nil`. The classification is in the table
  below. Two of the `e.ecs` sites — `mode` and the admin row's `managed` — are already correct for
  a remote row, which is worth knowing because it is why `managed:false` and "`ondemand` reads as
  `on`" need no work at all. Decision 1 now says so, and names `warm` as the site that answers
  neither half.
- **R3. Decision 10 was wrong twice.** `warm` falls past the controller check to
  `if !e.def.external() { return false }` (`engines.go:928-934`), so a remote row would report
  cold for ever; and putting it in the external branch instead makes it an active `engineHealthy`
  call behind a 10-second cache (`:893-896,935-944`) fired on every admin panel load — the probe
  decision 5 forbids, reached from the panel rather than from a generation. "What the local
  gateway last saw answer" describes nothing in the code. It now comes off the mirror, where the
  far CP publishes its own `"warm": true` (`engine_catalog.go:591-594`). Separately,
  `row["lifecycle"]` is the literal constant `engineLifecycleExternal` (`engine_admin.go:210`), so
  a remote row would call itself `external` and P1's label would have nothing to read.
- **R4. Decision 7's refusal refused nothing.** An implementation of `store.EngineModelStore`
  handed to the row's catalogue cannot refuse a write, because **no write goes through the
  catalogue**: all ten writers address `a.mgr.store` / `g.models` directly, keyed by the `key` in
  the request path (`engine_admin.go:865,868,876,882,885,888,1132,1177`,
  `engine_ingest.go:820,869`). The interface is ten methods (one reader, nine writers), so the stub
  was the larger change as well as the ineffective one. The review's own recommendation — a
  different **source** on `engineCatalog`, one field and one branch in `list`
  (`engine_catalog.go:74-88`, the only reader) — is now the decision, with the refusal moved to
  the admin write routes where it can actually happen. Also recorded: `hasModels` answers **true**
  when there is no store (`:116-118`), so a mirror that is merely absent would pass `serve`'s
  no-models gate and then 404 every named model (`engine_gateway.go:435-440`). It must be an empty
  list, never nil.
- **R5. The mirror is feasible, and two fields do not survive the wire.** `/internal/engine/catalog`
  does carry `files`, `params`, `base_model`, `sizes`, `negative`, `selected`, `default`, `warm`
  and the window (`engine_catalog.go:556-604`) — so decision 7's premise is sound. But `kind` is
  absent (LoRAs ride a separate array, `engine_gateway.go:270-286`) and `enabled` is absent (only
  enabled rows are published), and both have to be put back by the mirror. Mistaking a LoRA for a
  model puts it in the launch menu and makes `hasModels` true for a catalogue that can start
  nothing. Added as well, because it is the fact that makes a *borrowed* `s3_key` correct rather
  than a leak of the far bucket's layout: only its basename is ever used
  (`workspace/agent/engines.go:519-521`).
- **R6. Decision 6's trap is real, its mechanism is confirmed, and the fix had to move.**
  Confirmed by reading: the far side's `engine_waking` and `Retry-After` do survive the relay
  (`plain` copies status, headers and body, `engine_gateway.go:738-746`); a local deadline does
  become `engine_unavailable` (`:732-736`); `engineClient` caps nothing of its own
  (`:135-144`), so the knob really is the only bound; and `sdcppRetryable` is **shared** with the
  comfy provider (`comfy.go:670-681`), so the citation covers both image providers rather than one.
  Two corrections. `enginePlainHold()` is global and takes no argument (`:100-107`), so a 75-second
  *default* would raise managed rows above the ecs-ec2 ALB's 60-second idle timeout
  (`30-ingress.yaml:452`) and reintroduce the 504 the 45 s exists to avoid — the raise is per row.
  And the far side's 45 s is `AF_ENGINE_PLAIN_HOLD` on the far deployment, a knob this side cannot
  read: whatever number is chosen here, a far operator who raised theirs puts the local expiry
  first again. The `engine_waking` mapping is therefore load-bearing and the 75 s is comfort, which
  is now what the decision says. The streaming half needs nothing, as drafted, but not because the
  far side's first byte is immediate — it flushes its 200 at once and its first *body* byte is a
  heartbeat ten seconds later (`:620-628`, `:797-800`); what covers the gap is the local path's own
  heartbeat.
- **R7. A remote row's bearer cannot live in `apiKey`.** `dial` presents `eng.apiKey`
  (`engine_gateway.go:789-791`), one string fixed at build, and is not given the session claims at
  all (`:754`). Decision 8's token is per (key, session), so P0 threads the session through. Added
  to decision 4 with the P0 item. Also there: the far side already states its own `base_url`
  (`:236`), so composing `/engine/<key>/v1/` here duplicates an assertion the Workspace
  deliberately does not make either — read it instead.
- **R8. Decision 9's "both sides count" is false for the image role, and P0's completion test
  depended on it.** `engineUsageRowFor` refuses any non-chat row (`engine_usage.go:170-172`), so
  `recordUsage` stops at the in-memory warm-model note. Nothing durable is written on either side
  for a borrowed picture. Decision 9 is retitled and says so; decision 8's "the far side's rows
  carry the borrower's session name" is narrowed to the llm role; the "Done when" now proves that
  half through a chat completion; and attributing borrowed image spend becomes open question 7.
- **R9. Decision 3's procedure does not close, and this is the one open question the draft
  suspected.** Two of its three legs hold: a membership with no address at all can be created
  (`AddMembership` pre-creates the identity, `internal/tenantsrv/tenants.go:587`), and removal
  revokes at once (`engine_gateway.go:340-352`). The third does not. The token exists only inside
  that membership's own container (`workspace_lifecycle.go:401`, itself behind `PUBLIC_BASE_URL`),
  and **nothing starts another member's workspace or opens a session in one** — the admin surface
  is stop, clean-home, destroy and read-only lists (`tenant_wiring.go:80-100`,
  `admin_sessions.go:39`). So it can be read only by signing in *as* that membership, and a
  `user_key`-only invite makes an identity no IdP will authenticate. Minting it offline is
  derivable from this repository (`SHA-256(AF_MASTER_KEY)`, `main.go:128-129`) but needs a
  membership id that surfaces in one super_admin response only, and only for a membership with
  attributed spend (`cloudcost.go:734`). P0's credential is therefore a dedicated real account at
  the far IdP, stated in decision 3, with the alternative (moving P1's button into P0, at the cost
  of P0's "no new code over there") as open question 6. P0's fake-far-gateway tests are unaffected,
  which is why the verdict is still "may start".
- **R10. Decision 5's 🔥 was true of the wrong row.** The probe buys a box only when the row's URL
  already contains the engine path — which is the `AF_COMFY_URL=…/engine/image/v1` baseline in the
  Background, not decision 4's bare base, where `engineHealthURL` would reach the far CP's root
  (`engine_gateway.go:911-913`). The 🔥 stays, with its condition in the sentence, and the plain
  reason — there is no health path to probe — leads instead.
- **R11. Small ones, all applied.** The external branch is `engines.go:754-773`, not `:753-772`.
  The `service` rule is `:511-513` (and is not on P0's path at all, since remote rows are
  synthesised like the comfy env row — but a hand-written `lifecycle:"remote"` table row hits it,
  so it is in the "not managed here" half). `WafRateLimitPer5Min`'s `Default: 0` is
  `30-ingress.yaml:168`. And a remote row's stream failures log as the bare word `"engine"`:
  `logKey()` is nil-safe but anonymous (`engine_ecs.go:464-469`, printed at
  `engine_gateway.go:658` and `:692`), which matters because open question 5 makes a log line the
  operator's only signal that a borrowed role never appeared. P0 logs `eng.def.Key` there.
- **Checked and found sound, so recorded rather than changed.** The ingress ALB really has no
  path rules and no `Conditions:` gating them; Caddy really is a bare `reverse_proxy`; the
  `/engine/` and `/internal/engine/` prefixes really are exempt on every flavour; the pending
  guard and the demand counter are both nil-safe for a row with no machinery
  (`engine_gateway.go:527`, `engine_control.go:157`), so ADR 0076's nil-safety sweep covers a
  remote row too; the Console really needs no P0 change; and the "ec2-single first" entry in
  "Rejected alternatives" survives the review — nothing found here changes its ordering argument,
  so it stays as written.

### Where `external()` splits, site by site

Nine non-test call sites. "Not managed here" is the half a remote row joins; "nobody starts it"
stays external-only. The fourth column is what a missed site costs, since the draft's warning was
that one miss fails a remote row immediately.

| Site | Half | What it does | If missed |
|---|---|---|---|
| `engines.go:511` | not managed here | a table row needs a `service` | a hand-written `remote` row is rejected at parse; the whole table fails and the registry is nil |
| `engines.go:606` | not managed here | `AF_COMFY_URL` vs a table row | the log claims a remote row "is a managed row in the engine table", which is a lie |
| `engines.go:624` | not managed here | does this table need AWS | a borrowing native CP loads an AWS config for a feature it is not using (ADR 0076 decision 3) |
| `engines.go:754` | not managed here | the machinery a row is not given | a remote row gets an ECS adapter with an empty service, a controller goroutine, an active-set publish to SSM and a GPU ladder |
| `engines.go:932` | **neither** | `warm` for the panel | false for ever, or a health probe on every panel load — see R3 |
| `engine_admin.go:209` | not managed here | the admin row's shape | the panel claims `idle_secs` and a demand window about somebody else's box |
| `engine_admin.go:463` | not managed here | `ondemand` refused | `off`/`ondemand` offered for an engine this deployment cannot stop |
| `engine_table_reload.go:148` | not managed here | do not carry a ladder onto it | a restart request logged about a row the table never mentioned |
| `engine_table_reload.go:192` | not managed here | its absence from the table says nothing | "the table no longer declares image" on every table change |

And the four sites that ask the question a different way: `ensureStarted`
(`engine_gateway.go:851`) is the "nobody starts it" half spelled `e.ecs == nil`, and decision 5
replaces it; `mode` (`engines.go:877`), the admin row's `managed` (`engine_admin.go:200`) and
`warm`'s first line (`engines.go:929`) already answer correctly for a remote row and need nothing.

### Per-decision revisions (already applied above)

| Decision | What changed | Why |
|---|---|---|
| 1 | two predicates counted, not one / the four `e.ecs` sites / `warm` named as the site that fits neither half | R2, R3 |
| 2 | the four gates a borrowing-only CP falls through / only `adopt` is reusable, the poll is the mirror's / a key a local row holds is not borrowed | R1 |
| 3 | the credential cannot be read without signing in as the membership / a dedicated far IdP account is P0's answer / the offline mint and why it is not a route | R9 |
| 4 | `base_url` read from the far side, not composed / the per-session bearer cannot live in `apiKey`, and `dial` has no claims | R7 |
| 5 | the plain reason leads; the 🔥 keeps its condition (a URL that already contains the engine path) / `warm` named as the other probe | R10, R3 |
| 6 | the relay of the far refusal confirmed / the hold is raised per row, not globally, because of the 60 s ALB / the mapping is load-bearing and 75 s is comfort / both image providers share the retry predicate / the streaming gap is covered by the local heartbeat | R6 |
| 7 | a source on `engineCatalog`, not a stub store / the refusal moved to the admin write routes / never a nil source / `kind` and `enabled` reconstructed / the basename rule | R4, R5 |
| 8 | the session name is taken as stated / the far usage row is the llm role only | R8 |
| 9 | retitled: no ledger row for a picture on either side | R8 |
| 10 | `lifecycle` from the row, not the constant / `warm` from the mirror / the two log lines that name the key | R3, R11 |
| Open questions | 1 narrowed (what actually turns on the default) / 6 the credential / 7 borrowed image attribution | R6, R9, R8 |
| Phases | P0 items 1-8 restated with the gates, the threading, the mirror's two fields and the admin refusal / "Done when" proves the usage row through chat | R1, R4, R5, R7, R8 |
| Sources checked | four ranges corrected, twenty-four lines added | R11 and the re-draw |

## P0 as built (2026-09-12)

The code landed in four commits across three sessions — the foundation and the refusals, the
catalogue mirror, and the gateway — and `AF_REMOTE_ENGINE_URL` is the whole switch: unset, every
branch added here is unreachable and `notManagedHere()` is exactly the old `external()`. **Nothing
was measured on any deployment**; what follows is what building it corrected in the decisions above,
which is the ordinary "implementation corrects the text" stage and not a second review.

- **Two things the review's own count still missed, and both were caught by writing the code.**
  (a) A `lifecycle:"remote"` row declared while `AF_REMOTE_ENGINE_URL` is unset has no handle, so
  its catalogue has no source — and a catalogue that cannot be read at all answers `hasModels` TRUE,
  so the row passes `serve`'s no-models gate and then 404s `model_unknown` on every request that
  names a model. That is decision 7's own failure arriving by a route decision 7 did not name. Such
  a row is now refused at build with a line saying why, and `catalogSource()` answers an empty list
  even on a nil handle as the second lock. (b) **Absence is how the far side says "off".** A role
  whose mode is `off`, or whose last enabled model is removed, is *skipped* by the far catalogue
  handler rather than reported empty, so a mirror refreshed only from the rows that are present
  keeps offering a role that stopped being offered. The poll now synthesises an empty row for every
  borrowed role the answer does not mention, and drops that row's ten-second cache so the change is
  not held for another window.
- **Decision 4 grew a signature.** `dial` takes the session claims (`dial(ctx, eng, r, body,
  claims)`), because the bearer is per (engine key, session) and the function had no way to know
  which session was asking. The far side's `base_url` is read from the token answer as the decision
  says, and an unknown one is an error rather than a guessed `/engine/<key>/v1`.
- **Decision 6 is a second default, not a new one.** `enginePlainHoldFor(eng)` replaced
  `enginePlainHold()`; the managed default stays 45 s and the test that pins it under the ALB's
  60 s now also pins that a borrowed row is longer and that the two cannot be collapsed. The
  `engine_waking` mapping wraps all three places a relay can fail, and refuses to fire on a
  cancelled request — the loosening "the context ended, so it must be waking" would answer
  `engine_waking` to a caller who has already hung up.
- **Decision 7's refusal moved once more, to five routes.** `putModel`, `postModel`, `deleteModel`,
  `putNegative` and `postIngest` answer 400 `engine_not_ours` for a borrowed role, and the message
  names the far deployment because the operator's next act is over there. `negative_always` is read
  from the mirror for the same reason it cannot be written here. A new error code needs a Console
  catalogue entry or `TestCPEmittedErrCodesHaveConsoleCatalogEntry` fails — which it did.
- **Unchanged and worth saying:** no Agent change, no Workspace change, no CloudFormation change,
  and no new IAM. The Console needed one i18n line and nothing else, as decision 10 predicted.

**Still not done, and not P0's:** the live run and its timings (open question 3), the far side's
super-admin button (decision 3 / open question 6), the Console's "another fleet" label, the operator
chapter, and `AF_ENGINE_API_KEY_<KEY>` (decision 11, P2). Borrowed image spend is still attributed
nowhere on the far side (open question 7).

## P2 as built (2026-09-13)

Decision 11 alone, in the Control Plane and nowhere else: `AF_ENGINE_API_KEY_<KEY>`, read at
registry build for an EXTERNAL row, into the same `apiKey` field `dial` and `engineHealthy`
already present upstream. Unset, nothing changes. What building it settled, beyond what the
decision says:

- **The name is folded, because a table key is free text and an environment variable name is
  not.** `engineAPIKeyEnvName` upper-cases the key and writes every character outside `[A-Z0-9]`
  as `_`, so `image-2` is `AF_ENGINE_API_KEY_IMAGE_2`. Two keys can fold together; that is a
  table nobody writes, and the alternative is a key whose bearer cannot be declared at all.
- **One variable per ROW, not one per deployment.** A single shared bearer would present the
  credential of the proxy in front of the llm engine to a ComfyUI on the same network — the two
  are different boxes belonging to different reverse proxies, and ADR 0076's whole threat model
  is that reachability is the access control.
- **`AF_COMFY_API_KEY` wins on the row `AF_COMFY_URL` synthesises**, and the generic variable is
  its fallback rather than dead: an operator who declared that row inline instead has no
  `AF_COMFY_API_KEY` to set. One log line names both when both are declared, because a bearer
  that is silently not the one the operator just edited is a 401 with nothing to read.
- **The predicate is `external()`, and this is the one place in the whole ADR where the NARROW
  one is right.** A managed row's key is an SSM SecureString; a borrowed row buys its own per
  (engine key, session) and `dial` never reads `apiKey` for it (decision 4), so a static value
  there would be a second, staler answer to a question that already has one. The test that pins
  this pairs the borrowed row against an EXTERNAL row with the same key and the same variable
  set — pairing it against a MANAGED row would pass for an implementation that read nothing.
- **Four tests**, on `engine_external_test.go`: the name folding, the lifecycle pairing above,
  the precedence both ways, and the capability itself — an inline table with no AWS anywhere, and
  a chat completion that reaches an engine behind something which checks the bearer on the health
  path as well as on the request. Before this, that table produced a row with an empty `apiKey`
  and the health probe alone ended it in `engine_unavailable`.
