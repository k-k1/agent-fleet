# 0063. Draw PDFs with pdf.js; turn Office documents into something readable with anydoc

English | [日本語](0063-document-preview.ja.md)

- Status: **accepted — P0 (PDF) and P1 (a plain preview of Office documents) implemented**
  (2026-08-31). The design and the measurements are in [docs/82](../log/82-document-preview.md).
- Related: [0046-drawio-viewer.md](0046-drawio-viewer.md) (the same "read a binary inside the pane"
  shape; the bundling and the `?url` convention follow it) /
  [0027-markdown-code-editor.md](0027-markdown-code-editor.md) (how the File pane is put together)

## Background

A PDF or a slide deck sitting in a repository shows up in the Console as nothing but
"(binary, 86.8 KB)". The only way to read it is to download it and open a local application, so the
browser alone is not enough.

A constraint up front: **server-side conversion is not available.** There is no LibreOffice in the
workspace container and no root with which to install one. All rendering and conversion happens in
the browser.

## Decision

### 1. Draw PDFs with pdf.js (do not substitute text extraction)

For a PDF, **the way it looks is the information**. Turning a drawing, a form or a typeset document
into Markdown produces a different document. pdf.js (Apache-2.0, Mozilla) is the one practical
implementation that has been running for over a decade, and 126 KB gzipped plus a 366 KB worker,
**fetched only when a PDF is opened**, is a small enough price.

Rejected:

- **Extract text with anydoc and leave it at that** — rejected for the reason above, though it
  could make sense later as a secondary view.
- **Hand it to the browser's built-in viewer with `<iframe src=…>`** — a moment's work, but the
  theme, the info bar and the pane's own controls all stop applying: what gets embedded is another
  application rather than a Console surface, and we lose control of the display.

### 2. Bundle the cMaps and the standard fonts, with the version in the path

A Japanese PDF that does not embed its fonts renders no glyphs at all without the cMaps (the
encodings such as `UniJIS-UCS2-H` cannot be resolved). That is not rare in Japanese documents, so
**choosing not to bundle them is the same as choosing "Japanese PDFs are broken"**. The build grows
by 2.5 MB (dist 15 MB → 18 MB).

They live under `dist/assets/pdfjs/<version>/` because the Control Plane serves everything under
`assets/` as `immutable`. Putting the version in the path keeps a stale cMap from sticking around
when pdf.js is upgraded.

### 3. Convert Word / Excel / PowerPoint to Markdown with anydoc, and label it a "plain preview"

The route that aims at **reproducing the appearance** (docx-preview + SheetJS + @jvmr/pptx-to-html)
**rendered well in every measurement**. It is still not the one we take, for three reasons.

1. **There is no trustworthy PPTX implementation.** The package with the most convincing name,
   `pptx-preview`, **fails silently** — it leaves a black canvas and throws nothing. The only one
   that rendered properly is a year-old package with a single maintainer, and we are not putting the
   core of the preview in its hands today.
2. **One dependency against three.** anydoc covers docx / xlsx / pptx (plus odt / rtf / epub / csv)
   on its own, so updates and audits happen in one place.
3. **Markdown output means the existing surface just works.** MarkdownView, link resolution, copy
   and read-aloud all apply with nothing added.

The price is **the loss of presentation** (page layout, shape positions and cell colours are gone;
images become alt text). We do not hide it: the surface says "plain preview", and a download link to
the original always sits beside it. If a trustworthy PPTX renderer appears later, an appearance view
can be added alongside the plain preview — this decision does not close that door.

The WASM is large at 2.9 MB gzipped, but **only someone who opens a file of that format pays for
it**. The main startup chunk is unchanged.

### 4. Scanned PDFs, password-protected files and corrupt files stop with a reason

anydoc has no OCR, so a scanned PDF comes back as `needsOcr`. pdf.js reports a password-protected
file as `PasswordException` and a corrupt one as `InvalidPDFException`. **None of these is allowed
to sit silently on a blank surface**: the reason is shown, along with the download route for opening
it locally.

## Consequences

- The Console build grows by 2.5 MB (cMaps and standard fonts). The main chunk is unchanged.
- `console/package.json` gains `pdfjs-dist` and `@firecrawl/anydoc-wasm` (both lazily loaded).
- No backend change. The existing `api/fs/download` returns the raw bytes and honours Range.
