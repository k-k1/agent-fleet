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
| Changes daily — what is running right now | nowhere in the repository. It is public, that state is stale within days, and the deployment itself is the answer |
| Open until someone closes it — follow-ups, residual work, unverified acceptance, the plan ahead | GitHub issues (§10) |

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

## 3. Required front matter

Every file under `guide/` and `docs/build/` starts like this:

```markdown
---
audience: "who this is for, in one clause"
source_of_truth: "what is authoritative if this file disagrees with it"
---

# Title

English | [日本語](title.ja.md)
```

**Always double-quote the values** — a `source_of_truth` value contains colons and
commas.

There is no `updated` field: `git log` already records when a page changed, and a
hand-kept month went stale on a dozen chapters. `check_header` leaves a stray `updated:`
alone, but nothing reads it.

This is information for the writer and for CI, not something the reader came for, so it
does not belong in prose. The Console splits front matter off and renders it as a
metadata panel (`splitYamlFrontMatter`), so it is **demoted out of the body without
being lost**.

`source_of_truth` is what makes staleness survivable. For `docs/build/` it is usually
the code; for `guide/operate/` it is usually a script in `deploy/`; for `guide/member/`
it is the Console itself. State it, so a reader who finds a contradiction knows which
side to believe.

- **Only `source_of_truth` may be inherited from the shelf README.** The 16 files in
  `guide/member/` and the 6 in `guide/admin/` all carried the identical sentence — the
  same clause 22 times, carrying nothing for the reader. State it once in the shelf
  README and omit it from the chapters.
