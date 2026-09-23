---
audience: "anyone reading or editing files in the Console"
updated: "2026-09"
---

# 04. Files — tree, viewer, Markdown/slides

English | [日本語](04-files.ja.md)

## File tree

The **Files** section in the left pane shows the files in your workspace as a tree. Repository
nodes can be collapsed (the triangle "Collapse" / "Expand"). When you want to focus on one
working copy, folding the others keeps things tidy.

- **compact folders** — levels whose only content is a single subfolder are collapsed into one row like `a/b/c` (no wasted rows for deep single-child hierarchies; the same convention as VS Code).
- **Tree / Changes** — on repository nodes you can switch the display between "Tree" and "Changes" (changed files only). Changed files get "Untracked", "Added", "Modified", "Renamed", and "Deleted" badges.

Right-clicking a file or folder offers "New file", "New folder", "Copy the name",
"Copy the relative path", "Rename", and "Delete". Files also show "Open in reader" and
"Download". Deleting a folder removes its contents too, so a confirmation is shown.
See [Icons, badges, and menus](badges-and-menus.md) for when each item appears.

To add files, **upload by drag & drop** or create them via "New file" in the right-click menu.
If a file with the same name exists, an overwrite confirmation is shown. **Ctrl+click** (or
middle-click) a file to **open it in a new pane** while keeping the current view ([03](05-terminal.md)).

The tree is **refreshed automatically**, so there is normally no need to refresh by hand:

- **When a session finishes a turn** and goes back to waiting for you — the files it
  created, renamed or deleted in its working copy appear.
- **While a session is still working** — the working copy of a running session catches up
  every 20 seconds, so you can look in mid-task and see what has been made so far.
- **When you come back to the tab or the window** — this also picks up work that finished
  while you were away, and changes you made yourself in the terminal.
- **When you re-open a folder** — anything that changed while it was collapsed shows up.
- Right after a clone, and after the workspace starts or stops.

Rows for files an automatic refresh **added are tinted for a few seconds**, so you can see
which ones are new; nothing is lost when the tint fades. The ⟳ button in the section
header is still there for anything none of the above covers.

### Why some folders are invisible

Some folders are **intentionally hidden** from the tree. These are directories holding agent
login credentials and encrypted storage (credential files for claude / codex / opencode, SSH keys,
Agent Fleet internal state, and so on). To protect your secrets, they are neither listed nor
directly viewable. Not seeing them is not a problem.

## File viewer

Clicking a file shows its contents in the **viewer** in the main area. The top shows the file
name, format, size, and line count.

- **Syntax highlighting, line numbers, minimap** — code is highlighted with language detection. Toggle line numbers and the minimap under "File viewer" in ⚙Settings → the "Display" tab.
- **Huge files** — files that are extremely large or have extremely long lines automatically switch to "Plain view" (no highlighting or line numbers).
- **LFS pointers** — files whose Git LFS content has not been fetched show an "LFS pointer" badge. Enter the repository in a terminal and run `git lfs pull` to fetch the content ([04](03-code.md)).

### Markdown and Mermaid

For `.md` files, the toggle at the top switches between **"Preview"** and **"Source"**.
In the preview, Mermaid code blocks are rendered as diagrams. Each code block gets a
"Copy this code" button.

Links behave as follows.

- **External URLs** — open in a new tab.
- **Relative links within the repository** — open that file in the viewer / show the folder in **Files**.
- **`#heading` anchors** — scroll within the page.

### Marp slides

A `.md` file starting with `marp: true` can be displayed as **slides**. The toggle at the top
shows **"Slides", "Preview", "Source"**, and slides are the default view.

- **◀ / ▶** (or ← → / PageUp·PageDown / Space / Home·End) move one slide at a time.
- **⤢** switches to fullscreen.

### Diagrams (`.drawio`)

A `.drawio` / `.dio` file (and any `.xml` holding an `mxfile`) is shown as a **diagram**.
The toggle at the top switches between **"Diagram"** and **"Edit"** — or **"Source"** when
the file is read-only — so you can always drop down to the XML.

