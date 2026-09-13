# 0080. A folder of images is a grid of cards — the gallery is one new pane kind, and every entry is one item in a menu that already exists

English | [日本語](0080-image-gallery-pane.ja.md)

- Status: **proposed** (2026-09-13).
- Related: [0049](0049-session-changed-files.md) decision 4 and [0046](0046-drawio-viewer.md)
  (**do not add a `PaneKind`; add one more face to an existing pane** — this ADR argues the
  exception) / [0078](0078-sessions-overview-pane.md) (the nearest precedent: one pane kind, the
  existing menu borrowed whole, a grid of cards) /
  [0069](0069-image-generation-providers.md) (where generated images live, and for how long) /
  [0063](0063-document-preview.md) (`api/fs/download` is the one endpoint that returns raw bytes) /
  [0017](0017-keyboard-system.md) (registering in the command table)

## Context

There are two ways to look at an image in the Console. **Open one in the file pane**
(`viewer/ImageView.tsx`: wheel and pinch zoom, drag to pan), or **enlarge a shared-file card in
the mirror's transcript** (`FileCard` in `mirror/transcript/blocks.tsx` →
`mirror/parts/ImageLightbox.tsx`). Both are about ONE image: **there is no surface that lists what
a folder holds.**

Image generation (ADR 0069) is where that hurts most. `generate_image` writes to
`~/.cache/agent-fleet/generated/<sid>/image-<unixnano>-<n>.png`
(`internal/imagegen/store.go`; kept 30 days; 2-3 MB each).

- The folder is named by the **session UUID**, so the file tree is not a practical way in.
- A transcript card can end up inside a fold (the path under `.cache` is browsable, so it does
  open — but "the generated image never showed up" is a known reading of it).
- A generation produces several images at once and is usually run several times over, and there
  is **nowhere to compare the results**.

What is already in place, though, is most of the machinery.

- **The thumbnail endpoint exists**: `GET api/fs/download?path=…&thumb=<longest edge>`
  (`fs_thumb.go`). Measured: a 1024x1536 / 3.2 MB PNG comes back as 341x512 / ~60 KB, with
  `Cache-Control: private, max-age=60` and `Last-Modified`, so a second look is a 304. It has a
  disk cache and caps concurrent decodes at 2 (the host is shared and memory-constrained). **The
  parameter is advisory**: a format with no decoder, or an image already small, serves the
  original — a caller needs no capability check.
- **The zoom part exists**: `ImageView` exposes a zoom handle, and `ImageLightbox` already draws
  its own button row against it.
- **The card idiom exists**: body = enlarge, corner button = open the pane. The split keeps merely
  looking at a picture from costing a pane, and users have already learned it.
- **Adding a pane kind is a known drill** (ADR 0078 walked its eight places).

What is missing is the folder-scoped surface, and the ways in.

## Decisions

### Decision 1 — a new pane kind, `gallery` (not another face of the file pane)

```ts
| { kind: "gallery"; galleryPath: string; sort?: "new" | "name"; galleryFocus?: string; gallerySession?: string }
```

- **How this relates to 0049 decision 4 and 0046.** Both concluded that **when there is an
  existing pane to put it on**, one more face is cheaper than a kind. There is no such pane here.
  `kind:"file"`'s `filePath` is a contract about ONE FILE, and hanging off it are the edit buffer,
  the diff, external-change follow, read-aloud and the PDF/Office branches (`fileMode.ts`). A
  directory makes every one of them a lie. The SCM pane's subject is a repository — also not a
  folder. **`galleryPath` is the first field in `PaneContent` that names a directory**, and that
  is precisely the reason for the new kind.
- As a pane it gets splitting, tabs, pop-out, layout persistence, the phone's single-pane display
  and `Ctrl+F` **from machinery that already exists** — including sitting next to the mirror so
  you can watch images land.
- It walks all eight places (the union, the stored-value validation, the identity check, the
  render switch, the title, the minimap abbreviation, pop-out eligibility, i18n). **Forget the
  validation in `migrate.ts` and a reload turns the pane into a blank terminal.** `galleryPath` is
  a stored value, i.e. untrusted input: reject a non-string, a leading `/`, `..`, control
  characters and anything over the length cap — the same severity as the `browser` kind's `path`.
