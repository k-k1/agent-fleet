---
audience: "someone changing the code — a new contributor, a future maintainer, or an agent session"
source_of_truth: "the code (this shelf is the map and the design intent)"
updated: "2026-08"
---

# Building Agent Fleet

English | [日本語](README.ja.md)

This shelf answers **"how does it work?"**: the three processes and what each owns,
the two authentication layers, the API boundaries, the data model, the threat model,
the integrations, and how to build and test.

## What belongs here

- Responsibilities and wire contracts: which process owns what, and what the
  externally visible contract is.
- The shapes that repeat — how an agent kind is integrated, how a deployment adapter
  is structured — so the next one is added by following a pattern rather than by
  reverse-engineering the last one.
- Build, reflect and test practices.

## What does not

- **Line numbers, ever.** They are wrong within a week. Point at a grep-able anchor
  instead — an endpoint path, an environment variable name, an error code string — or
  at the code map, which is the one file allowed to enumerate paths and is expected
  to go stale.
- **Procedures for running a deployment** — [operate/](../../guide/operate/README.md) owns
  those, and this shelf links rather than copies.
- **User-facing instructions** — [use/](../../guide/member/README.md).
- **Journals.** A measurement, an incident post-mortem or a round-by-round
  investigation is not a design document. Put the durable conclusion here and the
  reasoning in [decisions/](../decisions/).

## Update trigger

| You changed | Update |
|---|---|
| An API group or path | the API contract map + the component chapter |
| A migration | the data model chapter |
| Authentication, crypto, isolation or audit | the security chapter |
| An external provider or agent CLI | the integrations chapter (+ the agent-kind pattern) |
| A deployment target, adapter or variable | the deploy chapter |
| Build, reflect or test mechanics | the development chapter |
| Where files live (a refactor) | the code map only — nothing else should move |
| A feature users can see | the relevant chapter, plus [ref/](../../guide/ref/README.md) and the reader's shelf |

## Chapters

**New here?** [01](01-architecture.md) → [05](05-api.md) → [06](06-data.md) →
[10](10-development.md). **Working on one component?** [01](01-architecture.md) then
its chapter. **Reviewing security?** [07](07-security.md) → [08](08-integrations.md) →
[01](01-architecture.md).

| | |
|---|---|
| [00 Project context](00-project-context.md) | status, the settled assumptions (v1), and what this was built out of |
| [01 Architecture](01-architecture.md) | delivery model, terms, the three processes, two auth layers, the main flows, the adapter seams |
| [02 Console](02-console.md) | the browser SPA |
| [03 Control Plane](03-control-plane.md) | responsibilities, the life of a request, background jobs |
| [04 Agent](04-agent.md) | the session model, integrating an agent kind, the workspace image |
| [05 API](05-api.md) | the two boundaries, the five relay paths, cross-cutting rules, where audit is written |
| [06 Data](06-data.md) | entities and migration practice |
| [07 Security](07-security.md) | threat model, isolation, the two auth layers, envelope encryption, egress |
| [08 Integrations](08-integrations.md) | every external provider, and the two patterns they fall into |
| [09 Deploy](09-deploy.md) | the forms, the adapters, the environment index, cost |
| [10 Development](10-development.md) | build, reflect a change, test, conventions |
| **[20 Adding an agent kind](20-add-an-agent.md)** | the pattern, and the traps that have actually bitten |
| **[21 Adding a deployment target](21-add-a-deploy-target.md)** | the contract an adapter owes |
| [90 Code map](90-code-map.md) | grep starting points — **the one file allowed to enumerate paths** |
| [91 Internal git](91-internal-git.md) | the tenant's own git hosting |
| [92 Driving a TUI](92-driving-a-tui.md) | verifying a modal screen you drive by keystrokes |
| [93 Worktree dependencies](93-worktree-deps.md) | what a worktree shares and what it duplicates, measured |

What the product can do is [ref/](../../guide/ref/README.md); why it is like this is
[decisions/](../decisions/).