- **Multiple pages** — the header shows `page n / m`; the arrows at the top left move between pages.
- **Zoom and pan** — Ctrl (⌘) + wheel, or a two-finger pinch, zooms around the pointer; a plain wheel or drag pans. Double-click / double-tap toggles between fit and actual size.
- **The theme follows the Console** — the diagram is redrawn in dark or light with you, keeping the page, zoom and position you were on.
- **Nothing leaves your deployment.** The viewer is bundled, so the diagram is never sent to a third-party service, and the drawing works with no external network at all.
- **Vendor icons** (AWS, GCP, Azure, Kubernetes, rack gear …) are fetched once per icon set by the Control Plane and cached for everyone. In a network-restricted deployment they may be missing, and then the shapes keep their size, colour, border and labels but the artwork inside is blank — the diagram still opens. Your operator can pre-seed them ([operator 02](../operate/03-run.md)).

There is a sample to try in this repository: **[`docs/assets/architecture.drawio`](../assets/architecture.drawio)**
(the deployment shape of Agent Fleet itself, on AWS and on a single Docker Compose host).

Editing the drawing itself is not supported yet — use the source view, or an external editor.

### PDFs and Office documents

A **PDF** is drawn as its pages, exactly as it looks (`1 / 12 pages` in the header). Step through
with the arrows, **zoom in and out**, and **fit to width**. **Japanese PDFs render their text too**,
even when the fonts are not embedded.

**Word / Excel / PowerPoint** (`.docx`, `.xlsx`, `.pptx`, plus `.odt`, `.rtf`, `.epub` and friends)
get a **plain preview**: the content is converted into something readable, and the surface says so —
**formatting, shapes and images are not reproduced**. When you need the document as laid out, open
the original from **Download** in the info bar.

- **Both the conversion and the rendering happen inside your browser.** The document is never sent
  to an outside service (the same rule as `.drawio`).
- The first time you open that format there is a short wait while it loads — nothing is loaded
  unless you open one.
- When it cannot be shown, **you are told why**: password-protected, corrupt, **pages that are
  images only** (reading the text would need OCR, which is not done here), or too large (over 40 MB).

## Image gallery

A pane that lays the images in a folder out as cards, so you can compare them without opening one
at a time — and see at a glance what a folder actually holds.

**Five ways in**, each of them one item in a menu that is already there:

- **Right-click a folder** in the file tree → "Open in gallery"
- **Right-click an image file** in the file tree → "Open in gallery" (its parent folder opens with
  that image enlarged; the item does not appear for non-image files)
- **"Gallery"** in the image viewer's header
- **"Open folder"** in the enlarged view of a shared-file card in the mirror
- **"Generated images (N)"** in a session's context menu (below)

A plain click uses the current pane; Ctrl/⌘-click and middle-click open another one. A phone has no
"beside", so it opens in place.

**What you see, and what you can do**

- **Sort** — "Newest" or "By name". The choice is remembered per pane, so moving between tabs does
  not reset it. When the workspace's Agent is old enough not to send modification times, the
  gallery falls back to name order and shows no relative time ("3 minutes ago") either.
- **Count and size** — the header carries the totals for the whole folder. A big folder stops at
  **300 images**, with "Show more" for the rest (each card is one thumbnail request).
- **A folder you have already seen opens at once** — the gallery remembers how each folder it
  has walked through looked, so coming back draws those cards immediately instead of waiting.
  **Your scroll position and anything you expanded with "Show more" come back with them**, so a
  folder you were halfway through carries on where you left it. It re-reads the folder behind
  that, and says "updating…" beside the count while it does — anything that really did arrive
  since appears afterwards with the usual highlight. If the folder itself is gone, you get an
  error rather than the contents it remembered.
- **Loading** — the first time you open a folder, while nothing has arrived yet, the pane shows a
  loading state (it never sits there showing only "Up"). Thumbnails are requested for the cards
  nearest the screen first, so opening a big folder does not fetch everything at once, and
  scrolling ahead does not get stuck behind pictures you have already scrolled past.