- `sort` lives in the pane's CONTENT, not in React state. A tab switch unmounts this view, and a
  setting that snapped back on every switch reads as broken (0078 decision 1).
- The identity check (`sameTarget`) is `galleryPath` alone. `sort` and `galleryFocus` are state of
  the same surface, so opening the same folder twice does not produce two panes.

### Decision 2 — the listing is the existing `api/fs/tree`; the only addition is `mtime` on `fsEntry`

- No new listing endpoint. One level of `{name, type, size}` is what this endpoint already returns.
- **Add `mtime`** (`workspace/agent/fs.go`). Neither "newest first" nor "3 minutes ago" can be
  written without it. Generated filenames carry a `unixnano`, so name order IS time order there —
  but that is an accident of generated images and does nothing for a folder of screenshots.
- No effect on the tree: `ProjectFiles`' `sameEntries` compares name and type only, so a moved
  mtime causes no extra repaint.
- **Recursion is out of P0** (decision 9, P1). `api/fs/search` must not stand in for it: it
  honours `.gitignore`, so generated and built files **vanish silently**, and its `q` is required,
  so "everything" cannot be asked for.
- Huge folders are handled on the client (decision 4). That `api/fs/tree` has no cap is an
  existing property of the tree, and this ADR does not change it.

### Decision 3 — what counts as an image is decided in one place, `imageFormat()`

`IMAGE_EXT` in the Console's `lib/filemeta.ts` is the only judge (the Agent's `imageContentType`
is its counterpart, and each comment points at the other). A gallery with its own extension table
would eventually disagree with the file pane — **an image the viewer opens but the gallery does not
list**. Whether a thumbnail is possible is deliberately NOT consulted: `thumb` is advisory, and
webp/avif/bmp/svg come back as originals for the browser to draw.

### Decision 4 — ask for `thumb=512`, the same number the mirror uses, plus lazy loading and a cap

- **512 is what the mirror's shared-file cards ask for.** The thumbnail disk cache is keyed on the
  requested edge, so a gallery asking for 256 would **decode the same image twice** (decoding is
  the cost — ~100 ms and width*height*4 bytes). Show it smaller with CSS instead.
- `loading="lazy"` plus `decoding="async"`. The first render stops at **300 images, newest first**,
  with a "show more". Without a cap, one unlucky folder queues 300 decodes behind a semaphore of 2.
- The original bytes are fetched only on enlarge (the lightbox). A card always shows the
  downscaled copy.

### Decision 5 — cards behave like the transcript's; the lightbox is shared and gains ← / →

- **Body = enlarge (lightbox), corner button = open the file pane.** `FileCard` already splits it
  this way, for the same reason: looking should not cost a pane.
- `mirror/parts/ImageLightbox.tsx` **moves to a shared home** and gains `←`/`→` navigation and a
  "3 / 12" position. `ImageView`'s zoom handle is used as it is. **The mirror's own appearance and
  behaviour do not change** — navigation appears only when a list is handed in.
- Escape stays on `useEscLayer`, and the existing close rules (backdrop closes, a click on the
  image stays a zoom, a pan that ends on the backdrop does not close) carry over unchanged.
- **No swipe navigation on a phone** (P0). Horizontal drag already belongs to session rotation
  (the `data-no-swipe` tug of war), and touching it would break an existing gesture. Buttons and
  arrow keys navigate.

### Decision 6 — auto-refresh follows the existing policy: events pull the trigger, intervals are the safety net

The numbers are `features/files/refreshPolicy.ts`, unchanged.

- Once on mount; on `useFilesStore`'s `tick` (workspace start/stop, an upload); **on return to the
  tab** (`REVALIDATE_GAP_MS`, a 10-second floor); **every 20 seconds while some session is
  running** (`WORKING_TICK_MS`, and the timer is folded away when nothing is); and the header's
  refresh button.
