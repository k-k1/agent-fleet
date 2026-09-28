# 0103. Branch names come from one resolver over four layers: repository, user, tenant, built-in

English | [日本語](0103-branch-naming-rules.ja.md)

- Status: **proposed** (2026-09-28). The survey, the git-flow and Bitbucket probes and the field
  study of a git-flow repository are in [docs/log/123](../log/123-branch-naming-rules.md).
  Three decisions were made in the issue before this ADR (#1120 "Decisions already made"):
  the built-in shape (decision 6), the repository outranking the user and the tenant (decision 2),
  and rules being advisory only (decision 8). The user also chose `{ref}` for the Jira default and
  switching users with an empty template to the new default without a compatibility shim.
- Follow-ups: #1124, #1125, #1126 (P0) / #1127 (P1) / #1128, #1129 (P2)
- Related: [0061](0061-work-item-inbox.md) decision 12 (the work-item default `feature/{key}`, replaced
  here) / [0031](0031-mcp-registry.md) (the tenant distribution this ADR's tenant layer copies)

## Context

Three places name a branch, each in its own style, and only one of them can be configured:

- **Work-item launch.** The Console renders the user's template (`workItemBranchTemplate`, default
  `feature/{key}`) with `{key}` and an ASCII-only `{slug}`.
- **Every other launch.** The launch modal, handoff, the image studio and `create_session` from agents
  get `temp/<random>` from the Agent when `new_branch` is empty.
- **Rename.** The chips offer `feat/ fix/ refactor/ chore/ docs/`, and the AI suggestion is told to use
  no prefix at all.

The base is the parent clone's current HEAD or a typed value. Nothing reads the repository's default
branch or its git-flow roles, and there is no tenant or repository layer. The af MCP tools expose no
rule, so agents and skills name branches freely.

Teams need their own conventions to coexist. The motivating case is a company repository on Bitbucket
Cloud that runs git-flow. It names branches `feature/<Jira key>` off `develop` and merges them into
`release/x.y.z`, while this repository's GitHub projects want `<type>/<number>-<slug>`. The field
study (docs/log/123 §4) found three things:

- **No machine-readable declaration exists in a fresh clone.** The team runs `git flow init` by hand
  after cloning. git-flow keeps its keys in `.git/config`, which a clone does not copy, and nobody runs
  `git flow init` in the clone Agent Fleet made.
- **Bitbucket's branching model returns its unconfigured defaults.** It said `development: main` and the
  four stock prefixes, which contradicts the team's practice of branching off `develop`. Telling a
  configured value from a default needs `admin:repository`, which our connection does not have.
- **`origin/HEAD` points at `main`.** "The default branch" would be the wrong base there.

## Decision

### Decision 1: a rule is `{match, name, base, types}`

The fields, shown as a JSON object. The repository file writes the same fields in git-config syntax
(decision 3).

```json
{
  "match": "bitbucket.org/acme/*",
  "name":  "{prefix}{ref}-{slug}",
  "base":  "head",
  "types": {
    "bugfix": { "prefix": "fix/", "base": "main", "from": ["Bug", "Defect", "bug"] }
  }
}
```

- `match`: a `host/owner/repo` pattern (below). A repository declaration has none.
- `name`: the template (decision 4).
- `base`: `head`, `default` or a branch name (decision 5).
- `types.<kind>`: per-kind overrides. `from` lists the issue types and labels that map to the kind.
- **Kinds are a closed vocabulary**: `feature`, `bugfix`, `hotfix`, `release`, `support`, `docs`,
  `chore`, `refactor`. The first five are git-flow's and Bitbucket's own kinds, so their declarations
  map one to one. The last three are the conventional-commit prefixes the rename chips offered.
- **Every field is optional**, including each field of a kind. A rule that sets only `base` is valid.
- **The repository file and the tenant/user stores share the fields.** The file uses git-config
  syntax (decision 3); the tenant and user stores hold the JSON above.

### Decision 2: four layers, merged field by field

Strongest first:

1. **Repository declaration** (decision 3)
2. **User** (below)
3. **Tenant default** (decision 10)
4. **Built-in default** (decision 6)

Layers merge **per field**, not per rule. A git-flow declaration only knows prefixes and the branch to
start from; it says nothing about what follows the prefix (docs/log/123 §5.1). A whole-rule override
would throw the rest of the rule away.

**How one field is resolved.** The fields are `name`, `base`, and each kind's `prefix`, `base` and
`from`. For each one:

1. Try the layers strongest first.
2. Inside a layer, try the matching rules most specific first.
3. The first rule that sets the field supplies it.

For example, an exact-repository rule that sets only `base` and a `*` rule that sets only `name` both
apply. Two exceptions to "each field on its own":

- The kind set (decision 3).
- `base`, whose kind and rule values are ordered together (decision 5).

**`match`.** It is compared with `host/owner/repo` taken from `origin`'s URL, lower-cased. Each `*`
matches exactly one segment, and a bare `*` matches every repository. Specificity is the number of
literal segments, so `bitbucket.org/acme/web` beats `bitbucket.org/acme/*`, which beats `*`. On a tie,
the rule listed first wins. The repository layer has no `match`: it is the repository.

**The user layer is its own store, not ui-prefs.**
- The Agent keeps the user's rules in a separate file behind `GET/PUT /branch-rules/user`.
- **Why not ui-prefs:** the Console writes ui-prefs as a whole and sends only the keys it knows, and the
  Agent replaces the file. An older Console saving any setting would silently drop a rules key it does
  not know.
- **The existing `workItemBranchTemplate` stays in ui-prefs and is not migrated.** The resolver reads a
  non-empty template as one more user rule, `{match: "*", name: <template>}`, listed after the stored
  rules. An older Console keeps editing and rendering it as today. A newer Console against an older Agent
  gets 404 and does the same (decision 7).

### Decision 3: the repository layer reads what the repository already says

The sources are merged field by field into one repository rule, strongest first:

1. **`.agent-fleet/branches`**, committed, in **git-config syntax**. A multi-valued `from` repeats the
   line:

   ```
   [naming]
   	name = {prefix}{key}
   	base = develop
   [type "hotfix"]
   	prefix = hotfix/
   	base = main
   	from = Incident
   	from = Outage
   ```

   The precedent is `.agent-fleet/launch-prompts.md`.
2. **`.gitflow`**, git-flow-next's committed shared config (git-config syntax, at the repository root).
3. **`gitflow.*` in the clone's config**, the keys gitflow-avh, nvie/gitflow, git-flow-next's avh
   compatibility and Fork write: `gitflow.branch.master`, `gitflow.branch.develop` and
   `gitflow.prefix.{feature,bugfix,release,hotfix,support}`. git-flow-next's native
   `gitflow.branch.<name>.{type,parent,prefix}` is read too. Worktrees share this config, so a key
   written in the parent clone reaches every worktree.
4. **Bitbucket Cloud's branching model** (`GET /2.0/repositories/{ws}/{repo}/branching-model`), for a
   Bitbucket remote with a connection. A field counts only when it differs from the unconfigured default:
   - `development` only when `use_mainbranch` is false;
   - `branch_types` only when some prefix is not the stock `bugfix/ feature/ hotfix/ release/`.

   The stock answer is what the field study got from a repository that runs git-flow off `develop`.

   **Waiting and staleness:**
   - With no copy for the repository yet, a resolve waits up to three seconds for the first fetch.
   - Past that, it answers without the model and says so, with `sources.bitbucket: "pending"` and a
     warning. A person then sees why the base is `head` instead of getting it silently.
   - A copy is kept for ten minutes. `GET …/branch-rule?refresh=1` fetches it again, and `sources`
     carries the time each copy was fetched.

What a git-flow source supplies:

- The prefix of each kind it lists.
- `base = <gitflow.branch.develop>` for `feature`, `bugfix`, `release` and `support`.
- `base = <gitflow.branch.master>` for `hotfix`.

The `gitflow.*` source counts only when both `gitflow.branch.master` and `gitflow.branch.develop` are
set. A half-written initialisation (decision 9) therefore reads as undeclared, not half-declared.

**The kind set.** The repository layer's kind set is the union of the kinds its sources list:
- the `[type "<kind>"]` sections of `.agent-fleet/branches`;
- the git-flow prefixes;
- Bitbucket's `branch_types`, when they count.

When the set is non-empty, **a kind outside it resolves as `feature`, whatever the weaker layers say**:
the repository decides which kinds exist (decision 2 ranks it first). Weaker layers still fill the
fields of kinds inside the set. So a `.gitflow` that lists feature, release and hotfix, with a user rule
`types.bugfix.prefix = fix/`, still names a Bug `feature/…`. nvie/gitflow and Fork have no bugfix,
so a Bug in such a repository becomes `feature/<key>`, which is what the team in the field study names it.

**Where the file is read from.** It is read from the working copy the resolver is given: the parent clone
at launch, as for `launch-prompts.md`, and the session's worktree at rename.

**A committed file (`.agent-fleet/branches` or `.gitflow`) is untrusted input.** Anyone who can push to the repository writes it, so it is read
under these limits:
- **It is read from the blob at `HEAD`** (`git cat-file blob HEAD:<path>`), not from the file system. A
  symlink is then a path string and is never followed, and uncommitted edits do not count. Only a
  regular-file entry is read, up to 16 KiB.
- **It is parsed with `git config --no-includes --file - --get-regexp …`. The `--no-includes` flag is
  required.** Measured: reading from stdin, git follows `include.path` by default. An include of
  `/etc/hostname` came back as a key, while with a path argument includes are off (docs/log/123 §6).
- **Only the known keys are read.**
  - From `.agent-fleet/branches`: `naming.name`, `naming.base` and `type.<kind>.{prefix,base,from}`,
    with `<kind>` from the vocabulary.
  - From `.gitflow`: the `gitflow.*` keys of item 3.

  Anything else is ignored with a warning.
- **Every prefix and base must pass `git check-ref-format --branch`.** A value that fails is dropped with a
  warning. A base reaches git only after `--end-of-options`.

User and tenant rules and the `gitflow.*` keys go through the same key and ref-name checks.

### Decision 4: placeholders

| Placeholder | GitHub `acme/web#1120` | Jira `PROJ-123` |
|---|---|---|
| `{ref}` | `1120` | `PROJ-123` |
| `{num}` | `1120` | `123` |
| `{key}` (as today) | `issue-1120` | `PROJ-123` |
| `{project}` | (empty) | `PROJ` |
| `{type}` | the kind, e.g. `bugfix` | same |
| `{prefix}` | the kind's prefix, e.g. `fix/` | same |
| `{slug}` | the title's ASCII slug | same (empty for a non-ASCII title until P2) |

- **`{key}` keeps its meaning** so that existing templates render as before. A Jira key keeps the case
  it was written in, as it does today.
- **Kind from a work item.** An explicit `kind` in the request wins. Otherwise the resolver tries these
  values in order, and the first one that maps wins:
  1. The tracker's own type: GitHub's issue `type`, Jira's `issuetype` (the Jira list starts fetching
     it).
  2. The labels, in the tracker's order.

  Each value is compared case-insensitively. It is looked up in `from` in decision 2's field order
  (layer, then specificity), and the built-in map comes last:
  - `bug`, `defect` → `bugfix`
  - `hotfix` → `hotfix`
  - `documentation`, `docs` → `docs`

  No match means `feature`. Decision 3's kind set is applied last.