- **Folders** — subfolders are cards too, and each one shows **the newest picture inside it as
  a cover** (the folder mark becomes a small badge over the picture), with **how many pictures
  are in it** underneath. A folder named by a session's UUID is no longer a guess. Opening one
  moves this pane into it. The first card,
  "Up", goes back to the parent, and the **breadcrumb** in the row under the header
  (`.cache / agent-fleet / generated`) jumps to any level. Ctrl/⌘-click and middle-click open the
  folder in another pane.
- **An always-on "Up", and the browser's Back button** — that breadcrumb row also has a
  **permanent "Up" button** (disabled at the root), so a long folder whose "Up" card has scrolled
  off screen is still one click from its parent. Folder navigation is retraced by the browser's
  own **Back button** too — however you got somewhere ("Up", the breadcrumb, or a card), the same
  number of Back presses gets you the same distance back.
- **Cards** — click the card to **enlarge** (← / → move through the folder, and "3 / 12" tells you
  where you are; **on a phone, swipe left and right** to move — only at fit, because while you are
  zoomed in a drag pans the picture); the button in the corner **opens it in the file pane**. Just
  looking never costs you a pane. **Right-click a card** (or press the Menu key) for copy path /
  copy name, rename and delete — on a folder, also "Open in another pane" — and, in a session's
  folder, a jump to the session that generated the pictures
  ([menus](badges-and-menus.md#cards-in-the-image-gallery)).
- **Refresh** — on open, on returning to the tab, and every 20 seconds while a session is running.
  New arrivals get the same highlight as the file tree, so you see a generation land. "Refresh" in
  the header re-reads at any time.
- Cards show a **downscaled copy**, requested at your screen's own pixel density — a sharp
  display gets a sharper thumbnail, a plain one is not sent pixels it cannot show. When you
  enlarge, that downscaled copy is what you see first — slightly blurred — and it sharpens when
  **a copy sized to your screen** arrives. That copy is re-made from the original pixels; for a
  generated picture it is roughly 120 KB against 1.1 MB, so it looks the same and only the wait
  shrinks. The neighbouring picture is fetched ahead, so ← / → do not wait.
  **When you need the real file** (to save it, or to inspect detail at full size), open it in the
  file pane with the button in the card's corner, or download it — those are still the original.

**Images a session generated**

Images made with `generate_image` stay in a per-session folder for 30 days. That folder is named
after the session's internal id, so the file tree is no way in. **"Generated images (N)" in the
session's context menu** is the way, and it appears **only for sessions that have generated
something** (N is how many). It is absent while the workspace is stopped — reading the images is
the Agent's job, and the Agent lives in the workspace.

To see **all of them at once**, an **"Open generated images"** button opens the parent folder from
four places: the **minimap's button row**, the leader key **`g g`** (and the command palette), the
**Files** section header in the left pane, and the **image-generation pane's header**. It opens on
a page of cards: one folder per session, plus the studio's own output (`console`). A session's
folder is labelled with **the session's name and its image count**, not its internal id. Inside
such a folder the breadcrumb row carries a button with that session's name, and pressing it opens
the conversation the pictures came from (it is absent once the session is gone).

## Image generation

A pane that makes pictures on your organisation's own ComfyUI **without an agent in the loop**.
Asking a session for a picture is right for "put an illustration in this document"; this is for
"forty variations of one prompt at three CFG values", where every round trip through a model
would cost a turn. A session can name the same settings you can (`generate_image` takes steps,
cfg, sampler, scheduler, seed, the negative prompt and the LoRA weights); what it leaves out runs
at the checkpoint's own published values, and a setting its family does not read is named in the
result's warnings rather than dropped in silence. What a session cannot see is what this form shows
you: each checkpoint's own published numbers as the placeholders, the fields its family does not
read greyed out, and a trial run before you commit forty.

**Two ways in:** the workspace action bar's **Images**, and the leader key **`g i`**. It is one
pane per workspace — opening it again focuses the one you have.

**Before you generate**

- **Model** — the checkpoints your administrator has enabled, under **the name their publisher
  gave them** ("unsloth/Qwen-Image-GGUF Q8_0" — enough to tell two sizes of one model apart).
  One whose source page your administrator has not read yet appears under its internal id instead.
  A dot marks one that is already
  loaded on the engine; anything else means the first picture waits for the engine to start.
  The header says which of the four states you are in: **ready**, **the engine is asleep**
  (with roughly how long the first picture will take), **the engine is starting**, or **no
  engine available**.
- **The family card** — under the model, one line per thing that family expects: whether it
  wants a tag list or sentences, the prefix that dialect usually opens with (offered as a chip
  — nothing is ever written into your prompt on its own), whether a negative prompt reaches it
  at all, and the step and cfg ranges worth staying inside. The families the catalogue knows
  run from SD 1.5 and SDXL to FLUX, Anima and Krea 2, the instruction-edit models Qwen-Image-Edit
  (2509 and 2511) and Qwen-Image 2.1, and **the default size follows the family**
  — SD 1.5 starts at 512 rather than the megapixel square the others share, because asking it
  for more gives you a doubled subject, not an error.
- **Fields a family does not read are disabled, with the reason on them.** `flux1` and
  `flux2-klein` ignore cfg; the distilled families — and a checkpoint of a guided family that
  runs at cfg 1 — ignore the negative prompt. That comes from the workspace, not from a table in
  the browser, so it cannot disagree with what actually runs.
- **The administrator's negative** shows as a chip you cannot remove, next to the deployment-wide
  one. It is applied on top of yours rather than mixed into your text.
- **LoRAs** — only the ones that match the model's family are listed. Each row shows the
  **trigger words** it would bring, so you can choose between two adapters before ticking
  either; ticking one turns those words into chips over the prompt, and unticking it removes
  those chips and nothing else. A missing trigger is the usual reason a LoRA "does nothing" —
  and if you generate without one, the result says so in as many words. The weight starts at
  the strength the adapter's author published, or 1 when the catalogue has none.

**Trial run, then the batch**

- **Trial run (`Ctrl+Enter`)** puts **one** picture at the **head** of the queue, at fewer steps,
  into `generated/console/trial/`. It appears in "Latest trial" beside the form with its seed and
  how long it took. **"Use this seed"** pins it, which is the whole point of trying before a
  sweep. Tick **Full steps** when the trial is meant to be the picture. Trial pictures are swept
  after 7 days.
- **Enqueue N (`Ctrl+Shift+Enter`)** puts N jobs — one picture each — at the tail as one group.
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
  neither the trial nor the batch can be sent — **"Inpaint needs a mask image"**.
- **How many reference pictures you may add depends on the model.** The field's heading counts
  them ("2 of 2"), and at the ceiling the add field itself goes away. The instruction-edit models
  — the kind you ask in words, "change the sign to CLOSED" — read **three**: the **first is the
  picture being redrawn** and the **rest are things to bring into it** ("put the plant from
  picture 2 and the duck from picture 3 on the table"). Every other model reads one. On a model with no slider, how much changes is decided by the instruction itself.
  These models offer **Edit** and **Inpaint** only — there is no Generate, since they cannot work
  without a picture — so choosing one while the operation is set to Generate switches it and says
  so (**"… does not support the previous operation — switched to Edit"**). Their mask must be the
  picture's own size; one that is not comes back as a failed job. **Qwen-Image 2.1** does both:
  it generates from a prompt alone, with the size fields, and edits by instruction with up to
  **ten** reference pictures.

**While it runs**

A group is one row: "12 / 40", a bar (done, failed, and the picture in flight filling by time
against the usual duration — it stops short of the end rather than claiming a completion nobody
has seen), and roughly how long is left. **Pause**, **resume**, **skip the current picture** and
**abort** all act on the group; the ✕ on a line cancels that one picture. Pausing everything
still lets trials through — that is what pausing is for. A batch left paused long enough lets the
engine go to sleep, and the row says so. The row also names the phase the current picture is in
— **starting the engine**, **uploading**, **sampling**, **fetching** — with its own seconds, and a
picture that had to wait for the engine to start still gets its full time to sample afterwards.

The pane polls only while something is unfinished, and stops while the tab is in the background.

**Where the pictures go, and getting the numbers back**

The default folder is `generated/console/`, which the gallery lists like any other and which is
**never swept** — you pressed the button for each of these. **Output folder** under Advanced puts
a run somewhere of your own naming. Every picture is written with a small record beside it, so
enlarging one anywhere in the Console (the studio, the gallery, a shared file in the mirror) and
pressing **Properties** shows the model, seed, size, steps, cfg, sampler, scheduler, LoRAs and
both prompts, each row with a copy button, plus **copy all as JSON** and **open in image
generation**, which loads the fields back into the form. Pictures made by an agent before this
existed can still be read: the graph ComfyUI embeds in the PNG carries the same numbers. Pictures
from the vendor routes (codex, agy) carry nothing, and the panel says that rather than showing
empty rows.

**Having the prompt written for you**

**"Write the prompt for me"** asks your assistant **once**, with the family's dialect, the model's
description and your LoRAs' trigger words already in the question. The answer comes back as a
**proposal**: use it, use the prompt only, or discard it. Nothing is applied until you press, and
no model is called unless you press the button. A member who has never signed in to any CLI has
no assistant to run, and everything above still works without it.

## Editing a file

Switch to editing with **View / Edit / Split** at the top of the viewer and you can fix the file
right there — no need to start a session for a small change (on `.md`, "Split" puts the editor
and the preview side by side).

- **Save with Ctrl/⌘+S** (or the Save button). The status line shows "Unsaved changes",
  "Saving", "Saved".
- **Leaving with unsaved work is stopped.** Moving a pane, reloading, logging out, popping out
  into another tab and the like ask first, offering **"Save and continue"** or **"Discard and
  continue"** (a file with unsaved changes cannot be popped out).
- **When it changes underneath you** — if an agent or another session rewrites the same file you
  get **"The file changed externally"**, and **"Check the diff"** lets you compare before taking
  it.
- **When it conflicts** — saving from a stale revision gives **"Conflicts with the remote
  change"**. Your text (mine) is kept, and you choose **adopt remote / discard mine / merge
  manually onto remote as the base**. Even when the outcome of a save cannot be determined, mine
  and the submitted snapshot are held so you can settle it with "Retry" or "Save explicitly".
  **Your edit is never silently lost.**
- **What cannot be saved** — a change over 2 MiB, content containing NUL, CR/CRLF newlines (LF
  only) and invalid Unicode are refused.

### Have an AI propose the change

**"AI suggestion"** in the editor asks an AI for a change to the selection (or to the whole file
when nothing is selected). Write the instruction, generate with **Ctrl+Enter**, review the diff
and choose **Apply** or **Reject** — nothing is rewritten on its own (if the text moved on after
generation, it tells you to regenerate). The tokens it spends are recorded in ⚙ Settings → Agent usage
as **"Edit suggestion (editor)"** ([12](12-settings.md#agent-usage)).

## Send a file to a session / chat

When you want an agent to "work on this file", open the file and use the **"Send"** button
that appears when you **select text** in the viewer (you can also send the whole file without
selecting a range). This action is not in the file's right-click menu.

- **Destination** — choose a running session (direct send) or the assistant (open in chat). You cannot send to a stopped session.
- **Comment (instructions)** — attach instructions such as "Translate this into Japanese and save it".
- The file is **passed by path**, and the session reads and writes it itself. This suits work that produces file output, such as translating a large file.

You can also tag items with a "Category" and pile them up in the **memo queue** to send in a
batch later ([07 Chat and memos](07-chat-memo.md)).
