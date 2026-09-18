---
audience: "a tenant administrator distributing integrations to the team"
updated: "2026-09"
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

An **Inference engines** section appears in the Admin modal wherever the Control Plane serves it
— it does not wait for an engine to be registered. A deployment that runs no engine yet sees the
same section with an empty list, and the **API tokens** item beside it (the Hugging Face and Civitai
tokens, below) opens all the same. The chat role (llm) is a self-hosted chat engine; the image role
(image) is ComfyUI. A row that names an images provider this build cannot serve is not dropped
silently: the panel marks the row and shows the provider name, and `generate_image` does not work on
it until the row is pointed at a provider the build knows.

The section is two screens. **Inference engines** is the machine: one row per engine, with its
mode, state and instance. **Model catalogue** on a row opens the models as a pane of their own,
with **Text** / **Image** for the role and **Models** / **LoRAs** for what is listed — a LoRA is
never mixed into the checkpoint list.

Each role takes one of three settings.

| | What it does |
|---|---|
| **Disabled** | The engine disappears from the launch menu and from `generate_image`, and requests are refused with 503. The instance stops right away. |
| **On demand** | A GPU instance is bought only when something asks, and it stops itself once nobody has used it for a while. **This is the default.** |
| **Always on** | The instance is never stopped. Answers are faster, but **you are billed while nothing is using it**. |

"State" is not the setting you chose — it is what is actually running. Right after you press
Disabled the mode is disabled and the state says stopping, because the instance does not vanish the
instant you press it. That is not a disagreement.

⚠️ **The first request waits.** A request to a stopped engine answers after roughly three
minutes, the time it takes to buy an instance and load the model (the call itself completes in one
go — nothing has to be retried). Before time-critical work you can warm it up by switching to
Always on — **and remember to switch it back.**

### The GPU instance class

The selector under the modes appears only where the deployment **declares instance classes or
offers**. On a deployment that declares neither, **there is no such control** — the instance is
whatever was fixed at deployment time.

Where the deployment declares **offers** — the same GPU rungs, each with how it is bought,
**On-demand** or **Spot** — the Control Plane buys from that list: the offers are tried **from
the top, in the order they were declared**, and when one cannot be bought the next is tried.
The panel lists them under **Offers, tried from the top in the order they were declared**, and
shows **Current offer** and **Tried, in order** with what each answered — **got it**, **no
capacity**, **quota**, **budget spent**, **taken away**, **cannot be bought as declared**. The
selector then reads **Automatic (the default)**; choosing a rung **pins** it (the badge says
**pinned, not automatic**, and **Back to automatic** undoes it), and a pinned offer never falls
through to the next one. Everything below about the next instance, the cold start and putting
it back applies the same way under offers.

It is for **temporarily moving a role onto a bigger GPU** in order to try a model that wants
more VRAM.

- **Choosing one does not change the instance that is running.** It applies to **the next instance
  bought**. While one is up the panel says "What is running is a g6.xlarge instance" and puts
  **"Replace it now"** next to it.
- **Replacing costs one cold start** (about 9 minutes for llm, 3 for image); the new instance fetches
  the model again. And **the new instance does not start until the old one has left the cluster** —
  starting sooner runs into the account's GPU limit, where the placement *silently never
  happens* and it merely looks like a slow start.
- **On a stopped engine, choosing costs nothing.** It is picked up by the start whoever uses it
  next was going to pay for anyway.
- 🔴 **Left on a non-default class, the hourly rate stays up.** That is why a "not the default"
  tag and **"Back to the default"** are shown permanently while it is. **Put it back when you
  are done.**
- **Some classes cannot be bought.** Choose one above the account's GPU limit and the instance never
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

### Taking models in, and what that has to do with tenants

Taking a model in is one press: **search** the model, press **add** on its card, read the plan the
card shows and press **take in**. The plan lists every file the model needs, one line each, with
what that press costs — a download in MiB, or **no download (already held)** / **no download
(moved inside the bucket)** when the deployment already has the bytes — then the licence and any
warnings the source carries. The Control Plane decides where each file goes and which parts the
family needs; nothing on the card asks for a role, a key or a file name, and a model split across
several files goes in with the same single press. The search can be narrowed to one **Family**
(the default is **Every family**), and it finds repositories laid out for ComfyUI as well as the
usual ones. On a phone the search, filter and sort stay put above the cards, the next page loads
when you reach the end, and the example images arrive as thumbnails.

