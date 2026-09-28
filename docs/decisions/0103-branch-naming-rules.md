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

```
match = "bitbucket.org/acme/*"      # host/owner/repo glob; absent in a repository declaration
name  = "{prefix}{ref}-{slug}"      # template (decision 4)
base  = "head"                      # "head" | "default" | a branch name (decision 5)

[types.bugfix]                      # per-kind overrides
prefix = "fix/"
base   = "main"
from   = ["Bug", "Defect", "bug"]   # issue types / labels that map to this kind
```

- **Kinds are a closed vocabulary**: `feature`, `bugfix`, `hotfix`, `release`, `support`, `docs`,
  `chore`, `refactor`. The first five are git-flow's and Bitbucket's own kinds, so their declarations
  map one to one. The last three are the conventional-commit prefixes the rename chips offered.
- **Every field is optional**, including each field of a kind. A rule that sets only `base` is valid.
- **The repository file and the tenant/user stores share the format.** The syntax is decision 3's;
  the tenant and user stores hold the same fields as JSON.

### Decision 2: four layers, merged field by field

Strongest first:

1. **Repository declaration** (decision 3)
2. **User** (Settings; the existing `workItemBranchTemplate` becomes a user rule with `match = "*"`)
3. **Tenant default** (decision 10)
4. **Built-in default** (decision 6)

Layers merge **per field**, not per rule. `name`, `base` and each kind's `prefix`, `base` and `from` are
taken from the strongest layer that sets them. A git-flow declaration only knows prefixes and the
branch to start from; it says nothing about what follows the prefix (docs/log/123 §5.1), so a
whole-rule override would throw the rest of the rule away.

Within a layer, the **most specific `match` wins**: an exact repository, then `owner/*`, then `host/*`,
then `*`. On a tie, the first listed wins. The repository layer has no `match`: it is the repository.

### Decision 3: the repository layer reads what the repository already says

The sources, strongest first, again merged field by field:

1. **`.agent-fleet/branches`**, committed. It uses **git-config syntax** and is read with
   `git config -f`:

   ```
   [naming]
   	name = {prefix}{key}
   	base = develop
   [type "hotfix"]
   	prefix = hotfix/
   	base = main
   	from = Incident
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

   The stock answer is what the field study got from a repository that runs git-flow off `develop`. The
   model is cached per repository for an hour, and a resolve never waits for it: the first resolve after
   a cold start runs without it.

What a git-flow source supplies:

- The prefix of each kind it lists.
- `base = <gitflow.branch.develop>` for `feature`, `bugfix`, `release` and `support`.
- `base = <gitflow.branch.master>` for `hotfix`.

A repository source that lists prefixes lists **every kind the team uses**. A kind it omits is resolved
as `feature`. nvie/gitflow and Fork have no bugfix, so a Bug in such a repository becomes
`feature/<key>`, which is what the team in the field study names it.

The file is read from the working copy the resolver is given. At launch that is the parent clone, as
for `launch-prompts.md`; at rename it is the session's worktree.

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
- **Kind from a work item.** Take the tracker's own type: GitHub's issue `type`, Jira's `issuetype`
  (the Jira list starts fetching it). Otherwise take the labels. Match them case-insensitively against
  every layer's `from`, then against the built-in map:
  - `bug`, `defect` → `bugfix`
  - `hotfix` → `hotfix`
  - `documentation`, `docs` → `docs`
  - anything else → `feature`

  Decision 3's omitted-kind rule is applied last.
- **Rendering keeps today's sanitising** (`sanitizeBranch`): only `[A-Za-z0-9._/-]` survives, empty
  segments collapse, and a separator left by an empty placeholder is dropped. If nothing follows the
  prefix, `{slug}` is appended. If that is empty too, the launch falls back to `temp/<random>`.
- **English slug (P2).** A non-ASCII title may get an English slug through the AI-assist one-shot. The
  resolver never waits for it: it answers with the deterministic slug and marks the name `provisional`,
  and the Console may ask again once. Without an AI assist the deterministic slug is final.

### Decision 5: base

The base is picked in this order:

1. A base the person typed.
2. The kind's `base`.
3. The rule's `base`.
4. The built-in `head`.

The values mean:

- `head`: the parent clone's current branch, which is today's behaviour.
- `default`: `refs/remotes/origin/HEAD`.
- A branch name: that branch, e.g. `develop`.

The built-in stays `head`, not `default`: the field study's repository has `origin/HEAD → main` and
branches off `develop`. A resolved base that exists neither locally nor on `origin` produces a warning,
and the launch uses `head`.

### Decision 6: the built-in default

- `name = "{prefix}{ref}-{slug}"`
- `base = "head"`
- Prefixes: `feature/`, `fix/` (bugfix), `hotfix/`, `release/`, `support/`, `docs/`, `chore/`,
  `refactor/`.

For GitHub this is the issue's `{type}/{num}-{slug}` (`feature/1113-work-item-pr-status`,
`fix/1120-…`). For Jira it keeps the project (`feature/PROJ-123-…`, or `feature/PROJ-123` for a
Japanese title). ADR 0061 decision 12's `feature/{key}` is replaced. A user with an empty template
gets the new default with no compatibility shim. A non-empty template becomes that user's `match = "*"`
rule, verbatim.

### Decision 7: one resolver, in the Agent

The Agent resolves: it holds the user prefs, the working copies and the cached tenant layer.

- `GET /repos/{name}/branch-rule` returns the effective rule:
  - `name`, `base` and `kinds[{kind, prefix, base}]`;
  - `sources`, which says where each field came from (e.g. `base: gitflow.branch.develop`);
  - `gitflow`: `declared`, `absent` or `suggest` (decision 9).
- `POST /repos/{name}/branch-name` takes `{item?: {provider, key, title, type, labels}, kind?, slug?}`
  and returns `{name, base, kind, provisional, warnings[], sources}`.
- `POST /repos/{name}/branch-name/check` takes `{name}` and returns `{warnings[]}`.
- The af MCP tool `branch_name` (P2) takes the same input as `POST …/branch-name` and names the working
  copy. Agents and skills (issue-to-pr) call it instead of inventing names.

`{name}` is the working copy under `~/repos`, as in the other `/repos/{name}` routes. When an older Agent
returns 404, the Console keeps using its own `branchForItem` with the user's template.

### Decision 8: the three styles become one; rules warn and never refuse

- **Work-item launch** asks the resolver for the name and the base.
- **Other launches keep `temp/<random>`**: deferred naming does not change.
- **Rename:**
  - The chips are the resolved kinds' prefixes, not a hard-coded list.
  - The AI suggestion returns a kind from the resolved set plus a slug, still in English, and the
    resolver composes the name.
  - The launch records the work item's key in the session meta, so a rename keeps `{ref}`.
- **Warning.** A name whose first segment is not a resolved prefix gets a warning in the launch and
  rename modals and from `check`. `temp/` is exempt. The name is never refused.

### Decision 9: "Initialize Git Flow" in the Console

A declaration is usually absent (decision 3, docs/log/123 §4), so reading one is not enough. A
repository (parent clone) gets **Initialize Git Flow**.

- **The fields** are Fork's six: production branch, development branch, feature / release / hotfix
  prefix and version tag prefix. An optional bugfix prefix is added.
- **Prefilled** from:
  - which of `develop`, `main` and `master` exist on `origin`;
  - the Bitbucket model's prefixes;
  - any keys already present.
- **Saving writes git-flow's own keys** into the parent clone's config with `git config`:
  - `gitflow.branch.master` and `gitflow.branch.develop`;
  - `gitflow.prefix.{feature,bugfix (when given),release,hotfix,support,versiontag}`.

  The `git flow` CLI, Fork and git-flow-next opened on the same clone then see the same settings.
  `gitflow.path.hooks` is left to git-flow's default.
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