- **Rendering keeps today's sanitising** (`sanitizeBranch`): only `[A-Za-z0-9._/-]` survives, empty
  segments collapse, and a separator left by an empty placeholder is dropped.
- **When the rendered name is only its prefix.** This is checked after rendering and sanitising: for
  example, `{prefix}{key}` with no work item renders `feature/`. Such a name gets the slug appended
  (`feature/<slug>`). If the slug is empty too:
  - at launch, it falls back to `temp/<random>`;
  - at rename, the resolver returns `name_empty` and the Console leaves the field for the person to type.
- **English slug (P2).** A non-ASCII title may get an English slug through the AI-assist one-shot. The
  resolver never waits for it: it answers with the deterministic slug and marks the name `provisional`,
  and the Console may ask again once. Without an AI assist the deterministic slug is final.

### Decision 5: base

The base is picked in this order:

1. **A base the person typed.** It is used as given. If it does not exist, the launch fails as it does
   today: a person's explicit choice is never replaced.
2. **The layers, strongest first.** Inside a layer, the kind's `base` comes before the rule's `base`, and
   the first one set wins. The repository layer's sources are merged first (decision 3), so there a
   git-flow `hotfix` base from the clone config beats a general `naming.base` from the file.
3. **The built-in `head`.**

Example: a repository `naming.base = develop` and a user `types.bugfix.base = main` give a Bug
`develop`, because the repository layer is tried first.

