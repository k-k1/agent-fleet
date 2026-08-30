# 0046. Ship drawio's official viewer for `.drawio`, and display it confined in a sandboxed iframe

English | [日本語](0046-drawio-viewer.ja.md)

- Status: **P0 implemented** (2026-08-16) plus **P1 (stencils) implemented** (2026-08-22. The design
  and the measurements are in [docs/65](../log/65-drawio-viewer.md)). Feasibility was measured in the
  development workspace's headless Chromium **with all external communication blocked**, and after
  implementation a verification harness (`npm --prefix console run drawio:check`) judges "it renders,
  with zero outbound requests" in a real browser every time. **P1 withdrew the original proposal (the
  frame fetching directly from `STENCIL_PATH`) on the basis of measurement, adding decisions 5b and
  5c.** The pre-seeding script for closed networks (P1b) and P2 onwards are not started.
- See also: [0027-markdown-code-editor.md](0027-markdown-code-editor.md) (the File pane's surfaces and
  the save machinery — this ADR adds one more surface) / [docs/35](../log/35-packaging.md) (what is bundled and
  distribution size) / [0031-mcp-registry.md](0031-mcp-registry.md) (the pattern of checking untrusted
  input against a name before using it)

## Context

The Console's File pane can only show `.drawio` as **raw XML**. `.drawio.svg` / `.drawio.png` already
display, since their extensions are images, so the gap is **plain `.drawio` alone**.

## Decision 1 — render with drawio's official `viewer-static.min.js`, bundled

Neither a home-grown renderer nor an external service.

- **Rendering fully offline was measured.** In a headless Chromium with DNS entirely blocked
  (`--host-resolver-rules="MAP * 127.0.0.1:1"`), shapes, edges, Japanese labels, multiple pages and
  even **compressed `<diagram>`** (deflate+base64) all rendered correctly. 76 ms to load, ~0 ms to
  render.
- The size is **one file of 4.0 MB (870 KB gzipped)**. The same order as mermaid and marp-core, which
  are already lazily loaded, and as long as it is lazily loaded it does not weigh on the initial
  render.
- **A home-grown renderer is rejected.** Diagrams that use shape libraries would be drawn
  "plausibly wrong". Silent low quality is worse than not displaying.
- **Server-side SVG conversion is impossible.** It needs drawio-desktop (Electron).
- **There is no alternative on npm** (`mxgraph` is only the substrate, `drawio` is unrelated, and
  `react-drawio` wraps an external embed). Vendoring is the only route.

## Decision 2 — do not load it into the app's window; confine it in a sandboxed iframe

Put our own container in an iframe with `sandbox="allow-scripts"` (granting neither
`allow-same-origin` nor `allow-popups`), and **have the parent fetch the XML with `api/fs/download`
and feed it in by postMessage**.

The reasons are all measured.

1. **It creates 932 globals.** Not just the `mxClient` family but `lang` / `dash` / `Base64` /
   `Spinner` / `pako` / `MathJax` / `Editor` / `Graph`, and it **overwrites `window.DOMPurify`**.
   Once it is in the app's window there is no way back.
2. **The toolbar's lightbox carries the diagram outside.**
   `GraphViewer.lightboxHost = window.DRAWIO_LIGHTBOX_URL` (defaulting to
   `https://app.diagrams.net`) is opened with `window.open` and handed the diagram.
3. The XML is untrusted input; even if something slips through it cannot reach the Console's DOM,
   cookies or API.
4. **P0 needs no extra configuration this way.** `<script src>` is not subject to CORS, so even an
   origin-less iframe can load a viewer from our own origin.

**The frame fetches nothing itself** (revised after the real-hardware failure of 2026-08-16).
Originally the viewer was loaded with `<script src>`, but **a request from an origin-less frame is
treated as cross-site and the `SameSite=Lax` session cookie is not attached**, so the CP's `authGate`
rejected `/assets/*` with 401 and only real hardware broke. Both the viewer body and the diagram's
XML are **fetched by the parent and passed by postMessage** (the frame evaluates it as an inline
script). This leaves the CP's authentication and caching rules untouched, and P1's stencils ride the
same path. **A change that adds an external origin to the frame's CSP** is equivalent to reverting to
this bug and is not made.