- **Never a resident poller** (the Console↔CP traffic-reduction stance). A generation takes
  minutes, so the end-of-turn signal (`sessionRefresh.ts`) is too late here and the 20-second net
  is what actually carries this surface. The listing is JSON, so the CP's ETag applies and an
  unchanged folder costs a 304.
- Images that just appeared get the tree's "new row" highlight, so a generation landing is visible.

### Decision 7 — every way in is one item in a menu that already exists; no new button, no new surface

1. **Right-click a folder in the rail** → "Open gallery".
2. **Right-click an image file in the rail** → the same item. `menuDir` already points at the
   parent, so it opens the parent's gallery positioned on that image (`galleryFocus`). Not shown
   for non-image files.
3. **The mirror's shared-files panel** → "Open folder". It opens the **parent folder** of that
   image, so no session resolution is involved.
4. **The image viewer's header** → "Gallery of this folder".
5. **A session's context menu** → "Generated images (N)". `sessions/SessionMenu.tsx` is shared by
   the rail row, the session tab and the overview card, so **one item is three ways in** (0078
   decision 7: never duplicate an item).

Opening follows the established convention: a plain click uses the current pane, `Ctrl/⌘` and
middle-click open another one. A phone has no "beside", so it opens in place (as revised in 0078
decision 3).

### Decision 8 — the session item appears only when there ARE generated images, and that costs two fields on the session wire

`session.Session` (Agent) → `sessionWire` (CP) → `types/session.ts` (Console) gain:

- `generatedImages` (the count, `omitempty`)
- `generatedImagesPath` (the folder, browse-root relative, `omitempty`)

- **Why the wire.** The folder is `uuidV5(dir + "|" + <session name>)`
  (`internal/session/uuid.go`), which **the Console cannot derive**. Copying that hash into TS is
  the kind of duplication that starts drifting silently the moment one side is fixed. Resolving it
  on click would decide "is there anything" AFTER the menu opened, so the item would grow in late,
  with the pointer already moving. With the path on the wire there is **no new endpoint and no CP
  allowlist entry**, and the click opens the pane immediately.
- **Cost.** One `ReadDir` of a small directory per session per list build (tens of µs; the list
  already reads — and sometimes writes — a meta file per session). If it ever shows up, memoize on
  the directory's mtime. The wire bytes are omitted at zero, so a deployment that never generates
  pays nothing.
- **The relay trap.** A field absent from `sessionWire` is dropped silently, and the symptom is
  "the item never appears". Three nets watch it: `wire.golden`, the Agent→CP round-trip test, and
  the comparison against the Console's type (`contract_session_test.go`).
- **While the workspace is stopped** both fields come from the DB mirror, i.e. absent, so the item
  disappears. That is correct: without the Agent the gallery cannot be read at all.
- **Only af's `generate_image` output is counted.** codex's own `generated_images` lives under
  `.codex`, which is deliberately unbrowsable (`fsDeny` — the very reason ADR 0069 moved ours),
  so it does not appear here. Pasted images are out of scope too: a different axis, with its own
  entry if one is ever wanted.

### Decision 9 — one level only; recursion is P1 and arrives as its own endpoint

P0 lists what is directly under `galleryPath`. Recursion means walking, which needs an endpoint
that can ignore `.gitignore` and bound the result (`GET /fs/images?path=&depth=&limit=`) — and that
gets built **once recursion is known to be needed**. The flat cases (generated images, `docs/img`,
a folder of screenshots) are most of the cases, and they come first.

## Options rejected

- **A modal gallery** (like cleanup / archive): cheap, but it throws away everything a pane gets
  for free — sitting next to the mirror, becoming a tab, tearing off into its own browser tab,
  surviving in the layout (decision 1).
- **Passing a directory to `kind:"file"`**: the `filePath` contract and all of FileView's
  machinery become lies (decision 1).
- **A gallery listing endpoint in P0**: `fs/tree` plus one `mtime` is enough. Building it up front
  duplicates the image test, the ordering and the cap across the Agent and the Console
  (decisions 2 and 3).
- **Recursing through `api/fs/search`**: it honours `.gitignore`, so generated files vanish
  silently, and `q` is required so "everything" cannot be asked for. **An empty result and a
  search that never ran look identical** (decision 2).