The values mean:

- `head`: the parent clone's current branch, which is today's behaviour.
- `default`: `refs/remotes/origin/HEAD`.
- A branch name: that branch, e.g. `develop`.

The built-in stays `head`, not `default`: the field study's repository has `origin/HEAD → main` and
branches off `develop`. A resolved base (not a typed one) that exists neither locally nor on `origin`
produces a warning, and the launch uses `head`.

### Decision 6: the built-in default

- `name = "{prefix}{ref}-{slug}"`
- `base = "head"`
- Prefixes: `feature/`, `fix/` (bugfix), `hotfix/`, `release/`, `support/`, `docs/`, `chore/`,
  `refactor/`.

For GitHub this gives the names the issue asked for (`feature/1113-work-item-pr-status`,
`fix/1120-…`). The issue wrote the shape as `{type}/{num}-{slug}`, but the bugfix kind's prefix is
`fix/`, so the template uses `{prefix}`. For Jira it keeps the project (`feature/PROJ-123-…`, or
`feature/PROJ-123` for a Japanese title).

ADR 0061 decision 12's `feature/{key}` is replaced. A user with an empty template gets the new default
with no compatibility shim. A non-empty template keeps working as a user rule (decision 2).

### Decision 7: one resolver, in the Agent

The Agent resolves: it holds the user rules and prefs, the working copies and the cached tenant layer.

