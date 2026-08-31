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
| Changes whenever the code changes — specs, procedures, capability tables | the shelves under `guide/`, and `docs/build/` |
| Never changes again — a decision, a measurement, an incident, a retired option | `docs/decisions/`, or the frozen `docs/log/` |
| Changes daily — what is running right now | `docs/HANDOFF.md` |

A file that mixes them cannot be reviewed for staleness, because there is no way to
tell which sentences are supposed to still be true. If you catch yourself appending a
"round 7" section to a living document, you are writing a journal: put the durable
conclusion in the living document and the journal in a decision record.

## 2. Shelves are cut by reader, not by genre

A document's home is decided by *who reads it*, never by what kind of document it is.
A genre-cut shelf cannot be shipped to anybody. There are three readers (ADR 0064):

| Reader | Where | Answers | Shipped |
|---|---|---|---|
| Someone deciding whether to try it | root `README.md` / `README.ja.md` | What is this, and do I want it? | GitHub only |
| Someone using it — member, tenant admin, deployment admin | `guide/` | How do I do this? | **into every container** |
| Someone changing the code | `docs/` | How does it work, and why is it like this? | **to nobody** |

**The directory boundary is the distribution boundary.** `guide/` is the only tree that
goes into a container, and it is not cut by role — everyone receives the same thing.

So the rule is one-way:

> **Never link from `guide/` into `docs/`. The other direction is free.**

`docs/` does not exist in the reader's container, so a link into it **resolves in the
repository and breaks in the reader's hands** — the worst kind of breakage, because it
passes every check that only looks at the repository. Saying "the design is covered in
the developer documentation" in prose is correct; making it clickable is not.
`check_closure` enforces this.

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

- **`guide/member/` and `guide/admin/` speak in screen names only.** The words the
  reader sees in the Console are the correct words. Environment variable names,
  internal kind identifiers, API paths and source file paths are not allowed in prose —
  quote them in a code block if you genuinely must show one. Checked by CI.
- **`guide/operate/` may use commands, paths and variables** — its reader is at a shell.
- **`guide/ref/` is the screen-term-to-implementation-term table itself**, so both
  vocabularies appear there.
- **`docs/build/` writes in wire contracts and responsibilities**: which process owns
  what, and what the externally visible contract is. **Never cite line numbers**; they
  rot within a week. Point at a grep-able anchor instead — an endpoint path, an env
  name, an error code string — or at the code map.

### Words to avoid on the reader-facing shelves

`driver`, `runtime`, `TUI`, `PTY`, `tmux`, `pane` as an implementation term, `kind` —
these describe how it is built, not what the reader sees. Use them in
`docs/build/`, and in a user-facing document only when the reader has
explicitly asked how the machinery works.

The same rule applies to agents talking to people: say "execution method" and
"Managed", not "driver"; say "session", not "tmux session".

### Writing conventions

- On-screen names are written **Repositories**, **Files**, **Sessions**.
- Session kinds are written `claude`, `codex`, `opencode`, `shell`, `ssm`.
- The product or the speaker may be written Claude, Codex.
- A button is written "Clone"; a command is written `git clone`.
- An **icon** is picture-only, a **status display** is a coloured state, and a **badge**
  is a small count or pane number.

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

## 6. Say it once, in `guide/ref/`

Capability facts — which agent supports what, which provider supports what, which
deployment target supports what, which role may do what — live in `guide/ref/` and
nowhere else. Other shelves link to the table. Two copies of a matrix means one of them
is already wrong.

It is also that the same question arrives from readers with different stakes. "Does
cursor support plan mode?" comes from the member choosing an agent, from the
administrator deciding what to offer, and from the developer adding an eighth kind. Keep
a copy of the answer on each shelf and **they will drift, and the reader cannot tell
which one is stale.**

`guide/ref/` tables whose axes exist in the code are checked against it: the agent columns
must cover the `Kind*` constants in `workspace/agent/internal/session/session.go`, and
the deployment rows must cover the profiles `newRuntimeFactory` accepts in
`control-plane/runtime.go`. CI compares; it does not generate, so you keep control of
the wording.

## 7. Never link into `docs/log/`

`docs/log/` is a frozen archive. A living document that links there is saying "the real
answer is in the journal", which is how the previous layout ended up with the spec
buried in a 3,400-line work log. Copy the fact you need into the living document and
cite the measurement in place. CI rejects such links.

## 8. Definition of done for a feature

A change that users can see is not finished until:

1. `guide/ref/features.md` has its row.
2. The shelf for the affected reader has its section.
3. If the change settled a question that could plausibly be reopened,
   `docs/decisions/` has the record — including what was rejected and why.

## 9. What each shelf is responsible for

Do not write "what belongs on this shelf" *into a document the reader sees* — that is an
instruction to the writer, not guidance for the reader. **It goes here.**

### `guide/member/` — someone running agents from the Console

- **Write**: procedures, in the order the reader will actually do them; what a screen,
  badge or menu means; what to try when something looks wrong, and how to tell "still
  working" from "stuck".
- **Do not write**: capability facts (only `guide/ref/` has them); anything the reader
  cannot see on screen (env names, internal identifiers, API paths, source paths);
  administration (`guide/admin/`); installing and keeping a deployment alive
  (`guide/operate/`).
- **Update trigger**: a change to a screen, a flow or a default that the reader would
  notice. **If a feature ships and this shelf says nothing about it, the feature is not
  done** (§8).

### `guide/admin/` — a tenant administrator

- **Write**: how to run members, limits, the audit log and team-wide integrations.
- **Do not write**: settings that span tenants — those belong to the deployment admin.
- **Update trigger**: changes to the tenant settings screens and to permissions.

### `guide/operate/` — whoever installs and keeps a deployment alive

- **Write**: what each step decides, and why it is decided that way.
- **Do not write**: the commands themselves — the runbooks under `deploy/` are
  authoritative and this shelf does not copy them.
- **Update trigger**: a change under `deploy/` that changes what a step means.

### `guide/ref/` — the capability tables everyone consults

- **Write**: the tables for features, agents, repository kinds, deployment targets,
  roles, settings and limits.
- **Do not write**: how to do something (that is the link target's job); an explanation
  of how the tables stay true (§6).
- **Update trigger**: a capability appearing or disappearing. Tables whose axes exist in
  the code are cross-checked by CI.

### `docs/build/` — someone changing the code

- **Write**: wire contracts, responsibilities, data flow, the shape of an extension.
- **Do not write**: user-facing procedures (`guide/`); dated work records (`docs/log/`).
- **Update trigger**: a contract changing.
