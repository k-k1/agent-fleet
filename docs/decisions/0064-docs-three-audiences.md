# 0064. Split the documentation by three readers, and stop cutting distribution by role

English | [日本語](0064-docs-three-audiences.ja.md)

- Status: **accepted** (2026-09-01).
- Related: [0055-idle-stop-and-carried-interactions.md](0055-idle-stop-and-carried-interactions.md) (also a case
  where behaviour is decided by what is visible from inside a container)

## Context

Users reported two things: **too many broken links** and **too much preamble**. Both
came from the same cause — **developer documentation and user documentation shared a
shelf and linked to each other**.

`scripts/docs-check.py --strict` reported 0 errors. Links broke anyway because
`check_links` only ever looked at **what exists in the repository**. What a reader
actually opens is the tree staged into their container at
`/usr/local/share/agent-fleet/docs`, and `build/`, `decisions/` and `CONVENTIONS` are
not in it. Measured on 2026-08-31 against develop `5e54f6a3`:

- **154 links** left the shipped tree for a developer shelf or the conventions
  (`use/` 48, `ref/` 68, `operate/` 28, `admin/` 10). Most were an end-of-chapter
  boilerplate line and "see dev/NN §X".
- Whole sections were addressed to writers: "What belongs here / What does not / Update
  trigger" in `use/README`; "Why a shared shelf / How these tables stay true" (with
  source paths) in `ref/README`; "Words to avoid on the reader-facing shelves / Writing
  conventions" in `ref/glossary`. The last one meant **we were shipping a style guide
  into every user's container**.
- The preamble of the 32 files in `use/` came to **429 lines** (13 on average). The
  `Audience:` / `Source of truth:` / `Updated:` lines exist for CI, and the `> Who this
  is for: …` block duplicated them in 26 of the 32 files.
- **Seven chapters had an H1 number that disagreed with the filename**, and
  `09-collaboration` and `11-troubleshooting` **both called themselves 09**. Around 60
  cross-reference labels still carried the old numbers.

## Decision

### 1. Three readers, three homes

| Reader | Where | Shipped |
|---|---|---|
| Someone deciding whether to try it | root `README.md` / `README.ja.md` | GitHub only |
| Someone using it — member, tenant admin, deployment admin | `guide/` | **into every container** |
| Someone changing the code | `docs/` | **to nobody** |

`docs/{use,admin,operate,ref,assets}` moved to
`guide/{member,admin,operate,ref,assets}`, so **the directory boundary is the
distribution boundary**.

### 2. Links are one-way

**Never link from `guide/` into `docs/`. The other direction is free.**

`docs/` does not exist in the reader's container, so a link into it **resolves in the
repository and breaks in the reader's hands**. Saying "the design is covered in the
developer documentation" in prose is correct; making it clickable is not.
`check_closure` enforces it.

`guide/` may still be read on GitHub — what we are separating is developer material
from user material, not public from private.

### 3. Distribution is no longer cut by role

`docsRolePrefixes` (member got `use/` and `ref/`; tenant_admin added `admin/`;
super_admin added `operate/` and `build/`) is gone. **Everyone receives all of
`guide/`.**

Why:

- **It was never a permission boundary.** The design comment in `workspace_docs.go`
  says so itself: "**not a leak (the repository is public)** but it is noise". Yet
  `docs_bridge.go` described it as access control — "a member cannot ask for a shelf
  above its role". **Two comments in one subsystem disagreed about what the mechanism
  was for**, and the contract was built on top of that. The content is the same
  Apache-2.0 source everyone can already read, so withholding `admin/` from a member
  protects nothing.
- **The noise was only ever `build/`.** The 33k lines of frozen journals are excluded
  from everyone by the distribution allowlist. Role scoping additionally withheld
  admin 1,388 + operate 3,262 + **build 5,651** lines, and only the last is noise. This
  split moves `build/` into `docs/`, where it ships to nobody — **which removes the
  reason to cut by role at all**. A deployment admin's container actually shrinks, from
  ~18,000 lines to ~12,000.
- **Role scoping was the cause of the broken-links complaint.** A link inside the
  shipped tree resolved for some readers and not others, which is why
  `use/12-settings → admin/README` could not be judged and needed a caveat. With one
  uniform bundle a link either reaches everybody or nobody.
- **A whole rule existed only to support it.** `check_notes` required "may be absent"
  in the same paragraph as any reference to a shelf that is not guaranteed. That
  requirement is gone.
- **It did not track the role anyway.** Docs are staged when the workspace starts, so a
  member promoted to tenant administrator **does not see `admin/` until the next
  start**.

**Remaining cost**: the tree an agent greps now includes `operate/` (host-level, root-
assuming procedures), so there is more room to answer a member's "how do I…" with a
deployment procedure. Each shelf names its reader at the top, and `workspace-notes.md`
points at `guide/member` first.

What does *not* change: the request still cannot select scope, and `decisions/` and
`log/` still go to nobody. Only "who receives what" changed, not "what is never sent".

### 4. Heading ids are checked against the Console's rule

`slug()` in `console/src/lib/filemeta.ts` collapses **a run of whitespace into one**
hyphen; github-slugger emits one hyphen per whitespace character. The two disagree on
any heading containing **punctuation surrounded by spaces**, such as `—` or `/` (`a — b`
is `a-b` in the Console and `a--b` on GitHub). Full-width parentheses go the other way:
the Console drops them, GitHub keeps them.

Measured across the repository, **52 links resolve only under the Console's rule and 10
only under GitHub's**. Readers of `guide/` open it in the Console, so anchors are
checked with **the Console's rule when the target is in `guide/`, and GitHub's when it
is in `docs/` or at the repository root**. Applying one rule everywhere reported
correct anchors such as `CONTRIBUTING.md#commits--prs` as broken — which it did.

## Rejected

- **Close the loop inside `docs/` (just make `use/` self-contained).** `ref/` would
  remain a shelf shared by four readers with "how these tables stay true" (and source
  paths) still inside it. That is not a separation.
- **Make `docs/` the shipped tree and move developer material to `dev/`.** The
  container path would match the repository path, but it is the largest move by far
  (126 decisions + 90 log + 34 build files) and buys only a name.
- **Delete the `Source of truth:` line everywhere.** In `guide/ref/` **all ten files
  have a different value** ("this table is authoritative"; "the screen column is the
  Console, the implementation column is the code"), which is real information for a
  reader who hits a contradiction. It was demoted into front matter instead. The 16
  files in `guide/member/` and the 6 in `guide/admin/` all carry the identical value,
  so those state it once in the shelf README.
- **Make `slug()` github-slugger-compatible.** One line would erase all 14
  disagreements — and **break the 52 links that only resolve under the Console's rule**.
  It fixes the minority by breaking the majority.

## Consequences

No link leaves `guide/`. Three checks were added (`anchors`, `closure`, `chapters`).
What CI verifies is now "is the shipped tree self-contained", not "does the path exist
in the repository".
