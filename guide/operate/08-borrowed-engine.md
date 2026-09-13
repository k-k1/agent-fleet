---
audience: "a deployment administrator running compose / native / docker who also has an Agent Fleet on AWS with GPU engines, and wants the first one to use the second one's"
source_of_truth: "the environment variables the Control Plane reads at startup; the far deployment's own catalogue for which engines exist and which models they offer"
updated: "2026-09"
---

# 08. Borrowing another deployment's engines

English | [日本語](08-borrowed-engine.ja.md)

The fleet's own GPU engines — the `llm` role behind chat completions, the `image`
role behind `generate_image` — are bought on AWS, so they exist on the `ecs-ec2`
target only. [07 Image generation on your own ComfyUI](07-image-engine.md) is one way
around that: point at a machine you already run. **This chapter is the other one.**

If you already operate an Agent Fleet on AWS, a second deployment on your own host
(`compose`, `native` or `docker`) can **use that one's engines**. The near deployment
relays; the far one keeps doing what it already does — deciding that something wants a
GPU, buying the box, loading the model, letting it go again. Nothing in a session
knows the difference: the borrowed models appear wherever this deployment's own would.

**What you get.** Both roles, `llm` and `image`, or just the one you name. No new
code on the far deployment, no port opened there, no VPN, no change to any workspace.

**What you do not get.**

- **Any control over the far engines.** You cannot start one, stop one, edit its
  model list, or see which box it is on. Those are the far administrator's, and this
  chapter is mostly about where that boundary falls.
- **Speech.** VOICEVOX is reached by a direct URL with no token-authenticated
  gateway in front of it, so borrowing a voice is a different design and does not
  exist.
- **The other direction.** An AWS deployment using an engine at home is the same row
  pointed the other way, but nothing here assumes a residential network is reachable,
  and it is not supported.

🔴 **Nothing in this chapter has been measured on a real pair of deployments.** The
code is tested against a stand-in far gateway; the timings in "What the first request
waits for" come from the far deployment's own cold starts, not from a borrowed one.
Where a number is unknown, it says so.

## The two variables, and the optional third

All three are read by the Control Plane **once at startup**, like `AF_COMFY_URL`
before them. Changing any of them is a CP restart.

| Variable | Meaning |
|---|---|
| `AF_REMOTE_ENGINE_URL` | The far fleet's base URL, **with no path**: `https://af.example.com`. Setting it is what turns borrowing on. |
| `AF_REMOTE_ENGINE_TOKEN` | The `afei_…` issuing token of a membership on the far deployment (next section). |
| `AF_REMOTE_ENGINE_KEYS` | Optional. Comma-separated role names — `image` to borrow only that one. Empty borrows every role the far fleet offers. |

**Both or neither.** A URL with no token could only ever be refused, so a half
declaration borrows nothing and says which half is missing in the log at boot.
Leaving `AF_REMOTE_ENGINE_URL` unset is the normal case and changes nothing at all.

Where to put them depends on the target:

| Target | Where |
|---|---|
| compose / docker | the `.env` next to the compose file — [deploy/compose/README.md](../../deploy/compose/README.md) |
| native | the environment of `af start`, or `Environment=` in the systemd unit — [deploy/native/README.md](../../deploy/native/README.md) |

**Do not point `AF_COMFY_URL` at the far fleet instead.** It looks like it should
work — see the last section for why it costs money and still fails.

### The rows appear when the far fleet answers, not at boot

Which engines exist, which API and provider each speaks, and which models they offer
are all **read from the far deployment's catalogue**, never declared here. That is
deliberate: an image role is ComfyUI on one deployment and sd.cpp on another, a
session composes a completely different request for each, and a guess would post a
ComfyUI graph at an OpenAI-compatible endpoint.

The consequence is operational:

- The catalogue is fetched at startup and then **every 10 minutes**. A borrowed role
  becomes available on the first fetch that succeeds.
- **A far fleet that is unreachable when the CP starts leaves the launch menu without
  those models**, and the only signal is a line in the CP log. It recovers on its own
  at the next poll.
- A failed fetch **keeps the previous answer** rather than emptying the list. A
  transient error must not read as "this engine has no models", which is the same
  thing as "do not use this engine".
- **A role this deployment already serves is not borrowed.** If a local `image` row
  exists, the far one is ignored and the log says so once per poll, until you change
  one side or the other.

## The credential

The far deployment already mints, for every membership, an **issuing token**
(`afei_…`). It opens exactly two routes over there — "give me a session-scoped engine
token" and "which engines do you offer" — and nothing else: no git, no MCP, no memos,
no API. That is the credential this feature wants.