- `GET /repos/{name}/branch-rule` returns the effective rule:
  - `name`, `base` and `kinds[{kind, prefix, base}]`;
  - `sources`, which says where each field came from (e.g. `base: gitflow.branch.develop`);
  - `gitflow`: `declared`, `absent` or `suggest` (decision 9).
- `POST /repos/{name}/branch-name` takes `{item?, session?, kind?, slug?}` and returns
  `{name, base, kind, provisional, warnings[], sources}`. The inputs:
  - `item` is `{provider, key, title, type, labels}`.
  - `session` names a session whose meta recorded an item (decision 8). The resolver uses that item when
    `item` is absent.
  - With no item, `{ref}`, `{num}`, `{key}` and `{project}` render empty.
  - `kind` falls back to the item's kind (decision 4), then to `feature`.
  - `slug` falls back to the item's title slug, then to empty.
  - If the result is empty, the call returns `name_empty` (decision 4).
- `POST /repos/{name}/branch-name/check` takes `{name}` and returns `{warnings[]}`.
- `GET/PUT /branch-rules/user` holds the user layer (decision 2).
- The af MCP tool `branch_name` (P2) takes the same input as `POST …/branch-name` and names the working
  copy. Agents and skills (issue-to-pr) call it instead of inventing names.

`{name}` is the working copy under `~/repos`, as in the other `/repos/{name}` routes. When an older Agent
returns 404, the Console keeps using its own `branchForItem` with the user's template.

### Decision 8: the three styles become one; rules warn and never refuse

- **Work-item launch** asks the resolver for the name and the base.
- **Other launches keep `temp/<random>`**: deferred naming does not change.
- **Rename:**
  - The chips are the resolved kinds' prefixes (`GET …/branch-rule`), not a hard-coded list. As today,
    pressing one swaps only the prefix of what is in the field and calls nothing.
  - The AI suggestion returns a kind from the resolved set plus an English slug. The Console passes
    both, with `session`, to `POST …/branch-name`, which composes the name.
  - A work-item launch records the item (`provider`, `key`, `title`, `type`, `labels`) in the session
    meta, so a rename keeps `{ref}` and the kind. A `temp/…` session has no item, so its rename renders
    `{prefix}<slug>` (decision 4).
- **Warning.** A name whose first segment is not a resolved prefix gets a warning in the launch and
  rename modals and from `check`. `temp/` is exempt. The name is never refused.

### Decision 9: "Initialize Git Flow" in the Console

A declaration is usually absent (decision 3, docs/log/123 §4), so reading one is not enough. A
repository (parent clone) gets **Initialize Git Flow**.

- **The fields** are Fork's six: production branch, development branch, feature / release / hotfix
  prefix and version tag prefix. An optional bugfix prefix is added. `support` has no field, as in
  Fork. It is written as `support/` only when it is absent, which is what `git flow init -d` does.