The search asks one source at a time, and the tabs above it are what this deployment offers:
**Hugging Face** and **Civitai** always, and **Civitai Red** — the sister domain Civitai split off
for NSFW browsing — only where the deployment was given it. A standard deployment does not have
it: no tab, no switch, and a search that asks for it anyway is refused. Where it was given (the
control plane's `AF_ENGINE_CIVITAI_RED`), a super_admin turns it on and off under **API tokens**,
without a redeploy. 🔴 It is a choice about what this deployment OFFERS, not a content filter —
the plain Civitai tab also answers models carrying Civitai's NSFW level, which is why every card
shows that level, and pasting a `civitai.red` address into the ingest form works either way.

Every hit also says **what the source will not let you do, before anything is downloaded**: for a
Hugging Face repository **gated (accept the terms)** or **gated (the author approves)**; for a
Civitai file **login required** (the Civitai token, below, is what gets past it); and the
licence and content flags — **non-commercial**, **credit required**, **no derivatives**, **same
licence only**, **paid**, **early access (paid until a date)**, **not public**, **on-site
generation only (no download)**, **NSFW** (with Civitai's own level), **real person**, **minor**,
**virus scan not clean**, **pickle warning**. A hit that shows none of them could not be told
apart — that is not the same as "anyone may".

The card arrives with the **Family** already chosen where the upstream's name ("SDXL 1.0",
"Illustrious" and the like) maps onto one of this deployment's families, and with the author's
published Steps / CFG / sampler filled in under **Read out of the author's description
(unverified):**, the sentence they were read from beside them; **Use these** keeps them. Nothing
is guessed: an upstream name the deployment does not recognise leaves the family at **choose
one**, and the press is refused until you pick. What you save is kept on the row and, at
generation time, overrides the family's own recipe **field by field** — an empty field keeps the
recipe (the row's **Parameters** shows what is declared).

Below the registered rows sits the **S3 bucket** (the **Bucket** tab) — every object the deployment
holds, what declares it, and what is still being taken in or failed. Bytes nobody declares can be
registered as a model or deleted there; a row whose files are incomplete is repaired from the row
itself (**complete**), never from a part. **Complete** also fixes a file that is right but sits under
the wrong key: it is moved inside the bucket (**Moved inside the bucket (no download)**) rather
than fetched a second time.

The registered rows are grouped by **family**, with the family as the heading over each section
and a row of chips above them that narrows to one — the rows that declare no family come first,
because those are the ones the Control Plane refuses to enable. A chat engine's rows, which
declare no family at all, are grouped by **publisher** instead. Each card is titled with the name
the publisher gave the model and the version beside it ("MeinaMix — Meina V11"), the row's id kept
underneath, and the example image the source publishes on the right. Rows taken in before this
existed carry neither: **Fetch names and example images** in the toolbar reads their source pages
one at a time and fills them in, and the same press sits on an individual card as **Read the source
page**. Both disappear once every row has a name. A row whose source is a plain URL, and a row with
no recorded source at all, cannot be filled in — there is no model page behind either, and the card
says so. Nothing is copied into the bucket: the row records where the publisher put the picture and
the browser fetches it, so an image the publisher deletes leaves an empty frame that the same press
repairs.

A chat engine's rows are grouped by the **repository** they came from — one quantisation
repository is one model published at a dozen sizes — and the heading says how many of those sizes
this deployment holds. **Show the other quantisations in this repository** lists the rest, each
with its size, what the KV cache costs at the window in the box above the table, and whether the
total fits the GPU class this engine is set to buy: **Fits**, **Tight (n%)**, or **Will not fit
this class**, which names the smallest class that would hold it. **Add** on any of them opens the
ordinary plan card, pre-filled. The table is an estimate of the weights plus the KV cache and says
so — it cannot include llama.cpp's compute buffers or the CUDA context — and changing the window
above it re-prices the table without changing any row.

The same verdict is on the plan card when a model is taken in, beside a **Context** field that now
opens at the largest window that fits the class rather than at the model's published ceiling. The
ceiling is still shown, labelled as one. This matters more than it sounds: a 27B's KV cache is
66,560 MiB at a 262,144-token window and 8,320 MiB at 32,768, so a screen that priced the cache at
the ceiling reported every large model as impossible.

A row can be kept in order after the fact. Its **Files** list records where each file came from,
linked as **Source page**. **Edit** on the row changes the description, the family, the measured
VRAM, a chat model's context window and max output, a LoRA's **trigger words** and the generation
**Parameters**. When a press is refused because something already holds its destination, the
card names it — **held by** the registered row, the ingest job, the bucket object or the running
task — and offers the one next step: **Register it**, **Complete it**, **Replace it**, **Forget the
row**, or **Dismiss the job**, which removes a finished line from the ingest history.

That press — putting the file in the bucket and creating a catalogue row — can be started by a
**super_admin only**, by default. Where the operator grants it
to a tenant, that tenant's **tenant_admins can take models in too** (Admin modal → the tenant →
"Limits & idle" → **Inference engine model ingest**).

The grant covers **starting an ingest and nothing else**. These four stay super_admin whether it is
granted or not:

- **Enabling** a model (making it something members can choose)
- Changing the image role's **selected checkpoint**
- **Forgetting** a row
- Registering the deployment's **Hugging Face token** and **Civitai token**

Both tokens live under the Admin modal's **API tokens** item, which opens even before any engine
is registered. Without the Hugging Face token only ungated repositories can be taken in; without
the Civitai token, assets that require a logged-in account cannot. Each is one token for the whole
deployment, stored encrypted, read by the ingest task only and never handed to an engine instance.

🔴 **The catalogue is one per deployment and is not split per tenant. The id of a model taken in is
visible from every tenant.** That is deliberate: the GPU instance is shared between tenants, and every
enabled model is synced onto it at every start — so a per-tenant catalogue would push the cold start
past ten minutes **at about five tenants, even with one model each** (measured; the sync is serial at
roughly 159 MB/s). The boundary worth having is the GPU instance, not the catalogue. **Do not read this
grant as isolation between tenants.**

Who accepted which licence is kept on the row: **which tenant, which person, when, and which
licence** — the same four in the audit log. A model taken in by a super_admin has no tenant against
it, which means "the operator accepted on behalf of the whole deployment".

#### What a tenant administrator sees

A tenant_admin of a granted tenant gets a reduced screen under **Tenant settings → "Inference
engine models"** (not the Admin modal — that one is super_admin only). It holds four things:
**the model search, the plan card, the bucket entries their own tenant's jobs produced, and the list
of catalogue rows** (id, name, family, licence and on/off — **read-only**). What it does not hold:
**the mode (disabled / on demand / always on), the GPU class, the state of the instance, enabling
and selecting a model, forgetting a row, and the API tokens** — each of those decides what
every *other* tenant runs, or who pays for the GPU. The bucket shows **that tenant's own objects
only**, and a super_admin's do not appear in it (a super_admin's own screen shows the whole bucket).

### Reading the current state

Below the setting is what that engine is doing right now. **A line that is not there means "not
known"** — nothing is padded out with a zero or a "not scheduled", so read the absences that way.

- **Instance started** — when the GPU instance joined the cluster, how long ago that was, and the
  instance id. If it says **Service updated** instead, no instance was found; that timestamp also
  moves on a deployment update, so it is not the instance's own lifetime.
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
  **An instance existed** adds the cold start (not able to answer yet) and the drain (the task is gone
  but the instance is not). **Those two also bill**, so use "an instance existed" when reconciling
  against an invoice.
- 🔴 **A blank cell means "no record", not "it was stopped".** The control plane was not running,
  or the engine did not exist yet, and it cannot be filled in afterwards. Time the engine spent
  stopped is grey, which is a different colour from blank.
- **No amounts are shown.** Real cloud spend is only available per day, so an hourly figure could
  only be a rate times a number of seconds — an estimate. This screen answers "when was it
  running"; "what did it cost" is answered by the cloud cost screen. The one real price this
  deployment knows is the hourly rate of each instance it bought, and that is written once, in the
  audit log's `engine.<engine>.offer` line, with the instance type that was actually bought
  ([03](03-audit-usage.md)).

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
