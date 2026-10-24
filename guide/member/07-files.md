---
audience: "anyone reading or editing files in the Console"
---

# 07. Files — tree, viewer, Markdown/slides

English | [日本語](07-files.ja.md)

## File tree

The **Files** section in the left pane shows the files in your workspace as a tree. Repository
nodes can be collapsed (the triangle "Collapse" / "Expand"). When you want to focus on one
working copy, folding the others keeps things tidy.

- **compact folders**: levels whose only content is a single subfolder are collapsed into one row like `a/b/c` (no wasted rows for deep single-child hierarchies; the same convention as VS Code).
- **Tree / Changes**: on repository nodes you can switch the display between "Tree" and "Changes" (changed files only). Changed files get "Untracked", "Added", "Modified", "Renamed", and "Deleted" badges.

Right-clicking a file or folder opens its menu: "New file", "New folder", copying its name or
path, "Rename" and "Delete", plus opening it in the reader or the gallery, "Download" (for a
folder, **"Download as zip"**, below) and handing a file to an assistant to translate or
summarize. Deleting a folder removes its contents too, so a confirmation is shown.
[Icons, badges, and menus](badges-and-menus.md#files-folders) lists every item and when it appears.

### Open a shell from a folder

Right-click a folder → **"Open shell here"** starts a shell session whose working folder is that
folder (named `shell · <folder name>`). For a folder inside a repository's working copy, the shell opens in that
folder under the working copy and the session belongs to that working copy. Paths outside the home folder work too.

### Download a folder as zip

Right-click a folder (or press the Menu key on it, or **long-press it on a touch screen**) →
**"Download as zip"**. The same item is in the image gallery's folder menu. The Console first checks
the folder — a toast then tells you the file name, how many files and how large it is, and what was
left out — and your browser saves `<folder>.zip`, with every file under one top-level `<folder>/`
and its sub-folders (empty ones too) and Unicode names intact.

- **Left out, on purpose:** `.git` and `node_modules` folders *below* the one you chose (if you
  choose one of those yourself, it is exported whole), folders the file browser never shows
  (credential and agent-state folders), and symbolic links, hard-linked files (more than one name
  for the same file) and special files (the toast counts them). The menu item's tooltip names
  the first two before you press.
- **Too large is refused, not cut short.** Past 20,000 files, 4,000 folders, 32 levels, 512 MiB
  or 45 seconds of reading ([limits](../ref/limits.md#fixed-in-the-product)) you normally get a
  message naming the limit and no download starts. Select a smaller folder. The check cannot see
  what only reading finds (a full disk, an I/O error, a file that grows, a slow volume running
  out of time): then the download itself fails and **only your browser's download list says so**,
  without a message in the Console. You never get a partial zip.
- **A name that cannot be stored in a zip** (a backslash, a control character, bytes that are not
  valid UTF-8) stops the whole download with a message naming the file; nothing is renamed or
  dropped silently.
- **It is not a snapshot.** Files are read one after another while the archive is built, so a file
  that is still being written may be caught part-way. If a file disappears or is swapped for a
  link after the Console's check, the download fails (the server answers "The folder changed
  while the zip was being made. Try again."), which your browser shows as a failed download.
- **One zip is built at a time per workspace**; a second press while one is in progress says the
  workspace is busy. Your home folder itself (the root of the tree) cannot be downloaded this way.
- Not every browser has been checked: touch long-press and the download itself were verified in
  tests only, not on a real iPhone or Android phone.

To add files, **upload by drag & drop** or create them via "New file" in the right-click menu.
If a file with the same name exists, an overwrite confirmation is shown. **Ctrl+click** (or
middle-click) a file to **open it in a new pane** while keeping the current view ([02](02-console-basics.md#arranging-multiple-views-panes)).

The tree is **refreshed automatically**, so there is normally no need to refresh by hand:

- **When a session finishes a turn** and goes back to waiting for you: the files it
  created, renamed or deleted in its working copy appear.
- **While a session is still working**: the working copy of a running session catches up
  every 20 seconds, so you can look in mid-task and see what has been made so far.
- **When you come back to the tab or the window**: this also picks up work that finished
  while you were away, and changes you made yourself in the terminal.
- **When you re-open a folder**: anything that changed while it was collapsed shows up.
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

- **Syntax highlighting, line numbers, minimap**: code is highlighted with language detection. Toggle line numbers and the minimap under "File viewer" in ⚙Settings → the "Display" tab.
- **Huge files**: files that are extremely large or have extremely long lines automatically switch to "Plain view" (no highlighting or line numbers).
- **LFS pointers**: files whose Git LFS content has not been fetched show an "LFS pointer" badge. Enter the repository in a terminal and run `git lfs pull` to fetch the content ([06](06-code.md)).

### Markdown and Mermaid

For `.md` files, the toggle at the top switches between **"Preview"** and **"Source"**.
In the preview, Mermaid code blocks are rendered as diagrams. Each code block gets a
"Copy this code" button.

Links follow [Links in rendered Markdown](#links-in-rendered-markdown); in the preview a relative
link is read from the file's own folder, and a `#heading` anchor scrolls within the page.

### Links in rendered Markdown

Wherever the Console renders Markdown (a file's preview, an agent's reply in the conversation, the
assistant chat, the document viewer), it turns these into links:

- **External URLs** open in a new tab.
- **A path to a file or folder in your workspace**, as a Markdown link (`[text](src/app.ts)`) or in
  backticks (`` `src/app.ts` ``): a file opens in the viewer, a folder shows in **Files**.
- **`$AF_WORK_DIR/…`** in a reply of a Terminal (CLI) session means that session's scratch folder
  (`~/.af-work/<session>`), and it is linked when the file or folder exists there. It stays plain
  text in a Managed session, in the chat and in the document viewer.
- **Ticket numbers** (`#956`, `owner/name#956`, a Jira key, and both ends of a range such as
  `#573-#598`) open the ticket's details, by the rules in
  [03 Starting sessions](03-starting-sessions.md#from-a-ticket-number-in-the-conversation). In a file's preview a
  `#956` is read against the working copy the file is in: only for a file under `~/repos/<name>/`
  whose origin is on github.com or bitbucket.org. A file in a Git repository nested inside the
  copy (a submodule, a vendored clone) or outside `~/repos` gets no ticket links at all, and the
  document viewer does not link ticket numbers.
- **Loopback URLs** (`http://127.0.0.1:<port>/…`, `http://localhost:…`, `http://0.0.0.0:…`) name a
  server in your workspace, not on your own machine, so a click opens that port of the workspace
  the way the session row's port chip does ([the chip](badges-and-menus.md#session-display)).
  Ctrl / ⌘ / middle-click opens it in a new tab: in a browser pane where the deployment has one,
  otherwise in the default target. A right-click or long-press opens the chip's menu. When no running session listens on the port the link is dimmed but still opens;
  a URL in backticks is a link only while one does. An `https:` loopback URL stays an ordinary
  link, and so does a URL in backticks holding a non-ASCII punctuation mark or symbol (such as an
  elided `…`). When the workspace is stopped, the click says the port cannot be opened.
- **Right-click** (or long-press, or the Menu key) on a file or folder link opens a menu with
  "Open in pane", "Open in new pane", "Show in Files" and the Files menu's items that change
  nothing ([menus](badges-and-menus.md#file-and-folder-links-in-replies-and-documents)).

Someone reading a shared session gets none of this: paths and ticket numbers stay plain text, and
URLs are ordinary links.

### Marp slides

A `.md` file starting with `marp: true` can be displayed as **slides**. The toggle at the top
shows **"Slides", "Preview", "Source"**, and slides are the default view.

- **◀ / ▶** (or ← → / PageUp·PageDown / Space / Home·End) move one slide at a time.
- **⤢** switches to fullscreen.

### Diagrams (`.drawio`)

A `.drawio` / `.dio` file (and any `.xml` holding an `mxfile`) is shown as a **diagram**.
The toggle at the top switches between **"Diagram"** and **"Edit"**, or **"Source"** when
the file is read-only. "Edit" and "Source" show the file's XML as plain text, so you can always drop
down to it; the drawing itself is not edited graphically (see below).

- **Multiple pages**: the header shows `page n / m`; the arrows at the top left move between pages.
- **Zoom and pan**: Ctrl (⌘) + wheel, or a two-finger pinch, zooms around the pointer; a plain wheel or drag pans. Double-click / double-tap toggles between fit and actual size.
- **The theme follows the Console**: the diagram is redrawn in dark or light with you, keeping the page, zoom and position you were on.
- **Nothing leaves your deployment.** The viewer is bundled, so the diagram is never sent to a third-party service, and the drawing works with no external network at all.
- **Vendor icons** (AWS, GCP, Azure, Kubernetes, rack gear …) are fetched once per icon set by the Control Plane and cached for everyone. In a network-restricted deployment they may be missing, and then the shapes keep their size, colour, border and labels but the artwork inside is blank, but the diagram still opens. Your operator can pre-seed them ([operator 03](../operate/03-run.md)).

There is a sample to try in this repository: **[`docs/assets/architecture.drawio`](../assets/architecture.drawio)**
(the deployment shape of Agent Fleet itself, on AWS and on a single Docker Compose host).

Editing the drawing graphically (moving shapes, drawing connectors) is not supported yet. "Edit"
changes the XML text only, the same way as for any other file ([Editing a file](#editing-a-file)); for
drawing, use an external editor.

### PDFs and Office documents

A **PDF** is drawn as its pages, exactly as it looks (`1 / 12 pages` in the header). Step through
with the arrows, **zoom in and out**, and **fit to width**. **Japanese PDFs render their text too**,
even when the fonts are not embedded.

**Word / Excel / PowerPoint** (`.docx`, `.xlsx`, `.pptx`, plus `.odt`, `.rtf`, `.epub` and friends)
get a **plain preview**: the content is converted into something readable, and the surface says so:
**formatting, shapes and images are not reproduced**. When you need the document as laid out, open
the original from **Download** in the info bar.

- **Both the conversion and the rendering happen inside your browser.** The document is never sent
  to an outside service (the same rule as `.drawio`).
- The first time you open that format there is a short wait while it loads, and nothing is loaded
  unless you open one.
- When it cannot be shown, **you are told why**: password-protected, corrupt, **pages that are
  images only** (reading the text would need OCR, which is not done here), or too large (over 40 MB).

## Editing a file

Switch to editing with **View / Edit / Split** at the top of the viewer and you can fix the file
right there, with no need to start a session for a small change (on `.md`, "Split" puts the editor
and the preview side by side).

- **Save with Ctrl/⌘+S** (or the Save button). The status line shows "Unsaved changes",
  "Saving", "Saved".
- **Leaving with unsaved work is stopped.** Moving a pane, reloading, logging out, popping out
  into another tab and the like ask first, offering **"Save and continue"** or **"Discard and
  continue"** (a file with unsaved changes cannot be popped out).
- **When it changes underneath you**: if an agent or another session rewrites the same file you
  get **"The file was changed outside this editor"**, and **"Check diff"** lets you compare before taking
  it.
- **When it conflicts**: saving from a stale revision gives **"Remote changes conflict with
  yours"**. Your text (mine) is kept, and you choose **Adopt remote / Discard mine / Use remote
  as base for manual merge**. Even when the outcome of a save cannot be determined, mine
  and the submitted snapshot are held so you can settle it with "Retry" or "Save explicitly".
  **Your edit is never silently lost.**
- **What cannot be saved**: a change over 2 MiB, content containing NUL, CR/CRLF newlines (LF
  only) and invalid Unicode are refused.

### Have an AI propose the change

**"AI suggest"** in the editor asks an AI for a change to the selection (or to the whole file
when nothing is selected). Write the instruction, generate with **Ctrl+Enter**, review the diff
and choose **Apply** or **Reject**; nothing is rewritten on its own (if the text moved on after
generation, it tells you to regenerate). The tokens it spends are recorded in ⚙ Settings → Agent usage
as **"Edit suggestions (editor)"** ([20](20-settings.md#agent-usage)).

## Send a file to a session / chat

When you want an agent to "work on this file", open the file and use the **"Send"** button
that appears when you **select text** in the viewer (you can also send the whole file without
selecting a range). This action is not in the file's right-click menu.

- **Destination**: choose a running session (direct send) or the assistant (open in chat). You cannot send to a stopped session.
- **Comment (instructions)**: attach instructions such as "Translate this into Japanese and save it".
- The file is **passed by path**, and the session reads and writes it itself. This suits work that produces file output, such as translating a large file.

You can also tag items with a "Category" and pile them up in the **memo queue** to send in a
batch later ([10 Chat and memos](10-chat-memo.md)).
