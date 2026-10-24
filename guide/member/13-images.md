---
audience: "anyone looking at, generating or refining images"
---

# 13. Images — gallery, generation, studio

English | [日本語](13-images.ja.md)

This chapter covers images: the gallery of images in a workspace, generating images and refining them in the studio, and having a session generate an image.

## Image gallery

A pane that lays the images in a folder out as cards, so you can compare them without opening one
at a time and see at a glance what a folder actually holds.

**Six ways in**, each of them one item in a menu that is already there:

- **Right-click a folder** in the file tree → "Open in gallery"
- **Right-click an image file** in the file tree → "Open in gallery" (its parent folder opens with
  that image enlarged)
- **Right-click any other file** → "Open folder in gallery" (its parent folder, no image focused)
- **"Gallery"** in the image viewer's header
- **"Open folder"** in the enlarged view of a shared-file card in the mirror
- **"Generated images (N)"** in a session's context menu (below)

A plain click uses the current pane; Ctrl/⌘-click and middle-click open another one. A phone has no
"beside", so it opens in place.

**What you see, and what you can do**

- **Sort**: "Newest" or "By name". The choice is remembered per pane, so moving between tabs does
  not reset it. When the workspace's Agent is old enough not to send modification times, the
  gallery falls back to name order and shows no relative time ("3 minutes ago") either.
- **Count and size**: the header carries the totals for the whole folder. A big folder stops at
  **300 images**, with "Show more" for the rest (each card is one thumbnail request).
- **A folder you have already seen opens at once**: the gallery remembers how each folder it
  has walked through looked, so coming back draws those cards immediately instead of waiting.
  **Your scroll position and anything you expanded with "Show more" come back with them**, so a
  folder you were halfway through carries on where you left it. It re-reads the folder behind
  that, and says "updating…" beside the count while it does; anything that really did arrive
  since appears afterwards with the usual highlight. If the folder itself is gone, you get an
  error rather than the contents it remembered.
- **Loading**: the first time you open a folder, while nothing has arrived yet, the pane shows a
  loading state (it never sits there showing only "Up"). Thumbnails are requested for the cards
  nearest the screen first, so opening a big folder does not fetch everything at once, and
  scrolling ahead does not get stuck behind pictures you have already scrolled past.
- **Folders**: subfolders are cards too, and each one shows **the newest picture inside it as
  a cover** (the folder mark becomes a small badge over the picture), with **how many pictures
  are in it** underneath. A folder named by a session's UUID is no longer a guess. Opening one
  moves this pane into it. The first card,
  "Up", goes back to the parent, and the **breadcrumb** in the row under the header
  (`.cache / agent-fleet / generated`) jumps to any level. Ctrl/⌘-click and middle-click open the
  folder in another pane.
- **An always-on "Up", and the browser's Back button**: that breadcrumb row also has a
  **permanent "Up" button** (disabled at the root), so a long folder whose "Up" card has scrolled
  off screen is still one click from its parent. Folder navigation is retraced by the browser's
  own **Back button** too: however you got somewhere ("Up", the breadcrumb, or a card), the same
  number of Back presses gets you the same distance back.
- **Include subfolders**: the button at the end of the breadcrumb row lists **every picture up to
  three levels down** in one grid, newest first; each card's name shows the subfolder it is in.
  The walk is bounded, so in a very large tree you get the newest ones and the header says
  "newest N only". It switches itself off when you move to another folder.
