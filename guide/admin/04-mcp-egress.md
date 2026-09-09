---
audience: "a tenant administrator distributing integrations to the team"
updated: "2026-08"
---

# 04. Distributing MCP servers and controlling outbound traffic

English | [日本語](04-mcp-egress.ja.md)

Handing the team a shared tool is yours to do; controlling outbound traffic belongs
to IT / the deployment administrator.

## Distributing an MCP server to everyone in the tenant (your permission)

Tenant settings → **"Operations → MCP distribution"** distributes an MCP server to every member of
the tenant.
A distributed server appears in each member's ⚙ Settings → MCP servers labelled **tenant**,
ready to use from their assistants and sessions (what they see:
[member/12 Settings](../member/12-settings.md#mcp-servers)).

### Only remote (HTTP) can be distributed

**stdio — running a command inside the workspace — cannot be distributed.** Distributing one
would be equivalent to an admin running an arbitrary command in everybody's container. Only a
**Streamable HTTP endpoint** can be distributed, specified by URL and headers. A member who
needs stdio registers it personally in their own settings.

### Decide how credentials are handled (this is the important one)

**"Credential handling"** in the form offers two choices. It is the key design decision of a
distribution.

| Choice | What is distributed | Fits |
|---|---|---|
| Distribute the values | The header values as well (stored encrypted, handed over only when the server starts) | A shared read-only token — everyone reads with the same credential |
| **"Each member enters the credential"** | **Only the endpoint and the header names**; each member enters the value in their own workspace | Per-person tokens, and anything you want attributable in an audit |

**A distributed value is readable inside every member's container.** For anything you want
attributed to a person, or any token with real power, choose "each member enters the
credential". Distributed that way, the member's card offers an **"Enter values"** flow (until
they do, the server is not used for them).

### When it takes effect

- Each member's workspace **fetches the distribution every 5 minutes**.
- It actually applies **from the next session they start**. Sessions already running do not
  change.
- **Disabling** keeps the definition but hands it to nobody. **Deleting** removes it from each
  workspace at its next fetch.
- For a member who has a **personal entry with the same name**, the distributed one wins (their
  screen says so). Pick names that are unlikely to collide.

### Practical notes

- Register it personally in your own workspace first and run the **connection test**; move to
  distribution once you know the configuration works.
- To rotate a distributed token, **replace the value in the distribution first**, then revoke
  the old token. Doing it the other way round breaks the tool for everyone until the next
  fetch.
- On a deployment with restricted egress, a distribution still fails if **the destination host
  is not allowed**. Check the allowlist below first.

## Speech (tenant-wide settings)

Speech is super_admin only (the **Speech** section of the Admin modal). It starts and stops the VOICEVOX (Zundamon) engine and
holds the **tenant-wide pronunciation dictionary**, which applies to everyone's text-to-speech
(a member's personal dictionary overrides the same spelling). When a product name or an
in-house term is consistently mispronounced, ask the super_admin to add it there.

## Inference engines (self-hosted GPUs — super_admin only)

An **Inference engines** section appears in the Admin modal only where the deployment actually
runs its own engines (`llamacpp` for conversation, `sdcpp` for images). Where it runs none, the
item is not there at all.

Each role takes one of three settings.

| | What it does |
|---|---|
| **Disabled** | The engine disappears from the launch menu and from `generate_image`, and requests are refused with 503. The box stops right away. |
| **On demand** | A GPU box is bought only when something asks, and it stops itself once nobody has used it for a while. **This is the default.** |
| **Always on** | The box is never stopped. Answers are faster, but **you are billed while nothing is using it**. |

"State" is not the setting you chose — it is what is actually running. Right after you press
Disabled the mode is disabled and the state says stopping, because the box does not vanish the
instant you press it. That is not a disagreement.

⚠️ **The first request waits.** A request to a stopped engine answers after roughly three
minutes, the time it takes to buy a box and load the model (the call itself completes in one
go — nothing has to be retried). Before time-critical work you can warm it up by switching to
Always on — **and remember to switch it back.**

### The GPU instance class

The selector under the modes appears only where the deployment **declares instance classes**. On
a deployment that declares none, **there is no such control** — the box is whatever was fixed at
deployment time.

It is for **temporarily moving a role onto a bigger GPU** in order to try a model that wants
more VRAM.

- **Choosing one does not change the box that is running.** It applies to **the next box
  bought**. While one is up the panel says "What is running is a g6.xlarge box" and puts
  **"Replace it now"** next to it.
- **Replacing costs one cold start** (about 9 minutes for llm, 3 for image); the new box fetches
  the model again. And **the new box does not start until the old one has left the cluster** —
  starting sooner runs into the account's GPU limit, where the placement *silently never
  happens* and it merely looks like a slow start.
- **On a stopped engine, choosing costs nothing.** It is picked up by the start whoever uses it
  next was going to pay for anyway.
- 🔴 **Left on a non-default class, the hourly rate stays up.** That is why a "not the default"
  tag and **"Back to the default"** are shown permanently while it is. **Put it back when you
  are done.**
- **Some classes cannot be bought.** Choose one above the account's GPU limit and the box never
  arrives; the ECS reason (`VcpuLimitExceeded`) is printed under the state as it came. Ask the
  deployment admin for a limit increase.

### The warning about whether a model fits

**When a model is enabled**, if what it wants exceeds the chosen class you are asked to confirm.
**It is not a refusal** — quantisation and offloading may still fit it, so you can go on
knowingly. Running short of VRAM does not slow CUDA down: it **crashes** it.

Where the figure came from is printed with it, and the three are not equally strong.

| Shown as | What it means |
|---|---|
| measured | The operator measured this model. |
| a weights-only floor | Derived from the file sizes: a **floor**, with nothing for the context. "At least this much". |
| unknown | Nobody measured it. 🔴 **That is not the same as saying it fits.** |

### Reading the current state

Below the setting is what that engine is doing right now. **A line that is not there means "not
known"** — nothing is padded out with a zero or a "not scheduled", so read the absences that way.

- **Box started** — when the GPU instance joined the cluster, how long ago that was, and the
  instance id. If it says **Service updated** instead, no box was found; that timestamp also
  moves on a deployment update, so it is not the box's own lifetime.
- **Stops by itself** — shown only for an engine running on demand. **It is absent under Always
  on**, because it does not stop. Same for Disabled and for an engine that is already stopped:
  the absence is the answer.
- **Requests in the last N min** — how many arrived in that window. ⚠️ This count lives in the
  **control plane's own memory**, so it resets to zero when the control plane is replaced. Until
  a full window has been counted you will see "this control plane has only been counting for …"
  beside it. **Last request**, next to it, **is stored**, so that one stays correct across a
  replacement. **A zero does not prove nobody is using the engine.**
- **Models** — the model names the stack declares. `(loaded)` means the engine is warm enough to
  actually answer; right after a start it reads `(declared, not loaded yet)`, and that is the
  three minutes above.
- **When a start is failing**, ECS's own reason is shown verbatim ("no container instance met
  all of its requirements", and so on) — you can read it here without opening the AWS console.

### History (14 days)

Opening **History** shows a heatmap of **24 hours down by date across**. One cell is one hour,
and the darker it is the longer the engine was up in that hour.

- The shade can mean one of two things. **Able to answer** is the time it could serve requests;
  **A box existed** adds the cold start (not able to answer yet) and the drain (the task is gone
  but the instance is not). **Those two also bill**, so use "a box existed" when reconciling
  against an invoice.
- 🔴 **A blank cell means "no record", not "it was stopped".** The control plane was not running,
  or the engine did not exist yet, and it cannot be filled in afterwards. Time the engine spent
  stopped is grey, which is a different colour from blank.
- **No amounts are shown.** Real cloud spend is only available per day, so an hourly figure could
  only be a rate times a number of seconds — an estimate. This screen answers "when was it
  running"; "what did it cost" is answered by the cloud cost screen.

## Controlling outbound traffic (egress — super_admin only)

This is the **Traffic** section of the Admin modal. **It is super_admin only, so it does not appear
in your tenant settings.** It belongs
to IT / the deployment administrator, so ask them when you need it (see the request template at
the end). The following is here so you know what happens on their side.

### log-only and enforce

- **log-only** — observes without blocking. Start here to **learn what actually flows**.
- **enforce** — **blocks** anything not on the allowlist.

The order matters. Switching straight to enforce also kills the traffic you need — agent CLI
updates, MCP endpoints, and so on. The correct progression is **observe in log-only → settle
the allowlist → enforce**.

### The allowlist, and requests from members

- Entries are added as a **host** or as **`.suffix.example.com`**, with an optional reason, and
  **retired** when no longer needed. With no added entries, only the product's built-in
  allowances apply.
- When a member requests access from ⚙ Settings → MCP servers, it lands here as
  **"Proposed (needs approval)"**. Review it and **approve** (it joins the allowlist) or
  **reject**. On the member's side it sits showing "waiting for an administrator's approval".
- **Observed destinations** shows, over a chosen period, where traffic actually went and
  whether it was **allowed / blocked / a block candidate**. Clearing the block candidates while
  still in log-only is what makes the switch to enforce uneventful.

## A template for asking upstream

For anything outside a tenant_admin's permission, ask the super_admin / IT. Include these three
and they can act on it as-is.

- **What**: the host to allow (`api.example.com` or `.example.com`)
- **Why**: which MCP server, for which work
- **How long**: permanent, or until when

---

- Previously: [03 Audit and usage](03-audit-usage.md)
- What members see: [member/12 Settings](../member/12-settings.md#mcp-servers)
