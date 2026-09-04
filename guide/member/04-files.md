---
audience: "anyone reading or editing files in the Console"
updated: "2026-08"
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

The tree is **refreshed automatically**: right after a clone, after the workspace starts
or stops, and **each time a session finishes a turn** — when it goes back to waiting for
you, the files it created, renamed or deleted in its working copy appear. Re-opening a
folder also re-reads it, so anything that changed while it was collapsed shows up.
There is normally no need to refresh by hand; the ⟳ button in the section header is
still there for a change nothing told the Console about (a command you ran in the
terminal, for instance) or while a session is still working.

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
