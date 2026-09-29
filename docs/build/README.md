---
audience: "someone changing the code — a new contributor, a future maintainer, or an agent session"
source_of_truth: "the code (this shelf is the map and the design intent)"
updated: "2026-09"
---

# Building Agent Fleet

English | [日本語](README.ja.md)

This shelf answers **"how does it work?"**: the three processes and what each owns,
the two authentication layers, the API boundaries, the data model, the threat model,
the integrations, how to build and test, and the pattern to follow when you add an agent
kind or a deployment target.

What this shelf holds, what it leaves to other shelves, and how it is written (wire
contracts and grep-able anchors, never line numbers) are
[CONVENTIONS §9](../CONVENTIONS.md#9-what-each-shelf-is-responsible-for) and
[§4](../CONVENTIONS.md#4-vocabulary-per-shelf). It links to these instead of copying them:

- running a deployment: [guide/operate/](../../guide/operate/README.md)
- what a member or an administrator sees and does: [guide/member/](../../guide/member/README.md),
  [guide/admin/](../../guide/admin/README.md)
- which kind, provider, target or role supports what: [guide/ref/](../../guide/ref/README.md)
  ([CONVENTIONS §6](../CONVENTIONS.md))
- why it is like this, including what was rejected: [decisions/](../decisions/)
- anything still to be done: GitHub issues ([CONVENTIONS §10](../CONVENTIONS.md#10-open-work-is-an-issue-not-a-sentence))

## Update trigger

| You changed | Update |
|---|---|
| An API group or path | [05](05-api.md), and the chapter of the component that serves it |
| A migration | [06](06-data.md) — the entities, and [§6.5](06-data.md#65-migration-practice) if the practice itself changed |
| Authentication, crypto, isolation or audit | [07](07-security.md); where audit is written is [05 §5.5](05-api.md#55-where-audit-is-written) |
| An external provider | [08](08-integrations.md) |
| An agent kind, or how its CLI signs in | [04 §4.3](04-agent.md#43-the-pattern-for-integrating-a-kind) and [08](08-integrations.md); a new kind also owes what [20](20-add-an-agent.md) lists at its end |
| A deployment target, adapter or variable | [09](09-deploy.md); a new target also owes what [21](21-add-a-deploy-target.md) lists at its end |
| What a kind, provider, target or role supports | the table in [guide/ref/](../../guide/ref/README.md), and nowhere else |
| Build, reflect or test mechanics | [10](10-development.md) |
| Where files live (a refactor) | [90](90-code-map.md), and every chapter that names a moved file as an anchor — grep the shelf for the old path |
| A feature users can see | the relevant chapter, and the definition of done in [CONVENTIONS §8](../CONVENTIONS.md#8-definition-of-done-for-a-feature) |

## Chapters

**New here?** [00](00-project-context.md) → [01](01-architecture.md) → [05](05-api.md) →
[06](06-data.md) → [10](10-development.md). **Working on one component?**
[01](01-architecture.md), then its chapter, and [90](90-code-map.md) for where to start
grepping. **Reviewing security?** [07](07-security.md) → [08](08-integrations.md) →
[01](01-architecture.md). **Adding an agent kind or a deployment target?**
[20](20-add-an-agent.md) or [21](21-add-a-deploy-target.md).

| | |
|---|---|
| [00 Project context](00-project-context.md) | the premises the other chapters rest on: status, the settled assumptions (v1), what this was built out of |
| [01 Architecture](01-architecture.md) | delivery model, terms, the three processes, two auth layers, the main flows, the adapter seams, what is built and what is not |
| [02 Console](02-console.md) | the browser SPA: stack, state and server sync, panes, information architecture, the display system, i18n, build and tests |
| [03 Control Plane](03-control-plane.md) | responsibilities, the life of a request, the Runtime abstraction, the MCP server, background jobs, self-hosted engines |
| [04 Agent](04-agent.md) | the session model, integrating an agent kind, state badges, chat, transcripts and usage, secrets, the workspace image, the browser manager |
| [05 API](05-api.md) | the two boundaries as a map (the route goldens are the full list), the relay paths, cross-cutting rules, where audit is written |
| [06 Data](06-data.md) | store layout, entities and their relationships, what is not in the database, migration practice |
| [07 Security](07-security.md) | threat model, isolation, the two auth layers, CP ↔ agent authentication, envelope encryption, audit, egress |
| [08 Integrations](08-integrations.md) | the external providers, the two patterns they fall into (the CP owns a callback, or not), and each one's contract |
| [09 Deploy](09-deploy.md) | the forms, the adapters and their knobs, ingress, the environment index, the AWS target, backup and upgrade assumptions, cost |
| [10 Development](10-development.md) | repository layout, seeing a change, testing, commits and branches, documentation |
| **[20 Adding an agent kind](20-add-an-agent.md)** | what to decide first, the surfaces a kind fills, the traps that have actually bitten, verification |
| **[21 Adding a deployment target](21-add-a-deploy-target.md)** | the contract an adapter owes, what is not its job, cost and latency, verification |
| [90 Code map](90-code-map.md) | grep starting points per directory — examples, not an inventory |
| [91 Internal git](91-internal-git.md) | the tenant's own git hosting: why this shape, storage, the token model, integration points |
| [92 Driving a TUI](92-driving-a-tui.md) | verifying a modal screen you drive by keystrokes, and the checklist for every CLI update |
| [93 Worktree dependencies](93-worktree-deps.md) | what a worktree shares and what it duplicates, per ecosystem, measured |
