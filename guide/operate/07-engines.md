---
audience: "a deployment administrator (super_admin) running the self-hosted inference engines and the speech engine"
source_of_truth: "the Admin modal's Inference engines, API tokens and Read aloud sections; the engine stack's declarations for what exists"
updated: "2026-10"
---

# 07. Inference engines and read aloud: the Admin panel

English | [日本語](07-engines.ja.md)

The fleet's own GPU engines and the speech engine are run from one place, the Admin modal, which only a
super_admin sees. This chapter is that panel: what each mode costs, the GPU class, taking models in,
reading the state. Where an engine comes from is a different question, with a chapter each:
[08 your own ComfyUI](08-image-engine.md), [09 another deployment's engines](09-borrowed-engine.md) and
[10 your own llama.cpp](10-llm-lan.md). A tenant administrator sees a reduced model screen only if you
granted it ([admin 02](../admin/02-limits.md#taking-engine-models-in-only-if-your-tenant-was-granted-it)).

## Inference engines (self-hosted GPUs — super_admin only)

An **Inference engines** section appears in the Admin modal wherever the Control Plane serves it.
It does not wait for an engine to be registered. A deployment that runs no engine yet sees the
same section with an empty list, and the **API tokens** item beside it (the Hugging Face and Civitai
tokens, below) opens all the same. The chat role (llm) is a self-hosted chat engine; the image role
(image) is ComfyUI. A row that names an images provider this build cannot serve is not dropped
silently: the panel marks the row and shows the provider name, and `generate_image` does not work on
it until the row is pointed at a provider the build knows.

The section is two screens. **Inference engines** is the machine: one row per engine, with its
mode, state and instance. **Model catalogue** on a row opens the models as a pane of their own,
with **Text** / **Image** for the role and **Models** / **LoRAs** for what is listed; a LoRA is
never mixed into the checkpoint list.

Each role takes one of three settings.

| | What it does |
|---|---|
| **Disabled** | The engine disappears from the launch menu and from `generate_image`, and requests are refused with 503. The instance stops right away. |
| **On demand** | A GPU instance is bought only when something asks, and it stops itself once nobody has used it for a while. **This is the default.** |
| **Always on** | The instance is never stopped. Answers are faster, but **you are billed while nothing is using it**. |

"State" is not the setting you chose; it is what is actually running. Right after you press
Disabled the mode is disabled and the state says stopping, because the instance does not vanish the
instant you press it. That is not a disagreement.

Under an engine running on demand, **Stop after** is how many minutes it stays up after its last
use before it stops itself. The field states its own minimum (the start deadline, so an engine
is never stopped while it is still starting) and takes at most 24 hours. A saved value applies
from the controller's next pass and is written to the audit log. An externally managed engine has
no such field: it is never stopped from here.

⚠️ **The first request waits.** A request to a stopped engine answers after roughly three
minutes, the time it takes to buy an instance and load the model (the call itself completes in one
go; nothing has to be retried). Before time-critical work you can warm it up by switching to
Always on, **and remember to switch it back.**

### The GPU instance class

The selector under the modes appears only where the deployment **declares instance classes or
offers**. On a deployment that declares neither, **there is no such control**: the instance is
whatever was fixed at deployment time.

Where the deployment declares **offers** (the same GPU rungs, each with how it is bought,
**On-demand** or **Spot**), the Control Plane buys from that list: the offers are tried **from
the top, in the order they were declared**, and when one cannot be bought the next is tried.
The panel lists them under **Offers, tried from the top in the order they were declared**, and
shows **Current offer** and **Tried, in order** with what each answered: **got it**, **no
capacity**, **quota**, **budget spent**, **taken away**, **cannot be bought as declared**. The
selector then reads **Automatic (the default)**; choosing a rung **pins** it (the badge says
**pinned, not automatic**, and **Back to automatic** undoes it), and a pinned offer never falls
through to the next one. Everything below about the next instance, the cold start and putting
it back applies the same way under offers.

The selector is for **temporarily moving a role onto a bigger GPU** in order to try a model that wants
more VRAM.

- **Choosing one does not change the instance that is running.** It applies to **the next instance
  bought**. While one is up the panel says "What is running is a g6.xlarge instance" and puts
  **"Replace it now"** next to it.
- **Replacing costs one cold start** (about 9 minutes for llm, 3 for image); the new instance fetches
  the model again. And **the new instance does not start until the old one has left the cluster**:
  starting sooner runs into the account's GPU limit, where the placement *silently never
  happens* and it merely looks like a slow start.
- **On a stopped engine, choosing costs nothing.** It is picked up by the start whoever uses it
  next was going to pay for anyway.
- 🔴 **Left on a non-default class, the hourly rate stays up.** That is why a "not the default"
  tag and **"Back to the default"** are shown permanently while it is. **Put it back when you
  are done.**
- **Some classes cannot be bought.** Choose one above the account's GPU limit and the instance never
  arrives; the ECS reason (`VcpuLimitExceeded`) is printed under the state as it came. Ask AWS
  for a GPU limit increase on the account.

#### A Spot offer is not bought until "accept interruption" is ticked

A **Spot** row in the offers list does not make one buyable on its own. Only a role whose
**"Accept interruption (this role may buy Spot instances)"** box (under the list) is ticked has
that row as a candidate. Until then the row stays in the list, badged **"needs interruption
accepted"**, and its entry in the selector cannot be chosen: the declaration is visibly there
rather than silently ignored.

What is being accepted is not "the instance may stop". It is these two:

- **Whatever was being processed at that moment is lost**: on llm, a conversation is cut off
  mid-answer.
- **The next request waits out a cold start** (about 9 minutes for llm, 3 for image).

An offer taken away twice in a row is skipped for the rest of that start, so the engine ends up on
an on-demand offer whose VRAM fits. Un-ticking the box **leaves the Spot instance that is running
alone**; it applies to the next instance bought, exactly like a class change.

### The warning about whether a model fits

**When a model is enabled**, if what it wants exceeds the chosen class you are asked to confirm.
**It is not a refusal**: quantisation and offloading may still fit it, so you can go on
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
what that press costs (a download in MiB, or **no download (already held)** / **no download
(moved inside the bucket)** when the deployment already has the bytes), then the licence and any
warnings the source carries. A warning also names a part the family needs (a text encoder, a
VAE) whose destination another row already declares or an unfinished ingest job has recorded,
and says what to do first: complete that row, or dismiss that job. The Control Plane decides where each file goes and which parts the
family needs; nothing on the card asks for a role, a key or a file name, and a model split across
several files goes in with the same single press. The search can be narrowed to one **Family**
(the default is **Every family**), and it finds repositories laid out for ComfyUI as well as the
usual ones. On a phone the search, filter and sort stay put above the cards, the next page loads
when you reach the end, and the example images arrive as thumbnails.

The search asks one source at a time, and the tabs above it are what this deployment offers:
**Hugging Face** and **Civitai** always, and **Civitai Red** (the sister domain Civitai split off
for NSFW browsing) only where the deployment was given it. A standard deployment does not have
it: no tab, no switch, and a search that asks for it anyway is refused. Where it was given (the
control plane's `AF_ENGINE_CIVITAI_RED`), a super_admin turns it on and off under **API tokens**,
without a redeploy. 🔴 It is a choice about what this deployment OFFERS, not a content filter:
the plain Civitai tab also answers models carrying Civitai's NSFW level, which is why every card
shows that level, and pasting a `civitai.red` address into the ingest form works either way.

Every hit also says **what the source will not let you do, before anything is downloaded**: for a
Hugging Face repository **gated (accept the terms)** or **gated (the author approves)**; for a
Civitai file **login required** (the Civitai token, below, is what gets past it); and the
licence and content flags: **non-commercial**, **credit required**, **no derivatives**, **same
licence only**, **paid**, **early access (paid until a date)**, **not public**, **on-site
generation only (no download)**, **NSFW** (with Civitai's own level), **real person**, **minor**,
**virus scan not clean**, **pickle warning**. A hit that shows none of them could not be told
apart; that is not the same as "anyone may".

The card arrives with the **Family** already chosen where the upstream's name ("SDXL 1.0",
"Illustrious" and the like) maps onto one of this deployment's families, and with the author's
published Steps / CFG / sampler filled in under **Read out of the author's description
(unverified):**, the sentence they were read from beside them; **Use these** keeps them. Nothing
is guessed: an upstream name the deployment does not recognise leaves the family at **choose
one**, and the press is refused until you pick. What you save is kept on the row and, at
generation time, overrides the family's own recipe **field by field**; an empty field keeps the
recipe (the row's **Parameters** shows what is declared).

Below the registered rows sits the **S3 bucket** (the **Bucket** tab): every object the deployment
holds, what declares it, and what is still being taken in or failed. Bytes nobody declares can be
registered as a model or deleted there; a row whose files are incomplete is repaired from the row
itself (**complete**), never from a part. **Complete** also fixes a file that is right but sits under
the wrong key: it is moved inside the bucket (**Moved inside the bucket (no download)**) rather
than fetched a second time.

The registered cards are laid out in **as many columns as the pane is wide**, and each one folds
its own contents on the width it got, so a wide pane compares several models side by side instead
of one per row. The registered rows are grouped by **family**, with the family as the heading over each section
and a row of chips above them that narrows to one; the rows that declare no family come first,
because those are the ones the Control Plane refuses to enable. A chat engine's rows, which
declare no family at all, are grouped by **publisher** instead. Each card is titled with the name
the publisher gave the model and the version beside it ("MeinaMix — Meina V11"), the row's id kept
underneath, and the example image the source publishes on the right. Rows taken in before this
existed carry neither: **Fetch names and example images** in the toolbar reads their source pages
one at a time and fills them in, and the same press sits on an individual card as **Read the source
page**. Both disappear once every row has a name. A row whose source is a plain URL, and a row with
no recorded source at all, cannot be filled in; there is no model page behind either, and the card
says so. Nothing is copied into the bucket: the row records where the publisher put the picture and
the browser fetches it, so an image the publisher deletes leaves an empty frame that the same press
repairs.

A chat engine's rows are grouped by the **repository** they came from (one quantisation
repository is one model published at a dozen sizes) and the heading says how many of those sizes
this deployment holds. **Show the other quantisations in this repository** lists the rest, each
with its size, what the KV cache costs at the window in the box above the table, and whether the
total fits the GPU class this engine is set to buy: **Fits**, **Tight (n%)**, or **Will not fit
this class**, which names the smallest class that would hold it. **Add** on any of them opens the
ordinary plan card, pre-filled. The table is an estimate of the weights plus the KV cache and says
so (it cannot include llama.cpp's compute buffers or the CUDA context), and changing the window
above it re-prices the table without changing any row.

The same verdict is on the plan card when a model is taken in, beside a **Context** field that now
opens at the largest window that fits the class rather than at the model's published ceiling. The
ceiling is still shown, labelled as one. A 27B's KV cache is
66,560 MiB at a 262,144-token window and 8,320 MiB at 32,768, so a screen that priced the cache at
the ceiling reported every large model as impossible.

A registered card whose source can be read back carries one more press: **Other sizes…** on a
chat model from a Hugging Face repository (the same ladder, reached from any of its rows) and
**Other versions…** on an image model from Civitai (the model's other versions, each with its
size, whether it fits and whether it is already held; a version whose size the source does not
give shows no verdict). Both end on the ordinary plan card. Taking one in makes a new row; to
swap, press **Start with this** on the new row, then forget the old one. The catalogue also keeps
its search results, filters and scroll position while you look at another tab.

A row can be kept in order after the fact. Its **Files** list records where each file came from,
linked as **Source page**. **Edit** on the row changes the description, the family, the measured
VRAM, a chat model's context window and max output, a LoRA's **trigger words** and the generation
**Parameters**.

The verdict from the plan card is on a chat model's **Edit** too, with **Use the largest this
class holds (n)** as one press. And changing the role's GPU class re-fits every row's window to
the new class by itself, listing what moved (old → new) and what could not be fitted, with the
reason. It takes effect the next time the engine starts; a running engine keeps the window it was
started with.

For the image families somebody on this deployment has actually measured, both screens (the plan
card and **Edit**) print that measurement beside the field, with the conditions it was taken
under (the GPU it was measured on, the picture size, the batch, how many reference pictures). The
same run reads higher on a larger GPU, where nothing has to be offloaded. **Edit** offers it as
one press; the ingest never writes it. That is deliberate: this column means "the operator measured it", and
the deployment sizes the GPU it buys from it. Left blank, the ladder falls back to the sum of the
row's files, which for a split family is well above what the run uses (28,676 MiB against a
measured 20,862 for Qwen-Image-Edit) and buys a larger instance than the model needs.

Three of the image families are instruction editors, and each goes in as a split family with
parts of its own. **Qwen-Image-Edit 2509** and **2511** edit and inpaint only (no plain
generation, no strength, no size choice), from up to three reference pictures. **Qwen-Image 2.1**
both generates and edits (up to ten references) and **shares no file with them**: its
autoencoder is a different one that the loader accepts and decodes to noise, so its parts are
taken in as files of their own even where the edit families' are already held. The ComfyUI version a
Qwen-Image 2.1 row needs is covered under [registering models by hand](08-image-engine.md#registering-the-models-by-hand).

When a press is refused because something already holds its destination, the
card names it (**held by** the registered row, the ingest job, the bucket object or the running
task) and offers the one next step: **Register it**, **Complete it**, **Replace it**, **Forget the
row**, or **Dismiss the job**, which removes a finished line from the ingest history.

Starting an ingest is a **super_admin** action by default. You can grant it to a tenant (Admin modal → the
tenant → "Limits & idle" → **Inference engine model ingest**), and that tenant's administrators then get a
reduced model screen of their own. What the grant covers, what stays with you and what they see is written for
them in [admin 02](../admin/02-limits.md#taking-engine-models-in-only-if-your-tenant-was-granted-it). The grant never
makes the catalogue per tenant, as the paragraphs below explain.

Both tokens live under the Admin modal's **API tokens** item, which opens even before any engine
is registered. Without the Hugging Face token only ungated repositories can be taken in; without
the Civitai token, assets that require a logged-in account cannot. Each is one token for the whole
deployment, stored encrypted, read by the ingest task only and never handed to an engine instance.

🔴 **The catalogue is one per deployment and is not split per tenant. The id of a model taken in is
visible from every tenant.** That is deliberate: the GPU instance is shared between tenants, and every
enabled model is synced onto it at every start, so a per-tenant catalogue would push the cold start
past ten minutes **at about five tenants, even with one model each** (measured; the sync is serial at
roughly 159 MB/s). The boundary worth having is the GPU instance, not the catalogue. **Do not read this
grant as isolation between tenants.**

### Reading the current state

Below the setting is what that engine is doing right now. **A line that is not there means "not
known"**. Nothing is padded out with a zero or a "not scheduled", so read the absences that way.

- **Instance started**: when the GPU instance joined the cluster, how long ago that was, and the
  instance id. If it says **Service updated** instead, no instance was found; that timestamp also
  moves on a deployment update, so it is not the instance's own lifetime.
- **Stops by itself**: shown only for an engine running on demand, at the time **Stop after**
  gives it. **It is absent under Always on**, because it does not stop. Same for Disabled and for an engine that is already stopped:
  the absence is the answer.
- **Requests in the last N min**: how many arrived in that window. ⚠️ This count lives in the
  **control plane's own memory**, so it resets to zero when the control plane is replaced. Until
  a full window has been counted you will see "this control plane has only been counting for …"
  beside it. **Last request**, next to it, **is stored**, so that one stays correct across a
  replacement. **A zero does not prove nobody is using the engine.**
- **Models**: the model names the stack declares. `(loaded)` means the engine is warm enough to
  actually answer; right after a start it reads `(declared, not loaded yet)`, and that is the
  three minutes above.
- **When a start is failing**, ECS's own reason is shown verbatim ("no container instance met
  all of its requirements", and so on); you can read it here without opening the AWS console.

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
  only be a rate times a number of seconds, which is an estimate. This screen answers "when was it
  running"; "what did it cost" is answered by the cloud cost screen. The one real price this
  deployment knows is the hourly rate of each instance it bought, and that is written once, in the
  audit log's `engine.<engine>.offer` line, with the instance type that was actually bought
  ([admin 03](../admin/03-audit-usage.md)).

## Read aloud (the speech engine and the shared dictionary)

The **Read aloud** section of the Admin modal has two jobs: it starts and stops the VOICEVOX
(Zundamon) engine, and it holds the **tenant-wide pronunciation dictionary**.

- **The engine** takes the same three modes as the inference engines (**Disabled** / **On demand** /
  **Always on**). Starting takes 1–2 minutes, and until it is up Japanese read-aloud is covered by
  Polly (silent where Polly is not set). On demand, the engine starts once demand builds up (2,000
  characters in 5 minutes) and stops after 30 minutes with nobody listening; every automatic start
  and stop is in the audit log. Disabling moves read-aloud to Polly at once and, on AWS, sets the ECS
  desired count to 0 about a minute later. A deployment with no VOICEVOX engine that is not
  ECS-managed either has nothing this screen could start, and the switch stays at disabled. A
  speech engine cannot be borrowed from another deployment ([09](09-borrowed-engine.md)).
- **The dictionary** applies to everyone's text-to-speech (a member's personal dictionary overrides
  the same spelling): one `spelling=reading` per line, `#` for a comment. Other members pick up a saved
  change on their next Console load. When a product name or an in-house term is consistently
  mispronounced, a tenant administrator will ask you to add it there.

## What a picture keeps out

Three places say what should NOT be drawn, and they are added together rather than overriding
one another:

- **the model row's own**: what this checkpoint's publisher recommends keeping out. Register it
  in the same panel, on the row.
- **the member's**, per request (`negative_prompt` on the image tool).
- **yours, for the whole engine**: the box labelled "excluded from every image". It applies to
  every request, whoever made it and whichever checkpoint answers.

When all three are empty, a fixed default is used (`blurry, lowres, deformed, watermark, text`).
A row that declares its own replaces that default rather than being added to it.

🔴 **This is a negative prompt, not a content filter.** The words are handed to the sampler as
something to steer away from. They are not a gate, a determined prompt outweighs them, and, most
importantly, **the distilled families ignore them completely**: Z-Image and FLUX.2 klein
sample at cfg 1, where the negative branch cancels out exactly, and FLUX.1 has no negative
input at all. The words reach the guided families (SD 1.5, SDXL, SD3.5, Anima, Krea 2,
Qwen-Image-Edit 2509 / 2511), and even there a row at cfg 1 cancels them the same way: a Turbo
variant, or Krea 2 and Qwen-Image 2.1, whose recipes start at cfg 1 unless the row raises it.
A request answered by a model that ignores them **says so in its warnings**, naming the
family. If a deployment needs a guarantee about what can be produced, this is not where it
lives.

The family also decides the size a request gets when it names none: the megapixel-era
families generate at 1024×1024, SD 1.5 at 512×512, because asking that UNet for 1024 returns
a plausible picture with the subject duplicated rather than an error. A row's own size list
still overrides the family's.