**Do not postMessage immediately after creating the iframe** (found the same day, in the same
investigation). The srcdoc document does not exist yet and the message is delivered to the initial
`about:blank` and lost (measured: 0/10/50ms did not arrive; 200ms did). Send after the frame emits
`ready`.

**"Could not load" and "cannot be read as a diagram" return different codes.** The bug above was a
failure to load the viewer, yet `parse` was returned uniformly, so the screen said "this cannot be
interpreted as a drawio diagram" and **made it look as though the fault was in the file**. Wrong
wording derails the whole investigation.

**The frame's contents are a `srcdoc` (one module that assembles a string), not a static file.** The
CSP, the neutralisation of external URLs and the message contract all gather in one place, and "are
we showing the lightbox?" and "are external URLs neutralised?" can be checked as strings in a unit
test. The only hashed asset is the 4 MB body, which the srcdoc loads with `<script src>`.

**The external URL defaults cannot be neutralised with an empty string** (found during
implementation). The viewer sets them as `window.X = window.X || "https://…"`, so `""` is falsy and
the external default survives. In fact `DRAW_MATH_URL` was going to `viewer.diagrams.net`, and only
the CSP was stopping it. We put in a dead value that does not go on the network, and **guarantee it
with the harness's "zero outbound requests"** (if a version bump adds a new name, it goes red there).

## Decision 2.5 — implement the interactions (zoom / pan) ourselves in the frame

`GraphViewer` **wires up no gestures at all** (`init` has `pinchEnabled=false` and
`setPanning(false)`, and it does not subscribe to the wheel). With only the toolbar buttons, neither
pinch on a phone, nor Ctrl+wheel, nor double-tap works. It is implemented inside the frame (details
and measurements in docs/65 §65.12).

- **`touch-action: none` is required** — without it a phone's pinch is taken by page zoom and never
  reaches the diagram.
- **Once the user has moved it, do not re-fit when the pane's dimensions change.**
- **`fitGraph()` cannot be used to re-fit** (it returns early and does nothing when the container
  width is unchanged). Restore the `graph.initialViewState` the viewer keeps.

## Decision 2.6 — pass the theme as the **string** `"dark"` / `"light"`

`isDarkMode()` compares strings, so passing a boolean silently renders light. Having darkened only
the background, **labels in the default colour (black) vanished against the dark background**
(measured contrast ratio 1.3:1).

**Do not judge this class of defect by pixels.** The picture that fails to go dark has *more* bright
pixels, because of the shapes' light fills (40778 versus 2387), and a naive threshold gives exactly
the opposite answer. Have the viewer return `isDarkMode()` and compare it with what was requested.

## Decision 2.7 — recreate the whole frame when the theme changes, carrying over where you were

drawio **does not anticipate switching theme back and forth within one document.**
`darkModeChanged()` only touches the CSS class and `color-scheme`; the colour decisions are fixed at
load and first render. Asking the same frame to redraw leaves the viewer answering
`isDarkMode() === true` while **container headings disappear and edge labels keep the light theme's
white pill with black text** (measured).

The price of recreating is re-evaluating 4 MB (~76 ms from cache), and switching theme is not a
frequent action, so it is accepted. **The page, the zoom and the position are carried over**, so it
feels continuous. The page is named by **the diagram's id, not its number** (`graphConfig.pageId`) —
numbers shift when pages are added or removed. The carry-over happens **only when the user had moved
it themselves**; otherwise it re-fits.

## Decision 2.8 — set the background colour inline on both html and body on every render

The srcdoc's stylesheet colours both `html` and `body`, so overriding only `html` inline at render
time is **painted over by `body`'s rule and never takes effect once**. The background was therefore
pinned to "whatever theme the frame was assembled with", and the two symptoms the user reported —
"it stays white when I switch to dark" and "the background stays dark when I switch to light (only
the elements go light)" — were **both this one thing**. **The background colour may be judged by
pixels, because it is a flat colour** (unlike decision 2.6's contrast, the threshold does not point
the wrong way).

