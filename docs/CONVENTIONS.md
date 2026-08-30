# Writing conventions

English | [日本語](CONVENTIONS.ja.md)

These rules apply to every file on every shelf. `scripts/docs-check.py` enforces the
mechanical parts; run it before you push.

```
python3 scripts/docs-check.py           # what CI runs
python3 scripts/docs-check.py --strict  # warnings become errors
```

## 1. One lifetime per file

This is the rule the previous layout lost, and the reason it collapsed. Three kinds of
text age at different speeds, and **they must never share a file**:

| Lifetime | Where it goes |
|---|---|
| Changes whenever the code changes — specs, procedures, capability tables | `use/` `admin/` `operate/` `build/` `ref/` |
| Never changes again — a decision, a measurement, an incident, a retired option | `decisions/`, or the frozen `log/` |
| Changes daily — what is running right now | `HANDOFF.md` |

A file that mixes them cannot be reviewed for staleness, because there is no way to
tell which sentences are supposed to still be true. If you catch yourself appending a
"round 7" section to a living document, you are writing a journal: put the durable
conclusion in the living document and the journal in a decision record.

## 2. Shelves are cut by reader, not by genre

The directory layout is also the distribution boundary — `docsRolePrefixes` in
`control-plane/workspace_docs.go` decides which shelves a container gets from the
reader's role. So a document's home is decided by *who reads it*, never by what kind
of document it is. A genre-cut shelf cannot be shipped to anybody.

## 3. Required header

Every file under `use/ admin/ operate/ build/ ref/` starts like this, within the first
12 lines:

```markdown
# Title

English | [日本語](title.ja.md)

Audience: who this is for, in one clause
Source of truth: what is authoritative if this file disagrees with it
Updated: 2026-08
```

`Source of truth:` is what makes staleness survivable. For `build/` it is usually the
code; for `operate/` it is usually a script in `deploy/`; for `use/` it is the Console
itself. State it, so a reader who finds a contradiction knows which side to believe.

## 4. Vocabulary per shelf

- **`use/` and `admin/` speak in screen names only.** The words the reader sees in the
  Console are the correct words. Environment variable names, internal kind
  identifiers, API paths and source file paths are not allowed in prose — quote them
  in a code block if you genuinely must show one. Checked by CI.
- **`operate/` may use commands, paths and variables** — its reader is at a shell.
- **`build/` writes in wire contracts and responsibilities**: which process owns what,
  and what the externally visible contract is. **Never cite line numbers**; they rot
  within a week. Point at a grep-able anchor instead — an endpoint path, an env name,
  an error code string — or at the code map.

## 5. Bilingual

English is canonical (`X.md`); Japanese lives beside it (`X.ja.md`). Both are updated
in the same change — a shelf with a stale translation is worse than one with none.

- The line right after the H1 is the language switcher, and it is the **only** link
  allowed to cross languages.
- Every other internal link stays inside its language: `.ja.md` files link to
  `.ja.md`. Links into the Japanese-only area (`log/`) point at the same target from
  both languages.
- `decisions/` is bilingual too, on the same terms. An ADR is append-only and
  immutable, so translating one is a translation and not a rewrite: never change the
  wording of a decision, a measurement or a discarded option to make it read better.
- Quote the Console's own strings for UI labels: `console/src/lib/i18n/locales/en.ts`
  for English, `ja.ts` for Japanese. Inventing a translation for a button creates a
  term the reader cannot find on screen.

## 6. Say it once, in `ref/`

Capability facts — which agent supports what, which provider supports what, which
deployment target supports what, which role may do what — live in `ref/` and nowhere
else. Other shelves link to the table. Two copies of a matrix means one of them is
already wrong.

`ref/` tables whose axes exist in the code are checked against it: the agent columns
must cover the `Kind*` constants in `workspace/agent/internal/session/session.go`, and
the deployment rows must cover the profiles `newRuntimeFactory` accepts in
`control-plane/runtime.go`. CI compares; it does not generate, so you keep control of
the wording.

## 7. Never link into `log/`

`log/` is a frozen archive. A living document that links there is saying "the real
answer is in the journal", which is how the previous layout ended up with the spec
buried in a 3,400-line work log. Copy the fact you need into the living document and
cite the measurement in place. CI rejects such links.

## 8. Definition of done for a feature

A change that users can see is not finished until:

1. `ref/features.md` has its row.
2. The shelf for the affected reader has its section.
3. If the change settled a question that could plausibly be reopened, `decisions/` has
   the record — including what was rejected and why.