- **Cards**: click the card to **enlarge** (← / → move through the folder, and "3 / 12" tells you
  where you are; **on a phone, swipe left and right** to move, only at fit, because while you are
  zoomed in a drag pans the picture); the button in the corner **opens it in the file pane**. Just
  looking never costs you a pane. **Right-click a card** (or press the Menu key) for **send to a
  session / assistant**, copy path / copy name, rename and delete (on a folder, also "Open in another pane") and, in a session's
  folder, a jump to the session that generated the pictures
  ([menus](badges-and-menus.md#cards-in-the-image-gallery)).
- **Refresh**: on open, on returning to the tab, and every 20 seconds while a session is running.
  New arrivals get the same highlight as the file tree, so you see a generation land. "Refresh" in
  the header re-reads at any time.
- Cards show a **downscaled copy**, requested at your screen's own pixel density: a sharp
  display gets a sharper thumbnail, a plain one is not sent pixels it cannot show. When you
  enlarge, that downscaled copy is what you see first (slightly blurred) and it sharpens when
  **a copy sized to your screen** arrives. That copy is re-made from the original pixels; for a
  generated picture it is roughly 120 KB against 1.1 MB, so it looks the same and only the wait
  shrinks. The neighbouring picture is fetched ahead, so ← / → do not wait.
  **When you need the real file** (to save it, or to inspect detail at full size), open it in the
  file pane with the button in the card's corner, or download it; those are still the original.

**Images a session generated**

Images a session made with its image tool stay in a per-session folder for 30 days. That folder is named
after the session's internal id, so the file tree is no way in. **"Generated images (N)" in the
session's context menu** is the way, and it appears **only for sessions that have generated
something** (N is how many). It is absent while the workspace is stopped, because reading the images is
the Agent's job, and the Agent lives in the workspace.

To see **all of them at once**, an **"Open generated images"** button opens the parent folder from
four places: the **minimap's button row**, the leader key **`g g`** (and the command palette), the
**Files** section header in the left pane, and the **image-generation pane's header**. It opens on
a page of cards: one folder per session, plus the image-generation pane's output (`console`). A session's
folder is labelled with **the session's name and its image count**, not its internal id. Inside
such a folder the breadcrumb row carries a button with that session's name, and pressing it opens
the conversation the pictures came from (it is absent once the session is gone).

## Image generation

A pane that makes pictures on your organisation's own ComfyUI **without an agent in the loop**.
Asking a session for a picture is right for "put an illustration in this document"; this is for
"forty variations of one prompt at three CFG values", where every round trip through a model
would cost a turn. A session can name the same settings you can
([below](#having-a-session-generate-an-image)). What a session cannot see is what this form shows
you: each checkpoint's own published numbers as the placeholders, the fields its family does not
read greyed out, and a trial run before you commit forty. Three fields appear only for the family
that reads them: **clip skip** for SD1.5 and SDXL (SDXL already runs at 2, so only 3 and up change
its picture), **guidance** for FLUX.1 (what its model cards call CFG; 3.5 to 5), and **shift** for
Anima (3 by default; 14 to 24 steadies the composition of a large picture).

**Two ways in:** the workspace action bar's **Images**, and the leader key **`g i`**. Opening it
again focuses the pane you have. The pane always has a **studio** open: the one this browser
opened last, else the one changed most recently on any device; only when you have no studio at all
is a new, empty one made. The draft is saved in the workspace, so what you started on a PC carries on
from your phone. To refine the draft in conversation with an agent, attach one to that studio
([below](#studio-refine-the-draft-by-talking-to-an-agent)).

**Before you generate**

- **Model**: the checkpoints your administrator has enabled, under **the name their publisher
  gave them** ("unsloth/Qwen-Image-GGUF Q8_0", enough to tell two sizes of one model apart).
  One whose source page your administrator has not read yet appears under its internal id instead.
  A dot marks one that is already
  loaded on the engine; anything else means the first picture waits for the engine to start.
  The header says which of the four states you are in: **ready**, **the engine is asleep**
  (with roughly how long the first picture will take), **the engine is starting**, or **no
  engine available**.
- **The family card**: under the model, one line per thing that family expects: whether it
  wants a tag list, sentences, or both (Anima takes both: tags for the subject, a sentence or two
  for composition and positions, which controls it more finely), the prefix that dialect usually opens with (offered as a chip;
  nothing is ever written into your prompt on its own), whether a negative prompt reaches it
  at all, and the step and cfg ranges worth staying inside. The families the catalogue knows
  run from SD 1.5 and SDXL to FLUX, Anima and Krea 2, the instruction-edit models Qwen-Image-Edit
  (2509 and 2511) and Qwen-Image 2.1, and **the default size follows the family**:
  SD 1.5 starts at 512 rather than the megapixel square the others share, because asking it
  for more gives you a doubled subject, not an error.
- **Fields a family does not read are disabled, with the reason on them.** `flux1` and
  `flux2-klein` ignore cfg; the distilled families (and a checkpoint of a guided family that
  runs at cfg 1) ignore the negative prompt. That comes from the workspace, not from a table in
  the browser, so it cannot disagree with what actually runs.
- **The administrator's negative** shows as a chip you cannot remove, next to the deployment-wide
  one. It is applied on top of yours rather than mixed into your text.
- **LoRAs**: only the ones that match the model's family are listed. Each row shows the
  **trigger words** it would bring, so you can choose between two adapters before ticking
  either; ticking one turns those words into chips over the prompt, and unticking it removes
  those chips and nothing else. A missing trigger is the usual reason a LoRA "does nothing",
  and if you generate without one, the result says so in as many words. The weight starts at
  the strength the adapter's author published, or 1 when the catalogue has none.

**Trial run, then the batch**

- **Trial run (`Ctrl+Enter`)** puts **one** picture at the **head** of the queue, at fewer steps,
  into `generated/console/trial/`. It appears in "Latest trial" beside the form with its seed and
  how long it took. **"Use this seed"** pins it, which is the whole point of trying before a
  sweep. Tick **Full steps** when the trial is meant to be the picture. Trial pictures are swept
  after 7 days.
- **Enqueue N (`Ctrl+Shift+Enter`)** puts N jobs (one picture each) at the tail as one group.
  The seed follows your choice: **random each time**, **fixed** (the knob for "same picture, vary
  the cfg"), or a **sequence**.
- `batch_size` under **Advanced** is a different thing: pictures sampled together inside one job.
  Faster on a card with headroom, an out-of-memory after a five-minute wait on one without, and
  no per-picture cancel. It stays at 1 unless you know the card.
- **Operation** under Advanced switches **Generate** to **Edit** or **Inpaint**. An edit takes
  **reference images** (a path in the workspace, or a file dropped onto the field) and a slider,
  **"How much of the input to change"**, from 0 to 1; the size fields step aside, because on an
  edit the input's own dimensions win. **Inpaint** adds a **"Mask (white is repainted)"** field
  that takes one file the same way (a path, or a file dropped onto it); until a mask is set,
  neither the trial nor the batch can be sent: **"Inpaint needs a mask image"**.
- **How many reference pictures you may add depends on the model.** The field's heading counts
  them ("2 of 2"), and at the ceiling the add field itself goes away. The instruction-edit models
  (the kind you ask in words, "change the sign to CLOSED") read **three**: the **first is the
  picture being redrawn** and the **rest are things to bring into it** ("put the plant from
  picture 2 and the duck from picture 3 on the table"). Every other model reads one. On a model with no slider, how much changes is decided by the instruction itself.
  These models offer **Edit** and **Inpaint** only (there is no Generate, since they cannot work
  without a picture), so choosing one while the operation is set to Generate switches it and says
  so (**"… does not support the previous operation — switched to Edit"**). The whole picture is
  edited (scaled to the model's size, never cropped), and a mask of another size is
  stretched over it, so it marks the same region either way. An edited picture's
  properties record the prompt, the negative and the size, as a generated one's do. **Qwen-Image 2.1** does both:
  it generates from a prompt alone, with the size fields, and edits by instruction with up to
  **ten** reference pictures.

**While it runs**

A group is one row: "12 / 40", a bar (done, failed, and the picture in flight filling by time
against the usual duration; it stops short of the end rather than claiming a completion nobody
has seen), and roughly how long is left. **Pause**, **resume**, **skip the current picture** and
**abort** all act on the group; the ✕ on a line cancels that one picture. Pausing everything
still lets trials through; that is what pausing is for. A batch left paused long enough lets the
engine go to sleep, and the row says so. The row also names the phase the current picture is in:
**starting the engine**, **uploading**, **sampling**, **fetching**, with its own seconds, and a
picture that had to wait for the engine to start still gets its full time to sample afterwards.

The pane polls only while something is unfinished, and stops while the tab is in the background.

**Where the pictures go, and getting the numbers back**

The default folder is `generated/console/`, which the gallery lists like any other and which is
**never swept**, since you pressed the button for each of these. **Output folder** under Advanced puts
a run somewhere of your own naming. Every picture is written with a small record beside it, so
enlarging one anywhere in the Console (this pane, the gallery, a shared file in the mirror) and
pressing **Properties** shows the model, seed, size, steps, cfg, sampler, scheduler (and clip
skip, guidance or shift where set), LoRAs and
both prompts, each row with a copy button, plus **copy all as JSON** and **open in image
generation**, which loads the fields back into the form. Pictures made by an agent before this
existed can still be read: the graph ComfyUI embeds in the PNG carries the same numbers. Pictures
from the vendor routes (codex, agy) carry nothing, and the panel says that rather than showing
empty rows.

### Studio — refine the draft by talking to an agent

The form above makes pictures on its own. A **studio** is the way to refine what is in that form
(the **draft**) in conversation with a session's agent. Say "read `docs/chars/aoi.md` and put
her in a harbour at dusk", then "darker", and the agent reads the material and rewrites the draft.
**A person always presses the generate buttons**, and pressing runs the same queue as above; the
agent never paints a batch on its own.

**Attaching an agent**

- Pressing **Attach an agent** starts one session and binds it to this studio. Merely opening the
  pane creates no session. If the session fails to start, the studio and its draft stay, unbound.
- **Choose a model first.** Prompts are written differently per model (SDXL-family, anima and
  Qwen-Image prompts are built differently), so the button stays disabled until one is chosen. Clear
  the model after attaching and the conversation's input is held until you choose again; switch to
  another model and the agent rewrites the prompt for it.
- You choose what you choose when starting any session: agent, model, effort, execution method,
  repository, subfolder, worktree, and permission prompts. **There is no prompt field**: the
  first turn is the studio's description of the agent's role. Until it arrives the pane says it
  is sending it, and it offers **Resend** only when sending failed or could not be confirmed.
- **Execution method**: which agents can be attached, and under which execution method (some
  work in Terminal (CLI) only, and one cannot be attached yet), is the "Bound to an image
  studio" row of the [Agents reference](../ref/agents.md) and its footnote. Pick an agent
  the row marks and a method it allows; anything else is refused.
- **Worktree is On by default**, so the agent can tell for certain which studio's session it is.
  Only Terminal (CLI) and lcpp let you turn it off. From inside a worktree the agent **cannot see
  uncommitted material** in your original working copy; commit what you want it to read.
- **Switch agent** rebinds the studio to a new session at any time (sonnet to opus, claude to
  codex). The draft, the edit history and the versions belong to the studio and stay. Opening a
  studio's session opens this pane, not the mirror.

**The draft changes as you talk**

- The left column is the conversation (the same view as the mirror), the middle one the draft,
  the right one results and history. A narrow pane folds them into **Chat / Settings /
  Results** tabs.
- **On a phone**, while you type the head, tabs and suggestions step aside and the draft bar (the
  draft's first line and **Trial**) stays above the keyboard. "N pictures are ready" stays under
  the tabs until you look at the results or close it.
- The agent can rewrite the prompt, the negative, steps / cfg / sampler / scheduler, the size,
  the LoRAs, the operation, the reference images and "how much to change the original". A field
  it moved stays outlined until you edit that field or press trial or enqueue.
- **Model, seed, count, output folder, label and mask image are yours alone.** Switching the
  model switches the family, and with it which fields are read, the sizes, and how long the first
  picture waits.
- **Locks (🔒)**: per field. A write to a locked field is dropped for that field only and the
  agent is told why. Nothing locks itself.
- A message you send from the conversation carries one short trailing cue that tells the agent
  "the draft changed, there are new results". The conversation view does not show it.

**People press trial and enqueue**

- The agent can only **trial**, and only **the draft exactly as it is on screen, one picture**;
  it has no way to pass anything of its own. The studio setting **Allow the agent to trial**
  (On by default) turns that off. The switch applies from the agent's next trial, in a session
  that is already running too; while it is off, the agent is told you have to turn it on.
- **Enqueueing N pictures is yours alone.**
- **The image tool is not available to a studio's session** (the agent does not see it, and a
  call is refused), so this pane is the one road to a picture. A CLI's own built-in image tool
  (codex's, for instance) is outside that limit.

**Edit history, rewind and versions**

- **Edit history**: one entry each time the draft changes: the time, who wrote it (the agent,
  you, or a rewind), and each changed field before and after. **Restore this point** is on the
  middle column's **Edit history** and on the "Draft updated" cards in the conversation. Nothing
  is deleted; the restore is itself an entry. Being your action, it restores every field
  including locked ones, and leaves the locks as they are.
- **Versions**: a copy of the draft at the moment someone pressed trial, enqueue, or the
  agent's trial. What you want to compare is "what was pressed", so editing alone makes no
  version, and pressing twice without editing makes two.

**Picture history and "Restore these settings"**

- The right column's **History** lists the studio's pictures, newest first. Each picture
  remembers the version it came from, and that survives a Workspace restart.
- **Restore these settings** puts back the draft that picture was made from (an edit-history entry,
  like any rewind). **Use as reference** places it as the next edit's reference image.
- You can have as many studios as you like and switch between them at the top of the pane. Its
  **+ New studio** (in the command palette, "New image studio") opens an empty studio in a pane
  beside this one, leaving the current studio as it is (on a phone it opens in the current pane
  instead of splitting the screen; switch back here). A studio you have not named is listed by
  when it was made ("Studio 9/27 14:05"); rename it from the gear menu. Deleting a studio deletes
  its draft, edit history and versions; **the pictures stay**.
- The results and the trial slot show **the jobs pressed in this studio** only. The queue is one
  per workspace, so other studios' and sessions' jobs are folded under "jobs from other studios
  and sessions"; that is where you see why your own batch has not started yet.
- **Open in image generation** in a picture's properties creates a new studio from that picture's
  settings and opens it.

**Knowledge notes**

- There is one note per model and one per family, grown by the agent and you together. They live
  in **`~/imagegen-knowledge/`** as `families/<family>.md` and `models/<model>.md`, each in four
  sections: **Summary** (short; the agent reads it every time), **Settings**, **Prompts** and
  **Records** (append-only).
- The agent writes only when told to remember something, or when it judges a result good or bad,
  and only to the document of the model the studio has chosen or of that model's family.
  **Deleting is yours.**
- They are ordinary files, so you can open and edit them from the file tree, and being outside
  `~/repos` they survive a Recreate. On a deployment whose file tree does not show the whole home
  folder they do not appear there; edit all four sections in the pane's **Notes** instead.

## Having a session generate an image

A session can **generate an image from a prompt**: a rough diagram, a stand-in mock, or a
picture for a README, the things you cannot hand over in prose.

**Off by default.** Turn it on under **Settings > Agents > Session > "Image generation"**. The
change applies to **sessions started from then on**; sessions already running keep their
current tools until restarted. The list of checkpoints and LoRAs is different: when an
administrator enables or disables one, running sessions are told within about a minute and see
the new list **without a restart**.

- **Every kind of session gets it**: claude and opencode, but codex and agy too. What is left
  out is **the one route that runs the session's own CLI**: a codex session cannot pick the
  Codex route, an agy session cannot pick the Antigravity one. That CLI already has image
  generation built in, and starting a second copy of it only pays twice. **The picture from the
  missing route is still available from that session's own built-in tool**, including "make
  the same prompt on both so I can compare", which is one call to each. Only when no route is
  left does the tool itself disappear.
- **A session attached to an image studio does not get it.** There the agent refines the draft
  and a person presses generate ([13 Images](13-images.md#studio-refine-the-draft-by-talking-to-an-agent)).
- **Which route draws it is a list of rows.** **Settings > Agents > Session > "Image provider
  order"** ranks every image route this workspace can reach: the deployment's own engines, each
  under its own name (with **ComfyUI** or **OpenAI-compatible** in brackets), and the CLI routes
  **Codex (the ChatGPT login)** and **Antigravity (agy)**. The first usable one is used, and **a
  call that fails falls through to the next**. The deployment's engines sit first by default
  because that GPU is paid for by the deployment, while a CLI route spends your own plan; a
  route that appears later is slotted the same way without moving what you ranked. While the
  workspace is stopped the list shows the built-in default only. With no route usable the tool
  does not appear.
- **You can name one in the request.** Say "make it with Codex", name one of the deployment's
  engines as it appears in that list, or say "make the same prompt on both so I can compare",
  and the agent picks that provider. A named provider is used on its
  own with no fall-through, so nothing is billed to a service you did not choose, and
  **comparing costs one image on each plan.** Say nothing and the order above decides.
- **Every image made on a CLI route spends the included usage of that plan**: the ChatGPT plan
  for Codex, the Gemini/Antigravity plan for Antigravity, both 3–5× faster than a text
  exchange. An image made on one of the deployment's own engines spends none of your plans. Image generation is not available at all on ChatGPT Free. **Which plan is drawn
  down is decided by that order**, so move the other one up if you would rather not spend one
  of them.
- **The prompt is sent to that provider (OpenAI, Google, or the deployment's engine).** Do not have images generated
  from instructions that carry anything confidential. Which one produced a picture is recorded
  with it.
- **Size, background and count are requests, not guarantees.** On the Codex route, asking for
  1024×1024 and getting 1536×1024 was measured. What actually came back is handed to the agent
  as a warning, so when the exact dimensions matter, check the result yourself.
- **The aspect ratio is the one exception, and only on the Antigravity route** (16:9 measured
  as 1376×768). You still cannot pick exact dimensions, but you can ask for landscape or
  portrait. The Codex route has no aspect-ratio setting at all.
- **Sampler settings can be asked for on the deployment's engines**, the same ones the form
  offers: steps, cfg, sampler and scheduler (and a seed, the negative prompt and LoRA weights),
  plus the three settings one family each reads, clip skip, guidance and shift
  ([what they are](#image-generation)). Anything left out runs at the checkpoint's published
  values, and a setting the model's family does not read (cfg on FLUX.1, a named scheduler on
  FLUX.2 klein) is **named in the result as not applied** rather than dropped in silence.
- **A LoRA asked for from chat carries its trigger words.** The tool lists each adapter with the
  words it was trained on and tells the agent to put one in the prompt, because an adapter loaded
  without its trigger costs the whole generation and changes nothing visible.
- **The picture appears in the conversation as a card** (click to enlarge, or open it in a
  pane). Anything the request did not get is noted above it, e.g. "asked for 1024x1024, got
  1536x1024".
- Results also open in the file pane, and are removed after 30 days
  ([where to find them](#image-gallery)).
- The card is shown for claude, opencode and GitHub Copilot sessions. Other kinds still get
  the path back, but the picture is not drawn into the conversation.