## Decision 3 — do not create a new `PaneKind`; add one surface to the File pane

Keep `kind: "file"` and add `diagram` to `fileMode`, giving **two modes, diagram ↔ XML source**.
Tabs, popout, dirty management, the keyboard and the route from the left pane all work unmodified,
and the source side uses the existing CodeMirror editing surface as is (docs/44's saving, conflicts
and following external changes). Detection is by extension (`.drawio` / `.dio`) plus **`.xml` files
starting with `<mxfile` or `<mxGraphModel`**.

## Decision 4 — fetch the diagram from `api/fs/download`, not `api/fs/file`

`api/fs/file` **truncates at 2 MiB** (`maxEditorFileBytes = 2 << 20`,
`workspace/agent/fs_fd_linux.go:19`). A `.drawio` with embedded images routinely exceeds it and
`content` turns into `(file too large to preview)`. `api/fs/download` has no size limit
(`http.ServeContent`). **This single point decides whether half the files open at all.**

## Decision 5 — do not bundle the stencils; the CP proxies them and caches to disk

We take "fetch them when they are needed". **On-demand behaviour at run time is there from the
start** — `mxStencilRegistry` fetches only the sets that appear in the diagram, once, so one diagram
never loads them all. The only problem is distribution size.

- **The stencil `.xml` files are 203 files / 40.8 MB** (`aws4.xml` alone is 6.21 MB; the whole
  `stencils/` including `LICENSE` and `clipart/*.png` is 205 files / 42.8 MB). Carrying that in the
  repository and the image permanently is not worth the proportion of diagrams that use it.
- **Only a ledger is bundled** (`control-plane/assets/drawio-stencils.json`: 203 entries of
  `name → sha256 → size`, 26 KB). The CP takes `GET /api/drawio/stencils/<set>.xml` and answers by
  checking the ledger → the cache → fetching from a pinned upstream if absent → checking the sha256 →
  saving. **The ledger lives on the CP, because the CP is what checks it**; a ledger on the Console
  side is no defence.
- **The ledger guarantees integrity and is at the same time the SSRF defence.** The set name comes
  from **the contents of an untrusted `.drawio`** (`shape=mxgraph.<set>.<x>`). An implementation that
  fetches a name absent from the ledger is a tool for "make the CP hit an arbitrary URL just by
  getting a diagram opened". The request carries no URL; the CP assembles it from the ledger's `base`
  and the set name.
- **Do not trim the ledger.** A set left out is a 404, i.e. that diagram silently degrades. All of it
  is 26 KB, so there is no motive to trim. **What gets trimmed is the pre-seeding**, not the ledger.
- **Outbound traffic happens once, on the CP, shared across the tenant.** It never leaves the user's
  browser.
- **On a closed network it degrades silently** — if the fetch fails it falls back to "outlines and
  colours only" (the same picture as P0) and the pane does not break. **It is not shown as an error**
  (the diagram opened correctly, so it is not an anomaly to put in front of the user). A pre-seeding
  script is provided alongside.

**What changes with and without the stencils has been measured.** Without them,
`shape=mxgraph.aws4.*` still gets its size, outline, gradient and label but **the icon artwork is
empty** (just an orange rectangle). Given the stencils, the correct A1 / EC2 icons appear.

## Decision 5b — do not let the frame fetch. It declares, and the parent fetches and hands over

"The CP proxies" from decision 5 is unchanged, but **who hits that CP** is revised from the original
proposal (point `STENCIL_PATH` at the CP and let the frame fetch; requiring CORS `*` and an authGate
exclusion). Measuring in a real browser confirmed that fetching directly from the frame breaks in
three ways.