🔴 **Never use a person's issuing token, including your own.** It is derived
deterministically from the far deployment's signing master, so there is no way to
invalidate one of them. Invalidating it means rotating that master — and the same
master is behind the git, memo and schedule tokens, so **every member of that fleet
is logged out**. One borrower you wanted to cut off costs you the whole fleet.

So the token must belong to **a membership used for nothing else**. Invite one on the
far deployment — it needs no workspace and never has to run anything — and then ask
that deployment for its token.

**The far deployment's super-admin issues it.** The route is
`POST /api/admin/engines/issue-token` with `{"tenant_slug": "...", "user_key": "..."}`,
and it answers with the token, the variable to put it in, what the token opens, and how
to revoke it. There is no `GET`: a credential must not end up in a URL, a browser
history or a proxy log. A membership that is not active is refused (409) rather than
handed a token that would only ever answer 401.

⚠️ **An older far deployment may not have that route.** If it answers 404, the
credential is still reachable the long way, and it is worth knowing why that way is so
awkward: the token exists only inside that membership's own workspace container, and no
administrative route starts another member's workspace or opens a session in one — the
admin surface offers stop, clean-home, destroy and read-only lists. So on an older far
deployment the procedure is to invite a member with **a real address that the far
sign-in provider will actually authenticate**, sign in as it once, read
`AF_ENGINE_ISSUE_TOKEN` out of its container's environment, and stop that workspace.

**Revoking a borrower is removing that membership** on the far deployment. The
membership is resolved live on every single request, so access stops at the next
request rather than at the end of some lifetime. There is no separate credential to
delete, no expiry to wait for, and no list to keep.

Treat the value like any other secret: it belongs in the same place your other
`AF_*` secrets live, not in a shell history and not in a ticket.

## What the far catalogue decides, and what you cannot change here

Under **Admin → Inference engines** a borrowed role looks like any externally managed
one, and it carries the far fleet's URL. What it shows about models — the ids, the
sizes, each row's own negative prompt, and the deployment-wide "excluded from every
image" list — is **a mirror of the far deployment's catalogue, read-only**.

Every write in that panel is refused for a borrowed role, with `400 engine_not_ours`
and a message naming the far deployment:

- enabling or disabling a model, or marking one selected or default,
- registering a model, or forgetting one,
- taking a model in (the ingest form),
- editing "excluded from every image".

All of those are done **in the far deployment's own admin panel**, by whoever
administers it. A change there reaches this deployment within one poll.

What you *can* still do here:

- **on / off.** "off" closes the route on this deployment — sessions stop being
  offered the engine — and does absolutely nothing to the far fleet's box.
- Nothing else. **"on demand" is refused**, exactly as it is for a ComfyUI of your
  own: it is a promise to release a box, and there is no box here to release.

### When the far side switches a role off

The far catalogue simply stops mentioning a role whose mode is `off`, or whose last
enabled model was removed. On this side that arrives as **zero models for that role**:

- the row stays in the panel, marked as having no models,
- requests are refused with `503 engine_unavailable` and "this engine has no enabled
  model — an administrator has to select one",
- the launch menu stops offering them.

The administrator who has to select one is **the far one**. Within one poll of them
switching it back on, it works again.

## What the first request waits for

A borrowed engine is usually asleep. The far deployment buys a GPU box when something
asks for one, and **the first request after that pays for the whole cold start** —
instance, image, model into VRAM.

- **Measured on the far deployment's own requests** (ADR 0071): 527 s for the `llm`
  role from a cold start, 165 s for the `image` role.
- 🔴 **The borrowed figure is not measured.** Two holds, two retry loops and an
  internet round trip sit between the two deployments. Expect "minutes", plan for the
  numbers above as a floor, and do not quote them as the borrowed cost.

What happens while that runs, so that a long first request is not read as a failure:

- **Chat (streaming).** The connection is held and a heartbeat is written every 10
  seconds, bounded by `AF_ENGINE_WAKE_TIMEOUT` (900 s by default).
- **Everything else (non-streaming).** The far deployment holds the request for
  about 45 seconds and then answers `503 engine_waking` with a `Retry-After`; that
  answer is relayed through unchanged. This deployment holds a **borrowed** row for
  75 seconds — deliberately longer, so the far side's own sentence is usually the one
  that arrives — against 45 seconds for a row it manages itself.
- **If this side's hold expires first, the answer is still `engine_waking`**, never
  `engine_unavailable`. That distinction is load-bearing: the image tool retries
  `engine_waking` for up to 16 minutes and does not retry `engine_unavailable` at
  all, so getting it wrong would turn a box on its way up into a permanent failure.

🔴 **The far side's 45 seconds is its own setting and you cannot read it.** If its
operator raised it, this side's hold expires first again whatever you do — which is
fine, because of the previous point. `AF_ENGINE_PLAIN_HOLD` overrides the hold if you
set it, but it is **one value for every row**, so raising it for a borrowed engine
also raises it for any engine this deployment manages itself. On `ecs-ec2` that is
how you reintroduce a 504 at the load balancer; leave it alone unless you have a
reason.