- **W x H on the card** (P0): `fs/tree` does not carry dimensions. A thumbnail's `naturalWidth` is
  the size AFTER an integer downscale, not the original — and because small images serve the
  original, it is **sometimes right**, which is worse than always wrong. The enlarged view and the
  file pane, which read the original, still show it. Putting it on the card needs an endpoint that
  reads headers via `DecodeConfig`; that is P1.
- **Asking for `thumb=256`**: less bandwidth, but the same image is decoded twice, once for the
  mirror and once here (decision 4).
- **Resolving on click to decide whether to show the session item**: the item grows in late
  (decision 8).
- **Deriving the session UUID in the Console**: the input to `uuidV5` (`dir + "|" + name`) is the
  Agent's internal business and starts drifting the moment it is copied (decision 8).
- **Carrying only the count and fetching the path from an endpoint**: it can gate the item, but it
  adds an endpoint, a CP allowlist line and a round trip for nothing (decision 8).
- **Thumbnails in the rail**: that collides with the left-pane IA that was withdrawn. The rail is
  rows; surfaces go on the right (as in 0078).
- **Virtual scrolling**: the 300 cap plus `loading="lazy"` is enough. Measure first (open
  question 1).
- **Swipe to navigate on a phone**: horizontal drag belongs to session rotation (decision 5).
- **Mixing in video and PDF**: start with a gallery of IMAGES. PDF already has its own surface
  (ADR 0063), and video needs a separate conversation about playback and bandwidth (open
  question 2).

## Impact

- **Two touches in the Agent**: `mtime` on `fsEntry`, and two fields in `wireSession` (the count
  and the path). No new route.
- **Two fields in the CP's `sessionWire`** (plus the golden and the round-trip test). **No new
  route means no allowlist entry.**
- **No additional polling**: the count rides the existing session list, and the folder is re-read
  only while somebody is looking (decision 6).
- **Console**: a new `features/gallery/` (view, pure functions, CSS, opener);
  `layout/{types,migrate,ops}.ts`; `features/panes/{Pane,paneTitle,LayoutMap}`; the four entry
  points (`project/ProjectFiles.tsx`, `sessions/SessionMenu.tsx`,
  `mirror/transcript/blocks.tsx`, `viewer/parts/FileViewerShell.tsx`); sharing
  `mirror/parts/ImageLightbox.tsx`; i18n (ja/en).
- **The bundle does not grow** (no new dependency: the thumbnails and the lightbox are both
  already there).
- Tests: pure functions (image filter, ordering, cap, totals); DOM (cards render, the empty state,
  enlarge vs the corner button, navigation, which right-click items appear); Go (`mtime` is
  carried, the count and path, omitted at zero, never crossing the denylist); a real browser (grid
  reflow, lazy loading).

## Phases

- **P0**: decisions 1-9. One folder level, plus the five ways in.
- **P1**: a card's right-click menu (open in a pane, download, copy path, delete); "send" to a
  session or an assistant; W x H (the header-reading endpoint); recursion into subfolders; a
  surface over all sessions under `generated`; tile size (S/M/L) and the matching `thumb`.
- **P2**: generalizing to "media" including video and PDF (whether it is wanted is open
  question 2).

## Open questions

1. **Is 300 the right cap?** It has not been measured. Count how many cards are visible at a pane
   width of 1100 px and how long two-at-a-time decoding makes that wait, then decide (and record it
   next to `fs_thumb.go`'s own measurements).
2. **Video and PDF?** This starts as a gallery of images, but a place like `docs/img` holds SVG,
   PNG and PDF together. Mixing them renames the thing to "media".
3. **Is a surface over all of `generated` wanted?** It is two levels, so N+1 `fs/tree` calls would
   build it — but mapping a folder back to a session name means reading `generatedImagesPath`
   backwards. Wait for the ask.
4. **Generated images of an archived or deleted session.** The images survive 30 days, but once the
   session leaves the list so does decision 8's entry. The tree still opens it, so P0 leaves this
   alone and watches whether the `generated` surface (open question 3) is the answer.
