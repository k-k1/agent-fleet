# 0080. A folder of images is a grid of cards — the gallery is one new pane kind, and every entry is one item in a menu that already exists

English | [日本語](0080-image-gallery-pane.ja.md)

- Status: **accepted** (drafted 2026-09-13; accepted the same day with the review's corrections
  folded in).
- Follow-ups: #961
- Related: [0049](0049-session-changed-files.md) decision 4 and [0046](0046-drawio-viewer.md)
  (**do not add a `PaneKind`; add one more face to an existing pane** — this ADR argues the
  exception) / [0078](0078-sessions-overview-pane.md) (the nearest precedent: one pane kind, the
  existing menu borrowed whole, a grid of cards) /
  [0069](0069-image-generation-providers.md) (where generated images live, and for how long) /
  [0063](0063-document-preview.md) (`api/fs/download` is the one endpoint that returns raw bytes) /
  [0017](0017-keyboard-system.md) (the command table — why nothing is registered is decision 1)
- The review and the three parallel implementation lanes are recorded in
  [docs/98](../log/98-image-gallery.md) (Japanese).

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
- As a pane it gets splitting, tabs, pop-out, layout persistence and the phone's single-pane
  display **from machinery that already exists** — including sitting next to the mirror so you can
  watch images land. (Find-in-pane is NOT among them: the Console has no such machinery, and
  `Ctrl+F` is the browser's own search, which can only hit the file names the cards render.)
- It walks all eight places (the union, the stored-value validation, the identity check, the
  render switch, the title, the minimap abbreviation, pop-out eligibility, i18n). **Forget the
  validation in `migrate.ts` and a reload turns the pane into a blank terminal.** `galleryPath` is
  a stored value, i.e. untrusted input: reject a non-string, a leading `/`, `..`, control
  characters and anything over the length cap — the same severity as the `browser` kind's `path`.
- `sort` lives in the pane's CONTENT, not in React state. A tab switch unmounts this view, and a
  setting that snapped back on every switch reads as broken (0078 decision 1). The default is
  `"new"`.
- `gallerySession` holds the session's SLUG (`name`), not its display name. It is set only when
  the pane was opened from decision 8's entry, and the render side resolves that name to the
  session it currently is, titling the pane "Generated images — <session display>" (the folder is
  named by a UUID, so the tail of the path is unreadable as a title). Without it the title is the
  folder name. **Never bake the display name in**: a rename would leave the old title behind, and
  `migrate.ts` validates this field as a session name (`ValidName`'s `^[A-Za-z0-9_-]{1,40}$`), so
  **a Japanese title is dropped whole on the next reload** — walked into during implementation.
- The identity check (`sameTarget`) is `galleryPath` alone. `sort`, `galleryFocus` and
  `gallerySession` are state of the same surface, so opening the same folder twice does not
  produce two panes.
- **Nothing is registered in the command table (0017)** in P0. 0078 could add `open.sessions`
  (`g s`) to `features/keys/commands.ts` because that surface takes no argument; a gallery needs a
  folder, and a global key has nothing to hand it. Opening is always contextual (a right-click, a
  header, a session). If a default target ever emerges — "the gallery I had open last" — it is P1.

### Decision 2 — the listing is the existing `api/fs/tree`; the only addition is `mtime` on `fsEntry`

- No new listing endpoint. One level of `{name, type, size}` is what this endpoint already returns.
- **Add `mtime`** (`fsEntry` in `workspace/agent/fs.go`). Neither "newest first" nor "3 minutes
  ago" can be written without it. Generated filenames carry a `unixnano`, so name order IS time
  order there — but that is an accident of generated images and does nothing for a folder of
  screenshots.
  - The shape is an integer `mtime` in unix seconds (`omitempty`). The `e.Info()` that already
    fills `Size` also returns `ModTime()`, so **directories get it from the same single call** —
    today `Size` is filled for files only, and that is not the line to copy here (sorting folders
    by recency is the obvious next ask).
  - **An older Agent returns no `mtime`.** The Agent ships separately from the Console (native,
    pinned versions), so this really happens. **Fall back to name order and print no relative
    time** — the same manners as `fs_thumb.go`'s advisory parameter: no capability probe, no
    version comparison.
- No effect on the tree: every reader declares its own local `Entry`
  (`features/project/ProjectFiles.tsx` and friends; there is no shared type), so one more field
  breaks nobody, and `ProjectFiles`' `sameEntries` compares name and type only, so a moved mtime
  causes no extra repaint.
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
  Decoding is not the only queue: once `max-age=60` has passed, a re-mount issues **one conditional
  request per card**, and they line up behind the browser's six-per-host limit (a 304 is still a
  round trip — open question 1).
- The original bytes are fetched only on enlarge (the lightbox). A card always shows the
  downscaled copy.
  - 🔴 **P1 found the gap (2026-09-14)**: "on enlarge" is exactly where the waiting moved. An
    original averages ~1 MB here, and the frame was blank until it arrived. The card's
    thumbnail now **stands in** and the same `<img>` swaps its `src` once the original has
    decoded (zoom and pan survive it), blurred one step so "not there yet" is legible and the
    picture visibly sharpens. Alongside it: the **neighbour is prefetched after 400 ms**
    (skipped when `saveData` is set), and the original's URL carries `v=<mtime>` too, so a
    picture paged back to costs no request at all.
  - 🔴 **A second P1 gap, same day (2026-09-14)**: three "feels slow" reports never measured the
    BROWSER side — only `fs_thumb.go`'s own decode cost was (95 ms cold / 44 µs cached, 4-wide).
    Measured in headless Chromium against a real 202-image folder
    (`console/scripts/gallery-perf/check.mjs`: drives the real bundle against a stub whose
    `api/fs/download` reproduces that same concurrency/latency shape, so the browser sees a
    realistic queue without needing a live Agent):
    - `entries === null` fell into the SAME branch as a populated folder, so only the "Up" card
      drew while the app was still booting — measured ~1.2 s with nothing else on screen, and no
      indication anything was happening. Fixed with its own branch (`EmptyState icon="loading"`).
      A background refresh never shows it — `load()` keeps the prior listing on screen on
      anything but the first read of a folder — so this is only ever the first look at one.
    - `loading="lazy"` alone requested 54 of 202 thumbnails with **zero scrolling** (Chromium's own
      "how far ahead is worth it" heuristic is generous, and nothing narrows a request back down
      once its card has scrolled out of view). Scrolling to the bottom right after mount left the
      now-visible row queued behind 50 leftover requests from cards nobody was looking at anymore,
      arriving ~1 s late.
    - Fixed with an `IntersectionObserver` per card (`useArmed`, `rootMargin: "480px 0px"`, rooted
      on `.gal-body` rather than the viewport — a pane can be narrower than the window), gating
      the `<img src>` itself rather than trusting `loading="lazy"` alone: a card outside the
      margin never enters the browser's six-per-host queue at all, and once armed it stays armed
      (scrolling a loaded picture away and back must not re-request it). Re-measured: 34 requests
      at rest (down from 54), and scrolling right after mount totals 33 requests instead of
      ballooning to 107, with 0 competing requests at the moment of scroll (was 50).
      `fetchPriority="high"` rides along once armed, on top of a queue that is now short in the
      first place.

### Decision 5 — cards behave like the transcript's; the lightbox is shared and gains ← / →

- **Body = enlarge (lightbox), corner button = open the file pane.** `FileCard` already splits it
  this way, for the same reason: looking should not cost a pane.
- `mirror/parts/ImageLightbox.tsx` **moves to a shared home** (`features/viewer/ImageLightbox.tsx`)
  and gains `←`/`→` navigation and a "3 / 12" position. `ImageView`'s zoom handle is used as it is.
  **The mirror's own appearance and behaviour do not change** — navigation appears only when a list
  is handed in.
- Escape stays on `useEscLayer`, and the existing close rules (backdrop closes, a click on the
  image stays a zoom, a pan that ends on the backdrop does not close) carry over unchanged.
  **Closing on the phone's Back does not**: `useBackClose` is called by `MirrorView`, not by the
  component. Owning "close" stays with the owner after the move, so the gallery wires the same hook
  — forget it and Back skips the lightbox and leaves the pane instead.
- **No swipe navigation on a phone** (P0). Horizontal drag already belongs to session rotation
  (the `data-no-swipe` tug of war), and touching it would break an existing gesture. Buttons and
  arrow keys navigate.
  - 🔴 **Added in P1 (2026-09-14, at the user's request).** One premise was wrong: the lightbox's
    overlay sets `data-no-swipe` ITSELF, so session rotation is already standing down while it is
    open and there is no tug of war inside the overlay. The only real competitor is the PAN while
    zoomed (`ImageView`), so the gesture is fenced twice: **touch only** (nobody drags a mouse
    sideways meaning "next"), and **at fit only** (zoomed in, a horizontal drag is the pan). The
    threshold is 48 px and the movement must be 1.5× more horizontal than vertical, so a vertical
    scroll that drifted sideways does not page.

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

### Decision 7 — every way in is one item in a row that already exists (a menu, or the lightbox's bar); no new surface

1. **Right-click a folder in the rail** → "Open gallery".
2. **Right-click an image file in the rail** → the same item. `menuDir` already points at the
   parent, so it opens the parent's gallery positioned on that image (`galleryFocus`). Not shown
   for non-image files.
3. **The button row of the lightbox a mirror `FileCard` opens** → "Open folder". It opens the
   **parent folder** of that image, so no session resolution is involved. There is no
   "shared-files panel" in the mirror: there are cards inside the transcript
   (`transcript/blocks.tsx`, `FileCard` / `UserFileBlock`), and **an image card's two targets are
   already spoken for** — body enlarges, corner opens the pane. A third corner button would make
   decision 5's split unreadable, so the item goes on a button row that already exists: the bar of
   the lightbox decision 5 shares. The gallery shows the same item in the same bar.
4. **The image viewer's header** → "Gallery of this folder". The header is
   `viewer/parts/FileHeadControls.tsx` (the info bar `FileView` assembles);
   `viewer/parts/FileViewerShell.tsx` is the reading surface and holds no header. The precedent
   for a folder-scoped action there is `FileView`'s `revealInFiles` (`onOpenDir`).
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
  (`internal/session/uuid.go`; the actual call is `session.UUID(meta.Dir, body.Session)` in
  `imagegen/http.go`). Both inputs are on the wire, so this is **not something the Console is
  unable to compute** — SHA-1 is in `crypto.subtle`. It is something it should not: copying an
  Agent-internal derivation is the kind of duplication that starts drifting silently the moment one
  side is fixed (and it would want an `await` in the synchronous place a menu is built). Resolving
  it on click would decide "is there anything" AFTER the menu opened, so the item would grow in
  late, with the pointer already moving. With the path on the wire there is **no new endpoint and
  no CP allowlist entry**, and the click opens the pane immediately.
- **Cost.** One `ReadDir` per session per list build (the list already reads — and sometimes
  writes — a meta file per session). **"A small directory" is not a guarantee**: retention is 30
  days, so a session that generates daily holds hundreds of files, and counting means reading every
  name. So **memoize on the directory's mtime in P0** — a few lines, and nobody has to come back
  and measure it. The wire bytes are omitted at zero, so a deployment that never generates pays
  nothing.
- **An older Agent sends neither field** (same reason as `mtime`), and then the item does not
  appear. No capability probe, no version comparison: absent means not shown, which fails the
  right way.
- **The relay trap.** `sessionWire` (`control-plane/workspace_handlers.go`) decodes the Agent's
  answer and re-emits it, so **a field it does not declare is dropped silently**. The Console's
  unit and DOM tests build `Session` objects directly, never crossing the relay, and the TS field
  is optional so typecheck stays quiet too; the symptom is "the item never appears". **Four places
  to touch**: `sessionWire` itself; `contract_session_test.go`'s `sessionWireBinding` AND `tsKeys`;
  `session_wire_test.go`'s Agent-shaped payload and post-relay expectation; and a regenerated
  `testdata/wire.golden` (its `# count:` moves). Only CI's `control-plane` job catches it, i.e.
  **it goes red only once the feature is finished**.
- **While the workspace is stopped** both fields come from the DB mirror, i.e. absent, so the item
  disappears. That is correct: without the Agent the gallery cannot be read at all.
- **Only af's `generate_image` output is counted.** codex's own `generated_images` lives under
  `.codex`, which is deliberately unbrowsable (`fsDeny` — the very reason ADR 0069 moved ours),
  so it does not appear here. Pasted images are out of scope too: a different axis, with its own
  entry if one is ever wanted.
  - 🔴 **That entry landed (2026-09-20).** Both the chat composer's pre-send chips and a sent
    turn's pasted-image thumbnails now open the shared `ImageLightbox` too
    (`features/chat/parts/ChatAttachStrip.tsx`, `ChatPastedThumb.tsx`) — a fourth caller of
    decision 5's shared component, wired through `ChatView`'s own lightbox state and
    `useBackClose`, same pattern as `MirrorView`. No wire field, no pane and no endpoint: a sent
    thumbnail used to `window.open` a new tab, and the composer's chips had no click handler at
    all.

### Decision 9 — one level only; recursion is P1 and arrives as its own endpoint

P0 lists what is directly under `galleryPath`. Recursion means walking, which needs an endpoint
that can ignore `.gitignore` and bound the result (`GET /fs/images?path=&depth=&limit=`) — and that
gets built **once recursion is known to be needed**. The flat cases (generated images, `docs/img`,
a folder of screenshots) are most of the cases, and they come first.

**Revised in P1 (2026-09-14, at the user's request): let the READER do the walking.**
Subfolders are drawn as cards, and opening one MOVES THIS PANE into it (the breadcrumb goes back;
`galleryFocus` and `gallerySession` are dropped on the way, `sort` is carried). **The listing is
still one level**: no recursive endpoint was built — it is one `fs/tree` here and another one
there. That answers "let me see the subfolder", and leaves the dedicated endpoint for the only
case that still needs it: flattening several levels into one grid (an X/Y grid, open question 2).

- **A folder card shows no image count — except a session's.** The session list already carries
  `generatedImagesPath` and `generatedImages`, so a card whose path matches one is labelled with
  the **session's display name and its count**. Doing the same for every folder would mean one
  `fs/tree` per card, which is exactly what decision 2 refused.
- `galleryPath` can now be the **empty string (the browse root)**. "Up" has to reach the same
  place the file tree starts at, or it dead-ends in `.cache`; the stored-value validator therefore
  accepts `""` while still rejecting a missing key (a truthiness test cannot tell the two apart).
- 🔴 **P1, user-requested (2026-09-14): the breadcrumb moves to its own row, gains an always-on
  "Up" button, and the browser's own Back button retraces folder navigation.**
  - The breadcrumb shared a single row with the title, the count and the sort toggle, and at a
    few levels deep it had nowhere left to grow. It now sits in its own full-width row below the
    head (`.gal-path`), the same pattern `TerminalView` already uses for `ContextBar` — a plain
    sibling under `<ViewHead>`, not a feature of the head itself.
  - **"Up" is now a persistent button in that row (disabled at the root), not only a grid card.**
    A long folder scrolled past its top has the card off-screen; the button is always there. It
    calls the exact same `navigate()` the breadcrumb and the grid's own "Up" card call, so all
    three — and the browser's Back button, below — always agree on where "up" leads.
  - **Back button integration turned out to be nearly free**: `layout/store.ts` already keeps one
    browser-history entry per **pushed** layout commit and restores it on `popstate`
    (`wireLayoutHistory`, predating this ADR) — `setPaneTarget` was simply one of the callers that
    opts OUT of pushing (`push: false`, the same stance as tab selection and a divider drag: a
    content tweak is not a place to come back to). The fix is a one-line change of stance for this
    one caller: `setPaneTarget` gained an optional third `push` argument (default `false`,
    every other caller unaffected), and `GalleryView.navigate()` — the function the breadcrumb,
    the folder cards and the header's "Up" button all already funnel through — passes `true`.
    No new history/popstate plumbing was written for the gallery at all.
  - Verified in headless Chromium against a real folder: enter a subfolder → click the header's
    "Up" → press Back twice → lands exactly back where the subfolder was entered, then back at
    the folder it was entered from. `sort` survives every step (it is read live, not stored in
    the history entry, so it is never what a Back press undoes).

### Decision 10 — a folder that has been walked into is remembered (listing, scroll position, page size)

Even after decisions 4 and 9, "opening a folder and coming back is slow" was reported again. It
is **the listing, not the thumbnails**: every change of `path` dropped `entries` to `null` and
waited for an `api/fs/tree` round trip — including when the destination is the folder that was
on screen a second earlier. The CP tags JSON GETs with a weak ETag, so an unchanged folder
already costs no BODY, but **a round trip is still a round trip**, and that window was blank.

- **`galleryCache.ts` remembers 30 folders** (LRU, module-level — a store would re-render every
  gallery pane on every scroll event, since the position is written from one). It holds the
  listing, when it was read, the **scroll position** and the **`limit`**. The last two are the
  reader's PLACE in the folder: being dropped at the top of the first 300 again after paging
  halfway down a folder is its own kind of slow.
- **Draw first, then read.** A cached folder is drawn without `EmptyState(loading)` and the read
  behind it only corrects what changed. **The "new card" tint diffs against the remembered
  names**, so walking back in lights up nothing. When nothing changed, the CP's 304 makes
  `api()` **replay the very same object**, so the memo dependencies do not change and neither
  the sort nor the `<img>` elements are redone — applying the difference is essentially free.
- **The one lie this cache could tell** is a folder that is gone. So a **first read that answers
  "not there / denied" throws the remembered listing away and shows the error state**, while a
  transport failure or a 5xx keeps what is drawn (decision 6's reasoning: emptying the grid over
  one 502 is the worse lie).
- **Point at it and it is read.** `pointerdown` on a folder card, and 120 ms of hover, fetch the
  listing (deduped while in flight, skipped for a folder read within 10 s), so the click lands on
  something already in hand.
- **The change of folder is made DURING the render** (React's "adjust state when a prop
  changes"). In an effect it happens after a paint, and that paint is the previous folder's
  cards under the new folder's path — a frame of the wrong pictures, each firing a thumbnail
  request for a path that does not exist. It shows on the way back out of a folder.
- Measured (`console/scripts/gallery-perf/check.mjs --case nav`, against a stub given a 150 ms
  listing round trip; the real 7 folders / 202 images): **back up out of a folder 280 ms → 60 ms**,
  **into the same folder again 345 ms → 140 ms**. What is left is not the network but the cost of
  **drawing 202 cards**, which is the missing measurement behind open question 1.
- **Two things measured and rejected** (both looked like wins): arming the first 12 tiles without
  waiting for the observer moved the nav numbers not at all, and pushed the requests fired by a
  scroll straight after mount from **33 to 44** — undoing decision 4's measurement. Marking only
  those first tiles `fetchPriority="high"` inverts the hint once requests are already gated by
  the observer: a card armed LATER is the one being looked at, and it would be the `auto` one.

### Decision 11 — a folder card shows what is inside it (`api/fs/tree?peek=<n>`)

A folder named by a session UUID tells a reader nothing: the card says "03603f64-9cbc-…". One
picture out of it says most of what they wanted to know.

- **The Console cannot build this.** As decision 9 refused, it would be one `fs/tree` per card.
  So it rides on the listing instead: with `peek=<n>` (1-4, advisory) a directory entry also
  carries `preview:[{name,mtime}]` and `images:<count>`.
- The cost is one ReadDir per subfolder, fenced three ways: the first 60 directories, a 300 ms
  budget for the whole listing, and n of at most 4. Past any of them the rest simply carry no
  preview (advisory, so there is no error to draw). Memoized on the directory's own mtime —
  which is what changes when an entry appears — so the gallery's 20-second re-listing is a hit.
- **Paired with `warm=`.** The covers live in OTHER folders, so warming the listed one never
  reaches them; without this a folder page (the generated root is exactly that) decodes every
  cover cold.
- A cover is fetched at `thumb=512`, the SAME key the grid inside will use, so walking into the
  folder finds its first pictures already warm. The cards are gated by `useArmed` like any
  other (sixty subfolders must not mean sixty requests), and a card with no cover — "Up", or
  any folder from an Agent that does not peek — never even observes.
- **The folder icon stays, as a badge over the picture.** Drop it and half the grid becomes
  tiles that look like pictures but are not, with no way to tell but clicking.
- A by-product: **every** folder can now show a count, where decision 8 could only do it for
  the ones the session wire knew about. The session NAME still comes from that wire.

### Decision 12 — the lightbox fetches a screen-sized copy, not the original (`preview=<max edge>`)

- 🔥 **The "middle step" idea died on measurement.** This deployment's generated pictures are
  **832x1216 PNGs of about 1.1 MB** — already the size a lightbox shows them at. Since only
  integer factors are available, there is no step to put between the card's thumbnail and the
  original (asking for 1024 gives factor 1, i.e. the original; 608 halves the picture and is
  visibly soft).
- **What pays is re-encoding, not resizing.** The same pixels as JPEG come to **113-138 KB,
  about 9-11% of the PNG** (measured over three real pictures at q80/85/90: 96-175 KB; 79-114 ms
  to decode plus ~30 ms to encode, both cached afterwards).
- So `preview=<max edge>` sits beside `thumb`, differing only in what happens when there is
  nothing to downscale: `thumb` serves the original (which the mirror's cards have relied on
  since decision 4), `preview` re-encodes at the source's own size.
  - Only for **PNG** sources (a JPEG would lose a second time; a GIF may be animated) and only
    when the picture is **opaque** (transparency has to stay PNG, which saves nothing). When it
    does not help, the existing `buf.Len() >= size` guard is the last backstop.
  - `preview` ROUNDS its factor where `thumb` truncates: 4000 px asked for at 2048 becomes
    2000, and 1216 px asked for at 1024 stays 1216. Overshooting the asked edge by less than
    half a step is fine for something being looked at, and not fine for a card.
  - `thumbMaxEdge` goes 1024 → **2048**, because the lightbox asks for its own viewport times
    the device pixel ratio. The Console quantises that to **1024/1536/2048**: a distinct edge
    per window size would be a distinct decode and cache entry in the Agent.
  - The cache key carries the MODE. Same file, same edge, different answer — sharing an entry
    means one of them serves the other's bytes.
- **The original is still one click away**: the card's corner button (file pane) and the
  download both serve the real bytes. The lightbox is a surface for LOOKING, and is the only
  place that gets a copy.
- The neighbour prefetch moved to the same door — prefetching one URL and then displaying
  another wastes the whole prefetch. The mirror's shared-file lightbox is untouched so far
  (the same move applies to it).

### Decision 13 — make the downscale itself cheap (stop calling `src.At()`)

Splitting the measured "95 ms for one cold picture" showed **`boxDownscale` alone at 95 ms and
1,571,330 allocations** — one per source pixel. Every `src.At()` instances a concrete colour value
into the `color.Color` interface, and that is an allocation each time.

- A type switch uses the **typed accessors** (`RGBAAt`, `NRGBAAt`, `GrayAt`, `YCbCrAt`) for the
  four types PNG and JPEG actually decode to; those return concrete values, so nothing is
  boxed. Anything else still falls through to `At()`.
- Measured (1024x1536 PNG, this shared host): **`boxDownscale` 95.2 ms → 24.2 ms, 1.57 M
  allocations → 3**, and **one cold 512 px thumbnail 154.6 ms → 40.8 ms**.
- 🔥 **Do not re-derive the arithmetic by hand.** The first version scaled `color.YCbCrToRGB`'s
  8-bit answer up to 16 bits; `color.YCbCr.RGBA()` works at 16-bit precision throughout and is
  **off by one** — a different picture. `TestPixelReaderMatchesAt` caught it. The fast path is
  "call the same RGBA(), without the boxing", and nothing else.

### Decision 14 — a card's longest edge follows the screen's density (revising decision 4's flat 512)

A card is 150-200 px wide at 4:3, so a 1x screen shows 138x104 to 200x150 CSS px. **Measured on
one real generated picture: 42 KB at 512 against 15 KB at 256** — 65% spent on pixels that
screen cannot show. **The Agent's cost is the same either way** (57 vs 59 ms: the work is the
decode, not the scale).

- Decision 4 fixed one number because the mirror asks for 512 and a second edge means decoding
  the same file twice. True, but **the mirror looks at shared files and the gallery at generated
  folders**, which in practice are different pictures.
- The grid, **the covers (decision 11)** and the lightbox's placeholder all read the SAME value.
  They show the same pictures at the same size, so splitting them is what would really cost a
  second decode. `warm=` sends that value too — warming 512 for cards that ask for 256 warms
  nothing that gets drawn.
- **Read per render**, not frozen into a constant: a window dragged to another monitor changes
  it, and being wrong costs one re-request at the other size.

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
  and the path). No new route — so **`testdata/routes.golden` must not move**; if it does, the
  implementation has left the design.
- **Two fields in the CP's `sessionWire`** (across the four places above:
  `contract_session_test.go`, `session_wire_test.go`, `wire.golden`). **No new route means no
  allowlist entry.**
- **No additional polling**: the count rides the existing session list, and the folder is re-read
  only while somebody is looking (decision 6).
- **Console**: a new `features/gallery/` (view, pure functions, CSS, `open.ts`);
  `layout/{types,migrate,ops}.ts`; `features/panes/{Pane,paneTitle,LayoutMap}`; the entry points in
  four files (`project/ProjectFiles.tsx` for both right-click items; `sessions/SessionMenu.tsx`,
  one item and three ways in; `viewer/parts/FileHeadControls.tsx` with `viewer/FileView.tsx`; the
  shared lightbox's bar, which is where a mirror `FileCard` lands); moving
  `mirror/parts/ImageLightbox.tsx` to `viewer/ImageLightbox.tsx`; two fields in `types/session.ts`
  (the CP's `tsKeys` reads that file); i18n (ja/en).
- **The bundle does not grow** (no new dependency: the thumbnails and the lightbox are both
  already there).
- Tests: pure functions (image filter, ordering, cap, totals); DOM (cards render, the empty state,
  enlarge vs the corner button, navigation, which right-click items appear); Go (`mtime` is
  carried, the count and path, omitted at zero, never crossing the denylist); a real browser (grid
  reflow, lazy loading).

## Phases

- **P0**: decisions 1-9. One folder level, plus the five ways in. **Started in parallel as three
  lanes**:
  - **Lane A (Go and the wire)**: `fsEntry.mtime`, the two `wireSession` fields, the CP's four
    places, the two fields in `types/session.ts`. It shares not one line with the other two.
  - **Lane B (the surface)**: the pane kind `gallery` through its eight places, and
    `features/gallery/` (view, pure functions, CSS, `open.ts`). It fixes `openGallery()`'s
    signature first.
  - **Lane C (the lightbox and the ways in)**: moving `ImageLightbox`, its navigation and position,
    the five entry points, i18n. The entry points call lane B's `open.ts`, so C **merges B's branch
    before finishing**.
  - The only cross-lane collision is i18n keys (`gallery.*` is B; the entry-point wording is C).
    `layout/types.ts` is touched by B alone.
- **P1 (partly landed 2026-09-14)**: ✅ folder cards and breadcrumb navigation (decision 9,
  revised); ✅ four buttons that jump to the generated root (the answer to open question 3);
  ✅ swipe paging in the lightbox (decision 5, revised).
  Still open: a card's right-click menu (open in a pane, download, copy path, delete); "send" to a
  session or an assistant; W x H (the header-reading endpoint); an endpoint that flattens several
  levels into one grid; tile size (S/M/L) and the matching `thumb`.
- **P2 (landed 2026-09-15)**: ✅ decision 10 (a folder walked into is remembered); ✅ decision 11
  (a folder's cover and count, `peek`); ✅ decision 12 (the lightbox's screen-sized copy,
  `preview`); ✅ decision 13 (the fast path in the downscale).
  ✅ decision 14 (a card's edge chosen by device pixel ratio).
  Still open: **using `preview` for the mirror's
  shared-file lightbox** (the same move as decision 12, not started); **warming `preview` at
  generation time** (only `thumb=512` is warmed today, so the first enlarge of a new picture
  pays ~110 ms to decode and ~30 ms to encode).
- **P3**: generalizing to "media" including video and PDF (whether it is wanted is open
  question 2).

## Open questions

1. **Is 300 the right cap?** It has not been measured. Count how many cards are visible at a pane
   width of 1100 px and how long two-at-a-time decoding makes that wait, then decide (and record it
   next to `fs_thumb.go`'s own measurements).
   - **2026-09-14**: the half of this question that worried about "a re-mount queues one
     conditional request per card" now has a different answer — decision 4's `useArmed` means 300
     rendered cards no longer implies 300 in-flight requests; only the ones actually near the
     scroll container's viewport ever ask at all, mount or remount alike. Whether 300 is the right
     number of cards to draw (how many fit a 1100 px pane, whether "show more" is reached too soon
     or too late) is still unmeasured.
   - **2026-09-15 (out of decision 10's measurements)**: the count itself now has a number too.
     With the listing already in hand, **drawing 202 cards costs about 140 ms** (`--case nav`'s
     "into the same folder again" leg, which pays no network at all); the same leg back to a root
     of 7 folders is 60 ms. So what 300 costs is not requests but **rendering**, and the next move
     is either a lower cap or virtualizing the grid (revisiting "virtual scrolling" under options
     rejected).
2. **Video and PDF?** This starts as a gallery of images, but a place like `docs/img` holds SVG,
   PNG and PDF together. Mixing them renames the thing to "media".
3. ~~**Is a surface over all of `generated` wanted?**~~ **Answered (2026-09-14): no surface of its
   own is needed.** With folders as cards (decision 9, revised) the generated root is just another
   gallery page, and mapping a folder back to a session is exactly the `generatedImagesPath`
   lookup this question predicted — free, because the session list already carries it. Four ways
   in: the minimap's button row, the command table's `g g` (**the one exception to decision 1's
   "nothing is registered"**: "a gallery needs a folder" does not apply to a fixed target), the
   Files section header, and the image-generation pane's header.
4. **Generated images of an archived or deleted session.** The images survive 30 days, but once the
   session leaves the list so does decision 8's entry. The tree still opens it, so P0 leaves this
   alone and watches whether the `generated` surface (open question 3) is the answer.