1. **It cannot get through authentication.** A request leaving the frame **carries no session
   cookie** (measured). A request from an origin-less frame is treated as cross-site and SameSite=Lax
   is dropped — exactly the hole that made `<script src>` 401 in §65.11-7.
2. **It would mean opening the CSP.** With `connect-src 'none'` the frame's fetch is stopped
   (measured: zero requests). Letting it through requires adding our own origin, and **that revives
   the route by which the frame can reach outside on its own** (the thing decision 2 closed).
3. **After one failure it never retries.** The failure is swallowed and `packages[basename] = 1` is
   set. Measured: after a failure, redrawing produced not a single request and the icons stayed
   empty. A transient failure is pinned for the frame's whole lifetime.

**Having the parent fetch and hand over removes all three, and the picture is identical** —
screenshots of the frame-fetching version and the version fed from the parent via
`parseStencilSets()` matched **byte for byte**. Redrawing after injection needs only
`graph.refresh()` (measured: at 1.8221× zoom, both the zoom and the position were preserved and only
the artwork appeared). **Work out which sets are needed from the expanded model** — scanning the raw
XML finds nothing in a compressed `<diagram>` (measured) — which also **removes the need for the
parent to inflate anything**.

The CP's stencil route therefore sits **inside authGate**, with neither CORS nor an authentication
exclusion.

## Decision 5c — never assemble the name as `basename + ".xml"`

The viewer has a **basename-to-filename remapping** in `mxStencilRegistry.libraries` (a table of 62
sets): `ios7icons → ios7/icons.xml`, `rackGeneral → rack/general.xml`, `ibmcloud → ibm_cloud.xml`,
`pidFlowSensors → pid/flow_sensors.xml`, and so on. Only basenames absent from the table fall back to
`basename.replace("_-_","_") + ".xml"` (157 of 203). The resolution rule exists only inside the
viewer, so **consulting that table in the frame** is the only correct implementation.

Two related measurements:

- **The 48 `.js` files under `SHAPES_PATH` are never fetched.** `libraries` mixes `.xml` and `.js` in
  its lists and the branch reads every file in a list, so it looks as though it would fetch them, but
  `mxStencilRegistry.allowEval = false` at the end of `viewer-static.min.js` blocks it and **neither
  the fetch nor the eval happens** (confirmed `allowEval === false` at run time; pointing both paths
  at our own server produced exactly one request, for `aws4.xml`). We do not need to touch
  `allowEval`.
- **The pure-JS sets have nothing to do with the ledger.** 21 of the 62 `libraries` entries have no
  `.xml` (`archimate3` / `sysml` / `c4` / `er` / `uml25` / `mockup/*` / `emoji`, …). Those shapes are
  baked into viewer-static and **already render correctly with zero requests** (measured on a diagram
  containing six such sets).
- **`libraries.sap` points at a `sap.xml` that does not exist** (there is no `sap` anywhere in
  upstream v31.1.8's `stencils/`). Absent from the ledger, i.e. a 404, is correct. Do not misread it
  as an omission and add it.

## Decision 6 — commit the viewer body to the repository and ship it as a Vite hashed asset

- **Not depending on an external network at build time** comes first, so the 4.0 MB is committed to
  `console/vendor/drawio/` pinned by version and sha256 (with Apache-2.0 attribution added to
  `NOTICE`). The 40.8 MB of stencils is not committed, per decision 5 — **the line for what gets
  bundled is drawn by size.**
- **It must not be dropped into `console/public/`.** The CP marks only `/assets/*` immutable;
  everything else is `no-store` (`control-plane/routes.go:776-785`). Dropped in plainly, the 4 MB
  would be re-fetched every time the pane opens. Import with `?url` so it lands in `dist/assets/`,
  and let version updates be expressed as a change of hash.

## Decision 7 — editing is out of scope for this ADR

It requires self-hosting the drawio webapp (`draw.war`, 53 MB), which puts distribution size in
another league. Raise a separate ADR when it is started. The save machinery itself can use docs/44's
revision/conflict machinery as is, so it is worth recording only that **the hard part is distributing
the editor, not saving**.