## What the panel will and will not show you

- **Externally managed**, carrying the far fleet's URL. It is not "managed" here,
  because there is no service here to manage.
- **warm** is **what the far deployment last observed about its own engine**, as its
  catalogue reports it — not a check made from here. Nothing on this side ever probes
  a borrowed engine, on the panel or anywhere else; probing would land on the far
  fleet's gateway, record demand and buy a box.
- **Left out rather than guessed:** state, desired count, which box, when it will
  stop, the idle window. None of them is this deployment's to claim about somebody
  else's instance.
- **No uptime history.** The heatmap is drawn from samples a control loop takes, and
  a borrowed row has no control loop.
- The **"+N s on the next cold start"** size estimate reads 0. File sizes are not on
  the wire, and that estimate is about a box this deployment does not pay for.

## Who pays, and what is recorded

**The far deployment buys the box and pays for it.** Nothing about a borrowed engine
appears under this deployment's cloud cost, because this deployment has no instance.

What is *recorded* differs by role, and the difference matters if you are the one
being borrowed from:

- **Chat (`llm`): this deployment writes a usage row**, as it does for any engine
  traffic, into the asking member's own ledger. The far deployment builds the same row
  — carrying **the borrowing session's name**, stated by this deployment and taken as
  given over there; it is a label, not a permission — but it has nowhere to deliver it,
  because a borrowing membership has no workspace. So the far side **keeps** it instead
  (see below). No unrelated member's ledger is touched either way.
- **Images: no engine usage row on either side.** An image answer carries no token
  counts, so nothing is written for it on the engine ledger, here or there. On this
  side a member's image generation is still visible, because the image tool writes
  its own usage row as it stores the file — counted in pictures, not tokens.

### If you are the one being borrowed from

The far deployment's Control Plane records what it cannot deliver, so "a GPU was bought
and I cannot tell who for" is no longer the answer. Both are read from one route, as a
super-admin of **that** deployment:

```
GET /api/admin/engines/<key>/attribution?from=YYYY-MM-DD&to=YYYY-MM-DD
```

- `memberships` — requests, successes, milliseconds and tokens per membership per hour,
  for **both roles**. This is the only count the image role has, and it is what answers
  "whose work was that box doing".
- `undelivered` — the chat rows kept whole, each with the borrowing session's name and
  the reason it could not be delivered (`no_workspace` is the ordinary borrowing case).

Two things to know about it:

- 🔴 **Kept is not delivered.** These rows are never posted into anybody's ledger later,
  so they do not appear on the usage graph. They are an operator's record, not a member's.
- There is **no Console screen** for this yet. Call the route, or read the
  `engine_membership_hourly` and `engine_usage_undelivered` tables. Both are pruned on
  the same 92-day retention as every other hourly bucket.

## When it does not work

The first place to look is the Control Plane's log on the borrowing side, because a
borrowed row that never appeared is not visible anywhere else. The lines worth
grepping for:

| Line says | Meaning |
|---|---|
| `borrowing is declared but … is unset` | only one of URL / token is set; nothing is borrowed |
| `is borrowed from …` | the role was adopted — this is the success line |
| `reading the borrowed catalogue from … failed` | the far fleet is unreachable, or refused. The status is in the line |
| `is already served by a … row` | a local row holds that role; the far one is ignored |
| `declares lifecycle "remote" but AF_REMOTE_ENGINE_URL / AF_REMOTE_ENGINE_TOKEN are unset` | a hand-written table row with nothing to borrow from; the role is not served |

And the answers a session can see:

- **`401` in the catalogue log line** — the borrowing membership was removed on the
  far deployment, or the token is wrong. Both look the same from here, which is
  correct: a removed member and a bad credential are the same amount of access.
- **`400 engine_not_ours`** — a write in this deployment's engine panel for a
  borrowed role. Do it in the far deployment's panel.
- **`503 engine_unavailable` with "no enabled model"** — the far side turned that
  role off, or it has nothing enabled.
- **`503 engine_waking`** — normal for a cold start. It carries a `Retry-After` and
  the image tool keeps asking.

🔴 **One shortcut to avoid.** Pointing `AF_COMFY_URL` at the far fleet's image route
(`https://…/engine/image/v1`) looks like it would work without any of this, and it is
the worst of both: that row is health-checked from here, the check lands on the far
gateway, **records demand and buys a GPU box** — and then fails anyway, because it
allows five seconds against a cold start of minutes. A failed check that buys a GPU
instance by the hour is the one outcome worth going out of your way to avoid.

Which target supports which engine arrangement is in
[ref/deploy-targets.md](../ref/deploy-targets.md).