- **Prefilled** from:
  - which of `develop`, `main` and `master` exist on `origin`;
  - the Bitbucket model's prefixes;
  - any keys already present.
- **Saving writes git-flow's own keys** into the parent clone's config with `git config`:
  - `gitflow.branch.master` and `gitflow.branch.develop`;
  - `gitflow.prefix.{feature,bugfix (when given),release,hotfix,support,versiontag}`.

  The `git flow` CLI, Fork and git-flow-next opened on the same clone then see the same settings.
  `gitflow.path.hooks` is left to git-flow's default.
- **The write is shared, so it is guarded.** Every worktree of the repository reads this config at once,
  and the modal says so.
  - Writes are serialised per parent clone by an Agent lock.
  - The request carries the values the modal was opened with. If the current keys differ, the answer is
    409 and the modal reloads.
  - The prefixes are written first and the two branch keys last. The resolver ignores the source until
    both branch keys exist (decision 3), so during a first initialisation a concurrent resolve sees
    either nothing or the whole new state.
  - A re-initialisation over existing keys can be read half-way for the moment of the write. This is
    accepted: names are advisory, and the next resolve settles.
  - A failure reports which keys were written. Pressing again rewrites them all.
  - Every value passes decision 3's ref-name check before anything is written.
- **It writes on a person's press only, and never creates a branch.** If the development branch
  exists neither locally nor on `origin`, it refuses. `git flow init` would create it; this does not.
- **The suggestion.** The work-item launch offers it (`gitflow: suggest`) when `origin` has `develop`
  and no repository source declares anything. It only offers; it never initialises by itself.

### Decision 10: the tenant layer (P1)

- The tenant admin keeps a list of rules with `match` in the CP.
- The Agent polls `GET /internal/branch-rules` every five minutes and keeps the last copy when the CP is
  unreachable, the same fail-open cache as tenant MCP servers (`mcp-tenant.json`).
- There is no "enforce" flag (decision 8).

### Out of scope

- **Where a branch merges to.** That includes the field study's `release/x.y.z`, a PR base and tag
  prefixes. Naming decides where a branch starts, not where it lands.
- **Refusing a name.**

## Rejected

- **A tenant "enforce" layer.** Decided in the issue: a rule warns. A refusal would block the rename a
  person meant.
- **Resolving in the Console only.** Agents and skills could not learn the rule. The Console could not
  read the working copy's git-flow keys or the committed file either.
- **TOML for `.agent-fleet/branches`**, as the issue sketched. The Agent has no TOML parser, and
  git-config syntax is what `.gitflow` and the git-flow keys already use: `git config -f` reads it, and
  so does every git user.
- **Bitbucket's `development` as the base whenever present.** The field study's repository got
  `main` back while branching off `develop`, and the settings endpoint that would tell the two apart
  needs admin scope.
- **`default` (`origin/HEAD`) as the built-in base.** It is `main` in that same repository.
- **Running `git flow init`.** git-flow is not installed in a Workspace, the variants disagree on
  prompts, and it creates branches. Writing the keys does what it would write.
- **Keeping the user's rules in ui-prefs.** An older Console writes ui-prefs whole with only the keys
  it knows, and the Agent replaces the file, so the rules would vanish the first time that Console saved
  any setting.
- **Resolving before the Bitbucket model arrives, silently.** A repository whose only declaration is
  Bitbucket's `development` would branch off `head` after every Agent restart, with nothing saying why.
- **Writing a `.gitflow` or `.agent-fleet/branches` file on the user's behalf.** That is a commit in the
  user's repository.
- **Whole-rule precedence.** A git-flow declaration would erase the user's `name`.

## Consequences

- Users with an empty template get `{prefix}{ref}-{slug}` instead of `feature/{key}`.
- The rename chip `feat/` becomes `feature/`, the built-in prefix for kind `feature`.
- A git-flow repository opened in Agent Fleet branches off `develop` once someone presses Initialize
  Git Flow, or commits `.agent-fleet/branches`. Until then it keeps today's parent-HEAD behaviour.
- A team whose names carry no slug (`feature/<key>`) needs a user, tenant or repository `name`. The
  built-in adds the slug.
- The Jira list fetches one more field (`issuetype`).