- **A shelf whose values differ cannot omit it.** All ten files in `guide/ref/` say
  something different ("this table is authoritative"; "the screen column is the Console,
  the implementation column is the code"), and that is real information.
- **Do not add a `> Audience: …` block quote underneath.** It restates `audience`. If a
  chapter has one genuinely useful sentence there, write it as the opening of the body,
  not as a quote.

## 4. Vocabulary per shelf

- **`guide/member/` and `guide/admin/` speak in screen names only.** The words the
  reader sees in the Console are the correct words. Environment variable names,
  internal kind identifiers, API paths and source file paths are not allowed in prose —
  quote them in a code block if you genuinely must show one. Checked by CI.
- **A label the reader is told to click is written bold and quoted** — `**"Stop"**` in
  English, `「**停止する**」` in Japanese — and must exist in the Console's catalogue of the
  same language (`console/src/lib/i18n/locales/`). Bold without quotes is emphasis and
  is not checked. A quoted bold that is deliberately not a label (a message to type, text
  the Control Plane prints) goes in `scripts/docs-label-allowlist.txt`. Checked by CI
  (`check_labels`).
- **A chapter longer than 600 lines draws a warning** (`CHAPTER_LINE_BUDGET`); it never
  fails the build. Split it when a natural seam exists.
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
- Quote the Console's own strings for UI labels: `console/src/lib/i18n/locales/en/`
  for English, `ja/` for Japanese (one file per domain). Inventing a translation for a button creates a
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
the deployment rows must cover the profiles `NewFactory` accepts in
`control-plane/internal/runtime/runtime.go`. CI compares; it does not generate, so you
keep control of the wording.

**The same applies to a fact no `ref/` table holds.** A fact written in two chapters
drifts into two different wrong answers, and nothing checks it. The engine gateway
token's scope was written in three chapters and was wrong in all three, differently.
So put it where its own question lives, and link from the others: a boundary in
`docs/build/07`, an outward contract in `08`, a responsibility in `03`. A chapter that
needs the fact but does not own it writes the context its own reader needs and links
that section — **it does not restate the fact**, not even word for word. A quotation is
still a second copy, it does not follow the original when that is corrected, and the two
language files cannot quote the same sentence anyway. If a value really has to appear
twice, say in both places that it does, and name the other one.

## 6a. Three ways a true sentence goes wrong

These are the mistakes this tree actually accumulated, each found more than once.

**A number carries what kind of number it is.** A decision record holds measurements,
list-price estimates and thresholds that were never met, often in one paragraph, and the
reader who quotes only the conclusion loses which is which. So say it: "measured on the
production deployment", "estimated from list price × assumed hours", "a condition the
move went ahead without meeting". A start time quoted as current fact turned out to have
been corrected twice by later measurements in the same record.

**Never generalise a contract from the kinds you happened to read.** "Every managed kind
speaks the runtime's session API" and "the conversation always lives in the CLI's native
store" were both written from claude, codex and opencode, and both break on an
in-process kind and on one whose native log is not stable — **for different reasons
each**, which is why one counter-example does not clear the claim. A sentence about
every kind is checked against every registered kind: the `Kind*` constants, each one's
`Driver` and `Capabilities`. Starting with the kind least like the others finds the
mistake soonest, but it does not finish the check.

**Do not claim a list is complete unless something checks it.** An exhaustive list is
true on the day it is written and silently wrong afterwards — and **inverting it is
still a claim about everything**: "reads are not counted" reads as a rule, and the
preview routes count a `GET`. So either say "for example", or invert it *and* keep the
hedge: what the code shows, bounded ("as a rule … for example … these are not a
complete list"). A negative claim about the absence of something ("this is not backed
up") is worth stating flatly, and then only bounded to where you looked: "the templates
declare no backup", not "there is no backup".

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

## 10. Open work is an issue, not a sentence

A decision record and a journal are frozen, so "still to do" written in one of them can
never be marked done — it stays in the prose forever and nobody can list what is still
open. **Anything that is waiting for someone to do it is a GitHub issue.** That covers a
phase that was not built, a finding set aside for another PR, an acceptance run on real
hardware that has not happened, and a residual that a review left.

- **Write issues in English.** The repository is public; the title and the first
  paragraph must make sense to someone who has never read the journal.
- **The issue is short; the evidence stays in the repository.** Link the ADR section or
  the `docs/log/` section that holds the measurements instead of copying them. (Issues
  may link into `log/`; §7 only binds the living shelves.)
- **The record points at the issue, once.** An ADR lists its follow-ups on one line —
  `Follow-ups: #123, #124` — under the Status line; a journal closes its residual list
  with the issue numbers. Adding that line is not a rewrite of a frozen file.
- **Label it when you file it** (`gh issue create --label …`). Pick one type —
  `bug`, `enhancement` or `documentation` — and one priority: `priority: high` for data
  integrity, user-facing regressions or operational risk, `priority: medium` for planned
  work after those, `priority: low` for deferred work. Add `follow-up` for something a
  merged change left behind and `needs-live-run` when it can only be closed by a run on
  real hardware or a deployment. The issue forms under `.github/ISSUE_TEMPLATE/` set the
  type for issues filed on github.com; `gh issue create` does not use them, and neither
  sets a priority.
- **Two labels say why an issue is not ready to build.** Each is removed the moment its
  condition ends, so the label always matches the state.
  - `needs-decision`: it cannot be built until the owner decides or an ADR is written.
    The first line of the body (or a pinned comment) states the decision as one question
    with its options. When it is decided, remove the label and write the decision into
    the issue.
  - `blocked`: it waits on something outside the issue — a date, an upstream release,
    another issue. The first line of the body reads `Waiting on: <what>, <when / #N>`.
    Remove the label when the wait ends.
- **Close an issue with a three-line comment**, so the reason survives the closed state:
  the evidence (PR, commit or live-check comment), why it is closed, and what would
  reopen it. Reopening is always fine. The reason is one of:
  - *Completed*: every acceptance item has been seen. A live check that could not be run
    is acceptable only when tests cover the behaviour, the comment names them and says
    "not seen live: …". Whatever can only be shown on real hardware or a deployment gets
    or keeps `needs-live-run` and stays open; an issue that carries that label is never
    closed on tests plus "not seen live", only by the run itself.
  - *Not planned* (`gh issue close N --reason "not planned"`): the premise is gone
    (feature retired, superseded by #N, the CLI or version moved on, measured as no
    longer relevant), or the issue has been idle for 4+ weeks and is not on the current
    roadmap. Name the evidence; age alone is enough only for the second kind, and never
    for an issue labelled `blocked`, `needs-decision` or `needs-live-run`, or one whose
    problem is still real (an open bug, unbuilt planned work). Those are closed only
    after the owner approves them in an inventory pass.
  - *Duplicate / superseded*: point at the issue that carries the work.
- **Umbrella issues do not linger half done.** When part of one is finished and the rest
  is concrete, file the rest as a new `follow-up` issue and close the parent with a
  pointer. An ongoing "keep lowering X" issue (a ceiling, a test time) stays open with
  its target stated in the body.
- **When a PR that only `Refs` an issue is merged, its author comments on the issue**
  with what remains (the PR's "Not verified" items), so the issue body plus its last comment always say what is left.
- **Take inventory periodically.** A pass classifies every open issue (done / verify
  here / verify on a deployment / verify unavailable / partial / ready / needs-decision /
  obsolete / waiting) and is recorded as one tracking issue with a table. The
  classifications are proposals until the owner approves the closes.
- **Code comments do not cite issues.** A comment carries the reason, not the history
  (see `AGENTS.md`).
- **An issue is published the moment it is filed**, and it bypasses the secret and
  forbidden-token scans the repository runs. Write it as carefully as a public commit:
  no hostnames of real deployments, account IDs, tokens or customer names.

## 11. Japanese notation in UI text and the guide

Terms are decided by the "Screen" column of `guide/ref/glossary.md`. This section covers
**notation**. It applies to the Japanese catalogue
(`console/src/lib/i18n/locales/ja/`) and the Japanese guide alike, and not to English.

- **Half-width space.** Put a space between Latin letters and Japanese, and between
  digits and Japanese (`Git ホスティング`, `{n} 人`, `30 日`).
- **Ellipsis and spelling variants.** "…" goes only on in-progress displays (`保存中…`) and on
  buttons that open a dialog. Everywhere else use the majority form (`送信`,
  `セッションを削除`, `API トークン`).
- **Kana.** Write すでに and ない in hiragana, not 既に / 無い.
- **Guide prose is です・ます調.** Every sentence that ends with 。 in the Japanese guide ends in the
  polite form (`読みます`, `ありません`, `必要です`), not the plain one (`読む`, `無い`, `必要`). The short
  form stays where it is not prose: table cells, badges, headings, list items that are not full
  sentences (no 。), checklist items (`- [ ]`), the description after `：` in a link list, a bold
  lead-in label (`**秘密は表示しない。** …`), and quoted text (「…」, code).
  `docs-check.py` (polite) enforces part of this rule: it refuses a sentence elsewhere that ends in a
  plain-form predicate (a verb including its past and negative forms, an adjective, or だ), such as
  `読む。` or `終わった。`. It does not detect a noun-ended sentence such as `これは必要。`, so review
  still has to catch those. A sentence that must stay plain goes into its `POLITE_EXEMPT` with a
  reason.
- **Words Japanese engineers use.** Write the term an engineer would say, usually the katakana loanword: ツール, not
  道具; インスタンス, not 箱; S3 Bucket, not バケツ. A plain native word that only translates the English reads as
  odd. `docs-check.py` (vocab) refuses the ones met so far; add a word to its `SLANG_BANNED` when a review finds
  another.
- **ログイン and サインイン are verbs.** Write 「ログインしていないとき」「ほかのプロファイルでログインしていれば」, not
  「ログインが無いとき」「ログインを持つ」「ログインを使い回す」. A noun use that reads naturally stays
  (「ログインが切れる」「ログインを承認する」), and so do UI labels such as 「専用のサインイン」.
- **Button form.** Buttons use the noun form only (`削除`, `停止`, `再開`). The する form
  (`削除する`) is allowed only on a confirmation dialog's execute button.
- **Boilerplate becomes sentences.** Rewrite noun-ended fixed phrases as sentences
  ("既定 OFF。" → "既定ではオフです。", "1〜10・既定 3。" → "1〜10 の範囲で指定します。既定は 3 です。",
  "0 = 無制限" → "0 を指定すると無制限になります。"). Do not join sentences with "＝" or "—";
  split them or use a connective.
- **Exception: badges, table cells and input placeholders** keep the short form
  ("要ログイン", "（任意）", "商用可"). A label's "0 = …" uses a half-width `=`.
  Placeholders are unified as "空欄: 配備の既定（1h）".

Apply these rules in the PR that edits the text for another reason. A notation-only change
ships separately for labels and for sentences.

## 12. Task pages and screenshots

The guide is being rewritten into short task pages for the public docs site (ADR 0131). The page map in
`k-k1/agent-fleet-site` (`docs/research/page-map.ja.md`) says which page every section of `guide/` and of the shipped
runbooks goes to. A page written for the new structure follows the rules below. The old chapters keep their form
until they are rewritten.

### One page, one task

- **One feature per page**, even when the page is short. Do not merge small pages to save navigation.
- **The title is what the reader wants to do**, as a verb phrase: "Use AWS SSO" (「AWS SSO を使う」), "Log in to
  EC2 with SSM" (「SSM で EC2 にログインする」), not "AWS" and not a list of features. No preamble in it either (not
  「社内の別のホストに…」) and no trailing parenthesis (not 「…（SSM）」): put the tool's name in the verb phrase.
  `docs-check.py --only history` refuses both on the `TASK_PAGES`.
- **The lead** is one or two sentences on what the reader can do afterwards.
- **How it works** (「全体像」) comes next: a two-column table, *what you do* / *what the agent does*. Agents use Agent
  Fleet's tools, so the reader needs this split before anything else. In Japanese, write 「全体像」, not 「しくみ」.
  For a feature agents do not touch (an SSM terminal), the second column is *what happens*.
- Then, as far as they apply: **Set it up** (with the screen), **Use it** (including what appears on screen, such as
  a login toast), **Use it more safely**, **When it goes wrong** (by symptom), **Related**.
- **Leave out** preamble ("your company's…"), how secrets are handled (that is one cross-cutting page, "How
  credentials are handled"), spec edge cases (they go to `guide/ref/`) and design reasons (they stay in
  `docs/decisions/`).
- **No history.** No ADR numbers, links or paths into `docs/`, issue numbers, or the date and version something was
  measured with. The reader wants the rule as it is now; when a fact rests on one observation, say so in words
  ("seen in one account") without the story. `docs-check.py --only history` enforces this on the pages listed in its
  `TASK_PAGES`.
- **Exit statuses are numbers with their name**: "exits with status 4", 「終了コード 4 で終わります」, not "exits
  with 4" or 「4 で終わります」.
- **Material for contributors is not published.** A procedure that only someone changing Agent Fleet runs, such as
  verifying a template on real infrastructure, belongs in `docs/`, even when it sits in a runbook today.
- UI labels are quoted from the catalogues (§4, §5).
- **Short blocks, not long prose.** A paragraph over about four lines becomes a list, a table or numbered steps. Show
  output with an example block (a `--list` with its rows) rather than describing its columns.
- **The reader's words.** Japanese is written first and read by the user in Japanese, so it must not read as a
  translation: no 「あなたの申告にすぎません」-style phrasing, ログイン / サインイン as verbs (§11), 「ない」「すでに」 in
  hiragana. When a sentence carried over from an English page reads stiff, rewrite it, do not polish it.

### Screenshots

- Take them with `console/scripts/shots/`: the real Console bundle against fictional fixtures, so they can be retaken
  every release and never show a tenant, an address or a real account ID. Scenes for the guide are marked
  `guide: true`.
- Light theme.
- **Crop to the component and the control that opens it**: a popover with its badge, a toast, a dialog. The whole bar
  or pane around it is noise.

### How a page is produced

1. Collect the facts from the locale catalogues, the components, the existing guide and the code.
2. Take the screenshots.
3. Write the Japanese page to the rules above.
4. A different model fact-checks the page against the sources; fix what it finds.
5. Put it on a docs-site preview; the user reads it there and comments once. Agree the flow with the user first
   only for a page whose structure is in question (a new kind of page, or a split that changes other pages).
6. Write English from the English catalogues, check the two languages against each other (same headings, table rows
   and list items), and land both in one change, with the old section's pointer and redirect (below). English stays
   canonical (§5).

### Moving a page

A page that moves, or a section that moves out of a page, gets a row in `guide/_redirects.tsv` in the same change:
`old<TAB>new`, both English and relative to `guide/`. The Japanese page follows its row. Then run
`python3 scripts/docs-check.py --only redirects`. It lists every link, policy note, topic note, Go string, Console
string and runbook that still uses the old path, with the new path to put there. Fix them in the same change. Frozen
records (`docs/decisions/`, `docs/log/`, published release notes) keep the old path.
