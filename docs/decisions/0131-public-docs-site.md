# 0131. A public documentation site at agent-fleet.org/docs/, and a guide rewritten around tasks

English | [日本語](0131-public-docs-site.ja.md)

- Status: **proposed** (2026-11-07). A pilot exists; nothing is merged. The pilot's code and draft pages are on the
  `docs/public-docs-site-pilot` branch of `k-k1/agent-fleet-site`. Everything below marked *(pilot)* was built and
  looked at there: the site was rendered from `v0.41.0` and checked with headless Chromium. Follow-ups: (issue to be
  filed).
- Related: [0064](0064-docs-three-audiences.md) (the three reader shelves, `guide/` is what ships) /
  [0063](0063-document-preview.md) (the Console's document viewer)

## Context

The user guide (`guide/`) is complete and accurate, and hard to read. It holds 175,603 English words, plus the same
again in Japanese. Several chapters run past 6,000 words (`member/16-cloud-accounts.md` has 10,861 words under two H2
headings). Titles list three to five features ("Files — tree, viewer, Markdown/slides"). One paragraph mixes the steps,
edge cases, design reasons and recovery. Agents grep it well, but a person looking for one task cannot find the point.

The user wants the guide published as HTML in the style of a modern documentation site: topics across the top, one
sidebar per topic, short pages with one feature each, search, and both languages. They also want the content
restructured, not just converted.

## Decision

### 1. Where and how it is built

- Served at **`https://agent-fleet.org/docs/`** (path, not subdomain; the user's choice), next to the landing page.
- Built with **Astro Starlight** in `k-k1/agent-fleet-site`, from a **release tag**, never `main`. `main` describes
  features that have not shipped. The input is `deploy/release/stage-docs.sh`'s output, which already closes the tree:
  runbooks and release notes are copied in, and their links are rewritten.
- Cloudflare Pages currently serves `site/` as is. Adding `/docs/` means deploying from Actions with the built output
  merged in. `_headers` gains a CSP for `/docs/`.
- Search is Pagefind, which ships with Starlight. *(pilot)* Japanese queries hit the expected pages (「子セッション」
  ranks `11-many-sessions` first; 「使用量の上限」 ranks the collaboration chapter and `admin/02-limits` near the top).
- Link validation (`starlight-links-validator`) fails the build on a broken link or anchor.

### 2. `guide/` stays the single source

The site is a second rendering of `guide/`, not a fork. The Console's user guide, the agents' knowledge and
`docs-check.py` keep reading the same files. A converter derives everything the site needs:

- the title from the H1, and the sidebar label without the `NN.` prefix;
- `X.ja.md` becomes `/ja/X/`, `README.md` becomes the directory's index, and the language-switch line is dropped;
- relative `.md` links become routes. Links that leave the staged tree point at GitHub at the same tag;
- GitHub alerts (`> [!TIP]`) become Starlight asides;
- screenshots are taken at 2x and get `width` set to half their pixel width, so they are never stretched.

**Heading ids follow the Console's rule** (`slug()` in `console/src/lib/filemeta.ts`) for `guide/`, and github-slugger
for the runbooks. `docs-check.py` already validates the guide's anchors that way. *(pilot)* With Starlight's default
slugger, 77 anchors broke; with the Console rule, none did.

### 3. Topics and layers

Topics across the top: **User guide / Team admin / Self-hosting / Reference / Release notes**, matching the shelves of
ADR 0064. Each topic has its own sidebar (`starlight-sidebar-topics`).

Two layers:

- **Task pages** (user guide, admin, self-hosting): one page per thing a reader wants to do.
- **Reference**: exhaustive, one page per command and one catalogue of MCP tools, plus the existing capability tables.
  The edge cases that task pages leave out live here, so nothing accurate is lost.

### 4. The task page model

The user agreed this model on the AWS page *(pilot)*, after one rejected draft.

- **Title**: what the reader wants to do, as a verb phrase: "Use AWS SSO" (「AWS SSO を使う」), not a noun or a list.
- **Lead**: one or two sentences on what the reader can do afterwards.
- **How it works** (「全体像」): a two-column table, *what you do* / *what the agent does*. Agent Fleet's tools are
  used by agents, so a reader needs this split first.
- Then: **Set it up** (with the screen), **Use it** (including what appears on screen, e.g. the login toast), **Use it
  more safely**, **When it goes wrong** (by symptom), **Related**.
- **Left out**: preamble ("your company's…"), and how secrets are handled. Secret handling goes to one
  cross-cutting page, "How credentials are handled". Spec edge cases go to the reference. Design reasons are not
  published.
- **UI labels are quoted from the locale catalogues** (`console/src/lib/i18n/locales/{en,ja}/`), never translated.

### 5. Screenshots

- Taken with `console/scripts/shots/` (the real Console bundle against fictional fixtures). They can be retaken every
  release and never carry a tenant, an address or a real account id.
- Light theme. **Crop to the component and the control that opens it**: a popover with its badge, a toast, a dialog.
  The whole bar or pane around it is noise, the user's explicit feedback.
- The harness gains an element `clip` (a union of selectors plus padding) and the doc scenes. *(pilot)*

### 6. Reference pages

- **Commands** (`af-aws-exec`, `af-gcloud-exec`, `af-db`, `af-memory`, `af-scratch`, `af-arch-repair`): one page
  each, written from the command's `--help`, which is the primary source.
- **MCP tools**: there are 81 tool names across the session's `af` server, the Console assistant (read / write) and
  the Control Plane's `/mcp` and `/mcp/memory`. The catalogue is **generated** from the golden tool lists
  (`workspace/agent/internal/mcpx/testdata/mcp_tools/list.json`,
  `control-plane/internal/mcpsrv/testdata/mcp_tools/list.json`). A small JA/EN description file supplies the text, and
  a check fails when a listed tool has no description. A hand-written list goes stale on the next tool.

### 7. How a page is produced

1. Agree the page's flow with the user: ten lines of what the reader does and sees.
2. Collect the facts from the locale catalogues, the components, the existing guide and the code.
3. Take the screenshots.
4. Write the Japanese page; the user reviews in Japanese.
5. A different model fact-checks the page against the sources. *(pilot)* Seventeen statements were checked: none
   wrong, five misleading, all fixed.
6. The user reads it.
7. Write English from the English catalogues, check the two languages against each other, and land both in one change.
   English stays canonical (`docs/CONVENTIONS.md` §5).

## Consequences

- **Paths in `guide/` change.** A redirect map (`guide/_redirects.tsv`) keeps old links working. `docs-check.py`
  validates it, `stage-docs.sh` rewrites old links in shipped release notes, and the site emits redirects. Pointers into
  `guide/` from `workspace/workspace-notes.md`, the topic notes, Go error text and the Console must move with the files.
- The writing rules in §4 and §5 go into `docs/CONVENTIONS.md` when this record is accepted.
- The Console's Markdown viewer (`marked`) does not render GitHub alerts. It has to before the guide uses them.
- The work is long: about 110 pages in two languages. It is split by shelf and tracked in one issue.

## Findings from the pilot

- **Cursor never receives the fleet policy** (`workspace/agent/agent_instructions.go`: "cursor cannot be reached at
  all"). It does not know `af-aws-exec` and has to be told. The other eight kinds do know it. The AWS page says so.
- The shots stub answered `/api/ssm/profiles`, `/api/ssm/hosts` and `/api/ssm/sign-ins` with objects, but the Console
  expects arrays. Settings › AWS profiles/SSM therefore always rendered an error in the harness.

## Rejected options

- **VitePress**: its built-in search does not segment Japanese.
- **Docusaurus**: heavier, and its i18n wants a separate tree, unlike `X.md` / `X.ja.md`.
- **A hosted service (Mintlify and the like)**: the guide would live outside the repository, and pinning it to a
  release would be hard.
- **Site-only content written separately from `guide/`**: two sources drift, and the Console's guide stays unreadable.
- **Converting without restructuring**: the pilot's first round rendered fine, and the user still could not find the
  point.
